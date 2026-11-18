package home

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/lib/view"
	configapi "github.com/rzbdz/newgate-ext/modules/config"
	configmod "github.com/rzbdz/newgate-ext/modules/config"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// This family's criteria come from **two places**, each with its own reason.
//
// One in the comments of configapi / lib/view (row IDs must be `profile/tier`, actions are
// built by config and must be passed through verbatim, row IDs on a chain card are unique
// across the concept), the other on the product side (the first screen must land on this section,
// an empty chain must have something to say). So below there are both shape assertions like
// "lay out what the port gave you" and assertions that go through the UI path like "does it
// still hold after being hooked into the ledger".
//
// Every test starts a **real** small graph (this module + config): the material is not
// hand-built configapi structs but what **parsed out** of a few config files in a sandbox.
// Hand-built, what is asserted here is "I think config would give that", and not one of the
// real contracts (how Actions are built, the Skips categories, where Steps come from) is
// touched.

// fixture is all the apparatus of one test: a usable config root in a sandbox, one real graph,
// one ledger.
type fixture struct {
	t       *testing.T
	reg     *view.Registry
	cfg     configapi.Config
	concept view.Concept
}

// newFixture opens the sandbox, starts the graph, hooks this section into the ledger, and
// **asks the snapshot once**.
//
// Going through Snapshot rather than calling concepts() directly: that hop through the ledger
// is one of this section's contracts (the identity cannot be empty, Kind must be given, no
// second party may report the same ID), and it really happens in the UI.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	testkit.Sandbox(t)
	// The config root itself must stand up: having no profile and "not even the directory" are
	// two different things -- the former is a fresh install, the latter is ListProfiles failing
	// (an error, not an empty config).
	if err := os.MkdirAll(paths.Mappings(), 0o770); err != nil {
		t.Fatal(err)
	}
	seedProviders(t, `{"providers":{
		"ark":{"base_url":"https://ark.example/v3","api_key":"k"},
		"zhipu":{"base_url":"https://zhipu.example","api_key":"k"}
	}}`)

	// Install only what is under test: this module + the real config (it alone can compute the
	// chain conclusion).
	g := testkit.Start(t, New(), configmod.New())
	cfg := testkit.Get(g, configapi.Capability)

	f := &fixture{t: t, reg: view.NewRegistry(), cfg: cfg}
	// overviewDeps{} is the **all absent** tier: this graph installs only this module and
	// config, so the health table, client catalog and gateway are all missing. That is exactly
	// one assembly this section must hold in (see the three Optionals in module.go) -- the
	// first screen still produces cards, just without the latency cell, without the probe button,
	// and with only the "all clients" tab above. The fully-installed routes are verified by
	// overview_test.go with a bigger graph.
	if _, err := registerView(f.reg, cfg, overviewDeps{}); err != nil {
		t.Fatalf("registering this section into the ledger failed: %v", err)
	}
	f.concept = f.snapshot()
	return f
}

// snapshot asks the snapshot again and pulls out the first screen's card (a new conclusion every
// call -- the producer recomputes each time).
//
// By `overviewID`, not "take the first": this section has **only one** card today
// (`home.chains` was deleted 2026-09-29), so the two are equivalent right now -- and taking it
// by ID is still right when the next card is added back.
func (f *fixture) snapshot() view.Concept {
	f.t.Helper()
	list, err := f.reg.Snapshot()
	if err != nil {
		f.t.Fatal(err)
	}
	for _, c := range list {
		if c.ID == overviewID {
			return c
		}
	}
	f.t.Fatalf("there is no %s in this section", overviewID)
	return view.Concept{}
}

func (f *fixture) seedFile(rel, body string) { f.t.Helper(); seedFile(f.t, rel, body) }

func (f *fixture) setDefault(name string) {
	f.t.Helper()
	if err := store.SetDefaultProfile(name, false); err != nil {
		f.t.Fatalf("setting the default tier failed: %v", err)
	}
}

// seedProviders writes a providers.json (**without opening the sandbox again**: see the
// comment on seedFile).
func seedProviders(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(paths.Root(), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ProvidersFile(), []byte(body), 0o660); err != nil {
		t.Fatal(err)
	}
}

// seedFile writes a tier file. The same set as the kernel's tests (reusing the kernel layout:
// the name is the file name, the extension decides the format).
//
// **One sandbox per test** (testkit.Sandbox makes a new temp directory on every call) -- so
// newFixture opens it once and this layer only writes files into it.
func seedFile(t *testing.T, rel, body string) string {
	t.Helper()
	if err := os.MkdirAll(paths.Mappings(), 0o770); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(paths.Mappings(), rel)
	if err := os.WriteFile(abs, []byte(body), 0o660); err != nil {
		t.Fatal(err)
	}
	return abs
}

// cardOf / rowIn pull out a card, a row by name. Not finding it is Fatal -- it means this
// screen is missing something, and "one row missing" on this screen merely looks like "this
// machine is not configured like that".
func cardOf(t *testing.T, ch view.Chains, profile string) view.ChainCard {
	t.Helper()
	for _, c := range ch.Cards {
		if c.Profile == profile {
			return c
		}
	}
	t.Fatalf("there is no %q card (there are %d now)", profile, len(ch.Cards))
	return view.ChainCard{}
}

func rowIn(t *testing.T, card view.ChainCard, tier string) view.ChainRow {
	t.Helper()
	for _, r := range card.Roles {
		if r.Tier == tier {
			return r
		}
	}
	t.Fatalf("the %s card has no %q tier (it has %d tiers now)", card.Profile, tier, len(card.Roles))
	return view.ChainRow{}
}

// ---------- the first screen lands on it ----------

// TestTheSectionIsTheLanding: this section declares itself the landing, and the section name /
// group really reach the UI.
//
// This test guards a failure **nothing else goes red for**: Landing is a method chained after
// Title (see view.Landing); drop it and it still compiles, the UI still opens, only the
// first screen quietly falls back to "the section first in Source alphabetical order" -- which
// merely looks like "the layout does not suit me".
//
// Group is the same: drop `.In` and this section sorts to the very top of the sidebar (the
// ungrouped sections come first, see Sidebar.svelte), and since it is the first screen's
// landing point, "why is it first" becomes unexplainable in the UI.
func TestTheSectionIsTheLanding(t *testing.T) {
	f := newFixture(t)
	sections := f.reg.Sections()
	if len(sections) != 1 {
		t.Fatalf("this section should register only one column, actual %d", len(sections))
	}
	s := sections[0]
	if s.Source != Name {
		t.Errorf("Source should be the machine marker %q, actual %q", Name, s.Source)
	}
	if !s.Default {
		t.Error("this section declared Landing(), so Sections must include default")
	}
	if s.Title == "" {
		t.Error("the column name must not be empty (the UI would show a column of buttons with no title)")
	}
	// **Not grouped**: the sidebar puts the section that declared the landing first in the
	// directory and draws no group heading for it (see rows in Sidebar.svelte). Grouped, it would
	// be swept into the middle together with a heading, and "why is it on top" could no longer be
	// answered -- being first is itself the statement.
	if s.Group != "" {
		t.Errorf("the landing column should not be grouped (it would get rolled into the middle), actual grouped into %q", s.Group)
	}
}

// ---------- empty config ----------

// TestNoProfilesIsAnEmptyScreen: with no profile at all it **does not panic** and the card
// list is empty.
//
// This is what a fresh install looks like (providers.json just created, no tier file written
// yet), and it is this section's most common first run -- a nil dereference would blank the
// screen here rather than anywhere else.

// ---------- one screen: chain head, empty chain, order ----------

// TestRowsCarryTheHeadOfEachTier: one row per tier, the chain head is the **first stop**, an
// empty chain has none.
//
// The first row of "one row per tier" is the most easily wrong of the few things this test can
// pin: a chain is **not truncated** (maxAttempts is an execution bound, not membership), so the
// heavy row must still be followed by ark/deepseek-lite -- drawing only the chain head would
// make that stop disappear from the screen even though it is on the chain.

// TestAnEmptyChainIsRedAndSaysWhy: when every candidate is skipped this row is an empty chain
// -- no chain head, a bad tone, and an end line that says clearly why.
//
// These three kinds of "empty" look completely different in the UI: no chain head is a dim
// `no steps`, bad colors it, and Note is the only thing that can tell the user "go add an
// api_key". Without Note, the user sees a **red empty chain** and does not know what to
// touch (this is where config's trade-off "skips are summarized only, but must be given" lands
// on this card).

// TestSkipCategoriesAreAllNamed is the ratchet for that category table.
//
// skipKindOrder / skipLabel recognize the **machine markers** resolve stamps onto Skip.Kind,
// and those strings are **rewritten literals** in this package (the reason is in chains.go:
// chain conclusions always go through configapi, the distribution does not import resolve).
// The price of rewriting is that drift like "the kernel renamed a marker" is not caught by the
// compiler -- the symptom is a whole category of skips **vanishing in silence** (count not
// reported, name not drawn), while a few missing numbers on the chain card look perfectly
// normal.
//
// So here the **really resolved** skips from the sandbox are passed through: every Kind that
// appeared must be recognized by that table (otherwise skipLabel files it under "other" and
// the counts no longer add up).
func TestSkipCategoriesAreAllNamed(t *testing.T) {
	f := newFixture(t)
	// Several categories in one go: undefined (provider does not exist), no-key (no
	// credential), duplicate (the same candidate written twice), excluded (another profile
	// excluded).
	seedProviders(t, `{"providers":{
		"nokey":{"base_url":"https://nokey.example"},
		"ark":{"base_url":"https://ark.example/v3","api_key":"k"}
	}}`)
	f.seedFile("demo.kv", strings.Join([]string{
		"heavy=ghost/model,nokey/m,ark/deepseek-v3,ark/deepseek-v3",
		"",
	}, "\n"))
	f.seedFile("side.kv", "prio=90\nexcluded=true\nheavy=ark/deepseek-v3\n")
	f.setDefault("demo")

	all, err := f.cfg.AllChains()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, one := range all {
		for _, chain := range one.Keys {
			for _, s := range chain.Skips {
				seen[skipKind(s)] = true
			}
		}
	}
	if len(seen) < 3 {
		t.Fatalf("this config should produce several kinds of skips, actual only %v -- the assertions below would spin green", seen)
	}
	for kind := range seen {
		known := false
		for _, k := range skipKindOrder {
			if k == kind {
				known = true
			}
		}
		if !known {
			t.Errorf("skip category %q is not in this table: it gets silently folded into %q and the counts do not match."+
				"did resolve change the marker names?", kind, skipOther)
		}
	}
	// The other direction: for a category the table recognizes, the wording must not be the
	// fallback sentence.
	for _, kind := range skipKindOrder {
		if kind == skipOther {
			continue
		}
		if seen[kind] && skipLabel(kind) == skipLabel(skipOther) {
			t.Errorf("category %q was labeled as \"other\"", kind)
		}
	}
}

// ---------- row IDs ----------

// TestRowIDsAreProfileSlashTier: the row ID is `profile/tier`, and **unique across the whole
// concept**.
//
// A tier name alone is not enough: `heavy` appears ten times across ten profiles, and "switch
// this tier to X" must touch one line in one file (see view.ChainRow.ID). When two profiles
// both have heavy, the two IDs must be separable -- the consequence of not separating them is
// not a display error, it is **clicking one button and editing another file**.

// ---------- actions: passed through verbatim, and they really run ----------

// TestRowActionsArePassedThroughUntouched: the actions config built appear **verbatim** on the
// matching rows (same count and IDs), and running one really writes to disk.
//
// Why not "the shape is right and that is enough": those closures capture **this tier of this
// file** (file path, the baseline right now, who to switch to); they are config's knowledge and
// this layer only carries them. Both ways of carrying them wrong are quiet -- hung on the wrong
// row (click to change another tier), or rebuilt from scratch (the write-to-disk knowledge
// growing a second copy in the distribution). So here the IDs are compared one by one against
// the port, and `Run()` is really called once to look at the disk.
//
// That run goes through the entry view prepares for rows (RunRowAction): exactly the path the
// UI takes when the button is clicked, and it locates by three steps `(concept ID, row ID,
// action ID)` -- with a wrong row ID step this reports "this row has no such action".

// TestRowActionsStayOnTheirOwnCard: another profile's rows have **none** of this one's
// actions.
//
// Row IDs are unique across cards (the test above), and actions hang on rows -- the two
// combined fail quietly: building one card from **another** profile's conclusion, and the user
// clicks the button on alt's row only to edit demo's file. So here it asserts both "no button
// when there is nobody to switch to" and that the click **cannot touch another file** (when
// the UI's snapshot is stale, it asks with an action that does not exist).
