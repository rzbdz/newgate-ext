package view

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// The ledger's behavior is unrelated to rendering, so it can be tested in the leaf -- the UI's
// tests would drag in HTTP and embedded assets, while what is pinned here are a few very concrete
// things: **registration produces nothing** (every CLI invocation reaches registration, and nobody
// opens the UI), duplicates must error, a release removes only its own, and it is concurrency-safe.

// TestRegisterDoesNotProduce is the most important one in this thing:
// registering a produce function **must not** compute the data along the way.
//
// What it guards is not performance, it is the pit of "every `newgate …` command rereads
// all config for nothing" -- the first version did exactly that (Start read all the data
// straight into concepts), and it was deleted. This test going red means that pit is back.
func TestRegisterDoesNotProduce(t *testing.T) {
	r := NewRegistry()
	var calls atomic.Int64
	if _, err := r.Register("config", tsection("config"), func() ([]Concept, error) {
		calls.Add(1)
		return []Concept{{ID: "a", Kind: KindCode}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("produced %d times at registration -- every CLI invocation reaches registration", n)
	}
	if _, err := r.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("must produce once at snapshot time, actually %d times", n)
	}
}

// TestSnapshotAsksEveryTime: refreshing in the UI has to be real refreshing
// -- the tier file the CLI just created must show up in the next snapshot.
// Caching an old one would freeze the daemon's UI at the moment it started.
func TestSnapshotAsksEveryTime(t *testing.T) {
	r := NewRegistry()
	n := 0
	if _, err := r.Register("config", tsection("config"), func() ([]Concept, error) {
		n++
		return []Concept{{ID: "profile." + string(rune('0'+n)), Kind: KindMapping}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	first, _ := r.Snapshot()
	second, _ := r.Snapshot()
	if first[0].ID == second[0].ID {
		t.Fatalf("two snapshots got the same concept (%s) -- the producer ran only once", first[0].ID)
	}
}

// TestSnapshotCanAskJustSomeContributors: when the UI refreshes counters
// and logs (cheap), it should not wake the config one along the way -- that
// one has to reread and reparse every profile and every source file. Making
// "refresh the counters" pay that cost spends money where nobody is looking.
func TestSnapshotCanAskJustSomeContributors(t *testing.T) {
	r := NewRegistry()
	var cheap, expensive atomic.Int64
	mustCount(t, r, "gateway", &cheap)
	mustCount(t, r, "config", &expensive)

	if _, err := r.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if cheap.Load() != 1 || expensive.Load() != 1 {
		t.Fatalf("without arguments it should ask everyone: gateway=%d config=%d", cheap.Load(), expensive.Load())
	}

	if _, err := r.Snapshot("gateway"); err != nil {
		t.Fatal(err)
	}
	if cheap.Load() != 2 {
		t.Errorf("the named one should be asked again: %d", cheap.Load())
	}
	if expensive.Load() != 1 {
		t.Errorf("the unnamed one must not be woken up: %d times", expensive.Load())
	}

	// An unknown name (that one is not installed, or was just turned off) is not
	// an error: refreshing is background behavior, and failing the whole refresh
	// for an absent contributor would leave the UI stuck on the previous frame.
	got, err := r.Snapshot("nobody")
	if err != nil {
		t.Fatalf("an unknown name must not make the refresh fail: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("asking a contributor that does not exist should yield empty: %+v", got)
	}
}

func mustCount(t *testing.T, r *Registry, name string, n *atomic.Int64) {
	t.Helper()
	if _, err := r.Register(name, tsection(name), func() ([]Concept, error) {
		n.Add(1)
		return []Concept{{ID: name + ".x", Kind: KindSeries}}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRejectsDuplicateID(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "one", Concept{ID: "a", Kind: KindCode})
	mustRegister(t, r, "two", Concept{ID: "a", Kind: KindCode})
	_, err := r.Snapshot()
	if err == nil {
		t.Fatal("the same ID reported by two sources must error -- otherwise the UI shows only one of them," +
			"and the other one's edit is never reachable")
	}
	for _, want := range []string{"one", "two"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name both sources (who collided with whom), missing %q: %v", want, err)
		}
	}
}

func TestSnapshotRequiresIdentityAndKind(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "x", Concept{Kind: KindCode})
	if _, err := r.Snapshot(); err == nil {
		t.Error("a concept without an ID must be rejected (the ID is the only thing the front end can locate it by)")
	}
	r2 := NewRegistry()
	mustRegister(t, r2, "x", Concept{ID: "a"})
	if _, err := r2.Snapshot(); err == nil {
		t.Error("a concept without a Kind must be rejected (the UI does not know how to render it)")
	}
}

// TestLedgerStampsTheSource: the source is filled by the ledger, not reported by the
// contributor. Reporting it wrong means taking the blame for someone else (the error
// text names someone else), while the ledger already knows who registered this entry.
func TestLedgerStampsTheSource(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "config", Concept{ID: "a", Kind: KindCode, Source: "lying module"})
	got, _ := r.Snapshot()
	if got[0].Source != "config" {
		t.Errorf("Source should be filled in from the ledger, actually %q", got[0].Source)
	}
}

func TestRegisterRejectsEmptySourceAndNilContributor(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Register("", tsection("x"), func() ([]Concept, error) { return nil, nil }); err == nil {
		t.Error("a contributor without a source name must be rejected (both errors and grouping must name it)")
	}
	if _, err := r.Register("x", tsection("x"), nil); err == nil {
		t.Error("a nil producer must be rejected (the UI would silently show nothing)")
	}
}

// TestReleaseOnlyRemovesItsOwn: a release is identified by **registration
// sequence number**, not by name. Registering the same module name twice is
// allowed, and the later one should not be removed by the former's release.
func TestReleaseOnlyRemovesItsOwn(t *testing.T) {
	r := NewRegistry()
	first, err := r.Register("config", tsection("config"), func() ([]Concept, error) {
		return []Concept{{ID: "a", Kind: KindCode}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first(); err != nil {
		t.Fatal(err)
	}
	if all, _ := r.Snapshot(); len(all) != 0 {
		t.Fatalf("must not remain after revoke: %+v", all)
	}
	if err := first(); err != nil {
		t.Fatalf("revoke must be idempotent: %v", err)
	}
	if _, err := r.Register("config", tsection("config"), func() ([]Concept, error) {
		return []Concept{{ID: "a", Kind: KindCode}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = first() // 再撤一次（幂等，且不许误删后来者那条）
	if all, _ := r.Snapshot(); len(all) != 1 {
		t.Fatalf("the latecomer's concept was deleted by mistake: %+v", all)
	}
}

// TestSnapshotIsSortedAndStable: both diagnostics and frontend grouping rely on it. Two runs of
// the same assembly must yield the same order -- otherwise the user will think something changed.
func TestSnapshotIsSortedAndStable(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "gateway", Concept{ID: "z", Kind: KindSeries})
	mustRegister(t, r, "config", Concept{ID: "b", Kind: KindCode}, Concept{ID: "a", Kind: KindCode})
	got, err := r.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"config/a", "config/b", "gateway/z"}
	for i, c := range got {
		if c.Source+"/"+c.ID != want[i] {
			t.Fatalf("Snapshot()[%d] = %s/%s, want %s", i, c.Source, c.ID, want[i])
		}
	}
}

// TestApplyCarriesTheEditThrough: a concept reports not only data, but also
// **how to change it**. This pins "the knowledge of writing belongs to whoever
// owns that data" -- the BFF only passes the frontend's JSON through verbatim.
func TestApplyCarriesTheEditThrough(t *testing.T) {
	r := NewRegistry()
	var got json.RawMessage
	var gotBase string
	mustRegister(t, r, "x", Concept{
		ID: "a", Kind: KindCode,
		Apply: func(edit json.RawMessage, base string) (string, error) {
			got, gotBase = edit, base
			return "sha256:new", nil
		},
	})
	c, err := r.Get("a")
	if err != nil {
		t.Fatal(err)
	}
	if c.Apply == nil {
		t.Fatal("Apply is not in the snapshot -- then the UI can never write")
	}
	rev, err := c.Apply(json.RawMessage(`{"text":"hi"}`), "sha256:old")
	if err != nil || rev != "sha256:new" {
		t.Fatalf("Apply's return value should be handed back to the UI verbatim: rev=%q err=%v", rev, err)
	}
	if string(got) != `{"text":"hi"}` || gotBase != "sha256:old" {
		t.Errorf("the edit content and the baseline should be passed through verbatim: edit=%s base=%s", got, gotBase)
	}
}

// TestGetReportsUnknownConcept: the UI uses this to tell 404 (the frontend's
// snapshot is stale, just reload) from 500 (something is really broken).
func TestGetReportsUnknownConcept(t *testing.T) {
	r := NewRegistry()
	_, err := r.Get("nope")
	if !errors.Is(err, ErrUnknownConcept) {
		t.Fatalf("errors.Is(ErrUnknownConcept) must recognize it, got: %v", err)
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("the error must contain that ID (it is the first thing anyone asks when debugging): %v", err)
	}
}

// TestConcurrentRegisterAndSnapshot: a module's Start (register) and the UI's
// rendering (snapshot) run on different goroutines. Only meaningful under -race.
func TestConcurrentRegisterAndSnapshot(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		id := string(rune('a' + i))
		go func() {
			defer wg.Done()
			rel, err := r.Register("s"+id, tsection("s"+id), func() ([]Concept, error) {
				return []Concept{{ID: id, Kind: KindCode}}, nil
			})
			if err != nil {
				t.Errorf("Register(%s): %v", id, err)
				return
			}
			rel()
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_, _ = r.Snapshot()
				_, _ = r.Get(id)
			}
		}()
	}
	wg.Wait()
}

func mustRegister(t *testing.T, r *Registry, source string, concepts ...Concept) {
	t.Helper()
	if _, err := r.Register(source, tsection(source), func() ([]Concept, error) { return concepts, nil }); err != nil {
		t.Fatal(err)
	}
}

// tsection builds a column name. The vast majority of cases only care about the ledger's
// behavior, not what the title looks like, so the title is just the source name --
// **assertions about column names live in the TestSections* tests**, and this one should
// not pin it too (pinning it would make someone change the title just to fix a test).
func tsection(name string) Section {
	return Title(func() string { return name })
}

// TestSectionTitlesAreAskedEverySnapshot is the ratchet for "why the column name is a function".
//
// What that column in the UI is called is reported by the module itself, and **it must
// not be translated when reporting**: registration happens in each module's Start,
// and whether the language layer is installed by then is not guaranteed (business
// modules do not depend on modules/locale; only the distribution's catalog-appending
// module does). A title evaluated at registration would freeze in the source language,
// and nothing would go red for it -- a column in the Chinese UI would be called
// Configuration forever, looking like "not translated yet" rather than like a bug.
//
// So this test watches not "is the title right" but **at which moment evaluation happens**: change
// what that sentence comes from after registration, and the next ask must return the new one.
func TestSectionTitlesAreAskedEverySnapshot(t *testing.T) {
	r := NewRegistry()
	title := "first"
	if _, err := r.Register("x", Title(func() string { return title }),
		func() ([]Concept, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if got := sectionsOf(t, r)["x"]; got != "first" {
		t.Fatalf("the column name should be computed by the registered closure, actually %q", got)
	}
	title = "second"
	if got := sectionsOf(t, r)["x"]; got != "second" {
		t.Fatalf("the column name is frozen at registration time (%q) -- it will not change with the language", got)
	}
}

// TestSectionsListSilentContributors: a source that produces not
// one concept right now must still have a place in the sidebar.
//
// The cost of it disappearing is not "one row fewer": the user will think that module
// is not installed and go looking elsewhere (dig through config, ask doctor). The scenes
// where this really happens today are the config directory being entirely unreadable,
// omo never having been taken over, not one switch point having reported -- and those
// are exactly the moments when seeing "this column is there, just empty" matters most.
func TestSectionsListSilentContributors(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Register("quiet", tsection("Quiet"),
		func() ([]Concept, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register("loud", tsection("Loud"),
		func() ([]Concept, error) { return []Concept{{ID: "a", Kind: KindCode}}, nil }); err != nil {
		t.Fatal(err)
	}
	got := r.Sections()
	if len(got) != 2 {
		t.Fatalf("both sources should be in the column list, actually %d: %+v", len(got), got)
	}
	if got[0].Source != "loud" || got[1].Source != "quiet" {
		t.Errorf("columns must be sorted by source (UI order must be stable across restarts): %+v", got)
	}
	if got[1].Title != "Quiet" {
		t.Errorf("the one that produced nothing should still have a name: %+v", got[1])
	}
}

// TestSectionsSurviveAnEmptyTitle: a column name is a piece of display copy. When it
// cannot be computed, fall back to the source name rather than fail the whole snapshot
// -- that cost is a blank UI, far more serious than one column's title looking bad.
func TestSectionsSurviveAnEmptyTitle(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Register("config", Title(func() string { return "  " }),
		func() ([]Concept, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if got := sectionsOf(t, r)["config"]; got != "config" {
		t.Errorf("an empty column name must fall back to the source name (a machine marker, at least not empty): %q", got)
	}
}

// TestRegisterRejectsNoSectionTitle: a missing column name must go red **at the
// moment of registration**, not wait for the UI to render -- that moment is one
// HTTP request and one render away from here, nobody will trace it back, and the
// symptom is a column in the sidebar with no title (or wearing a module name).
func TestRegisterRejectsNoSectionTitle(t *testing.T) {
	r := NewRegistry()
	rel, err := r.Register("x", Section{}, func() ([]Concept, error) { return nil, nil })
	if err == nil {
		_ = rel()
		t.Fatal("not reporting a column name must be rejected (use view.Title)")
	}
	if !strings.Contains(err.Error(), "x") {
		t.Errorf("the error must name which source: %v", err)
	}
}

// TestNobodyDeclaresALandingByDefault: when nobody declares a landing,
// **behavior is exactly as before this change** -- no Default at all,
// and the UI picks by its old rule (the first column with cards).
//
// This is the safety rope of this change: assemblies with `home` installed move their
// first screen, and those without it (the four specs other than `dist.json`, the
// kernel's own test graphs) must change not one character. Breaking it is a silent
// behavior change -- and nothing would go red for "where the first screen lands".
func TestNobodyDeclaresALandingByDefault(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"breaker", "config", "gateway"} {
		if _, err := r.Register(name, tsection(name), func() ([]Concept, error) { return nil, nil }); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range r.Sections() {
		if s.Default {
			t.Errorf("%s was marked as the landing point, but nobody declared it", s.Source)
		}
	}
}

// TestLandingIsTheDeclaredSection: the column that
// declared the landing carries it, the rest do not.
//
// What it sends to the UI is the `default: true` cell (SectionInfo.Default),
// and the frontend decides from it where the first screen lands -- before
// this, that was the accident of "whose name sorts first alphabetically".
func TestLandingIsTheDeclaredSection(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Register("breaker", tsection("Breaker"), func() ([]Concept, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	// The name is deliberately sorted **last** alphabetically: the landing comes
	// from a declaration, not from jumping the queue in the alphabet -- queue-jumping
	// (naming the source `aaa-home`) is what this change exists to kill.
	if _, err := r.Register("home", tsection("Home").Landing(), func() ([]Concept, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range r.Sections() {
		got[s.Source] = s.Default
	}
	if !got["home"] {
		t.Errorf("the column that declared Landing was not marked as the landing point: %+v", got)
	}
	if got["breaker"] {
		t.Errorf("the column that did not declare it must not be the landing point: %+v", got)
	}
}

// TestSeveralLandingsResolveBySource: when two modules both want to be the landing (a conflict
// in how the distribution is installed, not a mistake by either) take the smallest by Source.
//
// Why a deterministic rule is needed: without it, who gets picked depends on the order the modules
// Start, and that "change how it is installed and the first screen moves" error would make nothing
// go red. Taking the smallest uses the same ruler as Sections' sort, so readers are not confused.
func TestSeveralLandingsResolveBySource(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"zebra", "apple"} {
		if _, err := r.Register(name, tsection(name).Landing(), func() ([]Concept, error) { return nil, nil }); err != nil {
			t.Fatal(err)
		}
	}
	var landed []string
	for _, s := range r.Sections() {
		if s.Default {
			landed = append(landed, s.Source)
		}
	}
	if len(landed) != 1 || landed[0] != "apple" {
		t.Fatalf("with several landing points only the one with the smallest source should remain, actually %v", landed)
	}
}

func sectionsOf(t *testing.T, r *Registry) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, s := range r.Sections() {
		out[s.Source] = s.Title
	}
	return out
}

// Actions on a Row are located by **row**: whichever row's button runs, only that one runs.
//
// This pins that "the row ID really takes part in the lookup" -- without it, "switch
// this tier to X" on a card with ten rows would become "run the button with this name
// in the first row", and the symptom of that error is **changing the wrong row**.
func TestRunRowActionFindsTheRightRow(t *testing.T) {
	r := NewRegistry()
	var ran string
	btn := func(id string) Action {
		return Action{ID: id, Label: func() string { return id },
			Run: func() (string, error) { ran = id; return "", nil }}
	}
	mustRegister(t, r, "x", Concept{
		ID: "t", Kind: KindTable,
		Data: Table{Rows: []Row{
			{ID: "a", Cells: map[string]Cell{}, Actions: []Action{btn("probe")}},
			{ID: "b", Cells: map[string]Cell{}, Actions: []Action{btn("probe")}},
		}},
	})
	// Two rows have a button of the **same name**: exactly the case "locate by row" must tell apart.
	if _, err := r.RunRowAction("t", "b", "probe"); err != nil {
		t.Fatal(err)
	}
	if ran != "probe" {
		t.Fatalf("the button did not run: %q", ran)
	}
	// Both rows run the same closure, so the above cannot tell a from b -- switch
	// to one that can: each row runs its own closure (Run returns the row ID).
	var which string
	rowBtn := func(row string) Action {
		return Action{ID: "go", Label: func() string { return row },
			Run: func() (string, error) { which = row; return "", nil }}
	}
	r2 := NewRegistry()
	mustRegister(t, r2, "x", Concept{
		ID: "t", Kind: KindTable,
		Data: Table{Rows: []Row{
			{ID: "a", Cells: map[string]Cell{}, Actions: []Action{rowBtn("a")}},
			{ID: "b", Cells: map[string]Cell{}, Actions: []Action{rowBtn("b")}},
		}},
	})
	if _, err := r2.RunRowAction("t", "b", "go"); err != nil {
		t.Fatal(err)
	}
	if which != "b" {
		t.Fatalf("ran line %q, wanted line b", which)
	}
}

// Rows on a chain card (KindChains) **can carry actions
// too**, and row IDs are unique across the whole concept.
//
// This is the gap from when chains were added on 2026-09-22: the first version of
// RunRowAction only knew Table, so "where a row goes" was tied to the rendering shape.
// Two tests -- actions on a chain card can run, and **a tier name alone is not enough**
// (ten profiles means ten heavy's, and reporting only the tier changes the wrong one).
func TestRunRowActionWorksOnChainCards(t *testing.T) {
	r := NewRegistry()
	var got string
	head := func(row string, target string) Action {
		return Action{ID: "head:" + target, Label: func() string { return target },
			Run: func() (string, error) { got = row + "→" + target; return "", nil }}
	}
	mustRegister(t, r, "x", Concept{
		ID: "chains", Kind: KindChains,
		Data: Chains{Cards: []ChainCard{
			{Profile: "demo", Default: true, Roles: []ChainRow{
				{ID: "demo/heavy", Tier: "heavy", Actions: []Action{head("demo/heavy", "ark/lite")}},
			}},
			{Profile: "alt", Roles: []ChainRow{
				{ID: "alt/heavy", Tier: "heavy", Actions: []Action{head("alt/heavy", "zhipu/glm-4")}},
			}},
		}},
	})

	if _, err := r.RunRowAction("chains", "alt/heavy", "head:zhipu/glm-4"); err != nil {
		t.Fatal(err)
	}
	if got != "alt/heavy→zhipu/glm-4" {
		t.Fatalf("ran the wrong line: %q (both sides have a row named heavy, only the row ID can tell them apart)", got)
	}

	// When a row **does not** have this action, the error must say clearly which row
	// -- the snapshot in the UI's hands is stale, and this sentence is its only clue.
	_, err := r.RunRowAction("chains", "demo/heavy", "head:nope")
	if err == nil {
		t.Fatal("there is no such button on that row; it should error")
	}
	if !strings.Contains(err.Error(), "demo/heavy") || !strings.Contains(err.Error(), "head:nope") {
		t.Errorf("the error must name the row and the action, got: %v", err)
	}
}

// When rows of a Kind cannot carry actions, the error says **"this
// shape has no row actions"** rather than pretending that row existed.
//
// Why this is worth having: forget to add a branch in rowActions when adding a Kind
// with rows, and the symptom is a sentence "this row does not exist" -- so whoever
// investigates goes looking at the data, when the truth is a missing case in the code.
func TestRunRowActionOnAKindWithoutRows(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "x", Concept{ID: "c", Kind: KindCode, Data: struct{}{}})
	_, err := r.RunRowAction("c", "whatever", "go")
	if err == nil {
		t.Fatal("the code kind has no rows; it should error")
	}
	if !strings.Contains(err.Error(), KindCode) {
		t.Errorf("the error must say which Kind has no actions, got: %v", err)
	}
}

// Buttons on the overview card live on the **cards** (one "use it" per profile), while
// RunConceptAction looks for actions on the concept -- so it has to look into the data by Kind.
//
// Why this deserves two assertions: **a dozen cards all have the same family of actions**
// (one "use for claude" per profile), and the ledger takes the first match by ID. Without
// the profile in the ID, the user wanting to switch the third card actually changes the
// first -- silently changing the wrong thing, with not a hint of anything odd in the UI.
func TestRunConceptActionReachesOverviewCards(t *testing.T) {
	r := NewRegistry()
	var got string
	use := func(id, profile string) Action {
		return Action{ID: id, Label: func() string { return "use " + profile },
			Run: func() (string, error) { got = profile; return "", nil }}
	}
	mustRegister(t, r, "x", Concept{
		ID: "overview", Kind: KindOverview,
		Data: Overview{Cards: []OverviewCard{
			{Profile: "production", Actions: []Action{use("use:claude:production", "production")}},
			{Profile: "cheap", Actions: []Action{use("use:claude:cheap", "cheap")}},
		}},
	})

	if _, err := r.RunConceptAction("overview", "use:claude:cheap"); err != nil {
		t.Fatal(err)
	}
	if got != "cheap" {
		t.Fatalf("ran the wrong card: edited %q (this happens when one of the two machine markers is missing)", got)
	}

	// When the card **does not** have this button, the error must say clearly which action
	// -- the snapshot in the UI's hands is stale, and this sentence is its only clue.
	_, err := r.RunConceptAction("overview", "use:codex:cheap")
	if err == nil {
		t.Fatal("there is no such button; it should error")
	}
	if !strings.Contains(err.Error(), "use:codex:cheap") {
		t.Errorf("the error must name the action, got: %v", err)
	}
}

// When the data shape does not match the Kind it must
// **say so**, not pretend that card has no such button.
func TestRunConceptActionOnAMismatchedOverview(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "x", Concept{ID: "c", Kind: KindOverview, Data: struct{}{}})
	_, err := r.RunConceptAction("c", "any")
	if err == nil {
		t.Fatal("the shape does not match; it should error")
	}
	if !strings.Contains(err.Error(), "Overview") {
		t.Errorf("the error must say the shape is wrong, got: %v", err)
	}
}

// "Is this client installed" must be **two distinguishable values** on the wire.
//
// What it guards against is `omitempty`-style byte saving that looks harmless: both
// ends of `Ready` are meaningful (true = installed, false = not on this machine), and
// dropping `false` makes both "field absent" in JSON, so the UI can draw neither the gray
// of that tab nor the "not installed" mark. It reports no error -- those two just never
// appear. Measured: a production snapshot reported the uninstalled client as `None`.
func TestOverviewAgentSaysWhetherItIsInstalled(t *testing.T) {
	raw, err := json.Marshal(Overview{Agents: []OverviewAgent{
		{ID: "claude", Name: "claude", Profile: "production", Ready: true},
		{ID: "codex", Name: "codex", Profile: "production", Ready: false},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, false} {
		v, ok := back.Agents[i]["ready"]
		if !ok {
			t.Fatalf("the payload for client %d has no ready: %s", i, raw)
		}
		if v != want {
			t.Errorf("the ready for client %d should be %v, actually %v", i, want, v)
		}
	}
}

// "Is this one pinned, or following the global default"
// must also be **two distinguishable values** on the wire.
//
// The two things may resolve to the same profile right now (pinned to ds, and the global
// happens to be ds too), but after the global changes one follows and one does not -- with only
// the profile on the wire, the UI can only describe them the same way, so the user cannot tell
// "will changing the global also change this one", and the "set this one back to automatic"
// action has no premise to test (its condition is exactly "it currently has its own setting").
func TestOverviewAgentSaysWhetherItIsPinned(t *testing.T) {
	raw, err := json.Marshal(Overview{Agents: []OverviewAgent{
		{ID: "claude", Profile: "ds", Own: false},
		{ID: "codex", Profile: "ds", Own: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	// Both point at the **same** profile, and this cell is the only thing telling them apart.
	if back.Agents[0]["profile"] != back.Agents[1]["profile"] {
		t.Fatalf("this case assumes the two profiles are the same: %s", raw)
	}
	for i, want := range []bool{false, true} {
		v, ok := back.Agents[i]["own"]
		if !ok {
			t.Fatalf("the payload for client %d has no own: %s", i, raw)
		}
		if v != want {
			t.Errorf("the own for client %d should be %v, actually %v", i, want, v)
		}
	}
}

// Actions on the "default" card **must be reachable too**.
//
// It is not in Cards (it is not a profile) but on Overview.Auto, so the version that iterated
// Cards missed it -- the symptom being every button on that card answering "no such action":
// the members (`use:global:<profile>`) and "follow the default" (`auto:<agent>`) **only**
// live on it, so missing it cuts off both "change the default" and "set back to automatic".
func TestRunConceptActionReachesTheAutoCard(t *testing.T) {
	r := NewRegistry()
	var got string
	mustRegister(t, r, "x", Concept{
		ID: "overview", Kind: KindOverview,
		Data: Overview{
			Auto: &OverviewCard{Auto: true, Profile: "production", Actions: []Action{
				{ID: "use:global:cheap", Label: func() string { return "cheap" },
					Run: func() (string, error) { got = "global"; return "", nil }},
				{ID: "auto:claude", Label: func() string { return "use for claude" },
					Run: func() (string, error) { got = "auto"; return "", nil }},
			}},
			Cards: []OverviewCard{{Profile: "cheap"}},
		},
	})

	if _, err := r.RunConceptAction("overview", "use:global:cheap"); err != nil {
		t.Fatal(err)
	}
	if got != "global" {
		t.Errorf("the member action of the \"default\" card was not run, actually %q", got)
	}
	if _, err := r.RunConceptAction("overview", "auto:claude"); err != nil {
		t.Fatal(err)
	}
	if got != "auto" {
		t.Errorf("\"follow the default\" was not run, actually %q", got)
	}
}

// When a dropdown's value is the empty string, that cell **must appear on the wire too**.
//
// The machine values of tiers like "follow system" and "follow the global default" are
// the empty string, and for a select the empty string is a **legal value**, not "this
// cell is absent". Dropping it leaves the frontend with undefined and it has to `??
// ""` itself; that fallback happens to be right, but by luck -- a dropdown whose option
// table lacks the empty string would then display the first item while the stored value
// is empty, the screen says something different from the disk, and nothing goes red.
func TestToggleValueIsAlwaysOnTheWire(t *testing.T) {
	raw, err := json.Marshal(Toggles{Items: []ToggleItem{
		{ID: "theme", Kind: ToggleSelect, Label: "Theme", Options: []string{"", "dark"}},
		{ID: "on", Kind: ToggleSwitch, Label: "On", On: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	for i, it := range back.Items {
		if _, ok := it["value"]; !ok {
			t.Errorf("the payload for cell %d has no value: %s", i, raw)
		}
	}
}

// The Tone of a section action **must get through to the wire**.
//
// It has its own conversion path (SectionInfo builds ActionInfo by hand), separate
// from Action.MarshalJSON -- measured 2026-09-29: the `auto fallback` state button
// lit up on card actions but never on section actions, because that path dropped
// Tone. When one piece of information has two serialization paths, "one remembers,
// one forgets" is the easiest error to grow, and its symptom is just a wrong
// color. Tone is evaluated **on every snapshot**, not taken at registration time.
//
// Same reason as Label, but harder: Label is copy, once taken it is fine; Tone is **state**
// -- the action is registered once in Start, while the snapshot is recomputed on every
// request. A value taken on the spot would keep the color frozen at the moment the process
// started, hit for real (2026-09-30): after `newgate fallback off` the button was still green.
func TestActionToneIsReadAtSnapshotTime(t *testing.T) {
	r := NewRegistry()
	on := true
	mustRegister(t, r, "x", Concept{ID: "c", Kind: KindRecords, Data: Records{},
		Actions: []Action{{
			ID:    "fallback",
			Label: func() string { return "auto fallback" },
			Tone: func() string {
				if on {
					return ToneOK
				}
				return ToneBad
			},
			Run: func() (string, error) { return "", nil },
		}},
	})

	toneOf := func() string {
		t.Helper()
		list, err := r.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range list {
			if len(c.Actions) == 1 {
				// Go through the path that actually sends it (what the frontend gets).
				raw, err := json.Marshal(c.Actions[0])
				if err != nil {
					t.Fatal(err)
				}
				var back struct {
					Tone string `json:"tone"`
				}
				if err := json.Unmarshal(raw, &back); err != nil {
					t.Fatal(err)
				}
				return back.Tone
			}
		}
		t.Fatal("cannot find that action")
		return ""
	}

	if got := toneOf(); got != ToneOK {
		t.Fatalf("when on it should be %q, actually %q", ToneOK, got)
	}
	on = false
	if got := toneOf(); got != ToneBad {
		t.Errorf("after turning it off the **next snapshot** should turn red, actually %q -- is Tone taken at registration time?", got)
	}
}

func TestSectionActionToneReachesTheWire(t *testing.T) {
	r := NewRegistry()
	bad := "bad"
	if _, err := r.Register("x",
		Title(func() string { return "x" }).Does(Action{
			ID:    "fallback",
			Label: func() string { return "auto fallback" },
			Tone:  func() string { return bad },
			Run:   func() (string, error) { return "", nil },
		}),
		func() ([]Concept, error) {
			return []Concept{{ID: "c", Kind: KindRecords, Data: Records{}}}, nil
		}); err != nil {
		t.Fatal(err)
	}
	for _, s := range r.Sections() {
		if s.Source != "x" || len(s.Actions) != 1 {
			continue
		}
		if s.Actions[0].Tone != bad {
			t.Errorf("the column action's Tone should be %q, actually %q -- that hand-built ActionInfo path missed it", bad, s.Actions[0].Tone)
		}
		return
	}
	t.Fatal("the ledger has no such column")
}

// A section's Fields (what this column is for) must go **all the way to the wire**.
//
// It decides which category the UI files this column under in the catalog -- lose
// it and the sidebar falls back to "pile things up by the contributor's own group
// name", and that free text never ends up together across two distributions.
func TestSectionFieldsReachTheWire(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "x", Concept{ID: "c", Kind: KindRecords, Data: Records{}})
	if _, err := r.Register("y", Title(func() string { return "y" }).Of(FieldSettings),
		func() ([]Concept, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, s := range r.Sections() {
		if s.Source != "y" {
			continue
		}
		seen = true
		if s.Fields != FieldSettings {
			t.Errorf("Fields should be %q, actually %q", FieldSettings, s.Fields)
		}
	}
	if !seen {
		t.Fatal("the ledger has no such column")
	}
}

// TestRecordsBlankReachesTheWire: the field shape
// for a newly added record must really reach the UI.
//
// What this guards is a failure **visible only with an empty set**: the field
// shape lives on each record, so the frontend rendering a new card can only copy
// an existing one. With none there is nothing to copy -- pressing "add" yields an
// empty card with not one input box, and the user has no way to fill anything in.
//
// A fresh install is exactly that state (a config with
// nothing configured yet), so it is not a corner case.
func TestRecordsBlankReachesTheWire(t *testing.T) {
	blank := []Field{
		{ID: "host", Label: "host", Kind: FieldText},
		{ID: "mode", Label: "mode", Kind: FieldSelect, Options: []string{"a", "b"}},
	}
	raw, err := json.Marshal(Records{CanAdd: true, AddLabel: "add", Blank: blank})
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Blank []struct {
			ID      string   `json:"id"`
			Kind    string   `json:"kind"`
			Options []string `json:"options"`
		} `json:"blank"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("the cell read from the UI cannot be decoded: %v (%s)", err, raw)
	}
	if len(back.Blank) != 2 {
		t.Fatalf("blank should have 2 cells, actually %d (%s)", len(back.Blank), raw)
	}
	if back.Blank[0].ID != "host" || back.Blank[0].Kind != FieldText {
		t.Errorf("the first cell is wrong: %+v", back.Blank[0])
	}
	// Options must go too: only the contributor knows the dropdown's
	// candidates (only two protocols exist, only four tiers).
	if len(back.Blank[1].Options) != 2 {
		t.Errorf("the dropdown choices were lost: %+v", back.Blank[1])
	}
}

// TestCellHrefReachesTheWire: the clickable address in a cell must really reach the UI.
//
// The test is here rather than on the contributor's side: the address is often **computed**
// ("this remote UI is mounted under this prefix on this port"), the contributor hands it over
// along with the rest, and the UI only draws an <a>. Without carrying this cell, that address
// is just **a piece of text that looks like an address** in the UI -- the user selects it, copies
// it, pastes it into the browser, and any step can go wrong, while it all looks perfectly normal.
func TestCellHrefReachesTheWire(t *testing.T) {
	raw, err := json.Marshal(Table{
		Columns: []Column{{ID: "route", Label: "route"}},
		Rows: []Row{{ID: "r", Cells: map[string]Cell{
			"route": {Text: "/ui/remote/ds/", Href: "/ui/remote/ds/"},
			"plain": {Text: "no link here"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Rows []struct {
			Cells map[string]struct {
				Text string `json:"text"`
				Href string `json:"href"`
			} `json:"cells"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("cannot decode: %v (%s)", err, raw)
	}
	if got := back.Rows[0].Cells["route"].Href; got != "/ui/remote/ds/" {
		t.Errorf("Href = %q（%s）", got, raw)
	}
	// A cell with no link should not grow an href out of
	// nowhere -- an empty string is "this is not a link".
	if _, present := back.Rows[0].Cells["plain"]; !present {
		t.Fatal("the plain cell was lost")
	}
	if strings.Contains(string(raw), `"plain":{"text":"no link here","href"`) {
		t.Errorf("the plain cell was given an href: %s", raw)
	}
}
