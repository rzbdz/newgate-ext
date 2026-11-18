// Package home turns "see all my chains on one screen" into a section, and declares itself the
// UI's landing.
//
// # Why it is a module, not part of config's card
//
// All of this section's material comes from config (see the comment on
// configapi.Config.AllChains), but it is not config's own presentation surface -- the two
// answer **two different questions**:
//
//	config's section   "how to change it": the tier binding editor, source files, global
//	                   switches;
//	this section       "what it is now": who this tier resolves to right now, who is skipped,
//	                   and why.
//
// The same data under two questions, the user looks in a different place when opening the UI,
// so there are two cards.
//
// What matters more is **position**: which section the landing fell on used to be Source's
// alphabetical order (`breaker` came first, so it became the default), and that was a
// coincidence -- see Section.Default in lib/view. For this the kernel provides only a door for
// "who may declare itself the landing"; **who the landing is, the module says itself**; and the
// kernel knows nothing about "which screen should be seen first" (core/CLAUDE.md §0), so this
// message can only come from the distribution, and it lands here.
//
// # It knows no other module
//
// There are only two dependencies: config's read port (hard -- all of this screen's material
// comes from it, nothing can be drawn without it) and the web UI (weak -- skip it if the UI is
// absent, this module loses no other function because of that, the same rule as all business
// modules). So wherever it is installed in a spec it entangles nobody, and removing it only
// moves the landing.
package home

import (
	"context"

	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	breakerapi "github.com/rzbdz/newgate-ext/modules/breaker"
	configapi "github.com/rzbdz/newgate-ext/modules/config"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	gatewayapi "github.com/rzbdz/newgate-ext/modules/gateway"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Name is this module's name in the assembly table and in `newgate plugin`.
//
// It is also the Source of that section in the UI (a machine marker, see
// view.Service.Register): URLs, the landing, and the return trip of actions all follow it, so
// it does not change with the wording of the title.
const Name = "home"

// New declares this section.
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: Name,
		Desc: func() string {
			return i18n.T("the landing screen: every chain this machine resolves right now, in one view", nil)
		},
		// "other": it is not the kernel, gateway, UI, client or model, nor a bridge between them.
		// The same kind as arch-diagram -- a presentation surface that owns no mechanism, and
		// forcing it into any category above would make `newgate plugin`'s grouping lie.
		//
		// Note it is **not** "cli" (that is the value for web-dashboard / tui / simple-cli):
		// that one is the "UI shell" itself, while this is a section shown by the UI. The
		// criterion comes from the same source as the kernel UI rule -- delete it and the UI
		// still stands, with one section fewer.
		Type: "others",
		Requires: []modules.Requirement{
			// Hard dependency: the chain's conclusion can only be computed by config (see the
			// comment on configapi.Config: consumers who want that conclusion come and take the
			// port, do not import store/resolve and assemble it yourself). There is **no room
			// for fail-open** here: without it this screen cannot draw a single card, and
			// silently drawing an empty screen would make the user think the config is lost.
			modules.Need(configapi.Capability),
			// Weak dependency: register if the UI is there, skip if not. Installed in any spec
			// (including one with no UI at all), it never fails to install.
			modules.Optional(viewapi.Capability),
			// The three below are for the **two extra things** of the first screen's card (see the file
			// header of overview.go: "health" and "use it", beyond chains). All weak
			// dependencies, on the same criterion -- **without them, can this screen still tell
			// the truth**: no health table, no latency cell; no gateway, no probe button; no
			// client catalog, no row of tabs above; while the chains are listed as usual and the
			// "all clients" tier can still switch the chain as usual.
			//
			// Why not make them hard: then this section would exist only in an assembly with
			// "gateway + health table + some client" -- and "which modules are installed on this
			// machine" is the distribution's business (core/CLAUDE.md §0); a home page should
			// not constrain the spec.
			modules.Optional(breakerapi.Capability),
			modules.Optional(agentapi.AgentCatalogCapability),
			modules.Optional(gatewayapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			v, ok := modules.Get(ctx, viewapi.Capability)
			if !ok {
				return nil
			}
			// All three Optionals use a hand-written fetch (modules.Get returns ok), **not
			// MustGet**: a weak dependency being absent is the norm, and MustGet would panic.
			agents, _ := modules.Get(ctx, agentapi.AgentCatalogCapability)
			health, _ := modules.Get(ctx, breakerapi.Capability)
			gateway, _ := modules.Get(ctx, gatewayapi.Capability)
			release, err := registerView(v, modules.MustGet(ctx, configapi.Capability), overviewDeps{
				agents: agents, health: health, gateway: gateway,
			})
			if err != nil {
				return err
			}
			releases = append(releases, release)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
