package hermes

import (
	"fmt"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// gatewayCommand is `newgate hermes-gateway`: look at / sync / restart hermes' messaging
// gateway.
//
// Why not `newgate hermes <subcommand>`: `newgate hermes` is already the verb "launch hermes"
// (every client's ID is, see runtime's launch), and a subcommand would collide with hermes'
// own arguments (`hermes gateway …` is already a hermes command).
//
//	newgate hermes-gateway            status: taken over or not, is the list current, does the
//	                                  gateway need a restart
//	newgate hermes-gateway sync       sync the /model list right now (does not touch slots)
//	newgate hermes-gateway restart    let hermes drain the running sessions and restart the
//	                                  gateway
type gatewayCommand struct{}

var (
	_ cliapi.Command    = gatewayCommand{}
	_ cliapi.Documented = gatewayCommand{}
)

func (gatewayCommand) Names() []string { return []string{"hermes-gateway"} }

func (gatewayCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionTakeover,
		Usage:   i18n.T("hermes-gateway [sync|restart]", nil),
		Summary: i18n.T("hermes: show / sync the /model list / restart its messaging gateway", nil),
	}
}

func (gatewayCommand) Run(host cliapi.Host, args []string) int {
	switch sub := cliapi.Positional(args, 0); sub {
	case "", "status":
		printGateway()
		return 0
	case "sync":
		if !(Takeover{}).IsTakenOver(ConfigPath()) {
			return host.Die(64, i18n.T("hermes is not taken over — run newgate on hermes first", nil))
		}
		res, err := syncCatalog()
		if err != nil {
			return host.Die(70, err.Error())
		}
		if len(res.Changes) == 0 {
			fmt.Println(style.Item(style.OK, i18n.T("the /model list is already up to date", nil)))
			return 0
		}
		for _, c := range res.Changes {
			fmt.Println(style.Item(style.OK, c))
		}
		fmt.Println(style.Hint(i18n.T("the gateway re-reads config.yaml on the next message; no restart needed", nil)))
		return 0
	case "restart":
		if err := restartGateway(false); err != nil {
			return host.Die(70, err.Error())
		}
		return 0
	default:
		return host.Die(64, i18n.T("hermes-gateway: unknown subcommand {sub} (status / sync / restart)",
			i18n.A{"sub": sub}))
	}
}

func printGateway() {
	fmt.Println(style.Item(style.OK, statusText(true)))
	g := gateway()
	switch {
	case !g.Running:
		fmt.Println(style.Hint(i18n.T("the hermes messaging gateway is not running", nil)))
	case g.Started.IsZero():
		fmt.Println(style.Hint(i18n.T("gateway pid {pid}", i18n.A{"pid": g.PID})))
	default:
		fmt.Println(style.Hint(i18n.T("gateway pid {pid}, started {at}",
			i18n.A{"pid": g.PID, "at": g.Started.Format("2006-01-02 15:04:05")})))
	}
	if rec := loadRecord(); rec.SyncNote != "" {
		fmt.Println(style.Hint(i18n.T("last sync: {what}", i18n.A{"what": rec.SyncNote})))
	}
}
