package breaker

import (
	"path/filepath"
	"testing"
	"time"
)

func TestBreakerIsScopedToBinding(t *testing.T) {
	b, _ := clocked()
	for i := 0; i < 2; i++ {
		b.Report("relay", "slow", Input{Kind: KindConnError})
	}

	if b.Available("relay", "slow") {
		t.Fatal("a binding dropped by real traffic is still usable")
	}
	if !b.Available("relay", "fast") {
		t.Fatal("other models of the same provider were hit too")
	}
	status := b.Snapshot()
	if len(status) != 1 || status[0].Provider != "relay" || status[0].Model != "slow" {
		t.Fatalf("wrong breaker snapshot: %+v", status)
	}
	if status[0].Rule != "availability" || status[0].State != "open" {
		t.Fatalf("the drop reason/state did not make it into the snapshot: %+v", status[0])
	}
}

// TestSnapshotShowsFailuresBelowThreshold pins the recording blind spot fixed on
// 2026-09-17: bindings that had "failed but not reached the threshold" were once
// invisible to both the snapshot and persistence -- `newgate breaker` could not
// see "failed once, breaker not open yet", and a restart lost that count too.
func TestSnapshotShowsFailuresBelowThreshold(t *testing.T) {
	b, _ := clocked()
	b.Report("relay", "model", Input{Kind: KindConnError})

	got := b.Snapshot()
	if len(got) != 1 {
		t.Fatalf("a binding with 1 failure (not tripped) did not appear in the snapshot: %+v", got)
	}
	if got[0].Fails != 1 || got[0].Open || got[0].State != "closed" || got[0].Rule != "availability" {
		t.Fatalf("wrong failure count for a binding not yet tripped: %+v", got[0])
	}
}

// TestSnapshotDoesNotInventRowsForNoOps: outcomes that record nothing should not
// **create** a row either. Client cancels and the "equally wrong anywhere" 4xx arrive
// every time, and leaving a row for each would drown the `newgate breaker` output.
func TestSnapshotDoesNotInventRowsForNoOps(t *testing.T) {
	b, _ := clocked()
	b.Report("relay", "a", Input{Kind: KindClientCancel})
	b.Report("relay", "b", Input{Kind: KindUpstreamStatus, Status: 400, Body: []byte(`{"error":"bad param"}`)})
	b.Report("relay", "c", Input{Kind: KindUpstreamStatus, Status: 405})
	b.Report("relay", "d", Input{Kind: KindUpstreamSuccess})

	if got := b.Snapshot(); len(got) != 0 {
		t.Fatalf("an outcome with no action created a record out of nothing: %+v", got)
	}
}

func TestProbeGradesAndContextBuckets(t *testing.T) {
	b, _ := clocked()
	if grade, open := b.RecordProbe("relay", "fast", 200, 1,
		2*time.Second, 12*time.Second, ""); grade != ProbeFluent || open {
		t.Fatalf("2s = %s open=%v", grade, open)
	}
	if grade, open := b.RecordProbe("relay", "ok", 200, 1,
		8*time.Second, 12*time.Second, ""); grade != ProbeUsable || open {
		t.Fatalf("8s = %s open=%v", grade, open)
	}
	if grade, open := b.RecordProbe("relay", "slow", 200, 1,
		13*time.Second, 12*time.Second, ""); grade != ProbeLaggy || open {
		// Laggy is a **grade**, not a delisting: same reason as an unavailable one (2026-11-19) --
		// a probe measures a different request than the one the user will send. It sinks the
		// binding in Rank, which is what this line is here to pin.
		t.Fatalf("13s = %s open=%v", grade, open)
	}

	b.ObserveSuccess("relay", "fast", 64*1024, 20*time.Second)
	if tiny, large := b.Score("relay", "fast", 1), b.Score("relay", "fast", 64*1024); tiny >= large {
		t.Fatalf("context bucketing did not distinguish TTFT: tiny=%d large=%d", tiny, large)
	}
	if fluent, unknown, usable := b.Rank("relay", "fast", 1),
		b.Rank("relay", "unknown", 1), b.Rank("relay", "ok", 1); !(fluent < unknown && unknown < usable) {
		t.Fatalf("wrong grade order: fluent=%d unknown=%d usable=%d",
			fluent, unknown, usable)
	}
}

// TestOpenStateSurvivesRestart: the delisted state and the cooldown expiry must
// survive a graceful restart, or one version swap quietly releases all bad upstreams.
func TestOpenStateSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health.json")
	first := newTable()
	first.SetPolicy(Policy{Rules: map[Bucket]Rule{
		BucketAvailability: {Threshold: 1, Cooldown: time.Minute},
	}})
	if err := first.UseFile(path); err != nil {
		t.Fatal(err)
	}
	first.Report("relay", "model", Input{Kind: KindConnError})

	second := newTable()
	if err := second.UseFile(path); err != nil {
		t.Fatal(err)
	}
	if second.Available("relay", "model") {
		t.Fatal("a binding inside its cooldown was put back on the chain after a restart")
	}
	got := second.Snapshot()
	if len(got) != 1 || !got[0].Open || got[0].Fails != 1 ||
		got[0].State != "open" || got[0].Rule != "availability" {
		t.Fatalf("the dropped state was not fully restored: %+v", got)
	}
}

// TestLegacyHealthFileIsReadable: the health.json written by the known-good binary used
// for downgrade rollback has none of the new fields rule / state / open_until, and the new
// code must still read it -- the file name and keys may not change, precisely for this.
func TestLegacyHealthFileIsReadable(t *testing.T) {
	// The clock must be pinned; never read real time.
	//
	// The scene (found 2026-09-18): this fixture originally hardcoded
	// opened_at=2026-09-17T15:40:00Z and used newTable() (a real clock), so **after 15:41 that
	// day this test would necessarily go red** -- the cooldown (availability 60s) had long
	// expired, Available was by design supposed to let it through, and the assertion "still
	// unavailable" naturally did not hold. The test was broken from 2026-09-17T15:41:00Z on;
	// nobody just ran it. Use clocked(), and put opened_at **a few tens of seconds before
	// the clock's current moment**: what it expresses is "the old binary delisted it minutes
	// ago, and after a restart it must be restored as is", which is this test's real intent.
	b, _ := clocked()
	openedAt := time.Date(2026, 9, 17, 11, 59, 30, 0, time.UTC).Format(time.RFC3339)

	path := filepath.Join(t.TempDir(), "health.json")
	legacy := `[
  {
    "provider": "relay",
    "model": "old",
    "fails": 2,
    "open": true,
    "open_for_ns": 30000000000,
    "reason": "probe failed: dial tcp: i/o timeout",
    "grade": "unavailable",
    "latency_ms": 0,
    "checked_at": "` + openedAt + `",
    "opened_at": "` + openedAt + `",
    "score_ms": 1200
  },
  {
    "provider": "relay",
    "model": "bare",
    "fails": 1,
    "open": false
  }
]`
	if err := writeFile(path, legacy); err != nil {
		t.Fatal(err)
	}
	if err := b.UseFile(path); err != nil {
		t.Fatal(err)
	}
	if b.Available("relay", "old") {
		t.Fatal("the dropped state in the old file was not read in")
	}
	b.mu.Lock()
	openRec := b.records[bindingKey("relay", "old")]
	bareRec := b.records[bindingKey("relay", "bare")]
	b.mu.Unlock()
	// The old file has no rule field: back then there was only one ledger, availability.
	if openRec == nil || openRec.bucket != BucketAvailability || openRec.fails != 2 {
		t.Fatalf("the failure count of the old file was not restored by availability: %+v", openRec)
	}
	// Rows with only a failure count and the breaker not open must
	// also be restored -- otherwise a restart hands out a free pass.
	if bareRec == nil || bareRec.fails != 1 || !bareRec.openedAt.IsZero() {
		t.Fatalf("the not-tripped failure count of the old file was lost: %+v", bareRec)
	}
	if got := b.Score("relay", "old", 1024); got != 1200 {
		t.Fatalf("score_ms of the old file was not backfilled into the 4K tier: %d", got)
	}
}

func TestSnapshotReportsEveryContextBucket(t *testing.T) {
	b, _ := clocked()
	b.ObserveSuccess("relay", "model", 1024, 2*time.Second)
	b.ObserveSuccess("relay", "model", 16*1024, 4*time.Second)
	b.ObserveSuccess("relay", "model", 64*1024, 8*time.Second)
	b.ObserveSuccess("relay", "model", 256*1024, 16*time.Second)

	got := b.Snapshot()
	if len(got) != 1 {
		t.Fatalf("Snapshot() returned %d bindings", len(got))
	}
	wantScores := [4]int{2000, 4000, 8000, 16000}
	wantSamples := [4]int{1, 1, 1, 1}
	if got[0].Scores != wantScores || got[0].Buckets != wantSamples || got[0].Samples != 4 {
		t.Fatalf("context scores missing from snapshot: %+v", got[0])
	}
}

func TestTrafficScoresSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health.json")
	first := newTable()
	if err := first.UseFile(path); err != nil {
		t.Fatal(err)
	}
	first.ObserveSuccess("relay", "model", 16*1024, 4*time.Second)
	first.ObserveSuccess("relay", "model", 16*1024, 2*time.Second)
	first.Flush()

	second := newTable()
	if err := second.UseFile(path); err != nil {
		t.Fatal(err)
	}
	got := second.Snapshot()
	if len(got) != 1 || got[0].Scores[1] != 3400 || got[0].Buckets[1] != 2 {
		t.Fatalf("traffic score did not survive restart: %+v", got)
	}
}
