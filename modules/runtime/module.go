// Package runtime turns an Agent descriptor into a real client process.
//
// confighook decides "what the client is", config decides "which profile is current",
// runtime only finds the real binary hidden by the shim, builds the minimal env
// override and launches it. It resolves no model chain and modifies no client config.
//
// Legacy launch code still reads the catalog via the agentstate bridge. The Runtime
// component installs this read-only catalog in Start, restores it in Stop - clear lifetime.
package runtime

import (
	"context"

	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	configapi "github.com/rzbdz/newgate-ext/modules/config"
	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/runtime/agentstate"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	"github.com/rzbdz/newgate-ext/modules/runtime/launch"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
)

type service struct{}

var _ Runtime = (*service)(nil)

// New declares the client runtime component. Start wires the read-only AgentCatalog into
// the compatibility layer, Stop undoes the bridge; the Launch port itself stays stateless.
func New() modules.Component {
	var restore func()
	var releases []modules.Release
	service := &service{}
	return modules.Component{
		Name: "runtime",
		Desc: func() string {
			return i18n.T("takeover: rewrites a client's config so its traffic comes here; launches it back", nil)
		},
		Type: "client",
		Requires: []modules.Requirement{
			modules.Need(configapi.Capability),
			modules.Need(confighookapi.AgentCatalogCapability),
			// The state.json section this module owns is registered through confighook (see
			// RegisterStateField in Start below). This **was not declared** before, while Start
			// used MustGet - on 2026-09-21 tools/archmap's "imported but undeclared edge" probe
			// dug it out: with confighook removed, graph building failed first on **others**
			// (locale / pluginmanager), so this panic never ran and tests were green. Once an
			// assembly has this module but no confighook, it panics at Start.
			modules.Need(confighookapi.ConfigHooksCapability),
			// ui is a **weak dependency** (see component.Optional): the takeover status line,
			// the two checks, and this module's commands (start/stop/on/off/restart...) are all
			// contributed by it - with a UI they mount, without one they skip; takeover works.
			modules.Optional(cliapi.Capability),
			// Same for the web UI, and it is a **separate** edge: in a dashboard-only assembly
			// (no terminal UI), "who goes through newgate" must show on the web page too.
			modules.Optional(viewapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Runtime(service)),
			// The takeover half of the port: same service, another contract (see api.go's Control).
			modules.Provide(ControlCapability, Control(service)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			catalog := modules.MustGet(ctx, confighookapi.AgentCatalogCapability)
			restore = agentstate.Set(catalog)

			// taken_over in state.json belongs to this module (written when on/off wrap up).
			// Registered, someone catches "two modules grabbing one field" - it covered half.
			hooks := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			fieldRelease, err := hooks.RegisterStateField("runtime", "taken_over")
			if err != nil {
				return err
			}
			releases = append(releases, fieldRelease)

			// The web UI's copy (who goes through newgate) registers **first**: it does not need
			// cli, and a dashboard-only assembly needs it - once the block below returns, this
			// never runs (same as gateway/config).
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				rel, err := v.Register("runtime",
					viewapi.Title(func() string { return i18n.T("Takeover", nil) }).
						In(func() string { return i18n.T("Runtime", nil) }).
						Of(viewapi.FieldSettings), takeoverConcepts)
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}

			// The takeover status line and the two checks (takeover / backups) are self-reported:
			// this module writes those files; the UI should not read original/ for it.
			//
			// catalog is fetched here and now (Start's args are out of scope; both uses need it).
			cli, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			all := append(commands(catalog), launchCommands(service, catalog)...)
			for _, cmd := range all {
				release, err := cli.RegisterCommand(cmd)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			reporter := runtimeReporter{agents: catalog}
			for _, register := range []func() (modules.Release, error){
				func() (modules.Release, error) { return cli.RegisterStatus(reporter) },
				func() (modules.Release, error) { return cli.RegisterDiagnostics(reporter) },
				func() (modules.Release, error) { return cli.RegisterDump(reporter) },
			} {
				release, err := register()
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			return nil
		},
		Stop: func(context.Context) error {
			err := modules.ReleaseAll(releases)
			if restore != nil {
				restore()
			}
			return err
		},
	}
}

// Launch translates a port call into launch.Options, isolating runtime's launch protocol.
func (*service) Launch(agent *confighookapi.Agent, facts confighookapi.AgentFacts, args []string, profile string) int {
	return launch.Launch(agent, facts, args, launch.Options{Profile: profile})
}
