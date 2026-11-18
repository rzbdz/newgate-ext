package pluginmanager

import (
	"context"

	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// New declares the runtime switch ledger.
//
// Ordinary module shape: one capability, one Start, one Stop. **No kernel hook was
// added for it** - it is in the graph because tools/genmodules scans directories,
// like the other 15 modules. "Special" only in that most modules depend on it.
//
// Dependency direction (fixed three times 2026-09-18; **current** shape):
//
//	other modules --Need--> plugin-manager --Need--> confighook
//	other modules --Optional--> cli (mounting the UI is a step in its Start)
//
// Two things must be read carefully:
//
//   - **This module does not depend on cli**, only a weak dependency (component.
//     Optional): cli once Needed it for `newgate plugin` and the status line, and it
//     needed Need(cli) to register a command - a cycle, so the command lived in cli.
//   - **cli does not depend on this module either** (Requires is empty). Status goes
//     via cli.RegisterStatus, commands via cli.RegisterCommand: "this module -> ui",
//     and the UI knows nobody. Cut the ui's out-edges and no cycle forms.
//
// The only hard dependency is confighook: registering state.json fields must go
// through its port (as modules/claudecode does for classifier_naked and
// classifier_override). This module wants the component list, so it implements the
// kernel's CatalogAware: handed over once after assembly (see the Manager note in api.go).
var _ modules.CatalogAware = (*service)(nil)

func New() modules.Component {
	service := &service{}
	var releases []modules.Release
	return modules.Component{
		Name: "plugin-manager",
		Desc: func() string {
			return i18n.T("the module list and each module's runtime switches (newgate plugin)", nil)
		},
		Type: TypeInfra,
		Requires: []modules.Requirement{
			// ui is a **weak dependency** (see component.Optional): with the UI installed,
			// mount `newgate plugin` and that status line; else skip - the ledger works
			// either way. The web UI is the same, each registers its own (both coexist).
			modules.Optional(cliapi.Capability),
			modules.Optional(viewapi.Capability),
			modules.Need(confighookapi.ConfigHooksCapability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Manager(service)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			hooks := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			release, err := hooks.RegisterStateField("plugin-manager", StateKey)
			if err != nil {
				return err
			}
			releases = append(releases, release)

			// It is itself on the books, same RegisterSelf and same prefix rule - being
			// the ledger earns no back door. `newgate plugin` lists it under infra.
			self, err := service.RegisterSelf("plugin-manager", nil)
			if err != nil {
				return err
			}
			releases = append(releases, self)

			// The web UI's copy: switch points drawn as switches. Placed **before**
			// cli's "return if absent" block - dashboard-only assemblies need it too.
			//
			// Registration stores only a producer, not "is it on now": every `newgate ...`
			// lands here, and almost nobody looks at the UI (see the lib/view package note).
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				rel, err := v.Register("plugin-manager",
					viewapi.Title(func() string { return i18n.T("Plugins", nil) }).
						In(func() string { return i18n.T("Runtime", nil) }).
						Of(viewapi.FieldSettings),
					func() ([]viewapi.Concept, error) {
						return viewConcepts(service)
					})
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}

			// The command and status line are this module's UI, contributed by itself -
			// the UI does not know it. Skipped if ui is absent: no entry, ledger still works.
			cli, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			cmd := &command{manager: service}
			cmdRelease, err := cli.RegisterCommand(cmd)
			if err != nil {
				return err
			}
			releases = append(releases, cmdRelease)
			statusRelease, err := cli.RegisterStatus(cmd)
			if err != nil {
				return err
			}
			releases = append(releases, statusRelease)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
