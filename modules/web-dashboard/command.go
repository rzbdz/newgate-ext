package webdashboard

import (
	"fmt"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// `newgate web`: tells the user where this UI is.
//
// Why a command: its address is **the product of two facts** - the port (the one the
// daemon serves) and the prefix (mounted on porthub). Neither is in the user's head, and
// guessing wrong shows up as "will not open": you cannot tell a wrong port from a wrong
// path. It multiplies, and says if it is mounted in this process - two things, see below.
//
// It only prints, never opens a browser: v1 does not pick a browser for the user, nor
// xdg-open a machine that may have no GUI (that call hangs on a jump host). Copy to open.
type webCommand struct{ self *instance }

var (
	_ cliapi.Command    = (*webCommand)(nil)
	_ cliapi.Documented = (*webCommand)(nil)
)

func (c *webCommand) Names() []string { return []string{"web", "ui"} }

func (c *webCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionUI,
		Usage:   i18n.T("web", nil),
		Summary: i18n.T("where the browser interface is served, and how to reach it", nil),
	}
}

func (c *webCommand) Run(host cliapi.Host, _ []string) int {
	var port int
	var note string
	switch c.self.mode {
	case modeShared:
		// The port comes from state.json - the one written when the daemon started listening.
		// It is the only port truth the CLI side knows (`newgate status` reports it too).
		port = domain.ProxyPort
		if st := store.LoadState(); st != nil && st.Port != 0 {
			port = st.Port
		}
	case modeStandalone:
		// Fallback: no shared port in this build; the UI's own listen port is known only to it
		// (`NEWGATE_WEB_PORT` overrides, defaults to FallbackPort).
		port = standalonePort()
		note = i18n.T("this build has no shared port, so the interface listens on its own port "+
			"(the gateway is on a different one)", nil)
	default:
		// "No entry point in this build" and "the daemon is not running" are **two different
		// things**; naming which one tells the user what to do next (other build / start it).
		return host.Die(69, i18n.T("this build cannot serve a web interface: nothing provides "+
			"a shared port (porthub) or a serve-time hook (serving)", nil))
	}

	fmt.Println(style.Title("newgate web", "127.0.0.1"))
	fmt.Println(style.Rule(72))
	fmt.Println(style.Item(style.OK, fmt.Sprintf("http://127.0.0.1:%d%s/", port, Prefix)))
	if note != "" {
		fmt.Println(style.Hint(note))
	}
	if !host.DaemonRunning() {
		// The UI is served by the daemon: the mount in the CLI process is a ledger row, no
		// socket listening. Unsaid, users think the command itself starts the server.
		fmt.Println(style.Hint(i18n.T("the daemon is not running — the dashboard is served by it, "+
			"so start it first: newgate start", nil)))
	}
	fmt.Println(style.Hint(i18n.T("the interface is on loopback only; nothing listens on other interfaces", nil)))
	return 0
}
