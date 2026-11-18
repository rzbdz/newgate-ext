package codex

import (
	"github.com/rzbdz/newgate-ext/modules/pluginmanager"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// SwitchLiftTools is this move: lift Codex's
// additional_tools to the top-level tools (see st-tools.go).
//
// Turning it off = the behavior before 2026-09-23: the model sees no tools at all, so it falls
// back to the text format from training to "call tools", the client cannot parse it, and not
// one tool_result comes back. So it is on by default, and turning it off is a debugging action.
//
// **Why the switch belongs to this module rather than the crossing**: this move fixes "the shape
// Codex sends", independent of which upstream it is (Ark / DeepSeek / Gemini / ... read only the
// top level). The switch follows **the behavior's owner** -- before 2026-09-23 this one switch
// hung on codex_deepseek (`codex-deepseek.lift-tools`), so on the "Codex with Ark" path there
// was no switch to turn off at all, while the same behavior had two names in the same binary.
const SwitchLiftTools = "codex.lift-tools"

// Switches is the list of switch points this module reports, for module.go to register.
//
// The prefix is `codex` (= Component.Name), enforced by pluginmanager at registration time.
func Switches() []pluginmanager.Switch {
	return []pluginmanager.Switch{{
		Path:  SwitchLiftTools,
		Title: i18n.T("lift Codex tools from input[0] to the top level", nil),
		Why: i18n.T("Codex 0.155 puts tool definitions in `input[0]` "+
			"(type=additional_tools) and every upstream we ship reads tools from the "+
			"top level only: with the lift off the model sees no tools at all, answers "+
			"in its own text format, and Codex never gets a tool_result back", nil),
		Danger:  pluginmanager.DangerQuirk,
		Default: true,
	}}
}
