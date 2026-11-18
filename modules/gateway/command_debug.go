package gateway

import (
	"fmt"
	"strconv"
	"time"

	"github.com/rzbdz/newgate-ext/lib/durarg"
	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate/lib/i18n"
)

const debugDefaultMinutes = 30

// debugCommand is `newgate debug on|off [minutes] [--forever]`: full request logging.
//
// **Why it lives here and not in modules/cli**: what gets turned on is the gateway's
// forwarding side dumping each request and response (see forward's dump gate), so "is
// it on" is gateway semantics; cli need not know the verb, as with specialCommand.
//
// **Time-limited** by default: one request body can exceed 8KB (opencode's system prompt
// alone is ~97KB), and forgetting to turn it off fills the disk.
type debugCommand struct{}

var (
	_ cliapi.Command    = (*debugCommand)(nil)
	_ cliapi.Documented = (*debugCommand)(nil)
)

func (debugCommand) Names() []string { return []string{"debug"} }

func (debugCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionObserve, Rank: rankObserve,
		Usage:   i18n.T("debug on|off [minutes]", nil),
		Summary: i18n.T("Full request log (turns itself off after 30 minutes by default)", nil),
	}
}

func (debugCommand) Run(host cliapi.Host, args []string) int {
	arg := cliapi.Positional(args, 0)
	// Same as schema-repair: no argument must print usage, not default to off. This
	// command costs more - it would disable full logging when you just meant to peek,
	// and "where did the logs go" costs far more to debug than typing an on/off.
	switch arg {
	case "":
		return host.Die(64, i18n.T("Usage: newgate debug on [minutes] [--forever] | off (no argument changes anything)", nil))
	case "on", "off":
	default:
		// `newgate debug 30` means "on for 30 minutes" - kept.
		if _, err := durarg.Parse(arg); err != nil {
			return host.Die(64, i18n.T("debug: unrecognized argument {arg} (on / off / a number of minutes, e.g. 30)",
				i18n.A{"arg": strconv.Quote(arg)}))
		}
	}
	on := arg != "off"
	ttl := debugDefaultMinutes * time.Minute
	if on {
		for _, a := range args {
			var n int
			if _, err := fmt.Sscanf(a, "%d", &n); err == nil && n > 0 {
				ttl = time.Duration(n) * time.Minute
			}
		}
		if cliapi.Flag(args, "--forever") {
			ttl = 0
		}
	}

	until := ""
	if ttl > 0 {
		until = time.Now().Add(ttl).Format(time.RFC3339)
	}
	if err := gatewaystate.SetDebug(on, until); err != nil {
		return host.Die(70, err.Error())
	}

	if !on {
		fmt.Println(style.Item(style.Skip, "debug off"))
		return 0
	}
	if ttl > 0 {
		fmt.Println(style.Item(style.OK, i18n.T("debug on · turns itself off after {after}",
			i18n.A{"after": durarg.Format(int(ttl.Seconds()))})))
		fmt.Println(style.Hint(i18n.T("No expiry: newgate debug on --forever", nil)))
	} else {
		fmt.Println(style.Item(style.Warn, i18n.T("debug on · does not turn itself off", nil)))
		fmt.Println(style.Hint(i18n.T("Turn it off with newgate debug off", nil)))
	}
	fmt.Println(style.Hint(i18n.T("Log {path} · 16MB rotation, 4 files kept", i18n.A{"path": paths.LogFile()})))
	host.NotifyProxy()
	if host.DaemonRunning() {
		fmt.Println(style.Hint(i18n.T("Takes effect immediately, no restart needed", nil)))
	}
	return 0
}
