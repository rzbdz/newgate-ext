package codex

// Lifting the Codex client's tool dialect to the
// top-level `tools`: one move independent of any upstream.
//
// Why not a crossing: this step fixes **Codex's old shape**
// -- tools inside `input[0].additional_tools` -- which **no upstream** can read. Keeping that
// judgment in `modules/codex` (the owner of the client dialect) leaves the crossing modules
// (codex_deepseek / a future codex_ark etc.) responsible only for **each upstream's own
// fixes** (DeepSeek's custom -> function and the like). This adds a +1 between the two layers.
//
// Scope (measured scope):
//
// - Ark / Claude / smt-deepseek / Kimi / smt-claude get 200 but zero
//   tool_call on the old shape (silent failure, the nastiest family)
// - gemini / smt-codex / minimax get 400 outright
// - on the **new** shape where `tools` is already a
//   top-level non-empty array, this layer is a no-op
//
// With no shape to cover it does nothing: tool items with an unknown `type` are preserved as
// is (so Codex's newer extensions can be seen by the upstream without changing this module).

import (
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	gatewayapi "github.com/rzbdz/newgate-ext/modules/gateway"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	"github.com/rzbdz/newgate-ext/modules/pluginmanager"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

var _ special.Plugin = (*ToolsLift)(nil)

// ToolsLift is the one move, independent of any upstream, that
// lifts the codex client's tool dialect to the top-level `tools`.
//
// The name (`codex-tools`) goes into logs and `newgate
// st`; the plugin's unique identity must match.
type ToolsLift struct{}

func (ToolsLift) Name() string { return "codex-tools" }

// Before: this move must sort **ahead of** codex-deepseek -- codex-deepseek's
// Apply lifts again from `input[0].additional_tools` (measured: that second lift
// **replaces** the top-level tools with the same bytes + does the DeepSeek-specific
// custom->function downgrade along the way). So this move lifts first, and
// codex-deepseek's "custom downgrade" then takes effect in that replacement.
//
// The name must equal the Name codex-deepseek declares at registration (special.Register
// uses it). `special.Ordered`'s graph **silently ignores** unregistered names (see
// the quote at the top of modules/thinking/st-always.go) -- so in distributions without
// codex-deepseek (dist-hello / dist-dashboard etc.) this edge idles; whether this move
// is present is judged by "is codex installed", which the larger layer already controls.
func (ToolsLift) Before() []string { return []string{"codex-deepseek"} }
func (ToolsLift) After() []string  { return nil }

// Why gives `newgate st` a paragraph -- so operations can see "what happens if I turn it off".
func (ToolsLift) Why() string {
	return i18n.T("Codex 0.155 puts tool definitions in `input[0]` (type=additional_tools) while "+
		"every upstream we ship (Ark, DeepSeek, Claude, Gemini, GPT, Kimi, MiniMax, ...) reads tools "+
		"from the top level only; without the lift the model sees no tools, answers in text, and the "+
		"client never gets a tool_result. Switch off and every Codex call loses its tools.", nil)
}

// Match looks only at client identity -- no upstream binding. Binding an upstream is each
// crossing's job (see the model.MatchTarget part of modules/codex_deepseek/st-tools.go).
//
// This move is **client dialect normalization**, independent of upstream.
func (ToolsLift) Match(r *special.Request) bool {
	return r != nil && r.Agent == ID
}

// Apply lifts the tool tree. If any of the three does not
// hold, it does nothing -- fail-open is the rule at this layer.
//
// Shapes and switches:
//
// - switch point off (`newgate plugin codex.lift-tools off`) -> nothing (see switches.go)
// - input[].additional_tools absent -> nothing (nothing to lift)
// - top-level tools already a **non-empty array** -> the client already said so -> nothing
// - otherwise -> flatten the nested tree into a top-level tools array
//
// "Otherwise" includes `"tools": null`, `"tools": []`, and missing -- HasUsableTopLevelTools judges
// all three as "no usable top-level copy". The "present" side is the new Codex shape (top-level
// non-empty array), which is **the client's own explicit intent**, and should not be shadowed.
//
// It goes through pure byte surgery (the gateway/rewrite primitives): no extra
// fields read or written, not one item in input[] touched -- the upstream
// does not object to an extra additional_tools item (measured), so it is kept.
func (ToolsLift) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	if pluginmanager.Off(stateOf(r), SwitchLiftTools) {
		return body, nil, nil
	}
	if HasUsableTopLevelTools(body) {
		return body, nil, nil
	}
	arr, ok := ToolsInInput(body)
	if !ok {
		return body, nil, nil
	}
	tools, n := FlattenToolTree(arr)
	if n == 0 {
		return body, nil, nil
	}
	out, err := SetTopLevelTools(body, tools)
	if err != nil {
		// fail-open: if it cannot be changed, let the upstream report its own error
		return body, nil, err
	}
	return out, []string{noteLifted(n)}, nil
}

// stateOf takes the config snapshot from the plugin context. nil is allowed
// (the old call surface and unit tests) -- pluginmanager.Off always answers
// false for nil, i.e. "not off". The direction must not be reversed: a missing
// snapshot should not turn the patch off. Same as the codex_deepseek one.
func stateOf(r *special.Request) *domain.State {
	if r == nil {
		return nil
	}
	return r.State
}

// registerToolsLift registers this move and reports its switch point.
//
// The switch point follows **the behavior's owner**: lifting tools is the
// codex client dialect's business, independent of which upstream it is,
// so the switch is at `codex.lift-tools` (see the why in switches.go).
//
// Both ports are **capability values already obtained**, not a ctx: the caller
// (Start in module.go) holds them, and this move's Optional semantics (do
// not mount if gateway is off; if plugin-manager is off, mount only the hook
// and report no switch point) are judged there -- see that part in module.go.
func registerToolsLift(gw gatewayapi.Gateway, pm pluginmanager.Manager) ([]modules.Release, error) {
	var releases []modules.Release
	hook, err := gw.RegisterRequestHook(ToolsLift{})
	if err != nil {
		return nil, err
	}
	releases = append(releases, hook)
	if pm == nil {
		// An assembly without plugin-manager (the skeleton turned it off): the hook is mounted, the
		// switch point is not reported. "Switch absent" and "switch on by default" are the same behavior
		// -- pluginmanager.Off answers false for a path with no record (see Apply in st-tools.go).
		return releases, nil
	}
	self, err := pm.RegisterSelf(ID, Switches())
	if err != nil {
		modules.ReleaseAll(releases)
		return nil, err
	}
	releases = append(releases, self)
	return releases, nil
}
