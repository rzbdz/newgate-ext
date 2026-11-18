package hermes

import (
	"context"

	servingapi "github.com/rzbdz/newgate-ext/lib/serving"
	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	gatewayapi "github.com/rzbdz/newgate-ext/modules/gateway"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// New declares the Hermes client component.
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "hermes",
		Desc: func() string { return i18n.T("the Hermes Agent client", nil) },
		Type: "client",
		Requires: []modules.Requirement{
			modules.Need(confighookapi.ConfigHooksCapability),
			// web UI: when present, attach the "Hermes tiers" card (view.go); when not, skip.
			modules.Optional(viewapi.Capability),
			// terminal: the status line and `newgate hermes-gateway` (status.go / command.go).
			modules.Optional(cliapi.Capability),
			// serving: the model list's background sync runs only in the daemon (sync.go).
			// Without this edge (the skeleton distribution turns gateway off) there is no sync
			// -- `newgate on hermes` and `newgate hermes-gateway sync` work as usual, just not
			// automatically.
			modules.Optional(servingapi.Capability),
			// gateway: answer /models on hermes' behalf (st-models.go). A weak dependency --
			// the skeleton distribution turns gateway off, and then there is no proxy to ask.
			modules.Optional(gatewayapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Client{AgentID: ID}),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			hooks := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			add := func(r modules.Release, err error) error {
				if err != nil {
					return err
				}
				releases = append(releases, r)
				return nil
			}
			if err := add(hooks.RegisterAgent(Agent())); err != nil {
				return err
			}
			// installation: the official install script line (`newgate hermes -y`, or ask when
			// not installed). The command lives here rather than in the kernel -- switching
			// channels is this client's business.
			if err := add(hooks.RegisterAgentInstaller(ID, confighookapi.ExecInstaller(
				"bash", "-c", "curl -fsSL "+InstallScript+" | bash"))); err != nil {
				return err
			}
			if err := add(hooks.RegisterAgentFacts(ID, facts{})); err != nil {
				return err
			}
			// takeover: rewrite $HERMES_HOME/config.yaml (takeover.go). Values are computed live
			// at the moment of Apply.
			if err := add(hooks.BindTakeover(ID, Takeover{})); err != nil {
				return err
			}
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				if err := add(registerView(v)); err != nil {
					return err
				}
			}
			if ui, ok := modules.Get(ctx, cliapi.Capability); ok {
				if err := add(ui.RegisterStatus(status{})); err != nil {
					return err
				}
				if err := add(ui.RegisterCommand(gatewayCommand{})); err != nil {
					return err
				}
			}
			if gw, ok := modules.Get(ctx, gatewayapi.Capability); ok {
				if err := add(gw.RegisterRequestHook(modelsLister{})); err != nil {
					return err
				}
			}
			if s, ok := modules.Get(ctx, servingapi.Capability); ok {
				if err := add(s.OnServe("hermes-sync", startSync)); err != nil {
					return err
				}
			}
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
