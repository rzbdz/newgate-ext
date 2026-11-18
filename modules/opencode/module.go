// Package opencode describes the OpenCode client: install, takeover, launch family.
//
// This module registers only OpenCode's Agent descriptor and family identity. How the concrete
// config file is taken over, and which extra slots OMO has, belongs to opencodeomo; this boundary
// keeps the base client from depending backwards on an optional plugin.
package opencode

import (
	"context"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	modules "github.com/rzbdz/newgate/component"

	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
)

// New declares the OpenCode client: install, takeover, launch component: it provides the family identity, and registers the
// client descriptor for its lifetime.
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name:     "opencode",
		Desc:     func() string { return i18n.T("the OpenCode client: install, takeover, launch", nil) },
		Type:     "client",
		Requires: []modules.Requirement{modules.Need(confighookapi.ConfigHooksCapability)},
		Provides: []modules.Provision{
			modules.Provide(Capability,
				Client{AgentID: ID}),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			var err error
			var release modules.Release
			hooks := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			release, err = hooks.RegisterAgent(Agent())
			if err != nil {
				return err
			}
			releases = append(releases, release)
			// Install method: what `newgate config` uses on a machine without it (the same opening as
			// codex / claude / hermes, see the kernel's confighook.AgentInstaller).
			release, err = hooks.RegisterAgentInstaller(ID, confighookapi.ExecInstaller(
				"npm", "install", "--global", NpmPackage))
			if err != nil {
				return err
			}
			releases = append(releases, release)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
