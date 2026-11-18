package breaker

// This file is the thin adapter where **the health
// table plugs itself into the data plane** (2026-09-18).
//
// Before that the direction was reversed: `modules/gateway` declared
// `Need(breaker.Capability)`, put a `breaker.Breaker` into the `forward.Server.Health`
// field, and the hot path wrote `breaker.Input{Kind: breaker.KindConnError}`
// / `res.Verdict.Bucket` / `case breaker.BucketShape:` directly. That is **lib usage** --
// policy names appeared in gateway's source, while by the test in docs/03-architecture.md
// §2 ("does it need to be started, stopped, released?") the health table is a service
// with a lifecycle and an owner, and must go the capability way in the reverse direction.
//
// Now: gateway exposes four decision points from its own state machine (chain-build
// admission / outcome verdict / control-plane self-report / persist on shutdown), `plane`
// implements the corresponding interfaces, and they are registered in breaker's `Start` via
// `gateway.RegisterFilter`. The health table is now a **consumer**, not an imported library.
//
// The test (mechanically checkable): the string "breaker"
// no longer appears in `modules/gateway`'s source.
//
// # Why the adapter layer is here and not in gateway
//
// Because "what counts as a failure, which ledger it goes to, what counts as a shape error,
// what the trace left behind is called" are all **policy**, not mechanism. gateway only
// offers four outlets and one ledger (policy.Registry), and should know not one word about
// those four things. This layer's existence is the physical evidence of that boundary.

import (
	"encoding/json"
	"strings"

	gwpolicy "github.com/rzbdz/newgate-ext/modules/gateway/policy"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// plane is the health table's avatar at the data-plane decision points.
//
// It **has no state of its own**: all state is in `*table`, and plane only translates. That way
// `newgate breaker` (reading health.json / Snapshot directly) and the hot path see the same facts.
type plane struct{ t *table }

// All seven optional phases implemented. The complete var assertion is so
// that "if one interface is deleted some day", it fails to compile right here,
// rather than silently degrading to "that decision point is answered by nobody".
var (
	_ gwpolicy.Filter      = plane{}
	_ gwpolicy.Admitter    = plane{}
	_ gwpolicy.Adjudicator = plane{}
	_ gwpolicy.Controller  = plane{}
	_ gwpolicy.Flusher     = plane{}
	_ gwpolicy.EnvBinder   = plane{}
	_ gwpolicy.MetricNamer = plane{}
)

func (p plane) Name() string { return "breaker" }

func (p plane) Why() string {
	return i18n.T("binding health table: trips after consecutive failures, "+
		"releases one half-open trial when the cooldown expires", nil)
}

// --- Phase A: chain-build admission --------------------------------------

// Admit is Available plus one sentence of plain words.
//
// That reason ("breaker open") used to be hardcoded in modules/config/resolve/chain.go
// -- the admission test belongs to whoever provides it, and resolve only
// recognizes the **shape** "return one reason", not any provider's vocabulary
// (see the comment on resolve.Opts.Available). It has come home now.
//
// **The side effect is unchanged**: when the cooldown expires, Available issues this
// round's "half-open trial slot", and one slot lets exactly one real request through (see
// Available in state.go). resolve asks exactly once per candidate, so "let one through"
// holds naturally there -- which is also why policy.Admitter allows only one contributor.
func (p plane) Admit(provider, model string) (bool, string) {
	if p.t.Available(provider, model) {
		return true, ""
	}
	// This sentence is carried into Skip.Reason by resolve verbatim, so
	// it is both **the reason shown to humans** and the content of that column in `newgate tier`.
	// So it may be translated -- but **only it may**: the categorization of that column (skipKind
	// in modules/config) recognizes the machine category Kind set by resolve, not the wording
	// of this sentence. Before 2026-09-20 it recognized the wording (strings.Contains "breaker
	// open"), which made "one column fewer after switching language" a failure waiting to happen.
	return false, i18n.T("circuit open", nil)
}

func (p plane) Rank(provider, model string, contextBytes int) int {
	return p.t.Rank(provider, model, contextBytes)
}

// --- Phase B: outcome verdict ---------------------------------------------

// Judge turns one outcome into (do not switch hop? / charge
// it? / which numbers to record? / what trace to leave?).
//
// It **writes state** -- the difference from the "pure function" line in the gateway-side interface
// comment is spelled out here: Classify is pure, but recording and classification were one call
// in the old code (`table.Report`), and splitting them would make "claim a shape error" and "count
// only, never delist" each decided once and each drift on its own (the scene before 2026-09-17: the
// same 400 delisted mid-chain but not at the chain tail). So this keeps one call that judges and
// records together, at the cost that it makes a network request during the **pre-trip diagnostic**
// (up to 16s) -- the data plane has always called it this way, see verifyProbeAttempts.
func (p plane) Judge(in gwpolicy.Outcome) gwpolicy.Verdict {
	res := p.t.Report(in.Provider, in.Model, input(in))

	if in.Kind == gwpolicy.Succeeded {
		// The success outcome **also** goes through Report: a success after the cooldown expired is
		// the half-open trial's conclusion, and it unseals (see succeedLocked). Beyond that nothing
		// needs saying -- no stop, no recording, no trace; latency samples take the Observe path.
		return gwpolicy.Verdict{}
	}

	v := gwpolicy.Verdict{Stop: !res.Advance, Note: note(res, in.Provider)}
	switch {
	case in.Kind == gwpolicy.RejectedStatus && res.Bucket == BucketShape:
		// A request-shape error: not this one's account (the same body is equally
		// wrong anywhere), but it **must leave a trace**. Count only, never delist.
	case in.Kind == gwpolicy.RejectedStatus && res.Bucket == BucketNone:
		// 4xx of "the request itself is bad": also not charged to it.
	default:
		// Connection failure / stream truncation / 429 / 401,403,404 / 408,409,5xx: charge this binding.
		//
		// Stream truncation also comes in now (2026-09-18): the old code only logged a line for
		// it, so an upstream that breaks halfway through every stream was completely invisible
		// in `newgate status`'s failures -- it was recorded in the health table but not in
		// the gateway counters. This is the **only** deliberate behavior change in this round.
		v.Attribute = true
	}
	switch {
	case res.Opened:
		v.Metrics = append(v.Metrics, "breaker.opened")
	case res.Spared:
		v.Metrics = append(v.Metrics, "breaker.spared")
	}
	if res.Bucket == BucketShape {
		v.Metrics = append(v.Metrics, "breaker.skipped.shape_error")
	}
	if res.Shape != "" {
		// The literal `[shape-400]` and the directory prefix `dump/shape-400-<test>/` are
		// given by **the policy that claims it**: that is upstream dialect (which provider
		// is picky, what the test is called), and per the rules in docs/05-gateway.md
		// the forwarding path may not contain upstream-proprietary strings.
		v.Evidence = &gwpolicy.Evidence{Tag: "shape-400", Subject: res.Shape, Archive: true}
	}
	return v
}

// input turns the data plane's facts into the classification
// table's input. **One to one, no judgment added**.
func input(o gwpolicy.Outcome) Input {
	in := Input{
		Written:       o.ResponseStarted,
		IsLast:        o.IsLast,
		FallbackOn400: o.FallbackOn400,
	}
	switch o.Kind {
	case gwpolicy.Succeeded:
		in.Kind = KindUpstreamSuccess
	case gwpolicy.ConnectionFailed:
		in.Kind = KindConnError
	case gwpolicy.StreamCut:
		// A broken stream **by definition** has already written bytes (the first byte
		// arrived before it could break mid-way), so ResponseStarted is not consulted
		// here: this bit is meant to say "cannot be taken back", not "how far it got".
		in.Kind = KindStreamTruncated
		in.Written = true
	case gwpolicy.RejectedStatus:
		in.Kind = KindUpstreamStatus
		in.Status = o.Status
		in.Body = o.Body
	}
	return in
}

// note is the body of the old `breakerNote` (those two sentences are the
// health table's knowledge, and used to live in the forwarding path).
func note(res Result, provider string) string {
	switch {
	case res.Opened:
		return i18n.T("  [breaker opened: {provider} removed for now]",
			i18n.A{"provider": provider})
	case res.Spared:
		// Almost delisted, stopped by the diagnostic probe. It must be printed: this
		// explains the combination "the log shows failures, but `newgate breaker`
		// does not have it", which sends people looking in the wrong direction.
		return i18n.T("  [diagnostic probe proved {provider} still healthy, not tripped]",
			i18n.A{"provider": provider})
	}
	return ""
}

// Observe is only reached by the success outcome: record one
// real time-to-first-byte sample. Availability is not touched here -- delisting
// after the first byte has arrived is pointless (see succeedLocked in state.go).
func (p plane) Observe(in gwpolicy.Outcome) {
	if in.Kind != gwpolicy.Succeeded {
		return
	}
	p.t.ObserveSuccess(in.Provider, in.Model, in.RequestBytes, in.TTFT)
}

// --- Phase C: control plane -----------------------------------------------

// Doc merges the health table into the **top-level** `breakers` field of `/__newgate/status`.
//
// The key name and JSON shape are unchanged word for word: it is a cross-version
// wire contract (Status in `modules/breaker/status`; during graceful handover
// old and new binaries run side by side, and the old CLI reads exactly this key).
// Merging each contributor's fields into the top level of the status document,
// rather than stuffing them into an extra object, is precisely for this.
//
// The old fields sitting at the top level are still there: Requests / Failures / Breakers
// are exactly the same names today as yesterday; only where they are produced changed, from
// "the forwarding path calls Health" to "the forwarding path merges policy self-reports".
func (p plane) Doc() map[string]json.RawMessage {
	raw, err := json.Marshal(p.t.Snapshot())
	if err != nil {
		// Everything Snapshot returns is scalars and fixed-length arrays, so Marshal cannot fail;
		// even if it did, that would only lose one section of status, not 500 the whole endpoint.
		return nil
	}
	return map[string]json.RawMessage{"breakers": raw}
}

// ObserveProbes takes in active probe conclusions. It is **authoritative evidence** (active,
// controllable, repeatable), so it is not subject to the consecutive-failure threshold:
// bad means delist on the spot, good means unseal on the spot (even during quarantine).
//
// The returned Note is logged by the data plane verbatim: that "at least 60s, or one successful
// probe" describes the health table's backoff semantics, which only this place knows.
func (p plane) ObserveProbes(obs []gwpolicy.ProbeObservation) []gwpolicy.ProbeAck {
	out := make([]gwpolicy.ProbeAck, 0, len(obs))
	for _, o := range obs {
		grade, opened := p.t.RecordProbe(o.Provider, o.Model, o.Status, o.ContextBytes,
			o.Latency, o.SlowAfter, o.Error)
		ack := gwpolicy.ProbeAck{Opened: opened}
		if opened {
			ack.Note = i18n.T(
				"[probe] tripped {provider}/{model}: {grade} (at least 60s, "+
					"or a successful probe brings it back immediately)",
				i18n.A{"provider": o.Provider, "model": o.Model, "grade": grade})
		}
		out = append(out, ack)
	}
	return out
}

// --- Phase D: shutdown ----------------------------------------------------

// Flush writes out synchronously the latency samples not yet
// persisted in the throttling window (for graceful exit).
//
// Today's implementation has no error to report (persistLocked hands failures to onError), so
// it always returns nil; the error is kept on the interface because "persisting failed" may
// one day need to affect the exit code, and by then changing the interface would be too late.
func (p plane) Flush() error {
	p.t.Flush()
	return nil
}

// --- Runtime capabilities ------------------------------------------------

// BindEnv takes the runtime capabilities the data plane
// hands over and wires two things back into the health table:
//
//   - SetErrorHandler: read/write failures of health state must be spoken.
//     The outlet is the data plane's log, but **the sentence is the
//     health table's knowledge** (it used to live in the forwarding path);
//   - SetVerifier: the last active diagnostic before tripping (see SetVerifier in api.go).
//     It used to be a callback the data plane injected into the health table in reverse
//     -- that reverse edge was the hard evidence that "who owns the capability" was
//     unclear; now it belongs to the data plane and is lent to policy (policy.Env.Probe).
func (p plane) BindEnv(env gwpolicy.Env) {
	p.t.SetErrorHandler(func(err error) {
		// Formatting is done by i18n.T, so `%s` is used here:
		// a `%` in a translation must not deform this log line.
		env.Logf("%s", i18n.T("[breaker] health table read/write failed "+
			"(continuing with in-memory state): {err}", i18n.A{"err": err}))
	})
	p.t.SetVerifier(env.Probe)
}

// --- Naming of the observation surface -----------------------------------

// MetricGroup / MetricHint let this module describe the
// three rows of `newgate metrics` that belong to it.
//
// They used to be hardcoded in modules/gateway/metrics/hints.go (the three literals
// `breaker.opened` / `breaker.spared` / `breaker.skipped.shape_error`) -- which put the
// authority to explain "the breaker opened" into a package that does not know breakers.
//
// Identity ("breaker") and wording (the translation of `Breaker`) are given
// separately: that table sorts by group, and the sort must be language-independent
// -- the reason is in the comment on modules/gateway/metrics.Group.
func (p plane) MetricGroup(key string) (id, label string) {
	if strings.HasPrefix(key, "breaker.") {
		return "breaker", i18n.T("Breaker", nil)
	}
	return "", ""
}

func (p plane) MetricHint(key string) string {
	switch key {
	case "breaker.opened":
		return i18n.T("breaker opened, provider removed for now", nil)
	case "breaker.spared":
		return i18n.T("consecutive failures reached the threshold, but the pre-trip "+
			"diagnostic probe proved it healthy, so it was not tripped", nil)
	case "breaker.skipped.shape_error":
		return i18n.T("request shape error (a 400 claimed by a shape detector, "+
			"e.g. deepseek's reasoning pass-back check), skipped from breaker accounting", nil)
	}
	return ""
}
