package probe

import (
	"errors"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/modules/gateway/dialect"
	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
)

func TestCapabilityCacheSurvivesProcessStateReset(t *testing.T) {
	t.Setenv("NEWGATE_HOME", t.TempDir())
	dialect.Reset()
	quirk.Default.Reset()
	t.Cleanup(func() {
		dialect.Reset()
		quirk.Default.Reset()
	})

	target := Target{Provider: "relay", Model: "model"}
	dialect.Mark(target.Provider, target.Model, dialect.CapOpenAI)
	dialect.MarkUnsupported(target.Provider, target.Model, dialect.CapAnthropic)
	dialect.MarkUnsupported(target.Provider, target.Model, dialect.CapCountTokens)
	quirk.Default.Mark(target.Provider, target.Model, quirk.NoThinkingDisable)

	cache, err := loadCapabilityCache()
	if err != nil {
		t.Fatalf("reading the cache in an empty environment should not error: %v", err)
	}
	cache.capture(target)
	if err := cache.save(); err != nil {
		t.Fatal(err)
	}
	dialect.Reset()
	quirk.Default.Reset()

	if err := LoadCachedCapabilities(); err != nil {
		t.Fatalf("loading the cache should not error: %v", err)
	}
	if ok, known := dialect.Supports(target.Provider, target.Model, dialect.CapOpenAI); !ok || !known {
		t.Fatal("a supported dialect was not restored from the cache")
	}
	if ok, known := dialect.Supports(target.Provider, target.Model, dialect.CapAnthropic); ok || !known {
		t.Fatal("an unsupported dialect was not restored from the cache")
	}
	if !quirk.Default.Has(target.Provider, target.Model, quirk.NoThinkingDisable) {
		t.Fatal("a quirk was not restored from the cache")
	}
}

type fakeTimeout struct{}

func (fakeTimeout) Error() string   { return "timeout" }
func (fakeTimeout) Timeout() bool   { return true }
func (fakeTimeout) Temporary() bool { return true }

func TestTimeoutRetryOnlyRetriesTimeoutOnce(t *testing.T) {
	calls := 0
	status, latency, err := timeoutRetry(func() (int, time.Duration, error) {
		calls++
		if calls == 1 {
			return 0, 12 * time.Second, fakeTimeout{}
		}
		return 200, 2 * time.Second, nil
	})
	if calls != 2 || status != 200 || latency != 2*time.Second || err != nil {
		t.Fatalf("timeout retry = calls:%d status:%d latency:%s err:%v",
			calls, status, latency, err)
	}

	calls = 0
	wantErr := errors.New("connection refused")
	_, _, err = timeoutRetry(func() (int, time.Duration, error) {
		calls++
		return 0, time.Millisecond, wantErr
	})
	if calls != 1 || !errors.Is(err, wantErr) {
		t.Fatalf("non-timeout retried: calls:%d err:%v", calls, err)
	}
}

func TestSummarizeCountsUniqueModels(t *testing.T) {
	results := []Result{
		{Profile: "gpt", Role: "normal", Provider: "p", Model: "terra", OK: false},
		{Profile: "gpt", Role: "mid", Provider: "p", Model: "terra", OK: false},
		{Profile: "gpt", Role: "heavy", Provider: "p", Model: "sol", OK: true},
		{Profile: "gpt", Role: "vision", Provider: "p", Model: "sol", OK: true},
		{Profile: "gpt", Role: "light", Provider: "p", Model: "luna", OK: true},
	}
	summary := Summarize(results)
	if len(summary) != 1 || summary[0].OK != 2 || summary[0].Bad != 1 {
		t.Fatalf("a repeated tier must not be counted twice: %+v", summary)
	}
	if got := UniqueCount(results); got != 3 {
		t.Fatalf("UniqueCount = %d, want 3", got)
	}
	if got := FailedCount(results); got != 1 {
		t.Fatalf("FailedCount = %d, want 1", got)
	}
}

// TestPendingQuirksSurviveARestart pins "quirks learned while forwarding must also survive
// a restart".
//
// # Why this one is indispensable
//
// The failure the user reported on 2026-09-22 was "after a restart, the first Codex → GLM
// shot always 400s, then it fixes itself", and its cause is **two timescales that differ**:
// the patch's criterion lives in the in-memory table (empty after a restart), while the
// persisted cache is written only by `newgate probe`. So after an upgrade/restart that
// genuine 400 must be hit once more before it is learned back - always, with no visible
// reason.
//
// So what is measured here is **between those two moments**:
//
//	1 hit one 4xx (Learn records it in memory) → record it in the pending set;
//	2 shutdown (Flush) → write it into probe-capabilities.json;
//	3 process restart (in-memory table cleared);
//	4 LoadCachedCapabilities() → it must be in memory again, so **the first request no
//	  longer hits it**.
//
// Step 3 must be **a real clearing**, not "build another table": if the in-memory table
// survives, this test goes hollow (it tests exactly "rely on the disk copy when there is no
// in-memory table", see the comment on quirk.Default).
func TestPendingQuirksSurviveARestart(t *testing.T) {
	t.Setenv("NEWGATE_HOME", t.TempDir())
	quirk.Default.Reset()
	t.Cleanup(quirk.Default.Reset)

	target := Target{Provider: "relay", Model: "always-thinks"}
	// The criterion is registered by the module owning the patch (here the one in
	// modules/thinking, verbatim in signatures.go). The test registers one of the same shape
	// instead of importing that module: probe sits at the gateway layer, thinking above it,
	// and importing it would be a reverse dependency edge (the same reason as
	// modules/gateway/direction_test.go).
	rel, err := quirk.Default.RegisterSignature(quirk.Signature{
		Flag:  quirk.NoThinkingDisable,
		Any:   []string{"始终思考"},
		Label: "this model always thinks",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if rel != nil {
			_ = rel()
		}
	})
	// It goes through the real criterion instead of Mark: "recognizing the signature" is
	// itself under test on this path.
	learned := quirk.Default.Learn(target.Provider, target.Model, 400,
		[]byte(`{"error":{"message":"该模型始终思考，不支持关闭思考；请使用 low、high 或 max。"}}`))
	if len(learned) == 0 {
		t.Fatal("this message should be recognized (criterion in modules/thinking/signatures.go)")
	}
	var pending PendingQuirks
	if !pending.Record(target, QuirksOf(target)) {
		t.Fatal("a quirk just learned should be recorded in the copy waiting to be written")
	}
	if err := pending.Flush(); err != nil {
		t.Fatalf("writing on shutdown failed: %v", err)
	}

	// Restart: clear that bit in memory.
	quirk.Default.Reset()
	if quirk.Default.Has(target.Provider, target.Model, quirk.NoThinkingDisable) {
		t.Fatal("after clearing, the in-memory table should be empty (otherwise this test tests nothing)")
	}

	if err := LoadCachedCapabilities(); err != nil {
		t.Fatalf("loading the cache should not error: %v", err)
	}
	if !quirk.Default.Has(target.Provider, target.Model, quirk.NoThinkingDisable) {
		t.Fatal("the bit is missing from the in-memory table after a restart - the first request would hit the 400 again, which is exactly the window to fix")
	}
}

// TestMergeQuirksIsAUnion pins "merging into the cache is a union, not an overwrite".
//
// The two dialect conclusions in the cache come from a real probe; if a quirk learned while
// forwarding erased them, the symptom is **downstream sending with a wrong dialect** (the
// probe says this endpoint only accepts openai, and once wiped to "unknown" it tries
// anthropic) - and nothing points at that persist.
func TestMergeQuirksIsAUnion(t *testing.T) {
	t.Setenv("NEWGATE_HOME", t.TempDir())

	target := Target{Provider: "relay", Model: "m"}
	cache, _ := loadCapabilityCache()
	cache.Targets[target.String()] = capabilityEntry{
		Supports: uint32(dialect.CapOpenAI),
		Known:    uint32(dialect.CapOpenAI | dialect.CapAnthropic),
	}
	if err := cache.save(); err != nil {
		t.Fatal(err)
	}

	if _, err := MergeQuirks(map[Target]quirk.Flag{
		target: quirk.NoThinkingDisable,
	}); err != nil {
		t.Fatal(err)
	}

	after, _ := loadCapabilityCache()
	e := after.Targets[target.String()]
	if e.Known == 0 || e.Supports == 0 {
		t.Fatalf("the two dialect bits were wiped (%+v) - a conclusion learned by probing must not be overwritten by a single forward", e)
	}
	if quirk.Flag(e.Quirks) != quirk.NoThinkingDisable {
		t.Fatalf("the quirk bit was not merged in: %+v", e)
	}
}

// TestDiskQuirksReportsWhatIsWorthReloading: the answer to the startup question - "is there
// anything on disk worth restoring" - can only come from targets with **non-zero quirk bits**.
//
// The dialect conclusions a probe wrote (quirks==0) must not count as "there is a cache",
// or "has this machine ever probed" is always true.
func TestDiskQuirksReportsWhatIsWorthReloading(t *testing.T) {
	t.Setenv("NEWGATE_HOME", t.TempDir())

	withQuirk := Target{Provider: "relay", Model: "always-thinks"}
	dialectOnly := Target{Provider: "relay", Model: "plain"}
	cache, _ := loadCapabilityCache()
	cache.Targets[withQuirk.String()] = capabilityEntry{Quirks: uint32(quirk.NoThinkingDisable)}
	cache.Targets[dialectOnly.String()] = capabilityEntry{Known: 1, Supports: 1}
	if err := cache.save(); err != nil {
		t.Fatal(err)
	}

	got := DiskQuirks()
	if len(got) != 1 || got[0] != withQuirk {
		t.Fatalf("DiskQuirks = %+v, only the one with the quirk bit should be reported", got)
	}
}
