package codex

import (
	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// status reports codex's **takeover mode** into `newgate status`.
//
// Why this module reports it: the mode and that model-name table are both codex's own
// IP (see models.go), and the UI does not know them. The previous version had them only
// on the web's "Codex models" card, and not one word of them was visible in the terminal
// -- the user could not switch in the CLI, nor confirm "which mode am I actually in".
type status struct{}

var _ cliapi.StatusProvider = status{}

func (status) Status() []cliapi.StatusLine {
	// No codex on this machine: this line would describe a tool that does not exist. Not reported.
	if !(facts{}).Installed() {
		return nil
	}
	_, warn := readModels()
	value := i18n.T("takeover · config.toml gets the tier name", nil)
	if Mode() == ModeRename {
		value = i18n.N("{n} model name maps back to a tier",
			"{n} model names map back to tiers", len(effectiveModels()),
			i18n.A{"n": len(effectiveModels())})
	}
	if warn != "" {
		value += " " + style.Yellow(warn)
	}
	return []cliapi.StatusLine{{Rank: 26, Label: "Codex", Value: value}}
}
