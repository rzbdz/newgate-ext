package upgrade

import (
	"context"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// New declares this component.
//
// The only dependency is cli (Optional): in an assembly with no UI shell (`dist-dashboard.json`)
// this module still provides the capability and others still register actions, just without the
// `newgate upgrade` entry - "business modules depend on no ui" here means "works with no terminal".
func New() modules.Component {
	reg := newRegistry()
	var releases []modules.Release
	return modules.Component{
		Name: Name,
		Desc: func() string {
			return i18n.T("upgrades the binary and the data modules own, asking first (newgate upgrade)", nil)
		},
		// Same layer as modellist / verify: infrastructure, not a client, not an upstream patch.
		Type: "infra",
		Requires: []modules.Requirement{
			modules.Optional(cliapi.Capability),
		},
		// It must exist at Bind time: other modules register actions in their own Start, and
		// they may start first (their optional dependency on this port orders them after, but
		// building the table in New is the habit - it skips depending on Start order).
		Provides: []modules.Provision{
			modules.Provide(Capability, Service(reg)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			ui, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			rel, err := ui.RegisterCommand(upgradeCommand{svc: reg})
			if err != nil {
				return err
			}
			releases = append(releases, rel)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
