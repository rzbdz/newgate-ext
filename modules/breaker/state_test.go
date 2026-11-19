package breaker

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// clocked builds a health table with an injected clock. The state machine runs entirely on time,
// so tests do not sleep: the old implementation went by feel with `b.Cooldown = 5*time.Millisecond`
// + `time.Sleep`, both slow and flaky. The second returned function advances the clock.
//
// Along the way it registers a test shape test (see testShape). Since 2026-09-17 the
// real test is registered by the upstream module (modules/deepseek/shape.go), and
// core no longer has that string -- but the state machine's policy "the shape ledger
// never delists" still needs to be testable, so an equivalent one is hung here.
func clocked() (*table, func(time.Duration)) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	b := newTable()
	b.now = func() time.Time { return now }
	if _, err := b.RegisterShapeDetector(testShape{}); err != nil {
		panic(err)
	}
	return b, func(d time.Duration) { now = now.Add(d) }
}

// testShape is breaker's internal test test: it recognizes the shape "must be passed back + a field
// was named" (the real test is in modules/deepseek). It lives here so the state machine's tests
// do not depend on another module, while keeping the test's **shape** consistent with reality.
type testShape struct{}

func (testShape) Name() string { return "test-shape" }

func (testShape) Match(status int, body []byte) bool {
	if status != 400 {
		return false
	}
	if !bytes.Contains(body, []byte("must be passed")) {
		return false
	}
	return bytes.Contains(body, []byte("reasoning_content")) ||
		bytes.Contains(body, []byte("content[].thinking"))
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// connFail records one "connection failure", pushing the binding onto the availability ledger once.
func connFail(b *table, provider, model string) Result {
	return b.Report(provider, model, Input{Kind: KindConnError})
}

func state(t *testing.T, b *table, provider, model string) Status {
	t.Helper()
	for _, s := range b.Snapshot() {
		if s.Provider == provider && s.Model == model {
			return s
		}
	}
	t.Fatalf("the snapshot has no %s/%s", provider, model)
	return Status{}
}

func TestAvailabilityNeedsConsecutiveFailures(t *testing.T) {
	b, _ := clocked()
	if r := connFail(b, "relay", "model"); r.Opened {
		t.Fatal("the first real-traffic failure must not trip")
	}
	if !b.Available("relay", "model") {
		t.Fatal("the binding was dropped after the first failure")
	}
	if r := connFail(b, "relay", "model"); !r.Opened {
		t.Fatal("the second consecutive failure should trip")
	}
	if b.Available("relay", "model") {
		t.Fatal("the binding is still usable after tripping")
	}
}

// TestBucketSwitchRestartsTheStreak: "consecutive failures" means
// "the same class of problem in a row". A binding taking one connection
// failure and one 429, one each, is not yet enough to say it is broken.
func TestBucketSwitchRestartsTheStreak(t *testing.T) {
	b, _ := clocked()
	connFail(b, "relay", "model") // availability ledger, 1
	rate := func() Result {
		return b.Report("relay", "model", Input{Kind: KindUpstreamStatus, Status: 429})
	}
	if rate().Opened {
		t.Fatal("with the rate-limit ledger it tripped on the first one")
	}
	if rate().Opened {
		t.Fatal("the rate-limit ledger tripped on the second one (threshold is 3)")
	}
	if !rate().Opened {
		t.Fatal("the rate-limit ledger should trip on the third consecutive failure")
	}
	if got := state(t, b, "relay", "model"); got.Rule != "rate limit" {
		t.Fatalf("the trip reason should be rate limiting: %+v", got)
	}
}

// TestPerBucketPolicy: the three ledgers have different thresholds/cooldowns,
// and that is the core behavior change of 2026-09-17 -- before, a 429
// and a connection timeout used the same "two times, 60 seconds" ledger.
func TestPerBucketPolicy(t *testing.T) {
	tests := []struct {
		name      string
		in        Input
		threshold int
		cooldown  time.Duration
		rule      string
	}{
		{"connection failure", Input{Kind: KindConnError}, 2, 60 * time.Second, "availability"},
		{"first-byte timeout (indistinguishable on the client side)", Input{Kind: KindConnError}, 2, 60 * time.Second, "availability"},
		{"500", Input{Kind: KindUpstreamStatus, Status: 500}, 2, 60 * time.Second, "availability"},
		{"503", Input{Kind: KindUpstreamStatus, Status: 503}, 2, 60 * time.Second, "availability"},
		{"stream broke mid-way", Input{Kind: KindStreamTruncated, Written: true}, 2, 60 * time.Second, "availability"},
		{"429", Input{Kind: KindUpstreamStatus, Status: 429}, 3, 20 * time.Second, "rate limit"},
		{"401", Input{Kind: KindUpstreamStatus, Status: 401}, 1, 5 * time.Minute, "config"},
		{"403", Input{Kind: KindUpstreamStatus, Status: 403}, 1, 5 * time.Minute, "config"},
		{"404", Input{Kind: KindUpstreamStatus, Status: 404}, 1, 5 * time.Minute, "config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := clocked()
			for i := 1; i < tt.threshold; i++ {
				if r := b.Report("p", "m", tt.in); r.Opened {
					t.Fatalf("it tripped on #%d, threshold should be %d", i, tt.threshold)
				}
				if !b.Available("p", "m") {
					t.Fatalf("it dropped on #%d, threshold should be %d", i, tt.threshold)
				}
			}
			if r := b.Report("p", "m", tt.in); !r.Opened {
				t.Fatalf("it did not trip on #%d", tt.threshold)
			}
			got := state(t, b, "p", "m")
			if got.Rule != tt.rule {
				t.Fatalf("the reason should be %s: %+v", tt.rule, got)
			}
			if got.CooldownMs != tt.cooldown.Milliseconds() {
				t.Fatalf("the first cooldown should be %v, got %dms", tt.cooldown, got.CooldownMs)
			}
			if got.State != "open" || !got.Open {
				t.Fatalf("the state should be open: %+v", got)
			}
		})
	}
}

// TestShapeErrorsOnlyCount: shape errors never delist, but must be visible.
func TestShapeErrorsOnlyCount(t *testing.T) {
	b, _ := clocked()
	in := Input{Kind: KindUpstreamStatus, Status: 400, Body: []byte(shapeBody)}
	for i := 0; i < 50; i++ {
		if r := b.Report("p", "m", in); r.Opened {
			t.Fatalf("%d shape errors tripped it", i+1)
		}
	}
	if !b.Available("p", "m") {
		t.Fatal("a shape error dropped a usable binding")
	}
	got := state(t, b, "p", "m")
	if got.ShapeSkips != 50 || got.Fails != 0 || got.State != "closed" || got.Rule != "" {
		t.Fatalf("a shape error should only count: %+v", got)
	}
}

// TestHalfOpenAdmitsExactlyOneTrial: the half-open period lets through exactly **one**.
func TestHalfOpenAdmitsExactlyOneTrial(t *testing.T) {
	b, advance := clocked()
	connFail(b, "p", "m")
	connFail(b, "p", "m")

	advance(59 * time.Second)
	if b.Available("p", "m") {
		t.Fatal("it let a trial through before the cooldown ended")
	}
	advance(time.Second) // exactly at expiry
	if !b.Available("p", "m") {
		t.Fatal("it did not let a trial through after the cooldown ended")
	}
	if b.Available("p", "m") {
		t.Fatal("it let a second concurrent trial through in the half-open period")
	}
	if got := state(t, b, "p", "m"); got.State != "half-open" || !got.Trial {
		t.Fatalf("the state should be half-open with a trial in flight: %+v", got)
	}

	// No report ever arrives after the trial was issued (the client cancelled midway, the process
	// was replaced): when TrialTTL expires the trial counts as lost and another may be issued --
	// otherwise an unreported trial would pin the binding forever in "half-open, someone is trying".
	advance(45 * time.Second)
	if !b.Available("p", "m") {
		t.Fatal("after the trial TTL expired it did not let another through")
	}
}

func TestTrialFailureReopensWithBackoff(t *testing.T) {
	b, advance := clocked()
	connFail(b, "p", "m")
	connFail(b, "p", "m") // first open: 60s

	// Three rounds of "cooldown expires -> let a trial through
	// -> trial fails -> close": the cooldown should double.
	for _, want := range []time.Duration{120 * time.Second, 240 * time.Second, 480 * time.Second} {
		advance(want / 2)
		if !b.Available("p", "m") {
			t.Fatalf("after the cooldown %v ended it did not let a trial through", want/2)
		}
		r := connFail(b, "p", "m")
		if !r.Opened {
			t.Fatalf("a failed half-open trial did not trip right away (it should grow to %v after release)", want)
		}
		got := state(t, b, "p", "m")
		if got.CooldownMs != want.Milliseconds() {
			t.Fatalf("the backoff should be %v, got %dms", want, got.CooldownMs)
		}
		if got.State != "open" {
			t.Fatalf("after a failed trial the state should be open: %+v", got)
		}
	}

	// Doubling further hits the 10-minute ceiling.
	advance(480 * time.Second)
	if !b.Available("p", "m") {
		t.Fatal("after the cap it did not let a trial through")
	}
	connFail(b, "p", "m")
	if got := state(t, b, "p", "m"); got.CooldownMs != (10 * time.Minute).Milliseconds() {
		t.Fatalf("the cooldown should cap at 10 minutes, got %dms", got.CooldownMs)
	}
}

func TestTrialSuccessClosesAndResetsBackoff(t *testing.T) {
	b, advance := clocked()
	connFail(b, "p", "m")
	connFail(b, "p", "m")

	// Fail once first to push the cooldown to 120s.
	advance(60 * time.Second)
	if !b.Available("p", "m") {
		t.Fatal("it did not let a trial through after the cooldown ended")
	}
	connFail(b, "p", "m")

	advance(120 * time.Second)
	if !b.Available("p", "m") {
		t.Fatal("after the backed-off cooldown ended it did not let a trial through")
	}
	b.Report("p", "m", Input{Kind: KindUpstreamSuccess})

	got := state(t, b, "p", "m")
	if got.State != "closed" || got.Open || got.Fails != 0 || got.CooldownMs != 0 {
		t.Fatalf("a successful trial should close it and reset the backoff: %+v", got)
	}
	if !b.Available("p", "m") {
		t.Fatal("after closing, the binding is still unusable")
	}

	// After closing, two more failures: the cooldown must
	// restart from the **base** 60s, not continue backing off.
	connFail(b, "p", "m")
	connFail(b, "p", "m")
	if got := state(t, b, "p", "m"); got.CooldownMs != (60 * time.Second).Milliseconds() {
		t.Fatalf("after closing, the cooldown did not return to the base: %+v", got)
	}
}

// TestSuccessDuringIsolationDoesNotUnseal: a success during quarantine comes
// from a request whose chain was built earlier (the chain is built when each
// request starts), and cannot count as evidence of recovery -- otherwise a bad
// upstream leaking one success now and then could stay on the chain forever.
func TestSuccessDuringIsolationDoesNotUnseal(t *testing.T) {
	b, advance := clocked()
	connFail(b, "p", "m")
	connFail(b, "p", "m")

	advance(30 * time.Second)
	b.Report("p", "m", Input{Kind: KindUpstreamSuccess})
	if b.Available("p", "m") {
		t.Fatal("a success during isolation reopened it")
	}
	if got := state(t, b, "p", "m"); got.State != "open" {
		t.Fatalf("a success during isolation changed the state: %+v", got)
	}
}

// TestFailuresDuringIsolationAreNotNewEvidence: failures during quarantine likewise come from
// requests built earlier and are not new evidence: count no threshold and extend no quarantine.
func TestFailuresDuringIsolationAreNotNewEvidence(t *testing.T) {
	b, advance := clocked()
	connFail(b, "p", "m")
	connFail(b, "p", "m")
	before := state(t, b, "p", "m")

	advance(30 * time.Second)
	for i := 0; i < 10; i++ {
		if r := connFail(b, "p", "m"); r.Opened {
			t.Fatal("a failure during isolation was treated as a new trip event")
		}
	}
	after := state(t, b, "p", "m")
	if after.OpenUntil != before.OpenUntil {
		t.Fatalf("a failure during isolation extended the isolation: %v -> %v", before.OpenUntil, after.OpenUntil)
	}
	if after.Fails != before.Fails {
		t.Fatalf("a failure during isolation was booked in the ledger: %d -> %d", before.Fails, after.Fails)
	}
}

// TestProbeBypassesThreshold: a good probe conclusion is authoritative and unseals on the spot
// (**even during quarantine**).
//
// A **bad** conclusion deliberately does not delist any more (2026-11-19): a probe is a 4-token
// request without the client's fingerprint, and an upstream that sniffs for its own client
// answers it with an error while real traffic succeeds. That is exactly what happened: one
// `newgate probe` delisted all four smt-claude bindings and requests kept being served by
// another profile. The grade is still recorded (the ranker sinks it), but only real traffic
// delists. Kept below as the first half of the test so the asymmetry is pinned, not implied.
func TestProbeBypassesThreshold(t *testing.T) {
	b, advance := clocked()
	if grade, open := b.RecordProbe("p", "m", 503, 1024, time.Second, 12*time.Second, ""); grade != ProbeUnavailable || open {
		t.Fatalf("a failed probe must be recorded as %s and delist nothing, got grade=%s open=%v", ProbeUnavailable, grade, open)
	}
	if !b.Available("p", "m") {
		t.Fatal("a probe conclusion delisted the binding -- the 2026-11-19 outage is back " +
			"(real traffic decides, a probe only reports)")
	}
	// The evidence is still visible: the grade sinks it in Rank and `newgate breaker` shows it.
	if b.Rank("p", "m", 1024) < b.Rank("never-seen", "m", 1024) {
		t.Error("an unavailable probe grade must not rank better than an unobserved binding")
	}

	// Now a real trip, then the 2026-11-04 scenario: the user's probe succeeds during quarantine,
	// yet requests kept getting 404 until the cooldown expired -- the only recovery action
	// available had become a placebo. A successful active probe must return to the chain
	// immediately.
	for i := 0; i < 2; i++ {
		connFail(b, "p", "m")
		advance(time.Second)
	}
	if b.Available("p", "m") {
		t.Fatal("two real connection failures did not delist the binding")
	}
	advance(30 * time.Second)
	if grade, open := b.RecordProbe("p", "m", 200, 1024, time.Second, 12*time.Second, ""); grade != ProbeFluent || open {
		t.Fatalf("a successful probe should unblock immediately: grade=%s open=%v", grade, open)
	}
	if !b.Available("p", "m") {
		t.Fatal("after the probe unblocked it the binding is still unusable")
	}
	// Backoff reset: the next real failure restarts from the
	// base cooldown, not carrying the previous round's debt.
	if got := state(t, b, "p", "m"); got.State != "closed" || got.Open {
		t.Fatalf("wrong state after unblocking: %+v", got)
	}
}

// TestAvailableIsTheOnlyPathThatOpensTheTrialWindow pins the uniqueness of the state machine's
// read path: only Available advances the state machine. Rank/Score/Snapshot are pure reads.
func TestAvailableIsTheOnlyPathThatOpensTheTrialWindow(t *testing.T) {
	b, advance := clocked()
	connFail(b, "p", "m")
	connFail(b, "p", "m")
	advance(time.Minute)

	b.Rank("p", "m", 1024)
	b.Score("p", "m", 1024)
	state(t, b, "p", "m")
	if got := state(t, b, "p", "m"); got.Trial {
		t.Fatal("the read path (Rank/Score/Snapshot) handed out a trial slot")
	}
	if !b.Available("p", "m") {
		t.Fatal("Available did not let a trial through")
	}
}

// TestConcurrentAvailableGrantsOneTrial runs concurrency with a real clock: several requests
// asking the same binding at once during chain building, only one may get the trial slot.
func TestConcurrentAvailableGrantsOneTrial(t *testing.T) {
	b := newTable()
	b.SetPolicy(Policy{Rules: map[Bucket]Rule{
		BucketAvailability: {Threshold: 1, Cooldown: time.Millisecond},
	}})
	connFail(b, "p", "m")
	time.Sleep(5 * time.Millisecond)

	const n = 32
	var granted int32
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Available("p", "m") {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if granted != 1 {
		t.Fatalf("%d of %d concurrent requests got a trial slot, exactly 1 should", n, granted)
	}
}

// TestPreSealDiagnosticSparesAHealthyBinding is the direct regression for the
// 2026-09-17 scene: smt-deepseek was delisted when real traffic's **time to
// first byte** counted up to the threshold, while every `newgate probe` was
// fluent. One more piece of active evidence must be demanded before tripping.
func TestPreSealDiagnosticSparesAHealthyBinding(t *testing.T) {
	b, _ := clocked()
	calls := 0
	b.SetVerifier(func(provider, model string) bool {
		calls++
		if provider != "p" || model != "m" {
			t.Fatalf("the diagnostic probe asked about the wrong binding: %s/%s", provider, model)
		}
		return true // the probe says it is still reachable
	})

	connFail(b, "p", "m")
	r := connFail(b, "p", "m") // counts up to the threshold
	if r.Opened {
		t.Fatal("the diagnostic said usable, yet it was dropped anyway")
	}
	if !r.Spared {
		t.Fatal("should report spared")
	}
	if calls != 1 {
		t.Fatalf("the diagnostic probe ran %d times, exactly 1 should", calls)
	}
	if !b.Available("p", "m") {
		t.Fatal("the rescued binding is not on the chain")
	}
	got := state(t, b, "p", "m")
	if got.Fails != 0 || got.Spared != 1 || got.Open || got.State != "closed" {
		t.Fatalf("wrong ledger after the rescue: %+v", got)
	}
	if got.Reason == "" {
		t.Fatal("the rescue reason must stay in the snapshot (never silent)")
	}

	// The **point** of resetting the count: the next failure starts again from 1,
	// rather than reaching the threshold at once and probing again -- otherwise
	// an intermittently jittery binding would be probed on every request.
	connFail(b, "p", "m")
	if calls != 1 {
		t.Fatalf("the count was not reset and it probed again (#%d)", calls)
	}

	// The diagnostic changes its answer to unusable: the second
	// time the threshold is reached it must really delist.
	b.SetVerifier(func(string, string) bool { calls++; return false })
	if r := connFail(b, "p", "m"); !r.Opened || r.Spared {
		t.Fatalf("the diagnostic said unusable but it was not dropped: %+v", r)
	}
	if calls != 2 {
		t.Fatalf("the diagnostic should run once each time the threshold is reached, got %d", calls)
	}
}

// TestPreSealDiagnosticSealsWhenProbeAlsoFails: if the diagnostic says
// unusable, delist as usual, and the reason must show "this time it was double
// evidence", so it can be told apart from "delisted by passive traffic alone".
func TestPreSealDiagnosticSealsWhenProbeAlsoFails(t *testing.T) {
	b, _ := clocked()
	b.SetVerifier(func(string, string) bool { return false })
	connFail(b, "p", "m")
	if r := connFail(b, "p", "m"); !r.Opened || r.Spared {
		t.Fatalf("the diagnostic said unusable but it was not dropped: %+v", r)
	}
	got := state(t, b, "p", "m")
	if got.State != "open" || !strings.Contains(got.Reason, "diagnostic probe also failed") {
		t.Fatalf("the trip reason should state the diagnostic conclusion: %+v", got)
	}
}

// TestPreSealDiagnosticRunsOnceForConcurrentFailures: concurrent failures should probe only once.
func TestPreSealDiagnosticRunsOnceForConcurrentFailures(t *testing.T) {
	b := newTable()
	b.SetPolicy(Policy{Rules: map[Bucket]Rule{
		BucketAvailability: {Threshold: 1, Cooldown: time.Minute},
	}})
	var mu sync.Mutex
	calls := 0
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	b.SetVerifier(func(string, string) bool {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			entered <- struct{}{}
			<-release // block the first diagnostic so later failures all pile into verifying
		}
		return true
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); connFail(b, "p", "m") }()
	<-entered
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); connFail(b, "p", "m") }()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("concurrent failures triggered %d diagnostic probes, exactly 1 should", calls)
	}
}

// TestHalfOpenTrialFailureSkipsDiagnostic: a failed half-open trial is **active** evidence, and
// no diagnostic should be demanded again -- that would only prolong a bad upstream's quarantine.
func TestHalfOpenTrialFailureSkipsDiagnostic(t *testing.T) {
	b, advance := clocked()
	calls := 0
	b.SetVerifier(func(string, string) bool { calls++; return true })

	connFail(b, "p", "m")
	connFail(b, "p", "m") // this time it diagnoses (the result says healthy → no open)
	if calls != 1 {
		t.Fatalf("the first time the threshold is reached it should diagnose once, got %d", calls)
	}

	// Get this binding really delisted (the diagnostic changes its answer
	// to unusable), then wait for the cooldown to let a trial through.
	b.SetVerifier(func(string, string) bool { calls++; return false })
	connFail(b, "p", "m")
	if r := connFail(b, "p", "m"); !r.Opened {
		t.Fatal("the diagnostic said unusable but it was not dropped")
	}
	before := calls
	advance(time.Minute)
	if !b.Available("p", "m") {
		t.Fatal("after the cooldown it did not let a trial through")
	}
	if r := connFail(b, "p", "m"); !r.Opened {
		t.Fatal("a failed half-open trial did not trip right away")
	}
	if calls != before {
		t.Fatalf("a failed half-open trial should not ask for a diagnostic again (%d extra times)", calls-before)
	}
}
