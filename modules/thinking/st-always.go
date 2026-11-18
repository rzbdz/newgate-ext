package thinking

import (
	"github.com/rzbdz/newgate-ext/modules/gateway/probe"
	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
	"github.com/rzbdz/newgate-ext/modules/gateway/rewrite"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

var _ special.Plugin = (*alwaysThinks)(nil)

func Treatments() []special.Plugin { return []special.Plugin{alwaysThinks{}} }

// BestEffortDisableThink: the full semantics of "this call does not want to think", one operation:
//
//  1. Land the intent: **only when thinking is absent**, inject thinking:{"type":"disabled"}.
//     If it is written (adaptive / enabled / disabled) that is the client's explicit intent,
//     so respect it and do not touch it (2026-09-09 live capture + probe: a compact summary
//     request carries thinking:adaptive, forced to disabled it is rejected by always-thinking
//     models - glm-5.3 code 1210; probe: adaptive+tools -> 200, disabled -> 1210);
//  2. Translate per the current chain step's model: a model that cannot disable thinking
//     (quirk NoThinkingDisable) gets enabled + reasoning_effort:low, translated in place.
//
// best effort means: if it cannot be turned off, let it think (a user who puts a model that
// cannot disable thinking into the light tier is on their own - slow, but the request lives);
// if it cannot be translated, send as is. **Never fail the request over this intent.**
//
// Architectural position: the intent side (e.g. claude-bg's background call) calls it in the
// special chain; forward's chain loop runs it once per **step's model** (a fallback swap
// re-translates). The always-thinks plugin is the fallback half - it handles disabled written
// by other sources and the tools-without-thinking 1210 shape; both share one translation
// core (noDisableTranslate), so there is exactly one semantics.
func BestEffortDisableThink(body []byte, r *special.Request) ([]byte, []string, error) {
	why := i18n.T("this call does not want to think (best effort)", nil)
	var notes []string
	out := body

	// reasoning_effort and thinking:disabled are mutually exclusive (same note in st-deepseek):
	// if the client set a reasoning effort, do not turn thinking off.
	if _, effort := rewrite.TopLevelRaw(out, "reasoning_effort"); effort {
		return out, nil, nil
	}

	// 1) Land the intent - inject disabled only when thinking is absent.
	//    If written (adaptive/enabled/disabled) it is the client's explicit intent: respect it,
	//    do not touch it. A compact (summary) request carries thinking:adaptive, and forcing
	//    disabled gets it rejected by always-thinking models (glm-5.3 code 1210). best effort
	//    narrows from "cannot disable, let it think" to "never asked to think, so say it does not".
	if _, has := rewrite.TopLevelRaw(out, "thinking"); !has {
		nb, err := rewrite.InsertTopLevelRaw(out, "thinking", []byte(`{"type":"disabled"}`))
		if err != nil {
			notes = append(notes, i18n.T("thinking not injected ({err})", i18n.A{"err": err}))
		} else {
			out = nb
			// The injected JSON is an argument, not left in the message: `{...}` is placeholder
			// syntax, so in the message it reads as an argument named `"type":"disabled"`
			// (tools/i18n's check would report "call site has no matching argument").
			notes = append(notes, i18n.T("injected {patch} ({why})", i18n.A{
				"patch": `thinking:{"type":"disabled"}`, "why": why}))
		}
	}

	// 2) Model does not understand disabled? Translate in place to the least thinking it knows.
	//    Translation follows the (provider, r.Model) quirk - do nothing once the body has been
	//    switched to another model: "glm-5.3 cannot disable" does not imply "glm-4.5-air cannot".
	if bm, has := rewrite.TopLevelString(out, "model"); (!has || bm == "" || bm == r.Model) &&
		noThinkingDisable(r, r.Provider, r.Model) {
		if nb, translated, ns, err := noDisableTranslate(out); err == nil && translated {
			out = nb
			notes = append(notes, ns...)
		}
	}
	return out, notes, nil
}

// noDisableTranslate is the translation core (one semantics only): turn thinking:disabled into
// enabled + reasoning_effort:low, which always-thinking models understand. It acts only when
// thinking really is disabled; translated=false means there was nothing to translate.
//
// Live error (Zhipu GLM, code 1210):
//
//	[1210][this model always thinks and cannot disable thinking; use low, high or max.][2026...]
//
// Reproduced live (api.rvcompute.com aggregator + glm-5.3):
//
//	tools + no reasoning_effort            → 400 / 1210
//	tools + reasoning_effort=low|high     → 200
//	thinking:{"type":"disabled"}         → 400 / 1210
//	no tools, no thinking                 → 200
//
// Cause: the aggregator sees tools and stuffs "disable thinking" in for us, and an
// always-thinking model refuses outright. The aggregator cannot be changed, but an explicit
// effort overrides it; low is closest to "do not overthink" (none works too, but is unproven).
func noDisableTranslate(body []byte) (out []byte, translated bool, notes []string, err error) {
	raw, has := rewrite.TopLevelRaw(body, "thinking")
	if !has {
		return body, false, nil, nil
	}
	if t, _ := rewrite.TopLevelString(raw, "type"); t != "disabled" {
		return body, false, nil, nil
	}
	nb, rerr := rewrite.ReplaceTopLevelRaw(body, "thinking", []byte(`{"type":"enabled"}`))
	if rerr != nil {
		return body, false, nil, rerr
	}
	notes = append(notes, i18n.T("thinking:disabled → enabled (this model cannot turn thinking off)", nil))

	// Add the effort too (leave an existing one - respect a client-set effort)
	if _, has := rewrite.TopLevelRaw(nb, "reasoning_effort"); !has {
		if nb2, ierr := rewrite.InsertTopLevelRaw(nb, "reasoning_effort", []byte(`"low"`)); ierr == nil {
			nb = nb2
			notes = append(notes, i18n.T(`added reasoning_effort:"low" (the upstream error asks for low/high/max)`, nil))
		} else {
			// If it cannot be added, say so; do not keep only the "thinking translated" good
			// news: in the tools shape this is exactly what overrides the aggregator's implicit
			// disable, and without it the call still 400s (the tools path in this file reports
			// this; two paths must not have two attitudes).
			notes = append(notes, i18n.T("reasoning_effort not added ({err})", i18n.A{"err": ierr}))
		}
	}
	return nb, true, notes, nil
}

// alwaysThinks is the fallback translator: an always-thinking model 400s on "disable thinking"
// (GLM 1210). BestEffortDisableThink is the intent side's active entry; this plugin handles the
// 400 shapes caused by **other sources**:
//
//   - the client's own settings wrote thinking:disabled (leaked into a background call, or the
//     user really wants it off - indistinguishable, so always translate);
//   - tools present and thinking absent: the aggregator disables thinking for us → 1210.
//
// Why Match trusts only the quirk registry and never guesses from the model name: always-thinking
// is a property of a model version, it changes, and no interface exposes it. Guessing from the
// name adds fields to unrelated requests - worse than not fixing. Two registry sources: learned
// by hitting this error during forwarding (one failure buys immunity), or found by newgate probe.
type alwaysThinks struct{}

func (alwaysThinks) Name() string { return "always-thinks" }

// Before/After are both empty: this plugin has no ordering constraint on the **kernel** side.
//
// It should come after the "act first" plugins (claude-bg reroutes background requests,
// deepseek/glm fix shapes by model), but those live **in the distribution** - and before
// 2026-09-20 their names were listed here, which was backwards: the kernel knows no
// product plugins. Nor does it error: special's order graph **silently ignores** names
// that were never registered (addEdge), so those edges sat inert: a typo went unnoticed.
//
// The right direction is **owner declares**: product plugins say "I come before always-thinks"
// in their Before() (modules/claudecode, modules/deepseek, modules/claudecode_glm). See there.
// `app/ordering_test.go` pins "kernel order edges point only at kernel plugins" - that test
// reddened once when claudecode moved out, which is exactly proof that it works.
func (alwaysThinks) Before() []string { return nil }
func (alwaysThinks) After() []string  { return nil }

func (alwaysThinks) Why() string {
	return i18n.T("some models always think and return a 400 when told to turn thinking off "+
		"(GLM code 1210)\ngive these models an explicit reasoning_effort=low and change "+
		"thinking:disabled back to enabled (the fallback half of BestEffortDisableThink)", nil)
}

func (alwaysThinks) Match(r *special.Request) bool {
	if r == nil {
		return false
	}
	return matchTarget(r, r.Provider, r.Model)
}

// matchTarget: does this (provider, model) "not support disabling thinking" - **the only criteria
// site**, shared by Match and Apply. Two copies cost divergence, not duplication: Apply judging
// one less hits a model that never showed it, which means adding fields to a user's request.
//
// Two sources, either hit suffices:
//
//  1. **The learned table** (quirk.Flag): learned by hitting a 400 while forwarding, or found
//     by `newgate probe`. It lives in **memory**, so a restart means learning it again.
//  2. **The cache probes wrote down** (probe-capabilities.json): the on-disk version of that
//     table. Without it, the first Codex→GLM request after every upgrade/restart always 400s
//     (the table is empty), and the user sees "stream disconnected before completion" - always
//     on restart, fine later: the hardest to reproduce. 2026-09-22 hit exactly this shape.
//
// The cache path uses quirk.Default's **read-only** load (`probe.LoadCachedCapabilities` loads
// the same Default, see forward.Server.Start); it asks on demand and does not read disk on every
// Apply hot-path call: on a table hit (the learned ones) it never touches the disk.
func matchTarget(r *special.Request, provider, model string) bool {
	if noThinkingDisable(r, provider, model) {
		return true
	}
	return cachedNoThinkingDisable(provider, model)
}

// cachedNoThinkingDisable reads that one bit from the probe cache.
//
// Any read failure (file not written yet, bad permissions, junk) counts as "unknown" -
// **fail-open here means "do not translate"**: skip one fix (the upstream errors and the user
// sees it) rather than rewrite a body from a file that cannot be read. See noThinkingDisable.
var cachedNoThinkingDisable = func(provider, model string) bool {
	if err := probe.LoadCachedCapabilities(); err != nil {
		// No log line: this is every request on the hot path, and "cache unreadable" was already
		// reported once at gateway startup (the [probe] line in forward.Server.Start).
		return false
	}
	return quirk.Default.Has(provider, model, quirk.NoThinkingDisable)
}

// noThinkingDisable: does this (provider, model) not support disabling thinking.
//
// The table comes **from the gateway via special.Request** (see that field): this module reads
// the table that request saw, not the `gateway/quirk` package-level variable. The difference is
// not the value (same table) but **whether the dependency is visible** - a package-level call is
// a service locator dependency that Requires and the graph cannot see (docs/03-architecture.md §5
// forbids new ones).
// A missing table (tests, old call surfaces) counts as "not learned": skip one, never guess.
func noThinkingDisable(r *special.Request, provider, model string) bool {
	if r == nil || r.Quirks == nil {
		return false
	}
	return r.Quirks.Has(provider, model, quirk.NoThinkingDisable)
}

func (alwaysThinks) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	// Invariant guard: the quirk is learned per (provider, r.Model) and applies only to requests
	// still going to that model. On the chain r.Model is synced from the body (special.Apply), so
	// normally they cannot differ here; this is the last gate against "patch on the wrong model"
	// for direct calls or future paths.
	if bm, has := rewrite.TopLevelString(body, "model"); has && bm != r.Model {
		return body, nil, nil
	}

	var notes []string
	out := body
	needEffort := false

	if _, hasThinking := rewrite.TopLevelRaw(out, "thinking"); hasThinking {
		// disabled → translate (same core as BestEffortDisableThink)
		if nb, translated, ns, err := noDisableTranslate(out); err != nil {
			notes = append(notes, i18n.T("thinking left unchanged ({err})", i18n.A{"err": err}))
		} else if translated {
			out = nb
			notes = append(notes, ns...)
		}
	} else if _, hasTools := rewrite.TopLevelRaw(out, "tools"); hasTools {
		// No thinking but tools present: the aggregator disables thinking for us → 1210.
		// (Explicit adaptive/enabled is not this case - the client wants thinking, nobody disables it.)
		needEffort = true
	}

	// Add an explicit reasoning_effort (tools shape). This is the hand that really
	// overrides the aggregator's implicit disable.
	if needEffort {
		if _, has := rewrite.TopLevelRaw(out, "reasoning_effort"); !has {
			if nb, err := rewrite.InsertTopLevelRaw(out, "reasoning_effort", []byte(`"low"`)); err != nil {
				notes = append(notes, i18n.T("reasoning_effort not added ({err})", i18n.A{"err": err}))
			} else {
				out = nb
				notes = append(notes, i18n.T(
					`added reasoning_effort:"low" (the upstream error asks for low/high/max)`, nil))
			}
		}
	}

	// Responses dialect (Codex): the reasoning effort lives in a **nested reasoning object**,
	// not top-level reasoning_effort. None of the checks above hold on this path - before
	// 2026-09-22 this plugin only looked at thinking and top-level reasoning_effort, so a
	// Codex request went out without a single byte changed and hit the 400.
	//
	// Live (2026-09-22, Codex 0.155.1 → smt-glm/glm-5.3, path /a/codex/v1/responses):
	//
	//	reasoning.effort = medium | minimal | none → 400 "always thinks, cannot disable"
	//	reasoning.effort = low | high | max        → 200
	//	reasoning absent / {"summary":"auto"} / {} / "effort":null → 200
	//
	// And it does **not hinge on tools**: the same body without tools also 400s (measured),
	// so this is unrelated to needsEffort's "only with tools" shape - the criterion is just
	// "this model always thinks" plus "it does not accept this value".
	//
	// Why the symptom is hard to find: with `stream:true` the upstream reports it as **HTTP
	// 200 plus one `response.failed` in the event stream**, and Codex shows "stream
	// disconnected before completion: model always thinks..." - it looks like a dropped
	// connection, but really the body held a value it rejects. Status-only checks go green.
	if nb, fixed, ns, err := fixResponsesEffort(out); err != nil {
		notes = append(notes, i18n.T("reasoning.effort left unchanged ({err})", i18n.A{"err": err}))
	} else if fixed {
		out = nb
		notes = append(notes, ns...)
	}

	return out, notes, nil
}

// acceptedEfforts are the reasoning efforts an always-thinking upstream **accepts**.
//
// The error text names these three ("use low, high or max"), checked cell by cell on the
// Responses dialect on 2026-09-22: low / high / max → 200, medium / minimal / none → 400.
// All three, not just low, because this criterion's use is "**only touch a value the
// upstream rejects**" - accepting only low would treat a user's explicit high/max as
// something to fix, which is downgrading for them: silently changing behavior, worse than no fix.
var acceptedEfforts = map[string]bool{"low": true, "high": true, "max": true}

// effortForAlwaysThinks is what a "value the upstream rejects" is replaced with: low is
// closest to "do not overthink", and is what this repo always used on the anthropic dialect
// (see noDisableTranslate). high / max are accepted too, but that is **upping the user's
// bid** - guessing a tier it does not know toward "think more" costs their money and time.
const effortForAlwaysThinks = "low"

// fixResponsesEffort fixes the "upstream-rejected" thinking effort in the Responses dialect.
//
// Only **one value's** byte range: reasoning.effort. Everything else in the same object
// (Codex puts summary there) and every other byte in the body stays verbatim - the
// "no JSON round trip on the body" rule lands here.
//
// The criterion is **accept or not** (acceptedEfforts), not "looks like low": recognized
// values are not touched at all. Unknown shapes (reasoning not an object, effort not a
// string, absent) are left alone with fixed=false - let the upstream error on the original
// rather than guess. Absent effort is normal (Codex sends only `{"summary":"auto"}`; 200).
func fixResponsesEffort(body []byte) (out []byte, fixed bool, notes []string, err error) {
	raw, has := rewrite.TopLevelRaw(body, "reasoning")
	if !has {
		return body, false, nil, nil
	}
	eff, ok := rewrite.TopLevelString(raw, "effort")
	if !ok || eff == "" || acceptedEfforts[eff] {
		return body, false, nil, nil
	}
	inner, rerr := rewrite.ReplaceTopLevelString(raw, "effort", effortForAlwaysThinks)
	if rerr != nil {
		return body, false, nil, rerr
	}
	nb, rerr := rewrite.ReplaceTopLevelRaw(body, "reasoning", inner)
	if rerr != nil {
		return body, false, nil, rerr
	}
	return nb, true, []string{i18n.T(
		`reasoning.effort "{was}" → "{to}" (this model always thinks and takes low/high/max only)`,
		i18n.A{"was": eff, "to": effortForAlwaysThinks})}, nil
}
