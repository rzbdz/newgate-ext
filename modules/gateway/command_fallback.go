package gateway

import (
	"fmt"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate/lib/i18n"
)

// fallbackCommand is `newgate fallback on|off`.
//
// **Why it lives here and not in modules/cli**: the fallback chain is a data plane
// decision (forward's steps loop), and "switch or not" is gateway semantics, so the
// command belongs to gateway. Same reason as schemaRepairCommand.
//
// Why the command is worth having (2026-09-28): chain switching **hides upstream
// faults** - a dead provider still answers 200 via someone else, and the model name
// shown disagrees with the one that ran. To decide "is it A's fault or ours" you need
// one-keystroke fallback-off so requests **hit the upstream directly**. That is a
// debugging tool, not a token saver (it also drops the timeout budget wait).
//
// Relation to `--profile=xx`: that path pins the profile in the URL path and **does
// not need** this switch (see forward.Target.Profile). This switch governs every other
// path - so it defaults to **on** (not off), because off at the factory would cripple
// the gateway's main ability.
type fallbackCommand struct{}

var (
	_ cliapi.Command    = (*fallbackCommand)(nil)
	_ cliapi.Documented = (*fallbackCommand)(nil)
)

func (fallbackCommand) Names() []string { return []string{"fallback", "fallbacks"} }

func (fallbackCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionMaintenance, Rank: rankMaintenance,
		Usage:   "fallback on|off",
		Summary: i18n.T("Try the next candidate in the chain when one fails", nil),
	}
}

func (fallbackCommand) Run(host cliapi.Host, args []string) int {
	arg := cliapi.Positional(args, 0)
	// on|off must be explicit, word for word the schema-repair reason: `truthy("")` is
	// false, so defaulting a bare command to off would make "type it and see" silently
	// disable fallback machine-wide - a change **only noticed when something breaks**:
	// all normal until some provider dies and requests stop switching.
	if arg == "" {
		return host.Die(64, i18n.T("Usage: newgate fallback on|off (no argument changes anything)", nil))
	}
	on := truthy(arg)
	if err := gatewaystate.SetFallback(on); err != nil {
		return host.Die(70, err.Error())
	}
	if on {
		fmt.Println(style.Item(style.OK, "fallback on") +
			style.Dim(i18n.T("   A failing candidate hands the request to the next one in the chain", nil)))
	} else {
		fmt.Println(style.Item(style.Skip, "fallback off") +
			style.Dim(i18n.T("   Every request stops at the head of the chain, timeouts included", nil)))
	}
	// It changes the shared snapshot the forwarding path reads, so the watcher must
	// reload at once - otherwise the change waits for the next config edit to apply.
	host.NotifyProxy()
	return 0
}
