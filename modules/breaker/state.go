package breaker

import (
	"sync"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

type probeResult struct {
	Grade     ProbeGrade
	LatencyMs int64
	CheckedAt time.Time
}

// record is one binding's entire health state.
//
// The four forms of the state:
//
//	closed      openedAt is zero. Participates in chain building normally.
//	open        openedAt non-zero, now < openUntil. Delisted, not in the candidate chain.
//	half-open   open and now >= openUntil. Available lets **one** real request through as a
//	            trial (halfOpenAt is this trial's expiry); other concurrent requests are still
//	            kept out.
//	closed(again) trial succeeded -> openedAt cleared, backoff reset.
//
// Why half-open: "once delisted, it can only come back by a manual probe" was the hard
// flaw before 2026-09-17 -- ark was delisted by a real connection timeout and was stuck
// for 16 minutes, until the user typed `newgate probe` by hand. Half-open lets real
// traffic prove recovery itself. Safety is kept by three things: only let through after
// the cooldown expires, strictly once during the half-open period, and on a failed
// trial close the breaker immediately and double the cooldown (60s -> 120s -> 240s …,
// capped at 10 minutes), so a bad upstream does not come back to hit once a minute.
type record struct {
	fails      int       // consecutive failures in the current ledger
	bucket     Bucket    // which ledger `fails` counts (switching ledgers resets the count)
	openedAt   time.Time // non-zero = breaker is open
	openUntil  time.Time // when the cooldown expires
	halfOpenAt time.Time // non-zero = a half-open trial is in flight; value = when that trial expires
	cooldown   time.Duration
	reason     string
	shapeSkips int // count of requests with a shape error (never opens the breaker, only counts)
	spared     int // count of times the pre-trip diagnostic probe saved it from opening
	// verifying: a "pre-trip diagnostic" is in flight right now. What it blocks is concurrent
	// duplicate diagnostics: two failures arriving at the same binding at once should probe only once.
	verifying bool
}

func (r *record) state(now time.Time) string {
	switch {
	case r.openedAt.IsZero():
		return "closed"
	case now.Before(r.openUntil):
		return "open"
	default:
		return "half-open"
	}
}

// table is the health table itself. **One row per binding** (provider + model), not one row
// per provider -- different models from the same upstream often have one working and one not.
type table struct {
	mu      sync.Mutex
	records map[string]*record
	probes  map[string]probeResult
	ranker  *ranker
	shapes  shapeRegistry
	policy  Policy
	now     func() time.Time // injected clock for tests; always time.Now in production

	file         string
	persistedAt  time.Time
	persistTimer *time.Timer
	onError      func(error)
	loadErr      error // load-time failure, reported once SetErrorHandler is installed
	verify       func(provider, model string) bool
}

func newTable() *table {
	return &table{
		records: map[string]*record{},
		probes:  map[string]probeResult{},
		ranker:  newRanker(),
		policy:  DefaultPolicy(),
		now:     time.Now,
	}
}

// SetPolicy replaces the whole policy; zero items are filled from DefaultPolicy.
//
// Today there is **no production caller** (the zero-value policy is DefaultPolicy), and
// it is **not on the public Breaker interface** -- see the narrowing note in api.go.
func (b *table) SetPolicy(p Policy) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.policy = p.normalized()
}

// SetErrorHandler injects the persistence error outlet; health
// state must not be silently lost because a background write failed.
//
// Like SetVerifier, it is not on the public Breaker interface:
// the only caller is BindEnv (see the narrowing note in api.go).
//
// Load-time errors (a corrupt health.json) go here too: at that moment there
// is no outlet yet, so they are stored first and reported as soon as the
// outlet is installed -- "never silent" is a hard requirement of this repo.
func (b *table) SetErrorHandler(fn func(error)) {
	b.mu.Lock()
	b.onError = fn
	pending := b.loadErr
	b.loadErr = nil
	b.mu.Unlock()
	if pending != nil && fn != nil {
		fn(pending)
	}
}

// SetVerifier injects "the last diagnostic before tripping".
//
// When consecutive failures reach the threshold, do not delist yet: call this function
// for a few active probes, and if the probe says it still works, do not delist and reset
// the count. The reason is a real scene (2026-09-17): smt-deepseek was delisted many times,
// and every `newgate probe` came back fluent -- because what failed in real traffic was the
// **time to first byte** (the classifier chain stuffed a 126KB system prompt into a tight
// 12s budget), while a probe sends a minimal request and can never hit that boundary. When
// the two conclusions disagree, whose evidence is harder? A probe is **active, controllable,
// repeatable**, while passive traffic is a single data point affected by context size
// and queueing. Delisting silently hands the user to another model, and the costs are
// asymmetric, so one more piece of active evidence must be demanded before tripping.
//
// Returns true = this binding is still usable (do not delist); false = confirmed unusable
// (delist as usual). nil (the default) = skip this step, behaving exactly as before.
//
// **It is not on the public Breaker interface** (narrowed 2026-09-21): the only
// caller in the whole repo is the BindEnv below, and putting it on the interface would mean any
// module holding the health-table capability could **silently** remove this safety diagnostic
// with one `SetVerifier(nil)` -- no error, no edge on the dependency graph, and invisible on a
// change. One capability should have one door, and the door belongs to the module that owns it.
func (b *table) SetVerifier(fn func(provider, model string) bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.verify = fn
}

func bindingKey(provider, model string) string { return provider + "\x00" + model }

func (b *table) recordLocked(provider, model string) *record {
	key := bindingKey(provider, model)
	r := b.records[key]
	if r == nil {
		r = &record{}
		b.records[key] = r
	}
	return r
}

// Available answers whether this binding can enter the candidate chain right now.
//
// It is the **only** read path that advances the state machine: when the cooldown expires
// it moves the binding to half-open along the way and issues this round's trial slot. During
// chain building each candidate is asked once, so "let one through" holds naturally here.
func (b *table) Available(provider, model string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.records[bindingKey(provider, model)]
	if r == nil || r.openedAt.IsZero() {
		return true
	}
	now := b.now()
	if now.Before(r.openUntil) {
		return false
	}
	if !r.halfOpenAt.IsZero() && now.Before(r.halfOpenAt) {
		return false // a trial is already in flight; do not let a second one run concurrently
	}
	r.halfOpenAt = now.Add(b.policy.TrialTTL)
	return true
}

// Report reports the outcome of one upstream interaction and returns the verdict.
//
// This is the only recording entry point the data plane needs: classification
// (Classify) and state transitions are both here, and the caller only has to
// describe "what actually happened" truthfully. Before 2026-09-17 the data plane
// decided the semantics of 4xx itself in two separate branches, the two tests
// disagreed, and a shape error delisted mid-chain but not at the chain tail.
func (b *table) Report(provider, model string, in Input) Result {
	detector, shape := b.shapeOf(in.Status, in.Body)
	in.Shape = shape
	v := Classify(in)
	if in.Kind != KindUpstreamSuccess && v.Bucket == BucketNone {
		// Client cancel, the 4xx of "equally wrong anywhere": change nothing, and
		// **do not** create an empty row in the ledger -- otherwise every cancel
		// would stuff a row of all zeros for that binding into `newgate breaker`.
		return Result{Verdict: v}
	}

	b.mu.Lock()
	now := b.now()

	switch {
	case in.Kind == KindUpstreamSuccess:
		// Touch only existing records: a successful binding does not need
		// a row conjured out of nowhere, its latency sample lands elsewhere
		// (ranker), and state transitions can only happen on existing records.
		if r := b.records[bindingKey(provider, model)]; r != nil {
			b.succeedLocked(r, now)
		}
		b.mu.Unlock()
		return Result{Verdict: v}
	case v.Bucket == BucketShape:
		r := b.recordLocked(provider, model)
		r.shapeSkips++
		b.persistLocked()
		b.mu.Unlock()
		// Shape carries the claiming test's name: the data plane
		// uses it for dedicated logging and evidence saving, and needs to know no
		// upstream-proprietary string -- the name is chosen by the detector itself.
		return Result{Verdict: v, Shape: detector}
	}

	r := b.recordLocked(provider, model)
	opened, needsVerify := b.failLocked(r, v.Bucket, now)
	verify := b.verify
	if !needsVerify {
		b.mu.Unlock()
		return Result{Verdict: v, Opened: opened}
	}
	if verify == nil {
		// No diagnostic installed: open the breaker as before. Behavior is exactly as before 2026-09-17,
		// and an assembly without a probe capability will not delist one fewer bad binding because of it.
		b.openLocked(r, b.policy.rule(v.Bucket), now,
			i18n.T("consecutive real-traffic failures ({bucket})",
				i18n.A{"bucket": v.Bucket.ruleName()}))
		b.mu.Unlock()
		return Result{Verdict: v, Opened: true}
	}
	// Threshold reached, but **do not delist yet**: go outside the lock to run one active
	// diagnostic (probing makes a network request and must not be done holding the state
	// machine's lock). Only one goroutine diagnoses; other failures keep counting as usual.
	r.verifying = true
	b.mu.Unlock()

	healthy := verify(provider, model)

	b.mu.Lock()
	defer b.mu.Unlock()
	r = b.records[bindingKey(provider, model)]
	if r == nil {
		return Result{Verdict: v}
	}
	r.verifying = false
	if healthy {
		// The diagnostic says it still works: this consecutive failure was
		// single-point or context-related (time to first byte, queueing, large
		// prefill), not "this one is down". Reset the count, do not delist.
		r.fails = 0
		r.bucket = BucketNone
		r.spared++
		r.reason = i18n.T("pre-trip diagnostic probe proved it healthy ({bucket} counter cleared)",
			i18n.A{"bucket": v.Bucket.ruleName()})
		b.persistLocked()
		return Result{Verdict: v, Spared: true}
	}
	b.openLocked(r, b.policy.rule(v.Bucket), b.now(),
		i18n.T("consecutive real-traffic failures and the diagnostic probe also failed ({bucket})",
			i18n.A{"bucket": v.Bucket.ruleName()}))
	return Result{Verdict: v, Opened: true}
}

// succeedLocked records one real success.
//
// A "success" does not always unseal: a success during the cooldown may come from a
// request whose chain was built earlier (the chain is built when each request starts),
// and it cannot count as evidence of recovery -- otherwise a bad upstream that
// occasionally leaks one success could stay on the chain forever. Only a success after
// the cooldown expires is the half-open trial's conclusion, and only that unseals.
func (b *table) succeedLocked(r *record, now time.Time) {
	r.fails = 0
	if r.openedAt.IsZero() || now.Before(r.openUntil) {
		return
	}
	b.unsealLocked(r)
}

// unsealLocked closes the breaker immediately and resets the backoff. For the authoritative
// conclusion of an **active probe**: it is not subject to the quarantine (see RecordProbe).
func (b *table) unsealLocked(r *record) {
	r.openedAt = time.Time{}
	r.openUntil = time.Time{}
	r.halfOpenAt = time.Time{}
	r.cooldown = 0 // backoff reset: the next cooldown starts from the baseline again
	r.reason = ""
	r.bucket = BucketNone
	b.persistLocked()
}

// failLocked records one failure and returns (did this trip
// the breaker, should a pre-trip diagnostic run first).
//
// A failed half-open trial is **settled on the spot**, without the diagnostic: that trial is itself
// active evidence, and asking again only prolongs a bad upstream's quarantine. The diagnostic
// only serves the one case "passive traffic counted a possibly-alive binding up to the threshold".
func (b *table) failLocked(r *record, bucket Bucket, now time.Time) (bool, bool) {
	rule := b.policy.rule(bucket)
	if rule.Threshold <= 0 {
		return false, false // this ledger never opens the breaker
	}
	if !r.openedAt.IsZero() {
		if now.Before(r.openUntil) {
			// A failure during quarantine comes from a request whose chain was built earlier
			// and does not count as new evidence: count no threshold and extend no quarantine.
			return false, false
		}
		// Cooldown over (half-open): this failure is the trial's conclusion; close the breaker
		// immediately and back off. Do not recount the threshold -- the trial was the vote.
		r.halfOpenAt = time.Time{}
		b.openLocked(r, rule, now,
			i18n.T("half-open trial failed ({bucket})", i18n.A{"bucket": bucket.ruleName()}))
		return true, false
	}
	if r.bucket != bucket {
		// Ledger changed: "consecutive failures" means "the same class of problem
		// in a row", not "various problems adding up to two". A binding taking 429
		// and a connection timeout, one each, is not yet enough to say it is broken.
		r.bucket, r.fails = bucket, 0
	}
	r.fails++
	if r.fails < rule.Threshold {
		b.persistLocked() // persist even without opening: otherwise a restart hands out a free pass
		return false, false
	}
	if r.verifying {
		// A diagnostic is already in flight: count this one only and wait for its conclusion.
		// Two concurrent failures probing at once is paying twice for the same answer.
		b.persistLocked()
		return false, false
	}
	return false, true
}

func (b *table) openLocked(r *record, rule Rule, now time.Time, reason string) {
	switch {
	case r.cooldown <= 0:
		r.cooldown = rule.Cooldown
	case rule.Backoff > 1:
		r.cooldown = time.Duration(float64(r.cooldown) * rule.Backoff)
	}
	if rule.MaxCooldown > 0 && r.cooldown > rule.MaxCooldown {
		r.cooldown = rule.MaxCooldown
	}
	if r.cooldown <= 0 {
		r.cooldown = rule.Cooldown
	}
	r.openedAt = now
	r.openUntil = now.Add(r.cooldown)
	r.halfOpenAt = time.Time{}
	r.reason = reason
	b.persistLocked()
}

// RecordProbe records the four-tier conclusion of an active
// probe and returns the grade and whether it is still tripped.
//
// A probe is an active, independent health request, so it is **not subject to
// the threshold**: a bad conclusion delists on the spot, a good one unseals on the
// spot (even during quarantine, see below). This differs from the real-traffic path
// -- there a single success proves nothing and a single failure settles nothing.
func (b *table) RecordProbe(provider, model string, status, contextBytes int,
	latency, slowAfter time.Duration, probeErr string) (ProbeGrade, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := bindingKey(provider, model)
	now := b.now()
	grade := ProbeFluent
	reason := ""
	switch {
	case status == 200 && latency > slowAfter:
		grade, reason = ProbeLaggy, i18n.T("probe {latency} exceeded {limit}",
			i18n.A{"latency": latency.Round(time.Millisecond).String(), "limit": slowAfter.String()})
	case probeErr != "":
		grade, reason = ProbeUnavailable,
			i18n.T("probe failed: {err}", i18n.A{"err": probeErr})
	case status != 200:
		grade, reason = ProbeUnavailable,
			i18n.T("probe HTTP {status}", i18n.A{"status": status})
	case latency >= 3*time.Second:
		grade = ProbeUsable
	}
	b.probes[key] = probeResult{
		Grade: grade, LatencyMs: latency.Milliseconds(), CheckedAt: now,
	}
	if latency > 0 {
		b.ranker.observe(key, contextBytes, latency)
	}
	r := b.recordLocked(provider, model)

	if reason != "" {
		rule := b.policy.rule(BucketAvailability)
		r.cooldown = 0 // the probe's conclusion is authoritative: backoff restarts from the baseline
		b.openLocked(r, rule, now, reason)
		r.fails = rule.Threshold
		return grade, true
	}
	// A successful active probe is **authoritative evidence**: it just really got through to this
	// binding, so close the breaker on the spot -- **without waiting for the quarantine to end**.
	//
	// The scene on 2026-11-04: the user typed probe by hand while tripped, the conclusion was fluent,
	// yet requests kept getting 404 until the 60s quarantine ended. The minimum-quarantine floor
	// (added 2026-09-17) guards against "the upstream hiccups and a probe releases it immediately",
	// but today probes are **only user-initiated** (`newgate probe` / the test button in the UI),
	// so there is no automatic-probe jitter to worry about; the real-traffic path still goes through
	// the half-open trial, so short-term jitter still cannot break the quarantine. Turning the
	// only recovery action the user has into a placebo costs far more than that bit of jitter risk.
	r.fails = 0
	b.unsealLocked(r)
	return grade, false
}

// ObserveSuccess writes a real request's first-response latency into
// the corresponding context bucket. EWMA gives recent performance more
// weight while avoiding a single jitter permanently changing the order.
func (b *table) ObserveSuccess(provider, model string, contextBytes int, ttft time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ranker.observe(bindingKey(provider, model), contextBytes, ttft)
	// Latency samples come from the hot path, so they cannot be persisted on every request; write at
	// most once every 5 seconds. breaker/probe state changes still call persistLocked immediately.
	const interval = 5 * time.Second
	if time.Since(b.persistedAt) >= interval {
		b.persistLocked()
	} else if b.file != "" && b.persistTimer == nil {
		wait := interval - time.Since(b.persistedAt)
		b.persistTimer = time.AfterFunc(wait, func() {
			b.mu.Lock()
			b.persistTimer = nil
			b.persistLocked()
			b.mu.Unlock()
		})
	}
}

// Flush writes out synchronously the latency samples not yet
// persisted in the throttling window, for the daemon's graceful exit.
func (b *table) Flush() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.persistTimer != nil {
		b.persistTimer.Stop()
		b.persistTimer = nil
	}
	b.persistLocked()
}

// DiscardOpen forgets every delisting, and persists that at once.
//
// It is the other half of the pause switch (see SwitchAdmission): being paused must be *usable*
// the moment it is turned on, not "admissible but still showing a full table of tripped
// bindings". What is discarded is the **verdict**, not the evidence: fail counts, shape skips,
// spared counts and probe results stay, so `newgate breaker` still answers "who is failing".
//
// Only the pause switch calls this (from Admit, once per transition), so it is deliberately not
// on the Breaker port -- the port is "many readers, few writers", and this would be a fourth
// ownerless write door.
func (b *table) DiscardOpen() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	cleared := 0
	for _, r := range b.records {
		if r.openedAt.IsZero() {
			continue
		}
		cleared++
		b.unsealLocked(r)
	}
	if cleared > 0 {
		b.persistLocked()
	}
	return cleared
}

// Score returns the predicted TTFT (milliseconds) for the current context bucket.
// Fully unobserved uses a neutral 6s, sorting after fluent and in the middle of usable.
func (b *table) Score(provider, model string, contextBytes int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if score, ok := b.ranker.score(bindingKey(provider, model), contextBytes); ok {
		return score
	}
	return 6000
}

// Rank encodes the latency tier into the sort key:
// fluent first, unobserved in the middle, usable last.
func (b *table) Rank(provider, model string, contextBytes int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ranker.rank(bindingKey(provider, model), contextBytes)
}

// Snapshot freezes the current state, sorted by provider/model, easy to read and diff.
func (b *table) Snapshot() []Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	out := make([]Status, 0, len(b.records))
	for key := range b.entryKeysLocked() {
		provider, model := splitBindingKey(key)
		r := b.records[key]
		s := Status{Provider: provider, Model: model}
		if r != nil {
			s.Fails = r.fails
			s.Rule = r.bucket.ruleToken()
			s.ShapeSkips = r.shapeSkips
			s.Spared = r.spared
			s.CooldownMs = r.cooldown.Milliseconds()
			s.Reason = r.reason
			if !r.openedAt.IsZero() {
				s.Open = true
				s.OpenFor = now.Sub(r.openedAt)
				s.OpenedAt = r.openedAt
				s.OpenUntil = r.openUntil
			}
			s.State = r.state(now)
			s.Trial = !r.halfOpenAt.IsZero() && now.Before(r.halfOpenAt)
		}
		if s.State == "" {
			s.State = "closed"
		}
		if p, ok := b.probes[key]; ok {
			s.Grade, s.Latency, s.Checked = p.Grade, p.LatencyMs, p.CheckedAt
		}
		b.ranker.fill(&s, key)
		s.Rank = b.ranker.rank(key, 0)
		out = append(out, s)
	}
	sortStatuses(out)
	return out
}

// entryKeysLocked is the only test for "which bindings deserve to appear in the snapshot/on disk".
//
// It used to be openedAt ∪ scores ∪ probes -- which missed bindings
// that "failed but were not yet delisted", so `newgate breaker` could not see
// "failed once, breaker not open yet", and a restart lost that count too. Now
// the whole records table is in: as long as anything happened, the row exists.
func (b *table) entryKeysLocked() map[string]bool {
	keys := map[string]bool{}
	for key := range b.records {
		keys[key] = true
	}
	for key := range b.probes {
		keys[key] = true
	}
	for _, key := range b.ranker.keys() {
		keys[key] = true
	}
	return keys
}

func (b *table) reportLocked(err error) {
	if b.onError != nil {
		b.onError(err)
	}
}

func splitBindingKey(key string) (string, string) {
	for i := range key {
		if key[i] == 0 {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}
