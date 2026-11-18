package home

import (
	"github.com/rzbdz/newgate-ext/lib/view"
	configapi "github.com/rzbdz/newgate-ext/modules/config"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This file is the face handed to the UI: **see these chains of mine on one screen**.

// registerView hangs this section up. It does nothing without a web UI (see module.go).
//
// Registration **reads not one file**: the producer function `concepts` runs only once someone
// actually comes to look at the UI. This matters especially here -- the chain verdicts re-read and
// re-parse every profile (see configapi.Config.AllChains and the package comment in lib/view),
// while every `newgate …` command reaches this point, and on the vast majority of them nobody
// opens the UI.
func registerView(v view.Service, cfg configapi.Config, deps overviewDeps) (modules.Release, error) {
	// `.Landing()`: the first screen lands on this section. When several declare it, the one with
	// the smallest Source wins (see Sections in lib/view), so this is not a "grab the seat" move --
	// an extra declaration does not make this screen random, it only settles by Source.
	//
	// **No group** (it used to be `.In("Overview")`): the sidebar puts **the entry that declared the
	// landing** first in the directory, and draws no group heading for it (see rows in
	// Sidebar.svelte). Grouped, it would be rolled into the middle together with a heading, and "why
	// is it at the top" would have no answer -- while at the top itself, the position is the answer,
	// the same reason as "the ungrouped entries come first".
	section := view.Title(func() string { return i18n.T("Home", nil) }).Landing().Of(view.FieldRoutes)
	// This section carries **exactly one** action: the fallback state switch (see fallbackAction in
	// overview.go). There used to be a "probe them all" as well, removed per the user on 2026-09-29
	// (his words: "the probe-all button on the home page is stupid too, just remove it") -- probing
	// one profile is the card head's job, while "probe them all" is a batch operation that hits a
	// dozen upstreams and takes tens of seconds for a result, and on a screen meant for scanning,
	// the person clicking it mostly just wants to see one provider's latency.
	// The fallback-chain master switch, hung in the top right next to probing (same criterion: not
	// hung without the gateway -- there are no chains, and turning off a switch that does not exist
	// is meaningless).
	if a, ok := fallbackAction(deps.gateway); ok {
		section = section.Does(a)
	}
	return v.Register(Name, section, concepts(cfg, deps))
}

// concepts is "what these chains look like right now". Every call re-reads from disk -- a tier the
// CLI just created, a providers.json just changed, shows up in the next snapshot (see that
// passage in the view package comment).
//
// When it cannot be read, **the whole section errors** (rather than drawing an empty screen):
// AllChains has only two kinds of error -- the config directory being entirely unreadable, or not
// one profile being readable, and neither means "this machine has no chains". An empty screen
// would make the user think the config is gone, while the real cause (permissions, path) would not
// show a single word. This does not conflict with config handing out Broken per card: there **one
// file** is broken and the rest carry on; here what fails is "all the chains" itself.
//
// # Two concepts share one AllChains
//
// This section has two cards (the first-screen overview + the read-one-by-one chain card), and
// what they lay out must be the config of **the same moment**. Asking AllChains separately costs
// more than double (that one re-reads and re-parses every profile); worse, the disk may be changed
// between the two calls -- so the two cards state two different configs, and nothing on screen
// looks odd. So the chains are fetched once and passed down (see chains.go's file header).
func concepts(cfg configapi.Config, deps overviewDeps) view.Contributor {
	return func() ([]view.Concept, error) {
		all, err := cfg.AllChains()
		if err != nil {
			return nil, err
		}
		// **One card only**. There used to be a `home.chains` here too (the read-one-by-one screen:
		// what each tier resolves to, who was skipped, and why), deleted per the user on 2026-09-29 --
		// his words: "this tab, the card is useless, just delete it: candidate chains". Everything it
		// said has a copy on the first-screen card's timetable (opening that card gives the same
		// conclusion), and all it bought extra was one tab strip. That strip is especially expensive on
		// this screen: the home page wants "see it all on one screen", and one more tab is one more
		// layer to click through.
		return []view.Concept{overviewConcept(all, deps)}, nil
	}
}
