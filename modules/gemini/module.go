package gemini

import (
	"context"

	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// New declares the gemini-cli client: install, facts, launch (through the shim).
//
// No Config takeover and no gateway patch: this client is env-driven (shim mechanism, see
// agent.go) and speaks a dialect the gateway now parses natively. Anything upstream-specific
// (a quirk patch) would be its own module, not this one.
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "gemini",
		Desc: func() string {
			return i18n.T("the gemini-cli client: install, facts, launch", nil)
		},
		Type: "client",
		Requires: []modules.Requirement{
			modules.Need(confighookapi.ConfigHooksCapability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Client{AgentID: ID}),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			hooks := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			release, err := hooks.RegisterAgent(Agent())
			if err != nil {
				return err
			}
			releases = append(releases, release)

			// Install method: `newgate gemini` on a machine without it runs this. The command
			// is written here rather than in the kernel -- the package name is this client's
			// business (see AgentInstaller in confighook).
			release, err = hooks.RegisterAgentInstaller(ID, confighookapi.ExecInstaller(
				"npm", "install", "--global", NpmPackage))
			if err != nil {
				return err
			}
			releases = append(releases, release)

			release, err = hooks.RegisterAgentFacts(ID, facts{})
			if err != nil {
				return err
			}
			releases = append(releases, release)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
