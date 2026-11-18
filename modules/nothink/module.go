// Package nothink is the "globally do not think" switch: `newgate nothink on|forever|off`.
//
// # What it is
//
// A product policy **the user turns on explicitly**: while on, every Anthropic-dialect request gets
// `thinking:{"type":"disabled"}` written into it before going upstream -- **including the ones the
// client itself wrote adaptive / enabled into**. That second half is the key to whether this switch
// holds up: Claude Code's main loop carries `thinking:{"type":"adaptive"}` on every request (the
// 2026-09-29 field dump err-400-req000010: stream=true, 28 tools, model=heavy), and respecting the
// client's intent (the kernel's claude-bg rule) would here mean doing nothing at all.
//
// # Why it is best effort
//
// Some models cannot have thinking turned off: GLM 1210 ("this model always thinks; turning
// thinking off is not supported"), kimi ("only type=enabled is allowed"). For them, the minimal
// form that lands "do not want to think" is `enabled` + `reasoning_effort:"low"`; when it cannot be
// translated, send it as is. The semantics of that move exist in **exactly one copy**, in the
// kernel's `modules/thinking` (BestEffortDisable, together with the quirk table and the probe
// cache): this module expresses only the intent "nobody thinks, globally", and how it is translated
// is the model side's business -- so there is not one line of model knowledge here.
//
// # Why only the Anthropic dialect is recognized
//
// `thinking` is a field in that protocol. OpenAI-dialect clients (opencode / codex) **have no such
// field to write at all** -- filling in disabled for them is the "deepseek stopped thinking" field
// report (2026-09-15, see the comment in modules/claudecode_glm). So this claims by **dialect**
// (`r.Path`, criterion domain.IsAnthropicPath), not by client: whoever speaks this dialect is
// covered by this switch.
//
// # Why it lives in the distribution
//
// It is not "fixing an upstream quirk" (that is the model module's job), it is a **product
// trade-off**: the user is willing to trade "no reasoning this round" for money and waiting. So it
// is off by default throughout, must be turned on explicitly, and while on, `newgate status` keeps
// printing it (not silent), with `newgate nothink off` to stop at any time.
package nothink

import (
	"context"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	gatewayapi "github.com/rzbdz/newgate-ext/modules/gateway"
	thinkingapi "github.com/rzbdz/newgate-ext/modules/thinking"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Name is this module's name in the assembly table and in `newgate plugin` (= the directory name),
// and also the key of that cell in state.json (see StateKey).
const Name = "nothink"

// New declares this component. The reason for each of the four dependencies:
//
//   - gateway (Need): the plugin hangs on the special chain and changes the request body;
//   - thinking (Need): the translating half lives in the kernel, and this module does not rebuild
//     it (see the package comment);
//   - config-hook (Need): claim the "nothink" cell in state.json (same as
//     classifier_naked). Two modules fighting over the same key should be rejected at
//     **registration time**, not settled by who writes last;
//   - cli (Optional): `newgate nothink` is this policy's entrance. In an assembly without a UI
//     (the web-only one), this module still installs and the policy still takes effect, there is
//     just no command to change it -- "a business module depends on no ui" here means "it lives
//     without the UI installed".
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: Name,
		Desc: func() string {
			return i18n.T("asks every upstream not to think; best effort (newgate nothink on|forever|off)", nil)
		},
		// Same class as the kernel's modules/thinking: it is a thinking policy unrelated to any model
		// family.
		Type: "model",
		Requires: []modules.Requirement{
			modules.Need(gatewayapi.Capability),
			modules.Need(thinkingapi.Capability),
			modules.Need(confighookapi.ConfigHooksCapability),
			modules.Optional(cliapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			gateway := modules.MustGet(ctx, gatewayapi.Capability)
			hook, err := gateway.RegisterRequestHook(nothink{
				thinking: modules.MustGet(ctx, thinkingapi.Capability),
			})
			if err != nil {
				return err
			}
			releases = append(releases, hook)

			hooks := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			field, err := hooks.RegisterStateField(Name, StateKey)
			if err != nil {
				return err
			}
			releases = append(releases, field)

			if ui, ok := modules.Get(ctx, cliapi.Capability); ok {
				command, err := ui.RegisterCommand(nothinkCommand{})
				if err != nil {
					return err
				}
				releases = append(releases, command)
			}
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
