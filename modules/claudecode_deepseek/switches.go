package claudecode_deepseek

import (
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	"github.com/rzbdz/newgate-ext/modules/pluginmanager"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// SwitchInjectThinking is move 1: when Claude Code did not write thinking, add
// thinking:{"type":"disabled"}, so DeepSeek's thinking mode is never entered in the first place.
//
// It is the **only** move of this cross-cutting module, so it is the switch
// for "the whole plugin" -- no need to invent a separate module-level button.
//
// The prefix is `claudecode-deepseek` (hyphen): the path prefix must equal Component.Name
// verbatim, and component names use hyphens (config-hook / opencode-omo / plugin-manager
// all do), while only directory names use underscores. A typo is rejected at
// registration on the spot -- this was measured (the first version used an underscore).
const SwitchInjectThinking = "claudecode-deepseek.inject-thinking"

// Switches is the list of switch points this module reports, for module.go to register.
func Switches() []pluginmanager.Switch {
	return []pluginmanager.Switch{{
		Path:  SwitchInjectThinking,
		Title: i18n.T("inject thinking:disabled (the first step)", nil),
		Why: i18n.T("Claude Code's requests fall into DeepSeek thinking mode by default, "+
			"but it does not carry third-party thinking blocks into the next round — "+
			"so every call hits the 400 that says thinking must be passed back", nil),
		Danger:  pluginmanager.DangerQuirk,
		Default: true,
	}}
}

// stateOf takes the config snapshot from the plugin context. nil is allowed (the old call surface
// and unit tests often hit it) -- pluginmanager.Off always answers false for nil, i.e. "not
// off". The direction must not be reversed: a missing snapshot should not turn the patch off.
func stateOf(r *special.Request) *domain.State {
	if r == nil {
		return nil
	}
	return r.State
}
