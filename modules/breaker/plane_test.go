package breaker

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	gwpolicy "github.com/rzbdz/newgate-ext/modules/gateway/policy"
)

// TestPlaneJudgeTruthTable is the truth table of the **verdict table**: the data plane
// reports a fact, and policy answers "stop or not / charge it / which numbers / what trace".
//
// This table and TestClassify in modules/breaker/classify_test.go are two faces of the
// same semantics: that one pins "which ledger this goes to" (Classify's pure verdict),
// this one pins "what that ledger looks like on the data plane" (Attribute / Stop /
// Metrics / Evidence). Before the 2026-09-18 inversion, the latter half was scattered
// across three branches in modules/gateway/forward (one switch decided failures++,
// another if decided whether to keep evidence) -- exactly the scene of "policy
// vocabulary leaking into the kernel". Now it is all here, readable line by line.
//
// A fresh table per row (Judge writes state: failures below the threshold are persisted too).
func TestPlaneJudgeTruthTable(t *testing.T) {
	shapeErr := []byte(shapeBody)

	tests := []struct {
		name string
		out  gwpolicy.Outcome
		want gwpolicy.Verdict
	}{
		// -- success: do nothing (latency samples take the other path, Observe) --
		{"upstream success",
			gwpolicy.Outcome{Kind: gwpolicy.Succeeded},
			gwpolicy.Verdict{}},

		// -- connection-class failure: charge it; keep going if there is a next hop --
		{"connection failure (mid-chain)",
			gwpolicy.Outcome{Kind: gwpolicy.ConnectionFailed},
			gwpolicy.Verdict{Attribute: true}},
		{"connection failure (chain tail)",
			gwpolicy.Outcome{Kind: gwpolicy.ConnectionFailed, IsLast: true},
			gwpolicy.Verdict{Attribute: true, Stop: true}},
		{"connection failure (response already started)",
			gwpolicy.Outcome{Kind: gwpolicy.ConnectionFailed, ResponseStarted: true},
			gwpolicy.Verdict{Attribute: true, Stop: true}},

		// -- stream broken mid-way: what is written out cannot be taken back, so the hop
		// never changes, but it is a real availability problem (the old code only recorded
		// it in the health table, not in the failures count -- see the note on Judge) --
		{"stream broke mid-way",
			gwpolicy.Outcome{Kind: gwpolicy.StreamCut, ResponseStarted: true},
			gwpolicy.Verdict{Attribute: true, Stop: true}},

		// -- 400 + shape hit: not charged to this one, but a trace is left (described by Tag/Subject) --
		{"shape 400 (mid-chain)",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 400, Body: shapeErr},
			gwpolicy.Verdict{
				Metrics:  []string{"breaker.skipped.shape_error"},
				Evidence: &gwpolicy.Evidence{Tag: "shape-400", Subject: "test-shape", Archive: true},
			}},
		{"shape 400 (chain tail)",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 400, Body: shapeErr, IsLast: true},
			gwpolicy.Verdict{
				Stop:     true,
				Metrics:  []string{"breaker.skipped.shape_error"},
				Evidence: &gwpolicy.Evidence{Tag: "shape-400", Subject: "test-shape", Archive: true},
			}},

		// -- 400 (non-shape): the request itself is bad. fallback_on_400 only says
		// "should we try another upstream", it does not mean this is this one's account --
		{"non-shape 400 (fallback off)",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 400, Body: []byte(`{"error":"schema"}`)},
			gwpolicy.Verdict{Stop: true}},
		{"non-shape 400 (fallback on, mid-chain)",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 400,
				Body: []byte(`{"error":"schema"}`), FallbackOn400: true},
			gwpolicy.Verdict{}},
		{"non-shape 400 (fallback on, chain tail)",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 400,
				Body: []byte(`{"error":"schema"}`), FallbackOn400: true, IsLast: true},
			gwpolicy.Verdict{Stop: true}},

		// -- rate limit / config / availability: three ledgers, all charged to it --
		{"429 (mid-chain)",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 429},
			gwpolicy.Verdict{Attribute: true}},
		{"429 (chain tail)",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 429, IsLast: true},
			gwpolicy.Verdict{Attribute: true, Stop: true}},
		// The config ledger's threshold is 1 (a deterministic error; a second try is pure waste),
		// so the first one delists, and this row therefore also brings out breaker.opened.
		{"401 (config ledger, dropped after one)",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 401},
			gwpolicy.Verdict{Attribute: true, Metrics: []string{"breaker.opened"}}},
		{"403 (config ledger, dropped after one)",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 403},
			gwpolicy.Verdict{Attribute: true, Metrics: []string{"breaker.opened"}}},
		{"404 (config ledger, dropped after one)",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 404},
			gwpolicy.Verdict{Attribute: true, Metrics: []string{"breaker.opened"}}},
		{"402 (insufficient balance: a deterministic config problem, same ledger as 401/403/404, dropped after one)",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 402},
			gwpolicy.Verdict{Attribute: true, Metrics: []string{"breaker.opened"}}},
		{"408",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 408},
			gwpolicy.Verdict{Attribute: true}},
		{"409",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 409},
			gwpolicy.Verdict{Attribute: true}},
		{"500",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 500},
			gwpolicy.Verdict{Attribute: true}},
		{"599 (>=500 always counts as availability)",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 599},
			gwpolicy.Verdict{Attribute: true}},

		// -- other 4xx: the request itself is bad, ramming every upstream is pointless --
		{"405", gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 405},
			gwpolicy.Verdict{Stop: true}},
		{"418", gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 418},
			gwpolicy.Verdict{Stop: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table, _ := clocked()
			tt.out.Provider, tt.out.Model, tt.out.Binding = "p", "m", "p/m"
			got := (&plane{t: table}).Judge(tt.out)
			if got.Stop != tt.want.Stop || got.Attribute != tt.want.Attribute {
				t.Errorf("Judge(%+v) = {Stop:%v Attribute:%v}, want {Stop:%v Attribute:%v}",
					tt.out, got.Stop, got.Attribute, tt.want.Stop, tt.want.Attribute)
			}
			if strings.Join(got.Metrics, ",") != strings.Join(tt.want.Metrics, ",") {
				t.Errorf("the counter of Judge(%+v) = %v, want %v", tt.out, got.Metrics, tt.want.Metrics)
			}
			switch {
			case tt.want.Evidence == nil && got.Evidence != nil:
				t.Errorf("Judge(%+v) kept evidence it should not have: %+v", tt.out, got.Evidence)
			case tt.want.Evidence != nil && got.Evidence == nil:
				t.Errorf("Judge(%+v) should keep evidence but did not", tt.out)
			case tt.want.Evidence != nil && *got.Evidence != *tt.want.Evidence:
				t.Errorf("the evidence of Judge(%+v) = %+v, want %+v", tt.out, *got.Evidence, *tt.want.Evidence)
			}
		})
	}
}

// TestPlaneJudgeCountsFailuresIntoTheLedger pins the **recording** behind the verdict:
// outcomes charged to it really entered the health table, and those not charged really did not.
//
// It is separate from the truth table: that one checks "what was answered", this one
// checks "did state move after the answer" -- the two drifted once (before 2026-09-17,
// delisted mid-chain but not at the chain tail, because the test was written twice).
func TestPlaneJudgeCountsFailuresIntoTheLedger(t *testing.T) {
	counting := []struct {
		name string
		out  gwpolicy.Outcome
	}{
		{"connection failure", gwpolicy.Outcome{Kind: gwpolicy.ConnectionFailed}},
		{"429", gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 429}},
		{"404", gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 404}},
		{"500", gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 500}},
	}
	for _, tt := range counting {
		t.Run("booked/"+tt.name, func(t *testing.T) {
			table, _ := clocked()
			tt.out.Provider, tt.out.Model = "p", "m"
			(&plane{t: table}).Judge(tt.out)
			if got := rowOf(table, "p", "m"); got.Fails == 0 {
				t.Errorf("%s did not make it into the ledger: %+v", tt.name, got)
			}
		})
	}

	notCounting := []struct {
		name string
		out  gwpolicy.Outcome
	}{
		{"shape 400", gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 400, Body: []byte(shapeBody)}},
		{"non-shape 400", gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 400, Body: []byte(`{"error":"x"}`)}},
		{"405", gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 405}},
	}
	for _, tt := range notCounting {
		t.Run("not booked/"+tt.name, func(t *testing.T) {
			table, _ := clocked()
			tt.out.Provider, tt.out.Model = "p", "m"
			res := (&plane{t: table}).Judge(tt.out)
			if res.Attribute {
				t.Errorf("%s was judged as bookable", tt.name)
			}
			if got := rowOf(table, "p", "m"); got.Fails != 0 || got.Open {
				t.Errorf("%s went into the availability ledger: %+v", tt.name, got)
			}
		})
	}
}

// TestPlaneJudgeReportsOpenAndSpared pins "the two human
// sentences and two counters at the moment of delisting".
//
// They used to live in the data plane's `s.report()` and `breakerNote()` (one switch
// decided the counter, one function assembled the log suffix). After the inversion
// both come home together: **what delisting means** only the health table knows.
func TestPlaneJudgeReportsOpenAndSpared(t *testing.T) {
	t.Run("drops at the threshold", func(t *testing.T) {
		table, _ := clocked()
		p := &plane{t: table}
		out := gwpolicy.Outcome{Provider: "p", Model: "m", Binding: "p/m",
			Kind: gwpolicy.ConnectionFailed}

		first := p.Judge(out)
		if strings.Join(first.Metrics, ",") != "" || first.Note != "" {
			t.Errorf("the first request must not drop it (threshold 2): metrics=%v note=%q", first.Metrics, first.Note)
		}
		second := p.Judge(out)
		if strings.Join(second.Metrics, ",") != "breaker.opened" {
			t.Errorf("the second request should report breaker.opened, got %v", second.Metrics)
		}
		if !strings.Contains(second.Note, "breaker opened") {
			t.Errorf("the request that drops it must say why (otherwise the log has failures but no verdict): %q", second.Note)
		}
		if !second.Attribute {
			t.Error("the request that drops it naturally counts against it")
		}
	})

	t.Run("a diagnostic probe that stops it means no drop", func(t *testing.T) {
		table, _ := clocked()
		// Install the "pre-trip diagnostic": say it is still usable.
		table.SetVerifier(func(provider, model string) bool { return true })
		p := &plane{t: table}
		out := gwpolicy.Outcome{Provider: "p", Model: "m", Binding: "p/m",
			Kind: gwpolicy.ConnectionFailed}

		p.Judge(out)
		second := p.Judge(out)
		if strings.Join(second.Metrics, ",") != "breaker.spared" {
			t.Errorf("should report breaker.spared, got %v", second.Metrics)
		}
		// "The log shows failures, `newgate breaker` does not have it" is a combination
		// that sends people looking the wrong way, so this line must explain itself.
		if !strings.Contains(second.Note, "still healthy") {
			t.Errorf("the request that rescued it must say why: %q", second.Note)
		}
		if !table.Available("p", "m") {
			t.Error("the diagnostic says usable, yet the binding is not on the chain")
		}
	})
}

// TestPlaneAdmitIsAvailablePlusAReason pins phase A:
// the admission test and the reason shown to humans.
//
// The reason text used to be hardcoded in modules/config/resolve/chain.go -- the
// admission test belongs to whoever provides it, and resolve only recognizes the
// shape "return one reason". It has come home now, and that sentence in `newgate
// tier`, "why is it not the one I expected", is unchanged word for word.
//
// What is asserted is the reason's **source-language original** (the i18n key is
// that sentence): switching language or rewording must not make this test go red.
func TestPlaneAdmitIsAvailablePlusAReason(t *testing.T) {
	table, advance := clocked()
	p := &plane{t: table}

	if ok, why := p.Admit("p", "m"); !ok || why != "" {
		t.Errorf("when nothing is wrong it should be usable with no reason, got (%v, %q)", ok, why)
	}

	// Delisted: the config ledger delists on the first one.
	p.Judge(gwpolicy.Outcome{Provider: "p", Model: "m", Kind: gwpolicy.RejectedStatus, Status: 401})
	if ok, why := p.Admit("p", "m"); ok || why != "circuit open" {
		t.Errorf("after a drop it should be unusable with the reason 'circuit open', got (%v, %q)", ok, why)
	}

	// Cooldown expired: let one half-open trial through. **This is why Admitter allows only
	// one** -- this path has a side effect, and resolve asks exactly once per candidate.
	advance(6 * time.Minute)
	if ok, _ := p.Admit("p", "m"); !ok {
		t.Fatal("after the cooldown it should let one half-open trial through")
	}
	if ok, _ := p.Admit("p", "m"); ok {
		t.Error("the half-open slot is granted once only: a second concurrent request must not get in either")
	}
}

// TestPlaneDocKeepsTheWireKey pins phase C's **cross-version contract**:
// the field name the health table reports is still the top-level
// `breakers`, and the shape is still an array of status.Status.
//
// During graceful handover old and new binaries run side by side: the old CLI reads exactly this
// key and these JSON keys. The policy ledger merging contributors' fields into the top level of
// the status document (rather than stuffing them into an extra object) is precisely for this.
func TestPlaneDocKeepsTheWireKey(t *testing.T) {
	table, _ := clocked()
	p := &plane{t: table}
	p.Judge(gwpolicy.Outcome{Provider: "p", Model: "m", Kind: gwpolicy.RejectedStatus, Status: 401})

	doc := p.Doc()
	raw, ok := doc["breakers"]
	if !ok {
		t.Fatalf("the status document has no breakers key: %v", doc)
	}
	var got []Status
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("breakers is not an array of Status: %v", err)
	}
	if len(got) != 1 || got[0].Provider != "p" || !got[0].Open {
		t.Errorf("wrong snapshot: %+v", got)
	}
	// Not one field the old CLI reads is missing.
	//
	// The value of `rule` is a **machine marker** (a language-independent stable
	// marker, see ruleToken in classify.go): the old CLI displays it as is, without
	// parsing. Switching it to a translation would really break the handover window.
	if !strings.Contains(string(raw), `"open":true`) ||
		!strings.Contains(string(raw), `"rule":"config"`) {
		t.Errorf("the wire key changed (it would break old CLIs inside the graceful-handoff window): %s", raw)
	}
}

// TestPlaneObserveProbesIsAuthoritative pins "a probe is authoritative evidence":
// it is not subject to the consecutive-failure threshold -- a bad conclusion delists
// on the spot, a good one unseals on the spot (even during quarantine, 2026-11-04).
func TestPlaneObserveProbesIsAuthoritative(t *testing.T) {
	table, _ := clocked()
	p := &plane{t: table}

	// The threshold is 2, but one failed probe is enough.
	acks := p.ObserveProbes([]gwpolicy.ProbeObservation{{
		Provider: "p", Model: "m", Status: 500, Error: "connect refused",
		SlowAfter: time.Second,
	}})
	if len(acks) != 1 || !acks[0].Opened {
		t.Fatalf("a failed probe should drop it on the spot: %+v", acks)
	}
	if !strings.Contains(acks[0].Note, "at least 60s") {
		t.Errorf("that drop sentence is about the health table's backoff semantics, which only this place knows: %q", acks[0].Note)
	}
	if ok, _ := p.Admit("p", "m"); ok {
		t.Error("the probe says unusable, yet the binding is still on the chain")
	}

	// A good probe -> released on the spot, **without waiting for the
	// cooldown to expire** (a user who just probed should be able to use it).
	acks = p.ObserveProbes([]gwpolicy.ProbeObservation{{
		Provider: "p", Model: "m", Status: 200, Latency: 100 * time.Millisecond,
		SlowAfter: time.Second,
	}})
	if len(acks) != 1 || acks[0].Opened {
		t.Errorf("a good probe must not report opened: %+v", acks)
	}
	if ok, why := p.Admit("p", "m"); !ok {
		t.Errorf("the probe says it works, yet the binding did not return to the chain: %q", why)
	}
}

// TestPlaneBindEnvWiresTheTwoCapabilities pins the delivery of runtime capabilities: the log
// outlet is wired to persistence errors (never silent), and probing to the "pre-trip diagnostic".
//
// Both used to be injected **in reverse** by the data plane into the health table
// (SetErrorHandler / SetVerifier called by forward in Start) -- the direction was
// wrong, which was the hard evidence that "who owns the capability" was unclear.
func TestPlaneBindEnvWiresTheTwoCapabilities(t *testing.T) {
	table, _ := clocked()
	var logged []string
	probed := 0
	(&plane{t: table}).BindEnv(envStub{
		// **Render** this line and then capture it: BindEnv now passes `Logf("%s",
		// message)` (formatting is done by i18n, so a `%` in a translation must not
		// deform the log), and capturing only the format would show a literal `%s`.
		logf:  func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
		probe: func(provider, model string) bool { probed++; return false },
	})

	// Persistence failures must be spoken. Call that outlet directly (triggering the
	// write here is inconvenient, and what this checks is "did BindEnv wire it up").
	if table.onError == nil {
		t.Fatal("persistence errors have no exit - health state must not be silently lost because a write failed")
	}
	table.onError(errForTest{})
	if len(logged) != 1 || !strings.Contains(logged[0], "[breaker]") {
		t.Errorf("the error did not reach the data-plane log exit (and that sentence is health-table knowledge, "+
			"which should be provided here): %v", logged)
	}

	// The pre-trip diagnostic is wired to the data plane's probe capability.
	if table.verify == nil {
		t.Fatal("the diagnostic was not wired before tripping")
	}
	table.verify("p", "m")
	if probed != 1 {
		t.Errorf("the diagnostic called something other than the Probe the data plane lends us (called %d times)", probed)
	}
}

type envStub struct {
	logf  func(string, ...any)
	probe func(string, string) bool
	state *domain.State
}

func (e envStub) Logf(format string, args ...any) { e.logf(format, args...) }
func (e envStub) Probe(provider, model string) bool {
	return e.probe(provider, model)
}

// State is normally nil here (no switch set); the pause switch's own cases set it.
func (e envStub) State() *domain.State { return e.state }

// rowOf picks out a binding's row from the snapshot; with no such row it
// gives the zero value (equivalent to "nothing happened in the ledger"
// -- exactly what those "record nothing" assertions want to look at).
func rowOf(b *table, provider, model string) Status {
	for _, s := range b.Snapshot() {
		if s.Provider == provider && s.Model == model {
			return s
		}
	}
	return Status{}
}

type errForTest struct{}

func (errForTest) Error() string { return "writing to disk failed (test)" }

// TestPlaneMetricNamingLivesHere pins the naming ownership of the observation surface: the
// descriptions of the three `breaker.*` rows in `newgate metrics` are given by this module.
//
// Before 2026-09-18 they were hardcoded in modules/gateway/metrics/hints.go -- which put the
// authority to explain "what a breaker opening means" into a package that does not know breakers.
func TestPlaneMetricNamingLivesHere(t *testing.T) {
	p := &plane{t: newTable()}
	for _, key := range []string{"breaker.opened", "breaker.spared", "breaker.skipped.shape_error"} {
		// Assert the **identity**: it is language-independent and is the half
		// used for table sorting. The wording ("breaker") follows the language,
		// and pinning it would only make the test go red on a translation change.
		if id, label := p.MetricGroup(key); id != "breaker" || label == "" {
			t.Errorf("MetricGroup(%q) = (%q, %q), want identity %q with a non-empty label",
				key, id, label, "breaker")
		}
		if h := p.MetricHint(key); h == "" {
			t.Errorf("MetricHint(%q) is empty - these three lines used to have a note", key)
		}
	}
	// Do not claim someone else's counter: claiming it would push their description aside.
	if id, label := p.MetricGroup("chain.failover"); id != "" || label != "" {
		t.Errorf("MetricGroup claimed a counter that is not its own: (%q, %q)", id, label)
	}
	if h := p.MetricHint("chain.failover"); h != "" {
		t.Errorf("MetricHint claimed a counter that is not its own: %q", h)
	}
}
