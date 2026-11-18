// Package codex_deepseek handles only the cross semantics of Codex and DeepSeek.
//
// It fixes **the one thing where neither side is wrong and only together they break**:
// Codex 0.155 puts tool definitions in `input[0]`'s `additional_tools` (its own dialect),
// while DeepSeek's /responses reads tools only from the top-level `tools`. Each side
// follows its own rules, and the result is that the model sees not one tool -- it falls back
// to the text format from training to "call tools", the client cannot parse it, and so **not
// one tool_result comes back** (the words at the scene: "never once got a tool_result").
//
// The judgment belongs to the distribution: this is "client x model
// cross semantics", which does not hold with another upstream or another
// client (see CLAUDE.md §1). The kernel knows neither codex nor DeepSeek.
//
// Four changes, two directions, and missing one breaks it:
//
//	request side 1. input[0].additional_tools -> top-level tools (keeping that input item)
//	             2. `custom` in the tool tree -> `function` (the upstream knows only
//	                apply_patch as custom; anything else gets 400 Unsupported custom
//	                tool) -- **both sources are checked, and namespaces too**: old versions
//	                hang tools in input[0], new ones put them directly at the top level
//	                (same 400, reproduced on live on 2026-09-21); a custom hidden in a
//	                namespace gets a different 400
//	                (`Currently custom tools are not allowed inside a namespace`), so that
//	                level is recursed into too -- the shell stays, one entry changes.
//	response side 3. for downgraded tools, their function_call -> custom_tool_call
//	              4. function_call_arguments.* -> custom_tool_call_input.*, unwrapping shell
//
// Why the request side **keeps** input[0]: measured, the upstream does not error on
// the extra `additional_tools` item (with or without it, both 200 and both get a real
// function_call), so deleting it would be a change with no basis -- under "forwarding
// a request should be forwarding", if it can be left alone, leave it alone.
//
// How the response side knows which entries to change: **by reading the source the client
// sent** (the copy Apply received), where every custom tool is still `type: "custom"`.
// Inferring from the body after Apply is guessing (a downgraded function looks exactly
// like a native one); reading the source is fact. See the description of special.Egressor.
//
// **This move has a switch point** (`codex-deepseek.lift-tools`, see switches.go
// and Apply in st-tools.go): it is this module's only move, and when something goes
// wrong the action most needed is "turn it off and look again". The switch point
// is really read -- it was not before 2026-09-21, and `newgate plugin` reported
// "off" while requests were rewritten anyway, which is worse than no switch at all.
package codex_deepseek

import (
	"context"

	gatewayapi "github.com/rzbdz/newgate-ext/modules/gateway"
	pluginmanagerapi "github.com/rzbdz/newgate-ext/modules/pluginmanager"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	codexapi "github.com/rzbdz/newgate-ext/modules/codex"
	deepseekapi "github.com/rzbdz/newgate-ext/modules/deepseek"
)

// New declares the Codex x DeepSeek crossing component. Only when the client, model and gateway
// port all resolve does it register the fixes needed only after the two sides are combined.
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "codex-deepseek",
		Desc: func() string { return i18n.T("the Codex × DeepSeek crossing", nil) },
		Type: "bridge",
		Requires: []modules.Requirement{
			modules.Need(gatewayapi.Capability),
			modules.Need(codexapi.Capability),
			modules.Need(deepseekapi.Capability),
			modules.Need(pluginmanagerapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			gateway := modules.MustGet(ctx, gatewayapi.Capability)
			client := modules.MustGet(ctx, codexapi.Capability)
			model := modules.MustGet(ctx, deepseekapi.Capability)
			for _, treatment := range Treatments(client, model) {
				release, err := gateway.RegisterRequestHook(treatment)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			pm := modules.MustGet(ctx, pluginmanagerapi.Capability)
			self, err := pm.RegisterSelf("codex-deepseek", Switches())
			if err != nil {
				return err
			}
			releases = append(releases, self)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
