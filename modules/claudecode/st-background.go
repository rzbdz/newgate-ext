package claudecode

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/gateway/rewrite"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	thinkingapi "github.com/rzbdz/newgate-ext/modules/thinking"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

var (
	_ special.Plugin          = (*background)(nil)
	_ special.RoutePlugin     = (*background)(nil)
	_ special.StatusProvider  = (*background)(nil)
	_ special.BindingProvider = (*background)(nil)
	_ special.MetricProvider  = (*background)(nil)
)

func Treatments(thinking thinkingapi.Service) []special.Plugin {
	return []special.Plugin{background{thinking: thinking}}
}

// claudeBg fixes the slowness of Claude Code's small background
// requests when they hit a model that "thinks by default".
//
// Captured on site (2026-09, Claude Code 2.1.263, verified with NEWGATE_DUMP captures):
//
//	main loop     normal + stream=true + tools:33 + thinking:adaptive
//	background    mid   + non-streaming + no tools + thinking unwritten
//	classifier itself, on top: system ~126KB, starting with
//	           "You are a security monitor for autonomous AI coding agents";
//	           2 user messages (CLAUDE.md + the full session transcript);
//	           stop_sequences ["</block>"]; max_tokens 2112
//
// Chinese models like glm-5.3 **think by default**, so "unwritten" does not mean "off", and
// the classifier takes 15-30 seconds to answer, timing out and retrying in waves (a string of
// "client cancelled at the connect stage" in the log), and the whole dev session stalls with it.
//
// Two layers, each handling one thing:
//
//  1. Reroute (Route, routing layer): the classifier itself takes the **light chain**
//     entirely -- not just the chain head switched to light, but fallback too, within
//     the light chain. "Is this command safe" wants speed and cheapness, and that intent
//     must run through the whole chain: switching only the head means the moment light
//     dies it falls back to mid's build and is slow again. Other background calls (compact
//     summary, title generation) are not rerouted: they want a bit of quality and do not
//     block interaction; and when the marker does not match, no reroute happens naturally.
//  2. Disable thinking (Apply, body layer): for background calls that "did
//     not write thinking", add an explicit thinking:{"type":"disabled"}; those that wrote
//     one are untouched (the compact summary explicitly carries adaptive, and forcing
//     it to disabled gets rejected by always-think models -- see BestEffortDisableThink).
//     When it hits the 400 for "this model always thinks" (GLM 1210), the always-thinks
//     plugin, sorted last, backstops it (back to enabled + reasoning_effort:low) -- this
//     plugin only expresses the intent "the client does not want to think this time", and
//     translating it into what each upstream understands is the model-level plugin's job.
//
// Why the signature is reliable: the main loop **is always streaming** (corroborated by the
// dumps, including -p mode), so only small background calls are non-streaming; among background
// calls, the classifier itself is identified precisely by the system marker. Neither layer
// looks at Tier -- with real model names injected, Tier is "a guess back-derived from the name"
// (see resolve.realNameRoleOrder), and the marker plus non-streaming are the precise features.
//
// This is one of the few patches that **rewrites the
// client's explicit intent**, so three safeguards:
//   - notes / logs state plainly what changed (never silent, docs/01-product.md);
//   - `newgate st off claude-bg` removes it in one step
//     (reroute and thinking-disabled stop together);
//   - only Agent=="claude" is recognized (opencode and other clients are unaffected).
type background struct{ thinking thinkingapi.Service }

// classifierMarker is the classifier system's self-introduction (captured
// 4/4 in cc 2.1.263; still present on deobfuscation check in 2.1.295). **All four
// gates share the same system**: read out of 2.1.295 on 2026-10-10, gate (Bash)
// / sandboxNetwork / workflowGate / handoff all take this one sentence composed by
// xio()->GNe(), and flavour only changes the tail appended **inside the messages**.
const classifierMarker = "You are a security monitor"

// classifierStopSequences are the classifier's two stop sequences.
// **Only the auto_mode classifier in the whole client sets a top-level stop_sequences**
// (deobfuscation check 2026-10-10: the setting point is unique, the line in tao()
// `…V!=="fast"&&{stop_sequences:[_e?"</severity>": "</block>"]}`; the other two occurrences are
// generic pass-through that need the caller to supply them first, plus one in a source table).
// So "a top-level stop_sequences" is a structural test **independent of the prompt's wording**.
var classifierStopSequences = []string{"</block>", "</severity>"}

// isClassifier recognizes "this non-streaming request is Claude Code's safety
// classifier". The two tests are in an **or** relation; either one suffices:
//
//  1. the classifier's self-introduction appears in system
//     (shared by the four gates, see classifierMarker);
//  2. the top-level stop_sequences carries the classifier's stop
//     sequences (a structural test that survives wording changes).
//
// Why a structural test is wanted in addition to the marker: the marker is **prompt** text, and the
// wording is the client's business, so one release can move or rewrite it. And the cost of missing
// it is not "one upstream call saved less", it is **this request goes upstream, the upstream cannot
// return the classifier's shape -> the client judges "cannot evaluate" and blocks the action**
// (see the comment on classifierAnswer in classifier-naked.go). The cost of over-recognizing
// is only that one small background call gets short-circuited, so being generous is better.
//
// **Why the message bodies are not matched too**: the classifier's flavour tail
// (the stage 1/2 explanations) does land in the messages, but compact-summary requests
// **carry the whole session** down this path -- if anyone ever quoted those sentences in
// the session (this repo's docs and comments do), the compact would be short-circuited
// as a classifier, and its answer is meant to **replace the whole history**: that is
// session-level damage, far more expensive than a miss. system and stop_sequences both
// live on "the shell the client assembles itself", which the session body cannot reach.
func isClassifier(body []byte) bool {
	if raw, ok := rewrite.TopLevelRaw(body, "system"); ok && bytes.Contains(raw, []byte(classifierMarker)) {
		return true
	}
	if raw, ok := rewrite.TopLevelRaw(body, "stop_sequences"); ok {
		for _, end := range classifierStopSequences {
			if bytes.Contains(raw, []byte(end)) {
				return true
			}
		}
	}
	return false
}

func (background) Name() string { return "claude-bg" }

// Before makes "fill thinking first" plugins act ahead of the backstop translator (always-thinks).
//
// Only the kernel's own plugin names are listed here -- before 2026-09-20 it also listed `deepseek`
// and `glm`, two **distribution** names, which was the wrong direction (the kernel should not
// know product plugins), and since the special ordering graph silently ignores unregistered names,
// those two edges always idled in a pure-kernel build. Now that edge is declared by the product
// itself (see the Before in the distribution repo's modules/deepseek / modules/claudecode_glm), and
// `app/ordering_test.go` pins "the kernel's ordering edges only point at the kernel's own plugins".
func (background) Before() []string { return []string{"always-thinks"} }
func (background) After() []string  { return nil }

func (background) Why() string {
	return i18n.T("Claude Code's background non-streaming requests (the Bash classifier, etc.) carry no thinking, "+
		"yet Chinese models think by default → 15-30 seconds, waves of timeouts, a wedged session\n"+
		"the classifier (the system prompt says \"security monitor\") takes the whole chain to the light tier "+
		"(fallback included); other background calls only get thinking disabled "+
		"(the streaming main loop is unaffected)", nil)
}

func (background) Route(body []byte, request *special.Request, state *domain.State) (special.RouteDecision, bool) {
	if request == nil || request.Agent != ID || request.Stream || !isClassifier(body) {
		return special.RouteDecision{}, false
	}
	decision := special.RouteDecision{
		Tier:             "light",
		FirstByteTimeout: state.Timeouts.ClassifierFirstByte(),
		Note:             i18n.T("classifier rerouted → the light-tier chain (fallback included)", nil),
		Metric:           "route_light",
	}
	if override, _ := classifierOverride(state); override != nil {
		head := *override
		decision.Head = &head
		decision.OverrideNote = i18n.T("classifier override → {head} (the highest priority globally, ahead of any profile)",
			i18n.A{"head": head.String()})
		decision.OverrideFailNote = i18n.T("classifier override {head} did not take effect; falling back to the light-tier chain",
			i18n.A{"head": head.String()})
	}
	return decision, true
}

func (background) Status(state *domain.State) []special.StatusItem {
	if state == nil {
		return nil
	}
	override, err := classifierOverride(state)
	if err != nil {
		return []special.StatusItem{{
			Label: i18n.T("Classifier config", nil),
			Value: i18n.T("classifier_override is not valid: {err}", i18n.A{"err": err}),
		}}
	}
	if override != nil {
		return []special.StatusItem{{
			Label: i18n.T("Classifier override", nil),
			Value: i18n.T("{head} · the highest priority globally, ahead of any profile", i18n.A{"head": override.String()}),
		}}
	}
	return []special.StatusItem{{
		Label: i18n.T("Classifier rerouting", nil),
		Value: i18n.T("Claude Code Bash classifier → the light-tier chain", nil),
	}}
}

func (background) Bindings(state *domain.State) []domain.Binding {
	if state == nil {
		return nil
	}
	if override, _ := classifierOverride(state); override != nil {
		return []domain.Binding{*override}
	}
	return nil
}

func classifierOverride(state *domain.State) (*domain.Binding, error) {
	if state == nil {
		return nil, nil
	}
	raw := state.ModuleConfig["classifier_override"]
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var binding domain.Binding
	if err := json.Unmarshal(raw, &binding); err != nil {
		return nil, fmt.Errorf("%w", err)
	}
	if binding.Provider == "" || binding.Model == "" {
		return nil, i18n.E("provider / model must both be filled in", nil)
	}
	return &binding, nil
}

func (background) Metrics() []special.MetricInfo {
	return []special.MetricInfo{{
		Action: "route_light",
		Hint:   i18n.T("Bash classifier: the whole chain is rerouted to light", nil),
	}}
}

// Match recognizes the class "Claude Code's small background calls":
// initiated by claude + non-streaming. The precise determination of the
// classifier itself (the system marker) is in Route, which handles rerouting.
func (background) Match(r *special.Request) bool {
	return r != nil && r.Agent == ID && !r.Stream
}

// Apply, after recognizing a background call, hands "this call does not want to think"
// to BestEffortDisableThink -- the intent is here, the translation (minimum thinking when
// the model does not support disabling) is there, best effort: if it cannot be disabled,
// let it think, and never fail because of that. When the reroute did not hit (Route
// did not recognize the classifier), thinking is disabled anyway, slow but not dead.
func (b background) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	if b.thinking == nil {
		return body, nil, fmt.Errorf("thinking capability is unavailable")
	}
	return b.thinking.BestEffortDisable(body, r)
}
