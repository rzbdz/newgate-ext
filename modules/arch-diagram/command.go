package archdiagram

import (
	"fmt"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// `newgate arch`: tells the user where this diagram is.
//
// Same reason as `newgate web`: the address is port times prefix, both of
// which are in no user's head, and the symptom of guessing wrong is "it does
// not open" -- you cannot tell whether the port is wrong or the path is wrong.
//
// It also states plainly **whether this run can scan imports**: half of that diagram
// needs a source tree (see handler.go). Without saying so, a machine in deployment
// opens it, sees only the assembly diagram, and the user thinks the diagram is broken.
type archCommand struct{ svc *service }

var (
	_ cliapi.Command    = (*archCommand)(nil)
	_ cliapi.Documented = (*archCommand)(nil)
)

func (archCommand) Names() []string { return []string{"arch", "architecture"} }

func (archCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionUI,
		Usage:   i18n.T("arch", nil),
		Summary: i18n.T("where the architecture diagram is served (dev builds)", nil),
	}
}

func (c archCommand) Run(host cliapi.Host, _ []string) int {
	port := domain.ProxyPort
	if st := store.LoadState(); st != nil && st.Port != 0 {
		port = st.Port
	}
	fmt.Println(style.Title("newgate arch", "127.0.0.1"))
	fmt.Println(style.Rule(72))
	fmt.Println(style.Item(style.OK, fmt.Sprintf("http://127.0.0.1:%d%s/", port, Prefix)))
	if root := c.svc.root; root == "" {
		fmt.Println(style.Hint(i18n.T("no source tree found here, so only the assembly graphs will be drawn — "+
			"the import scan needs one (set {env} to point at it)", i18n.A{"env": SrcEnv})))
	} else {
		fmt.Println(style.Hint(i18n.T("imports are scanned from {root}", i18n.A{"root": root})))
	}
	if !host.DaemonRunning() {
		fmt.Println(style.Hint(i18n.T("the daemon is not running — this diagram is served by it, "+
			"so start it first: newgate start", nil)))
	}
	return 0
}
