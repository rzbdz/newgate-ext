package config

import (
	"context"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	modules "github.com/rzbdz/newgate/component"

	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/roleprov"
)

type port struct{ roles *roleprov.Registry }

var _ Config = (*port)(nil)

// New declares the config component: it provides the one Config port and installs
// the dynamic tier registry during its lifetime. Stop restores the old defaults,
// so tests and repeated assembly leave no process-level compatibility state behind.
func New() modules.Component {
	port := &port{roles: roleprov.NewRegistry()}
	var restore func()
	var releases []modules.Release
	return modules.Component{
		Name: "config",
		Desc: func() string {
			return i18n.T("tiers, providers and state.json: what they mean, and how they are read and written", nil)
		},
		Type: "infra",
		Requires: []modules.Requirement{
			// ui is a **weak dependency** (see component.Optional): with the UI
			// installed, mount this module's own commands and display surfaces; without
			// it, skip -- config works as usual, just with no command-line entry point.
			modules.Optional(cliapi.Capability),
			// The web UI is equally a **weak dependency**, for exactly the same reason:
			// with web-dashboard installed, mount this module's control surface;
			// without it, skip. The two UIs register their own, and can coexist.
			modules.Optional(viewapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Config(port)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			restore = roleprov.InstallDefault(port.roles)

			// The web UI's copy of the display surfaces: **registered separately from
			// cli** (two UIs can coexist in one process). So the order of this block
			// has nothing to do with the next one, and must not be skipped by "return
			// if no cli" -- that is exactly the "dashboard only" configuration.
			//
			// Only producer functions are registered here; not one file is read: every `newgate ...` command
			// runs to here, and in most processes nobody will open the UI (see lib/view's package comment).
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				rel, err := registerView(v)
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}

			// The config's **own** commands and display surfaces (tier / profiles / profile / --set-profile
			// / init / reload, the files and chain-path checks, the "config" line and the two tables in
			// status) are injected into the UI by this module: the UI knows neither profile nor chain.
			ui, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			for _, cmd := range commands() {
				release, err := ui.RegisterCommand(cmd)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			for _, register := range []func() (modules.Release, error){
				func() (modules.Release, error) { return ui.RegisterDiagnostics(reporter{}) },
				func() (modules.Release, error) { return ui.RegisterStatus(reporter{}) },
				func() (modules.Release, error) { return ui.RegisterStatusBlocks(reporter{}) },
				func() (modules.Release, error) { return ui.RegisterDump(reporter{}) },
				func() (modules.Release, error) { return ui.RegisterGlossary(glossary{}) },
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

// RegisterRoleProvider delegates ownership of extensions
// to the role registry, and returns Release as is.
func (p *port) RegisterRoleProvider(provider RoleProvider) (modules.Release, error) {
	return p.roles.Register(provider)
}
