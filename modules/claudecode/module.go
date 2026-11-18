// Package claudecode describes the Claude Code client family.
//
// A client module owns only Claude Code's own facts: binary name, environment
// slots, protocol, and the request treatments a client generally needs. It
// registers the Agent with confighook, the plugins with gateway, and offers
// a tiny Client capability for cross-cutting components to recognize it.
//
// DeepSeek's or GLM's special replay rules do not belong here; behavior that only holds
// for "Claude Code meets some model" goes in claudecode_deepseek / claudecode_glm.
package claudecode

import (
	"context"

	modules "github.com/rzbdz/newgate/component"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	gatewayapi "github.com/rzbdz/newgate-ext/modules/gateway"
	thinkingapi "github.com/rzbdz/newgate-ext/modules/thinking"

	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// New declares the Claude Code client component. It registers the client
// descriptor and state fields, then wires the client-specific handlers
// into the gateway; every registration handle is released by Stop.
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "claudecode",
		Desc: func() string { return i18n.T("the Claude Code client", nil) },
		Type: "client",
		Requires: []modules.Requirement{
			modules.Need(gatewayapi.Capability),
			modules.Need(confighookapi.ConfigHooksCapability),
			modules.Need(thinkingapi.Capability),
			// ui is a **weak dependency** (see component.Optional): `newgate naked` is this module's
			// command, registered if a UI is installed and skipped if not. The dependency direction is
			// **this module -> cli**: cli knows no business module, and the module knows it instead.
			modules.Optional(cliapi.Capability),
			// Same for the web UI (another ui edge): in an assembly with only the
			// dashboard there is no terminal UI, and "is naked mode on right now" should
			// still be visible -- and in that assembly it is the only entry point.
			modules.Optional(viewapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability,
				Client{AgentID: ID}),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			config := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			release, err := config.RegisterAgent(Agent())
			if err != nil {
				return err
			}
			releases = append(releases, release)
			// Runtime facts (installed or not, which tier a slot takes right now) are **registered
			// separately** and not stuffed into the descriptor: the descriptor says "what this
			// client is", the facts say "how it is right now". See confighookapi.AgentFacts.
			release, err = config.RegisterAgentFacts(ID, facts{})
			if err != nil {
				return err
			}
			releases = append(releases, release)
			// Installation: `newgate config` or `newgate claude -y` on a machine without it goes
			// through this. The command is written here rather than in the kernel -- changing the
			// channel is this client's business (see AgentInstaller in the kernel's confighook).
			release, err = config.RegisterAgentInstaller(ID, confighookapi.ExecInstaller(
				"npm", "install", "--global", NpmPackage))
			if err != nil {
				return err
			}
			releases = append(releases, release)
			release, err = config.RegisterStateField("claudecode", "classifier_override")
			if err != nil {
				return err
			}
			releases = append(releases, release)
			release, err = config.RegisterStateField("claudecode", "classifier_naked")
			if err != nil {
				return err
			}
			releases = append(releases, release)
			gateway := modules.MustGet(ctx, gatewayapi.Capability)
			thinking := modules.MustGet(ctx, thinkingapi.Capability)
			for _, treatment := range Treatments(thinking) {
				release, err = gateway.RegisterRequestHook(treatment)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			release, err = gateway.RegisterRequestHook(classifierNaked{})
			if err != nil {
				return err
			}
			releases = append(releases, release)

			// The web UI copy (how the classifier is treated right now) registers **first**: it does not
			// depend on cli and must be there in an assembly with only the dashboard -- once the block below
			// returns, this would never run. And in that assembly this card is the **only** place naked mode
			// can be seen (status and the naked command both belong to cli); see the comment in view.go.
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				release, err = v.Register("claudecode",
					viewapi.Title(func() string { return i18n.T("Claude Code", nil) }).
						In(func() string { return i18n.T("Clients", nil) }).
						Of(viewapi.FieldClients),
					func() ([]viewapi.Concept, error) {
						return []viewapi.Concept{classifierConcept(), slotsConcept()}, nil
					})
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}

			// Contribute your own command yourself: the UI does not know this module, this module knows
			// the UI. Skipped if ui is not installed (see the Optional above): module features as usual.
			cli, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			release, err = cli.RegisterCommand(nakedCommand{})
			if err != nil {
				return err
			}
			releases = append(releases, release)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
