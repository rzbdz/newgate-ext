// Package opencodeomo wires Oh My OpenAgent in as an optional extension of OpenCode.
//
// OMO touches three boundaries at once: rewriting its own config file, contributing dynamic roles
// to config, and registering commands and diagnostics with the CLI. One component holds the Release
// for all of them, so the takeover and the registrations can be undone completely, while config,
// the CLI and the base opencode module learn nothing about OMO's format.
package opencodeomo

import (
	"context"
	"os"
	"path/filepath"

	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	configapi "github.com/rzbdz/newgate-ext/modules/config"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	opencodeapi "github.com/rzbdz/newgate-ext/modules/opencode"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

type configTakeover struct{}

var _ agentapi.ConfigTakeover = (*configTakeover)(nil)

// Takeover creates the OMO config takeover adapter; it returns an interface to hide the stateless
// implementation details.
func Takeover() agentapi.ConfigTakeover { return configTakeover{} }

// New declares the OMO integration component: it binds the OpenCode config takeover, contributes
// extra tiers, and registers its own commands and diagnostics with the CLI.
//
// Commands and diagnostics go through cli's Register (not Provide into a "command port"): every
// contribution gets a Release, and the four edges (takeover + roles + commands + diagnostics) share
// one releases accumulator, all undone together in Stop.
//
// This also **reverses the start order of cli and opencode-omo**: before 2026-09-17 omo started
// first, cli after it and took a snapshot once in Start; now cli starts first, and omo takes
// cliapi.Capability in its Start and then registers. The stop order reverses accordingly to omo
// first, cli after -- so the target is necessarily still alive when releasing.
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "opencode-omo",
		Desc: func() string {
			return i18n.T("oh-my-openagent's sub-agent slots: classify, bind, explain (newgate omo)", nil)
		},
		Type: "client",
		Requires: []modules.Requirement{
			modules.Need(configapi.Capability),
			modules.Need(agentapi.ConfigHooksCapability),
			// Read side: only to answer "is opencode installed at all" (whether that card should be
			// locked gray). The same provider (config-hook), so no new dependency edge.
			modules.Need(agentapi.AgentCatalogCapability),
			modules.Need(opencodeapi.Capability),
			// ui is a **weak dependency** (see component.Optional): with a UI installed, hang the omo
			// command and its checkup item up; without one, skip -- slot takeover works as usual.
			modules.Optional(cliapi.Capability),
			// The web UI likewise (the other one): an assembly with only the dashboard has no terminal
			// UI, and "which slot uses which model" should still be visible.
			modules.Optional(viewapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			config := modules.MustGet(ctx, agentapi.ConfigHooksCapability)
			configStore := modules.MustGet(ctx, configapi.Capability)
			client := modules.MustGet(ctx, opencodeapi.Capability)
			release, err := config.BindTakeover(client.AgentID, Takeover())
			if err != nil {
				return err
			}
			releases = append(releases, release)
			release, err = configStore.RegisterRoleProvider(omoRolesProvider{})
			if err != nil {
				return err
			}
			releases = append(releases, release)

			// The web UI's copy (slot bindings) is registered **first**: it does not depend on cli, and
			// must be there in a dashboard-only assembly too -- once the block below returns, this one
			// would never run.
			// Is opencode present on this machine: if not, this card changes the behavior of a tool that
			// does not exist, and the UI should lock the whole card gray (see lib/view's Concept.Locked).
			// The criterion exists in one copy only (the catalog port's Installed, minus our own shim);
			// this does not look up PATH itself.
			lock := ""
			if catalog := modules.MustGet(ctx, agentapi.AgentCatalogCapability); !catalog.Installed(client.AgentID) {
				// The reason carries **what to do**: this cell's entire use is telling the user "why it
				// cannot be changed" and "how to make it changeable". The command that installs it is
				// injected by the agent module itself (see confighook's Install capability), and `-y` is
				// only recognized when that is not installed -- otherwise the flag would be handed to
				// opencode itself as is.
				lock = i18n.T("opencode is not installed on this machine — "+
					"nothing on this card takes effect yet. Install it with `newgate opencode -y`.", nil)
			}
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				viewRelease, err := v.Register("opencode-omo",
					viewapi.Title(func() string { return i18n.T("opencode", nil) }).
						In(func() string { return i18n.T("Clients", nil) }).
						Of(viewapi.FieldClients),
					func() ([]viewapi.Concept, error) { return omoConcepts(lock) })
				if err != nil {
					return err
				}
				releases = append(releases, viewRelease)
			}

			cli, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			release, err = cli.RegisterCommand(omoCommand{})
			if err != nil {
				return err
			}
			releases = append(releases, release)
			release, err = cli.RegisterDiagnostics(diagnostics{})
			if err != nil {
				return err
			}
			releases = append(releases, release)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}

// Targets forwards the interface call to the target discovery for the current environment.
func (configTakeover) Targets() []string { return TargetFiles() }

// IsTakenOver checks whether a single target already points at newgate.
func (configTakeover) IsTakenOver(target string) bool {
	return IsTakenOver(target)
}

// Apply performs a reportable, reversible takeover on all targets.
func (configTakeover) Apply(port int) ([]*agentapi.TakeoverReport, error) {
	return ApplyAll(port)
}

// Restore undoes the config rewrites this adapter owns.
func (configTakeover) Restore() ([]string, error) { return RestoreAll() }

// SlotsFile returns the newgate-owned config path for OMO's dynamic tiers.
func SlotsFile() string { return filepath.Join(paths.Config(), "omo-slots.json") }

// TargetFiles locates the configs to take over per the OpenCode/XDG convention,
// and prefers the OMO file the user already has, to avoid creating a second config by accident.
func TargetFiles() []string {
	if dir := os.Getenv("NEWGATE_TARGET_DIR"); dir != "" {
		return []string{
			filepath.Join(dir, "opencode.json"),
			filepath.Join(dir, "oh-my-openagent.json"),
		}
	}
	home := paths.Home()
	configHome := filepath.Join(home, ".config")
	if value := os.Getenv("XDG_CONFIG_HOME"); value != "" {
		configHome = value
	}
	out := []string{filepath.Join(configHome, "opencode", "opencode.json")}
	for _, candidate := range []string{
		filepath.Join(configHome, "opencode", "oh-my-openagent.json"),
		filepath.Join(configHome, "oh-my-openagent", "oh-my-openagent.json"),
		filepath.Join(home, ".oh-my-openagent.json"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return append(out, candidate)
		}
	}
	return append(out, filepath.Join(configHome, "opencode", "oh-my-openagent.json"))
}

// writeAtomicMode is a thin wrapper over the kernel's WriteAtomic (see confighook/backup.go).
// It keeps the local name because the slot table uses 0660 (that file is shared with other users),
// and with one naming convention the reader need not pause over "which implementation is this".
func writeAtomicMode(path string, content []byte, mode os.FileMode) error {
	return agentapi.WriteAtomic(path, content, mode)
}
