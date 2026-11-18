package setup

// This file wires the wizard into assembly: one `newgate config` command.
//
// Deps are all **kernel public ports**, and all Optional - same criterion as our modules:
//
//	cli absent        no entry at all (the only one where nothing happens)
//	confighook absent no client list, so install/takeover is skipped wholesale
//	runtime absent    same (no takeover, no lifecycle)
//
// Missing the last two, the wizard **still** writes the first upstream - the most useful
// half of a web-only assembly. So it is not "one missing, drop everything".

import (
	"context"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	runtimeapi "github.com/rzbdz/newgate-ext/modules/runtime"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Name is this module's name in the assembly table and `newgate plugin` (= dir name).
const Name = "setup"

// New declares this component.
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: Name,
		Desc: func() string {
			return i18n.T("the first-run wizard: pick clients, one upstream, start (newgate config)", nil)
		},
		Type: "infra",
		Requires: []modules.Requirement{
			modules.Optional(cliapi.Capability),
			modules.Optional(agentapi.AgentCatalogCapability),
			modules.Optional(runtimeapi.ControlCapability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			ui, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			agents, _ := modules.Get(ctx, agentapi.AgentCatalogCapability)
			control, _ := modules.Get(ctx, runtimeapi.ControlCapability)
			rel, err := ui.RegisterCommand(configCommand{agents: agents, control: control})
			if err != nil {
				return err
			}
			releases = append(releases, rel)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}

// configCommand is `newgate config` (alias `setup`).
//
// Why the name was free: `newgate config` had no command in the config module (its
// commands are init / profile / tier / agents...), yet users type it first. It belongs
// to this module now because it does "**set everything up**", not "change one setting"
// - the latter is the tui and config commands' job.
type configCommand struct {
	agents  agentapi.AgentCatalog
	control runtimeapi.Control
}

var (
	_ cliapi.Command    = configCommand{}
	_ cliapi.Documented = configCommand{}
	_ cliapi.Unstyled   = configCommand{}
)

func (configCommand) Names() []string { return []string{"config", "setup"} }

// Unstyled: a full-screen wizard, not newgate's layout (see cliapi.Unstyled).
func (configCommand) Unstyled([]string) bool { return true }

func (configCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionMaintenance,
		Rank:    5, // first in the maintenance section: the first thing to do on a new machine
		Usage:   "config [--reconfigure] [--no-start]",
		Summary: i18n.T("first-run wizard: install and take over clients, write the first upstream, start the proxy", nil),
	}
}

func (c configCommand) Run(host cliapi.Host, args []string) int {
	if c.control == nil {
		// A skeleton assembly (runtime off) has no takeover or lifecycle. Writing the upstream
		// is still worth it, but `newgate config` promises the whole run - say so when it cannot.
		return host.Die(69, i18n.T("this build has no runtime module, so it cannot take clients over; use newgate init and newgate on instead", nil))
	}
	opt := Options{
		Reconfigure: cliapi.Flag(args, "--reconfigure") || cliapi.Flag(args, "--reset"),
		Yes:         cliapi.Flag(args, "--yes") || cliapi.Flag(args, "-y"),
		NoStart:     cliapi.Flag(args, "--no-start"),
	}
	return Run(Deps{Agents: c.agents, Control: c.control, Out: stdout}, opt)
}
