package home

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/lib/view"
	breakerapi "github.com/rzbdz/newgate-ext/modules/breaker"
	configapi "github.com/rzbdz/newgate-ext/modules/config"
	configmod "github.com/rzbdz/newgate-ext/modules/config"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	gatewayapi "github.com/rzbdz/newgate-ext/modules/gateway"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This family verifies **the first screen's card** (home.overview): whether the two things it has
// that the chain card does not -- "who is this client using right now" and "how fast is each
// station on the chain" -- are really laid out per the contract.
//
// Same approach as chains_test.go: bring up a **real** small graph (this module + config, plus the
// ports this screen uses), with material parsed from a few config files in the sandbox. Each of
// the three optional ports gets a **fake** stand-in (see provides): installing the real ones would
// each drag in a string of other dependencies, and those have nothing to do with what is being
// verified here. The stand-in implements the **contract interface**, not a copy of the real
// module's internals.

// provides builds a test component that provides exactly one capability.
func provides(name string, p modules.Provision) modules.Component {
	return modules.Component{
		Name:     name,
		Desc:     func() string { return "test stub" },
		Type:     "others",
		Provides: []modules.Provision{p},
		Start:    func(context.Context, modules.Context) error { return nil },
		Stop:     func(context.Context) error { return nil },
	}
}

// installedFacts is the runtime fact "this client is installed" (see confighook.AgentFacts).
//
// Why not look Agent.Bin up on PATH (that is the default criterion): the test machine has neither
// the claude nor the codex command, and **a test must not depend on what is installed on the dev
// machine**. Facts win over PATH, which is the very wording of that criterion in the real
// implementation (see confighook.InstalledDefault).
type installedFacts bool

func (f installedFacts) Installed() bool             { return bool(f) }
func (installedFacts) SlotTier(agentapi.Slot) string { return "" }

// ovFixture is the whole apparatus for this family.
type ovFixture struct {
	t    *testing.T
	reg  *view.Registry
	cfg  configapi.Config
	deps overviewDeps
}

// newOverviewFixture opens a sandbox, brings up the graph, and registers this section into the
// ledger.
//
// deps are decided by the stand-ins in extra: **different tests install a different subset of the
// optional ports**, and "none installed" is one of them (and then this screen must still hold
// up).
func newOverviewFixture(t *testing.T, extra ...modules.Component) *ovFixture {
	t.Helper()
	testkit.Sandbox(t)
	if err := os.MkdirAll(paths.Mappings(), 0o770); err != nil {
		t.Fatal(err)
	}
	// The config root must exist: "no profile at all" and "not even the directory" are two different
	// things -- the former is a fresh install, the latter is ListProfiles failing to read (that is an
	// error, not an empty config).
	seedProviders(t, `{"providers":{
		"ark":{"base_url":"https://ark.example/v3","api_key":"k"},
		"zhipu":{"base_url":"https://zhipu.example","api_key":"k"}
	}}`)
	comps := append([]modules.Component{New(), configmod.New()}, extra...)
	g := testkit.Start(t, comps...)

	f := &ovFixture{t: t, reg: view.NewRegistry(), cfg: testkit.Get(g, configapi.Capability)}
	// Take the three optional ports. Absence is legal (zero value), and that is one of the tested
	// properties.
	if v, ok := testkit.Maybe(g, agentapi.AgentCatalogCapability); ok {
		f.deps.agents = v
	}
	if v, ok := testkit.Maybe(g, breakerapi.Capability); ok {
		f.deps.health = v
	}
	if _, err := registerView(f.reg, f.cfg, f.deps); err != nil {
		t.Fatalf("registering this section into the ledger failed: %v", err)
	}
	return f
}

// overview fetches this moment's first-screen card.
func (f *ovFixture) overview() view.Overview {
	f.t.Helper()
	all, err := f.reg.Snapshot()
	if err != nil {
		f.t.Fatalf("snapshot failed: %v", err)
	}
	for _, c := range all {
		if c.ID != overviewID {
			continue
		}
		if c.Kind != view.KindOverview {
			f.t.Fatalf("Kind should be %q, actual %q", view.KindOverview, c.Kind)
		}
		// Assert on the type, not on the JSON: the shape is **contract** (the front end picks its
		// renderer by Kind too), so a failed conversion means someone changed the type of Data -- and on
		// the UI side that only shows up as "this screen went blank".
		data, ok := c.Data.(view.Overview)
		if !ok {
			f.t.Fatalf("Data should be view.Overview, actual %T", c.Data)
		}
		return data
	}
	f.t.Fatalf("this section did not report %s", overviewID)
	return view.Overview{}
}

// auto fetches the "default" card at the front of the grid (it is not a profile, see
// view.OverviewCard.Auto).
func (f *ovFixture) auto() view.OverviewCard {
	f.t.Helper()
	a := f.overview().Auto
	if a == nil {
		f.t.Fatal("there is no \"default\" card on the landing screen")
	}
	if !a.Auto {
		f.t.Fatal("that card was not marked Auto -- the UI will draw it as a profile")
	}
	return *a
}

func (f *ovFixture) card(profile string) view.OverviewCard {
	f.t.Helper()
	for _, c := range f.overview().Cards {
		if c.Profile == profile {
			return c
		}
	}
	f.t.Fatalf("there is no %q card on the landing screen", profile)
	return view.OverviewCard{}
}

// actions is the set of action IDs on this card (the assertions ask "is it there", not the order).
func actions(c view.OverviewCard) map[string]bool {
	out := map[string]bool{}
	for _, a := range c.Actions {
		out[a.ID] = true
	}
	return out
}

// seedFile writes one profile to disk (reusing the helper in chains_test.go).
func (f *ovFixture) seedFile(rel, body string) {
	f.t.Helper()
	seedFile(f.t, rel, body)
}

// twoProfiles is this family's most-used hand: production (the global default) and cheap.
func (f *ovFixture) twoProfiles() {
	f.t.Helper()
	seedFile(f.t, "production.kv", "normal = ark/deepseek-v3\n")
	seedFile(f.t, "cheap.kv", "normal = zhipu/glm-4\n")
	if err := store.SetDefaultProfile("production", false); err != nil {
		f.t.Fatal(err)
	}
}

// ---------- the two paths of this screen ----------

// TestOverviewIsTheOnlyCardOnTheLandingSection: this section has **exactly one card**.
//
// There used to be two: the first-screen overview, plus a `home.chains` (the read-it-one-by-one
// screen). On 2026-09-29 the latter was deleted per the user (his words: "this tab, the card is
// useless, just delete it: candidate chains") -- everything it said is on the first screen's
// timetable, and all it bought extra was one tab strip. This pins "deleted cleanly": one card
// coming back means one more tab to click through in the UI.
func TestOverviewIsTheOnlyCardOnTheLandingSection(t *testing.T) {
	f := newOverviewFixture(t)
	f.twoProfiles()

	all, err := f.reg.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		ids := make([]string, 0, len(all))
		for _, c := range all {
			ids = append(ids, c.ID)
		}
		t.Fatalf("this section should have only one card, actual %d: %v", len(all), ids)
	}
	ov := all[0]
	if ov.ID != overviewID {
		t.Errorf("that one should be %s, actual %s", overviewID, ov.ID)
	}
	if ov.Kind != view.KindOverview {
		t.Errorf("Kind should be %q, actual %q", view.KindOverview, ov.Kind)
	}
	// Every profile it lays out must be there (one missing means "some chain does not exist on the
	// UI", and nothing would report that).
	if got := len(ov.Data.(view.Overview).Cards); got != 2 {
		t.Errorf("both tiers should each have a card, actual %d", got)
	}
}

// TestOverviewAgentsAndUseActions: with the client catalog present, the top lists tabs from it,
// and the "use this" button is reported once per client, **excluding the one already in use**.
//
// Every criterion is one that really comes up in the product:
//   - `codex` was set on its own (state.active.codex=cheap), so the production card must have
//     `use:codex`;
//   - `claude` was never set on its own (it follows the global default = production), so the
//     production card **must not** have `use:claude` -- pointing it at itself is meaningless;
//   - the global entry (`use:global`) appears only when "this profile is not the global default";
//   - **an uninstalled client reports no button at all**: changing it would route nothing through
//     it.
func TestOverviewAgentsAndUseActions(t *testing.T) {
	cat := testkit.NewCatalog().
		Add(&agentapi.Agent{ID: "claude"}, &agentapi.Agent{ID: "codex"}, &agentapi.Agent{ID: "opencode"}).
		WithFacts("claude", installedFacts(true)).
		WithFacts("codex", installedFacts(true)).
		WithFacts("opencode", installedFacts(false))
	f := newOverviewFixture(t, provides("catalog-stub",
		modules.Provide(agentapi.AgentCatalogCapability, cat.AsCatalog())))
	f.twoProfiles()
	if err := store.SetActiveProfile("codex", "cheap"); err != nil {
		t.Fatal(err)
	}

	ov := f.overview()
	var ids []string
	for _, a := range ov.Agents {
		ids = append(ids, a.ID)
	}
	// The empty "all clients" entry at the end **must be there**: `use:global:<profile>` can only be
	// drawn while that is the current entry (see actions() in Overview.svelte), so dropping this
	// entry equals deleting "switch every client's chain together" from this screen -- and silently
	// at that.
	if strings.Join(ids, ",") != "claude,codex,opencode," {
		t.Fatalf("tabs should be sorted by id, all three clients present, and an empty tier at the end, actual %v", ids)
	}
	last := ov.Agents[len(ov.Agents)-1]
	if last.ID != "" || !last.Ready {
		t.Errorf("the last tier should be the empty \"all clients\" tab and clickable, actual %+v", last)
	}
	if last.Profile != "production" {
		t.Errorf("the empty tier should report the global default production, actual %q", last.Profile)
	}
	for _, a := range ov.Agents {
		if a.ID == "" {
			continue
		}
		want := a.ID != "opencode"
		if a.Ready != want {
			t.Errorf("Ready for %s should be %v, actual %v", a.ID, want, a.Ready)
		}
		if a.ID == "codex" && a.Profile != "cheap" {
			t.Errorf("codex has its own cheap setting, the tab should say so, actual %q", a.Profile)
		}
		if a.ID == "claude" && a.Profile != "production" {
			t.Errorf("claude has no override, it should report the global default production, actual %q", a.Profile)
		}
		// Own and Profile are two different things, and this is exactly where they separate: codex was
		// set on its own (Own=true), claude merely follows the global default and happens to resolve to
		// production (Own=false). Without this cell, the UI can only say both states in one sentence --
		// and their routing differs: change the global default and claude moves, codex does not.
		if a.ID == "codex" && !a.Own {
			t.Error("codex has its own override, so Own should be true")
		}
		if a.ID == "claude" && a.Own {
			t.Error("claude has no override and just follows the default, so Own should be false")
		}
	}

	prod := actions(f.card("production"))
	if !prod[useActionID("codex", "production")] {
		t.Error("the production card should have use:codex (codex on its own is set to cheap)")
	}
	// **This one is the point of the whole block**: claude really is on production right now (it
	// follows the global default, and the default is production), but it is **not pinned** to
	// production -- "pin it" and "let it follow" are two states, so the "just use it" button must
	// still be there. The previous version compared against the resolved result, judged "it is
	// already in use", and so the step of pinning **could never be made**.
	if !prod[useActionID("claude", "production")] {
		t.Error("claude follows the default on production but is not pinned -- there should be a use:claude to pin it")
	}
	if prod[useAutoPrefix+"claude"] {
		t.Error("claude is not pinned to any of them, so it should not offer \"back to auto\"")
	}
	if prod[useActionID(useGlobal, "production")] {
		t.Error("production is the global default, so it should not offer use:global")
	}
	if prod[useActionID("opencode", "production")] {
		t.Error("opencode is not installed, so its buttons should not be offered")
	}

	cheap := actions(f.card("cheap"))
	if !cheap[useActionID("claude", "cheap")] {
		t.Error("the cheap card should have use:claude:cheap (pin claude to cheap)")
	}
	if cheap[useActionID("codex", "cheap")] {
		t.Error("codex is already pinned to cheap, so use:codex should not be offered again")
	}
	// **The same card also carries the "point auto at it" entry**: it is drawn only on the "auto"
	// tier (the front end picks by the current tier, see actions in Overview.svelte). The two
	// sentences living on one card is on purpose -- same card, same gesture, meaning decided by
	// which tier you are on.
	if !cheap[useActionID(useGlobal, "cheap")] {
		t.Error("the cheap card should have use:global:cheap (clicking it under the auto tier points auto at cheap)")
	}

	// The "auto" one: it resolves to production right now, and it **lays out no member table** --
	// which profiles it can point at is said by the profile cards below (clicking them changes
	// auto).
	au := f.auto()
	if au.Profile != "production" {
		t.Errorf("\"auto\" should report the production that it currently resolves to, actual %q", au.Profile)
	}
	if !au.Default || au.File == "" {
		t.Errorf("\"auto\" should carry the default marker and the file it points at, actual %+v", au)
	}
	am := actions(au)
	if am[useActionID(useGlobal, "cheap")] {
		t.Error("the \"auto\" card should have no member actions -- you choose by clicking a profile card itself")
	}
	if !am[useAutoPrefix+"codex"] {
		t.Error("codex has its own override, so there should be a \"follow auto\" entry")
	}
	if am[useAutoPrefix+"claude"] {
		t.Error("claude has no override and already follows, so there should be no \"follow auto\" for it")
	}
	// Its chain must be **identical word for word** to the profile it points at: built separately,
	// config changing between the two disk reads would make one profile show two different chains
	// on two cards.
	if len(au.Roles) != len(f.card("production").Roles) {
		t.Errorf("the \"auto\" chain should match the production one word for word, tier counts %d vs %d",
			len(au.Roles), len(f.card("production").Roles))
	}
	if cheap[useActionID("opencode", "cheap")] {
		t.Error("opencode is not installed, so its buttons should not be offered")
	}
}

// TestOverviewUseActionWritesTheHeader: clicking "just use it" really must land on that client's
// row in state.json.
//
// Take the **action** path (RunConceptAction) rather than calling store directly: that is what the
// UI click really is, and this path has a hop (the ledger finds the card by concept ID, then finds
// that ID among the card's actions) that only holds at the moment of the call -- a mistyped action
// ID (the part after `use:`) shows up on this hop as "the button does nothing when clicked".
func TestOverviewUseActionWritesTheHeader(t *testing.T) {
	cat := testkit.NewCatalog().Add(&agentapi.Agent{ID: "claude"}).
		WithFacts("claude", installedFacts(true))
	f := newOverviewFixture(t, provides("catalog-stub",
		modules.Provide(agentapi.AgentCatalogCapability, cat.AsCatalog())))
	f.twoProfiles()

	if got := store.LoadState().Active["claude"]; got != "" {
		t.Fatalf("at the start claude should have no override, actual %q", got)
	}
	if _, err := f.reg.RunConceptAction(overviewID, useActionID("claude", "cheap")); err != nil {
		t.Fatalf("running use:claude failed: %v", err)
	}
	if got := store.LoadState().Active["claude"]; got != "cheap" {
		t.Fatalf("after running use:claude, state.active.claude should be cheap, actual %q", got)
	}
	// Run the same action a second time and it is no longer on that row (one "already using it" is
	// not reported) -- that is exactly what "the button disappeared" looks like on the protocol,
	// and it must be reported as a sentence that makes sense, not as silence.
	if _, err := f.reg.RunConceptAction(overviewID, useActionPrefix+"claude"); err == nil {
		t.Error("once it is already in use this action should no longer exist, the second click should report it")
	}
}

// TestOverviewLatencyLandsOnTheStation: with samples in the health table, that station on the
// chain must carry a number and a color, and the color follows breaker's tier (the thresholds have
// a single implementation, see breaker/status).
//
// This guards the first screen's most important thing: the user compares speeds with it. One entry
// for each of the three tiers, and the "no samples" one **must carry nothing** -- drawing a 0ms is
// saying "so fast there is no latency", while the truth is "unknown".
func TestOverviewLatencyLandsOnTheStation(t *testing.T) {
	table := breakerapi.NewTable()
	f := newOverviewFixture(t, provides("breaker-stub",
		modules.Provide(breakerapi.Capability, table)))
	f.seedFile("production.kv",
		"normal = ark/fast-model, ark/ok-model, ark/slow-model, ark/unprobed-model\n")

	// Record one probe verdict each for the three. The slow one at 13000ms crosses SlowMs=12000.
	for _, c := range []struct {
		model string
		ms    time.Duration
	}{{"fast-model", 900 * time.Millisecond}, {"ok-model", 4 * time.Second}, {"slow-model", 13 * time.Second}} {
		table.RecordProbe("ark", c.model, 200, 0, c.ms, 12*time.Second, "")
	}
	if n := len(table.Snapshot()); n == 0 {
		t.Fatal("not a single health record was kept -- do the snapshot keys and the RecordProbe keys not match?")
	}

	// Tier rows are ordered by capability, high to low (domain.Roles), and this profile only writes
	// normal -- so Roles[0] cannot be taken (that is heavy, and empty).
	var row view.OverviewRow
	for _, r := range f.card("production").Roles {
		if r.Tier == "normal" {
			row = r
		}
	}
	if row.Tier == "" {
		t.Fatal("there should be a normal row on production")
	}
	byModel := map[string]view.OverviewStep{}
	for _, s := range row.Steps {
		byModel[s.Model] = s
	}
	for _, c := range []struct{ model, tone string }{
		{"fast-model", view.ToneOK}, {"ok-model", view.ToneWarn}, {"slow-model", view.ToneBad},
	} {
		s, ok := byModel[c.model]
		if !ok {
			t.Fatalf("the chain is missing %s", c.model)
		}
		if s.LatencyMs == 0 {
			t.Errorf("%s should carry a latency sample", c.model)
			continue
		}
		if s.LatencyTone != c.tone {
			t.Errorf("the tone of %s should be %q, actual %q (%dms)", c.model, c.tone, s.LatencyTone, s.LatencyMs)
		}
	}
	if s := byModel["unprobed-model"]; s.LatencyMs != 0 || s.LatencyTone != "" {
		t.Errorf("the station with no sample should carry nothing, actual %dms/%q", s.LatencyMs, s.LatencyTone)
	}
}

// TestOverviewWithoutOptionalPorts: with no optional port installed at all, this screen must
// **still draw its card as usual**.
//
// This is the only place where the "one thing missing and the whole card vanishes" class of bug
// goes red: no health table means no latency, no gateway means no probe button, no client catalog
// means only the "all clients" entry -- while not one chain is missing, and the "all clients use
// it" sentence is still there too (under this assembly it is the only way to switch the chain).
func TestOverviewWithoutOptionalPorts(t *testing.T) {
	f := newOverviewFixture(t)
	f.twoProfiles()

	ov := f.overview()
	if len(ov.Agents) != 1 || ov.Agents[0].ID != "" || !ov.Agents[0].Ready {
		t.Fatalf("with no client catalog there should be only one empty \"all clients\" tier, actual %+v", ov.Agents)
	}
	if len(ov.Cards) != 2 {
		t.Fatalf("both cards should be there, actual %d", len(ov.Cards))
	}
	if !actions(f.card("cheap"))[useActionID(useGlobal, "cheap")] {
		t.Error("\"point auto at it\" must be present -- with no client installed it is the only way to switch chains")
	}
	for _, c := range ov.Cards {
		if actions(c)["probe"] {
			t.Errorf("%s card offers a probe button, but this graph has no gateway", c.Profile)
		}
		for _, r := range c.Roles {
			for _, s := range r.Steps {
				if s.LatencyMs != 0 || s.LatencyTone != "" {
					t.Errorf("with no health table there should be no latency: %s/%s carried %dms", s.Provider, s.Model, s.LatencyMs)
				}
			}
		}
	}
}

// ---------- probe fan-out ----------

// TestProbeBatchReportsWhatWentWrong: when probing a batch, **the ones that did not connect must
// be said out loud**.
//
// The answer the person pressing this button wants is exactly "which ones do not connect", so
// those must appear in the returned sentence; only when all connected is nil returned (and then
// the banner should not appear).
func TestProbeBatchReportsWhatWentWrong(t *testing.T) {
	targets := []probeTarget{
		{provider: "a", model: "m1", key: "a/m1"},
		{provider: "a", model: "m2", key: "a/m2"},
	}
	err := probeBatch(func(provider, model string) (gatewayapi.ProbeOutcome, error) {
		if model == "m2" {
			return gatewayapi.ProbeOutcome{}, i18n.E("no route to host", nil)
		}
		return gatewayapi.ProbeOutcome{OK: true, Status: 200}, nil
	}, targets)
	if err == nil {
		t.Fatal("one did not get through, this request must report it (a silent success is the worst answer)")
	}
	if !strings.Contains(err.Error(), "a/m2") {
		t.Errorf("the sentence should name which one did not get through, actual: %v", err)
	}
	if strings.Contains(err.Error(), "a/m1") {
		t.Errorf("the one that got through must not be counted as failed, actual: %v", err)
	}

	if ok := probeBatch(func(string, string) (gatewayapi.ProbeOutcome, error) {
		return gatewayapi.ProbeOutcome{OK: true, Status: 200}, nil
	}, targets); ok != nil {
		t.Errorf("when everything gets through there should be no banner, actual: %v", ok)
	}
}

// TestProbeTargetsDedupe: the same binding appears many times on one screen (cross-profile chains
// are normal), and a probe **must hit it only once**: the same upstream hit twice by us at the same
// moment brings no new information, and puts pressure on the other side.
func TestProbeTargetsDedupe(t *testing.T) {
	chains := []configapi.Chain{
		{Key: "normal", Steps: []configapi.Step{bind("p", "m1"), bind("p", "m2")}},
		{Key: "light", Steps: []configapi.Step{bind("p", "m2"), bind("p", "m1"), bind("q", "m1")}},
	}
	seen := map[string]int{}
	got := probeTargetsOf(chains)
	for _, t2 := range got {
		seen[t2.key]++
	}
	for _, want := range []string{"p/m1", "p/m2", "q/m1"} {
		if seen[want] != 1 {
			t.Errorf("%s should appear exactly once, actual %d times", want, seen[want])
		}
	}
	if len(got) != 3 {
		t.Errorf("after deduplication there should be 3 entries, actual %d", len(got))
	}
}

func bind(provider, model string) configapi.Step {
	return configapi.Step{Binding: domain.Binding{Provider: provider, Model: model}}
}
