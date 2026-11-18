package codex_deepseek

import (
	"github.com/rzbdz/newgate-ext/modules/pluginmanager"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// SwitchDegradeTools is this move: downgrade `custom` declarations
// in the Codex tool tree to `function` (see st-tools.go).
//
// Turning it off has a direct symptom: the upstream 400 `Unsupported custom tool: 'exec'. Only
// 'apply_patch' is supported.` (a different one when hidden in a namespace: `Currently custom tools
// are not allowed inside a namespace`). So turning it off is a debugging action; on by default.
//
// **Why the switch belongs to this module**: recognizing only apply_patch as custom
// is **DeepSeek's** rule; with Codex and Ark / Claude / Kimi that move should
// not run at all (they natively take custom, see the measured table at the top of
// modules/codex/st-tools.go). So it is the crossing's switch, not the client's.
//
// **Before 2026-09-23 it was called `codex-deepseek.lift-tools`**; the rename
// is because two things actually sat under the same switch -- "lift tools from input[0]
// to the top level" and "downgrade custom to function" -- and the former has nothing
// to do with which upstream it is. The lift moved to modules/codex (`codex.lift-tools`,
// aligning the behavior with the switch's owner), and only DeepSeek's half stays here.
const SwitchDegradeTools = "codex-deepseek.degrade-tools"

// Switches is the list of switch points this module reports, for module.go to register.
//
// The prefix is `codex-deepseek` (hyphen): the path prefix must be
// word for word the Component.Name; only the directory name uses
// underscores. A mistake is rejected at registration (measured).
func Switches() []pluginmanager.Switch {
	return []pluginmanager.Switch{{
		Path:  SwitchDegradeTools,
		Title: i18n.T("degrade Codex custom tools for DeepSeek", nil),
		Why: i18n.T("DeepSeek's /responses accepts `custom` tools only for "+
			"apply_patch and answers 400 for every other one (the message inside a "+
			"namespace is a different one); with the degrade off a Codex request "+
			"carrying any other custom tool is rejected outright", nil),
		Danger:  pluginmanager.DangerQuirk,
		Default: true,
	}}
}
