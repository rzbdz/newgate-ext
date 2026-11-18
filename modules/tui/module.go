// Package tui is a menuconfig-style terminal UI: for editing profiles / tier assignments.
//
// # It is a **standalone ui module** (extracted from modules/cli's inner subpackage, 2026-09-18)
//
// The cli is not the only UI. tui and a future web are peers: each is a module, each
// injects its entry into **the currently installed** ui (see CLAUDE.md §4, "ui is just a
// module").
//
// The direct benefit: modules/cli lost a chunk unrelated to "command-line parsing" - it
// used to live in cli/tui/ only because it was typed from the cli, not because it belongs there.
//
// # What it depends on
//
// Only two **shared leaves**: config/store and gateway/controlplane (profiles read/write,
// and telling the daemon to re-read after a write), plus confighook's **AgentCatalog**
// (which clients can be changed; optional - without it this screen only shows the global
// default). It does **not depend on cli**: it only injects a command, also optional
//
// Why store can be used directly: it is a leaf shared by gateway / runtime / config (same
// level as config/paths), not "some module's internals" - so this import is not a dependency
// edge, and its absence from Requires is correct (the ratchet only looks at Requires, on
// purpose: store and paths are "shared infrastructure", the same class as lib/).
//
// The controlplane.Notify() after writing is not required for correctness either (the
// watcher's fingerprint covers state.json, so it re-reads within a second at worst), but
// every config write in the repo notifies - see the note in tui.go.
package tui

import (
	"context"
	"fmt"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/lib/i18n"
)

// New declares the terminal UI module.
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "tui",
		Desc: func() string { return i18n.T("a menuconfig-style terminal interface", nil) },
		// Type is a product-layer classification, values set by the assembler (see pluginmanager).
		Type: "cli",
		Requires: []modules.Requirement{
			// ui is a **weak dependency** (see component.Optional): with a UI installed, mount
			// `newgate tui`; without, skip - this module has no other duty, so it does nothing.
			modules.Optional(cliapi.Capability),
			// The client catalog: with it there is a "which one" column; without, only the global default.
			modules.Optional(agentapi.AgentCatalogCapability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			ui, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			catalog, _ := modules.Get(ctx, agentapi.AgentCatalogCapability)
			release, err := ui.RegisterCommand(tuiCommand{catalog: catalog})
			if err != nil {
				return err
			}
			releases = append(releases, release)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}

// tuiCommand is `newgate tui` (alias menuconfig).
type tuiCommand struct{ catalog agentapi.AgentCatalog }

var (
	_ cliapi.Command    = tuiCommand{}
	_ cliapi.Documented = tuiCommand{}
	_ cliapi.Unstyled   = tuiCommand{}
)

func (tuiCommand) Names() []string { return []string{"tui", "menuconfig"} }

// Unstyled: a full-screen UI, not newgate's layout (see cliapi.Unstyled).
func (tuiCommand) Unstyled([]string) bool { return true }

func (tuiCommand) Help() cliapi.HelpLine {
	// Rank 50 = the maintenance section (same as the UI's own batch); gaps left for inserts.
	//
	// Summary is computed at help rendering, not at declaration: T here follows the language.
	return cliapi.HelpLine{
		Section: cliapi.SectionMaintenance,
		Rank:    50,
		Usage:   "tui",
		Summary: i18n.T("menuconfig-style UI: the profile each client uses, and the bindings behind it", nil),
	}
}

func (c tuiCommand) Run(host cliapi.Host, _ []string) int {
	if err := Run(c.catalog); err != nil {
		return host.Die(70, fmt.Sprintf("tui: %v", err))
	}
	return 0
}
