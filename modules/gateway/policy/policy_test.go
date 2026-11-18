package policy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// --- Stubs ---------------------------------------------------------------

type named struct {
	name string
	why  string
}

func (n named) Name() string { return n.name }
func (n named) Why() string  { return n.why }

type admitter struct {
	named
	asked   int
	ok      bool
	reason  string
	rank    int
	onAdmit func()
}

func (a *admitter) Admit(provider, model string) (bool, string) {
	a.asked++
	if a.onAdmit != nil {
		a.onAdmit()
	}
	return a.ok, a.reason
}

func (a *admitter) Rank(string, string, int) int { return a.rank }

type adjudicator struct {
	named
	outcomes  []Outcome // what Judge received (the ones to judge)
	observeds []Outcome // what Observe received (success observations)
	verdict   func(Outcome) Verdict
}

func (a *adjudicator) Judge(o Outcome) Verdict {
	a.outcomes = append(a.outcomes, o)
	if a.verdict == nil {
		return Verdict{}
	}
	return a.verdict(o)
}

func (a *adjudicator) Observe(o Outcome) { a.observeds = append(a.observeds, o) }

type controller struct {
	named
	doc  map[string]json.RawMessage
	acks []ProbeAck
	got  []ProbeObservation
}

func (c *controller) Doc() map[string]json.RawMessage { return c.doc }
func (c *controller) ObserveProbes(obs []ProbeObservation) []ProbeAck {
	c.got = append(c.got, obs...)
	return c.acks
}

type flusher struct {
	named
	n     int
	flush func() error
}

func (f *flusher) Flush() error {
	f.n++
	if f.flush == nil {
		return nil
	}
	return f.flush()
}

// namer recognizes only its own key - the real implementation (breaker) is written the same
// way: claiming someone else's counter would displace their description.
type namer struct {
	named
	prefix string
	group  string // identity: a stable ASCII token (for sorting/dedup)
	label  string // wording: the anchor shown to people
	hint   string
}

func (n namer) MetricGroup(key string) (string, string) {
	if strings.HasPrefix(key, n.prefix) {
		return n.group, n.label
	}
	return "", ""
}

func (n namer) MetricHint(key string) string {
	if strings.HasPrefix(key, n.prefix) {
		return n.hint
	}
	return ""
}

type envrec struct {
	logs   []string
	probes []string
}

func (e *envrec) Logf(format string, args ...any) { e.logs = append(e.logs, format) }
func (e *envrec) Probe(provider, model string) bool {
	e.probes = append(e.probes, provider+"/"+model)
	return true
}

type binder struct {
	named
	got Env
}

func (b *binder) BindEnv(env Env) { b.got = env }

// --- The minimal system --------------------------------------------------

// TestEmptyRegistryIsTheMinimalSystem pins the defaults of all four ports **with no
// contributors**.
//
// This is the fallback direction of the whole inversion: gateway must work on its own with
// "not a single policy installed". Get one default backwards (say admission defaults to
// false) and the gateway degrades into "you must install a breaker to forward at all" -
// a far worse failure than a broken breaker.
func TestEmptyRegistryIsTheMinimalSystem(t *testing.T) {
	r := New()
	if !r.Empty() {
		t.Fatal("a new ledger should be empty")
	}
	if ok, why := r.Admit("p", "m"); !ok || why != "" {
		t.Errorf("minimal system: all candidates usable, got (%v, %q)", ok, why)
	}
	if n := r.Rank("p", "m", 1024); n != NeutralRank {
		t.Errorf("minimal system: no reordering (neutral key), got %d", n)
	}
	if v := r.Judge(Outcome{Kind: ConnectionFailed}); v.Stop || v.Attribute ||
		len(v.Metrics) != 0 || v.Note != "" || v.Evidence != nil {
		t.Errorf("minimal system: no opinion at outcome time, got %+v", v)
	}
	if doc := r.Doc(); doc != nil {
		t.Errorf("minimal system: no extra control-plane section, got %v", doc)
	}
	if acks := r.ObserveProbes([]ProbeObservation{{Provider: "p"}}); acks != nil {
		t.Errorf("minimal system: probe conclusion accepted but not answered, got %v", acks)
	}
	if id, label := r.MetricGroup("anything"); id != "" || label != "" {
		t.Errorf("minimal system: claims no counters, got (%q, %q)", id, label)
	}
	if h := r.MetricHint("anything"); h != "" {
		t.Errorf("minimal system: gives no note, got %q", h)
	}
	r.Flush(nil) // no Flusher: must not panic
	r.BindEnv(&envrec{})
	r.Observe(Outcome{Kind: Succeeded})
}

// --- Rejection at registration time --------------------------------------

// TestRegisterRejectsAnonymousAndDuplicate pins the two kinds of rejection at registration.
//
// "First come, first served" is the worst behavior this feature could have: "who took this
// name" would be invisible in the listing, and once the duplicate check is deferred to the
// read side (dispatch/report time) the error turns into a silent overwrite at runtime.
func TestRegisterRejectsAnonymousAndDuplicate(t *testing.T) {
	r := New()
	if _, err := r.Register(named{}); err == nil {
		t.Error("a contributor without a name should be rejected")
	}
	if _, err := r.Register(nil); err == nil {
		t.Error("a nil contributor should be rejected")
	}
	if _, err := r.Register(named{name: "a"}); err != nil {
		t.Fatal(err)
	}
	_, err := r.Register(named{name: "a"})
	if err == nil || i18n.ID(err) != "policy: contributor {name} is already registered" {
		t.Errorf("a duplicate registration under the same name should be rejected with a reason, got %v", err)
	}
}

// TestRegisterRejectsSecondAdmitter pins "admission has at most one contributor".
//
// The reason is not conservatism: Admit **is not a query**. When the cooldown expires, the
// health table issues a "half-open trial slot", one slot lets exactly one real request
// through, and resolve asks each candidate exactly once. Multiple Admitters ANDed together
// would make "is a slot issued" depend on **another unrelated contributor**'s return value:
// the first issues the slot, the second denies, and that binding can neither enter the chain
// nor is anyone trying it, for the whole TTL.
func TestRegisterRejectsSecondAdmitter(t *testing.T) {
	r := New()
	if _, err := r.Register(&admitter{named: named{name: "first"}}); err != nil {
		t.Fatal(err)
	}
	_, err := r.Register(&admitter{named: named{name: "second"}})
	if err == nil || i18n.ID(err) != "policy: only one contributor may be an admitter; {name} is already taken by {holder} (it carries side effects, see the Admitter docs)" {
		t.Fatalf("the second admission should be rejected, got %v", err)
	}
	// The ledger must be clean after a rejection: a half registration must leave no trace.
	if len(r.Filters()) != 1 {
		t.Errorf("contributors after the rejection = %d, want 1", len(r.Filters()))
	}
}

// TestRegisterRejectsDocKeyCollision pins field ownership in the status document.
//
// Two contributors reporting the same top-level field can only end one way: one of them is
// silently overwritten - and the field name is a cross-version wire contract (old CLIs read
// exactly these keys), so a silent overwrite makes the upgrade window depend on registration
// order. So a collision must fail right there, at **registration time**.
func TestRegisterRejectsDocKeyCollision(t *testing.T) {
	r := New()
	first := &controller{named: named{name: "a"}, doc: map[string]json.RawMessage{
		"breakers": json.RawMessage(`[]`)}}
	if _, err := r.Register(first); err != nil {
		t.Fatal(err)
	}
	second := &controller{named: named{name: "b"}, doc: map[string]json.RawMessage{
		"breakers": json.RawMessage(`[]`)}}
	_, err := r.Register(second)
	if err == nil || i18n.ID(err) != "policy: state document field {key} is already taken by {owner}" {
		t.Fatalf("the same state field claimed by two contributors should be rejected, got %v", err)
	}
	// Non-colliding ones get in.
	third := &controller{named: named{name: "c"}, doc: map[string]json.RawMessage{
		"quotas": json.RawMessage(`{}`)}}
	if _, err := r.Register(third); err != nil {
		t.Fatalf("fields that do not collide still cannot get in: %v", err)
	}
}

// TestReleaseIsScopedAndIdempotent pins the semantics of release: the handle you got
// withdraws only your own registration, and calling it twice is harmless (Stop must be
// idempotent).
func TestReleaseIsScopedAndIdempotent(t *testing.T) {
	r := New()
	first := &admitter{named: named{name: "a"}, ok: true}
	release, err := r.Register(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if !r.Empty() {
		t.Error("the ledger should be empty after unregistering")
	}
	if err := release(); err != nil {
		t.Errorf("a repeated unregister must be harmless (Stop must be idempotent), got %v", err)
	}

	// Release → register the same name again: the old handle must not remove the new
	// registration. (The second half uses a plain contributor rather than an admitter -
	// admission allows only one, which is a separate rule.)
	if _, err := r.Register(named{name: "a"}); err != nil {
		t.Fatal(err)
	}
	stale, err := r.Register(named{name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if len(r.Filters()) != 2 {
		t.Errorf("a stale handle deleted someone else's registration: %d remain now", len(r.Filters()))
	}
	_ = stale
}

// --- Composition per phase -----------------------------------------------

// TestAdmitAsksExactlyOncePerCandidate pins "each candidate is asked exactly once".
//
// The count is not a style question: it is the premise that makes half-open trial slots work
// (issue one slot = let one real request through). resolve guarantees it on the integration
// side (it calls once per candidate); what is pinned here is that the ledger itself never
// asks extra - say calling twice in order to "compose several sources".
func TestAdmitAsksExactlyOncePerCandidate(t *testing.T) {
	r := New()
	a := &admitter{named: named{name: "a"}, ok: true}
	if _, err := r.Register(a); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{"p1/m1", "p2/m2", "p3/m3"} {
		parts := strings.SplitN(candidate, "/", 2)
		if ok, _ := r.Admit(parts[0], parts[1]); !ok {
			t.Fatalf("%s should be usable", candidate)
		}
	}
	if a.asked != 3 {
		t.Errorf("three candidates asked %d times, want 3", a.asked)
	}
}

// TestJudgeMergesVerdictsAndToleratesPanic pins phase B's merge rules and fail-open.
//
// Composition is hardcoded in one place (merge) instead of left to callers, because a rule
// like "whose verdict wins" inevitably drifts once it is written twice. fail-open points the
// same way as the kernel default: one bad plugin must never take the whole chain's judgment.
func TestJudgeMergesVerdictsAndToleratesPanic(t *testing.T) {
	r := New()
	stops := &adjudicator{named: named{name: "stops"}, verdict: func(Outcome) Verdict {
		return Verdict{Stop: true, Metrics: []string{"b.metric"}, Note: "  [I stop]"}
	}}
	attrs := &adjudicator{named: named{name: "attrs"}, verdict: func(Outcome) Verdict {
		return Verdict{Attribute: true, Metrics: []string{"a.metric", "b.metric"},
			Evidence: &Evidence{Tag: "t", Subject: "s"}}
	}}
	boom := &adjudicator{named: named{name: "boom"}, verdict: func(Outcome) Verdict {
		panic("the policy crashed")
	}}
	for _, f := range []Filter{stops, attrs, boom} {
		if _, err := r.Register(f); err != nil {
			t.Fatal(err)
		}
	}
	v := r.Judge(Outcome{Provider: "p", Model: "m", Kind: RejectedStatus, Status: 400})
	if !v.Stop || !v.Attribute {
		t.Errorf("Stop/Attribute is an OR: any single stop stops it, got %+v", v)
	}
	if strings.Join(v.Metrics, ",") != "a.metric,b.metric" {
		t.Errorf("Metrics are deduplicated and sorted (output must be stable), got %v", v.Metrics)
	}
	if v.Note != "  [I stop]" {
		t.Errorf("Note pieces are joined in registration order, got %q", v.Note)
	}
	if v.Evidence == nil || v.Evidence.Tag != "t" {
		t.Errorf("Evidence takes the first non-nil, got %+v", v.Evidence)
	}
	if len(attrs.outcomes) != 1 {
		t.Errorf("every judge should be asked, got %d", len(attrs.outcomes))
	}
}

// TestObserveDeliversToEveryAdjudicator pins the fan-out of successful observations
// (latency samples pick their bucket from it).
func TestObserveDeliversToEveryAdjudicator(t *testing.T) {
	r := New()
	a, b := &adjudicator{named: named{name: "a"}}, &adjudicator{named: named{name: "b"}}
	for _, f := range []Filter{a, b} {
		if _, err := r.Register(f); err != nil {
			t.Fatal(err)
		}
	}
	r.Observe(Outcome{Kind: Succeeded, RequestBytes: 1234, TTFT: 20 * time.Millisecond})
	if len(a.observeds) != 1 || a.observeds[0].RequestBytes != 1234 || len(b.observeds) != 1 {
		t.Errorf("a success observation was not delivered to everyone: a=%v b=%v", a.observeds, b.observeds)
	}
	if len(a.outcomes) != 0 {
		t.Errorf("Observe must not run a verdict along the way (that would make a policy book a success as a failure): %v", a.outcomes)
	}
}

// TestDocMergesTopLevelKeys pins phase C's merge: everyone's fields go into the **top
// level** rather than into an extra object (the old CLI reads exactly those top-level keys).
func TestDocMergesTopLevelKeys(t *testing.T) {
	r := New()
	for _, c := range []*controller{
		{named: named{name: "a"}, doc: map[string]json.RawMessage{"breakers": json.RawMessage(`[1]`)}},
		{named: named{name: "b"}, doc: map[string]json.RawMessage{"quotas": json.RawMessage(`{}`)}},
	} {
		if _, err := r.Register(c); err != nil {
			t.Fatal(err)
		}
	}
	doc := r.Doc()
	if len(doc) != 2 {
		t.Fatalf("document merge result = %v", doc)
	}
	if string(doc["breakers"]) != "[1]" {
		t.Errorf("a contributor's value was changed: %s", doc["breakers"])
	}
}

// TestObserveProbesFansOut pins the fan-out of probe conclusions and the collection of acks.
func TestObserveProbesFansOut(t *testing.T) {
	r := New()
	a := &controller{named: named{name: "a"}, acks: []ProbeAck{{Opened: true, Note: "on"}}}
	b := &controller{named: named{name: "b"}}
	for _, c := range []*controller{a, b} {
		if _, err := r.Register(c); err != nil {
			t.Fatal(err)
		}
	}
	obs := []ProbeObservation{{Provider: "p", Model: "m"}}
	acks := r.ObserveProbes(obs)
	if len(a.got) != 1 || len(b.got) != 1 {
		t.Errorf("the conclusion was not delivered to every control-plane contributor: a=%d b=%d", len(a.got), len(b.got))
	}
	if len(acks) != 1 || !acks[0].Opened || acks[0].Note != "on" {
		t.Errorf("the ack was not collected: %+v", acks)
	}
}

// TestMetricNamingTakesTheFirstClaim pins the "claim" semantics of observability naming:
// the first to say it recognizes the counter wins, and when nobody does the result is an
// empty string (the caller falls back to the kernel default).
func TestMetricNamingTakesTheFirstClaim(t *testing.T) {
	r := New()
	for _, n := range []namer{
		{named: named{name: "a"}, prefix: "breaker.", group: "breaker", label: "breaker", hint: "breaker open"},
		{named: named{name: "b"}, prefix: "breaker.", group: "other", label: "other", hint: "other note"},
	} {
		if _, err := r.Register(n); err != nil {
			t.Fatal(err)
		}
	}
	// Assert on **identity**: it is the language-independent half, and the half sorting
	// uses. The rendering follows the language; pinning it here would only turn red when a
	// translation changes.
	if id, label := r.MetricGroup("breaker.opened"); id != "breaker" || label != "breaker" {
		t.Errorf("the group takes the first claimant, got (%q, %q)", id, label)
	}
	if h := r.MetricHint("breaker.opened"); h != "breaker open" {
		t.Errorf("the note takes the first claimant, got %q", h)
	}
	if id, label := r.MetricGroup("chain.failover"); id != "" || label != "" {
		t.Errorf("with no claimant it should give an empty string (caller falls back), got (%q, %q)", id, label)
	}
	if h := r.MetricHint("chain.failover"); h != "" {
		t.Errorf("with no claimant the note should be an empty string (caller falls back), got %q", h)
	}
}

// TestFlushReportsFailuresWithoutBlocking pins phase D: a persist failure must be spoken
// out (never silent) but must never block shutdown.
func TestFlushReportsFailuresWithoutBlocking(t *testing.T) {
	r := New()
	boom := errors.New("disk full")
	bad := &flusher{named: named{name: "bad"}, flush: func() error { return boom }}
	good := &flusher{named: named{name: "good"}}
	for _, f := range []Filter{bad, good} {
		if _, err := r.Register(f); err != nil {
			t.Fatal(err)
		}
	}
	var logged []string
	r.Flush(func(format string, args ...any) {
		logged = append(logged, format+"|"+strings.Join(toStrings(args), ","))
	})
	if bad.n != 1 || good.n != 1 {
		t.Errorf("everyone should be persisted once: bad=%d good=%d", bad.n, good.n)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "bad") {
		t.Errorf("a persist failure must be reported and named: %v", logged)
	}
}

// TestBindEnvReachesOnlyBinders pins the delivery surface of runtime capabilities: only to
// those implementing EnvBinder, and the capability is handed over **by the data plane at
// Serve time** (see the three-phase rule in the package comment).
func TestBindEnvReachesOnlyBinders(t *testing.T) {
	r := New()
	b := &binder{named: named{name: "binder"}}
	plain := &adjudicator{named: named{name: "plain"}}
	for _, f := range []Filter{b, plain} {
		if _, err := r.Register(f); err != nil {
			t.Fatal(err)
		}
	}
	env := &envrec{}
	r.BindEnv(env)
	if b.got != Env(env) {
		t.Error("someone implementing EnvBinder did not get the runtime capability")
	}
}

func toStrings(in []any) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
