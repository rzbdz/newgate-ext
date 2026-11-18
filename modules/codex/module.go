// Package codex describes the Codex (OpenAI's official CLI) client.
//
// This module does exactly three things: register the **descriptor** (what
// it is), the **install method** (how to install), and runtime facts (installed or
// not). How models are taken over and which slots exist belong to the later config
// module -- this split makes "installable" and "configurable" testable separately,
// and keeps the base client from depending on an optional capability in reverse.
package codex

import (
	"context"

	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	configapi "github.com/rzbdz/newgate-ext/modules/config"
	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	gatewayapi "github.com/rzbdz/newgate-ext/modules/gateway"
	pluginmanagerapi "github.com/rzbdz/newgate-ext/modules/pluginmanager"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// New declares the Codex client: install, takeover, launch component.
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "codex",
		Desc: func() string { return i18n.T("the Codex client: install, takeover, launch", nil) },
		Type: "client",
		Requires: []modules.Requirement{
			modules.Need(confighookapi.ConfigHooksCapability),
			// Dynamic roles (codex's model names -> tiers, see models.go). **A hard
			// dependency** rather than a weak one: the symptom when that table cannot
			// be registered is "switch a model in codex and get a 404", which is exactly
			// what this module now fixes -- silent degradation is unacceptable here.
			modules.Need(configapi.Capability),
			// Web UI: if present, mount the "Codex models" card (see view.go); if not, skip. A weak
			// dependency -- none of this module's functionality is lost, there is just no browser entry.
			modules.Optional(viewapi.Capability),
			// Gateway: this move (client -> generic upstream tool lift) needs a port that can Register
			// a request hook. **Weak dependency**: the skeleton distribution (dist-hello) turns gateway
			// off, and then the hook is not mounted and the client has no forwarding -- but the client's
			// own descriptor/facts are unaffected, and forcing a Need would keep hello from starting.
			modules.Optional(gatewayapi.Capability),
			// plugin-manager: the **runtime switch point** for the tool-lifting move
			// (`codex.lift-tools`). A weak dependency -- without it tool lifting runs as usual,
			// there is just no switch to turn it off (see registerToolsLift in st-tools.go).
			modules.Optional(pluginmanagerapi.Capability),
			// Terminal UI: if present, report the takeover mode into status (see status.go); if not, skip.
			modules.Optional(cliapi.Capability),
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

			// Install method: `newgate codex -y` (or the question when not installed) goes through
			// it. The command is written here rather than in the kernel -- changing the package
			// name or channel is this client's business (see AgentInstaller in confighook).
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

			// Takeover: rewrite ~/.codex/config.toml (see takeover.go). The tier and window declarations
			// are computed at **the moment of Apply** -- the slot mapping and the active profile can
			// be changed by the user at any time, and reading once at registration would freeze them.
			release, err = hooks.BindTakeover(ID, Takeover{})
			if err != nil {
				return err
			}
			releases = append(releases, release)

			// codex's model names <-> tiers (see models.go). This contribution **follows the takeover
			// mode**: roles are really registered only in rename mode, for the reason on rolesProvider
			// -- unconditional registration would short-circuit the "resolve a concrete model name back
			// to a tier" path, and that would change the behavior of **people who did not opt in**.
			release, err = modules.MustGet(ctx, configapi.Capability).RegisterRoleProvider(rolesProvider{})
			if err != nil {
				return err
			}
			releases = append(releases, release)

			// Tool dialect normalization (lift, see st-tools.go). Skip with no gateway -- see
			// the Optional comment. The switch point is reported to plugin-manager (without it
			// that move runs as usual, just with no runtime switch -- see registerToolsLift).
			if gw, ok := modules.Get(ctx, gatewayapi.Capability); ok {
				pm, _ := modules.Get(ctx, pluginmanagerapi.Capability)
				hookReleases, err := registerToolsLift(gw, pm)
				if err != nil {
					return err
				}
				releases = append(releases, hookReleases...)
			}

			// Web UI: register the "Codex models" card (see view.go).
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				release, err := registerView(v)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			// Terminal: the takeover-mode line (see status.go). The mode can be
			// switched on the web page; the terminal should at least see it --
			// this is exactly one place where "a new feature has no injection".
			if ui, ok := modules.Get(ctx, cliapi.Capability); ok {
				release, err := ui.RegisterStatus(status{})
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
