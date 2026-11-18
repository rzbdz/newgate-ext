package pluginmanager

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
)

// The UI's copy of switch points (view.go) is asserted in two halves: **what is drawn
// right**, and **what is written** - the latter matters most: one press changes runtime behavior.

func register(t *testing.T, m Manager, module string, sws ...Switch) {
	t.Helper()
	if _, err := m.RegisterSelf(module, sws); err != nil {
		t.Fatal(err)
	}
}

func sw(path, danger string, def bool) Switch {
	s := Switch{Path: path, Title: "title of " + path, Why: "why of " + path, Default: def}
	s.Danger = Danger(danger)
	if s.Danger == DangerFootgun {
		s.TTL = 5 * time.Minute
	}
	return s
}

// TestViewSeparatesFootgunsFromTheRest: a footgun must not appear in the **writable** card.
//
// Its write semantics require a time limit, and this version has no UI to pick one;
// mixed in, it adds a switch that fails whenever pressed - worse than none. So: its own card.
func TestViewSeparatesFootgunsFromTheRest(t *testing.T) {
	m := start(t)
	register(t, m, "demo",
		sw("demo.kill", "safe", true),
		sw("demo.mode", "quirk", false),
		sw("demo.gun", "footgun", true),
	)

	concepts, err := viewConcepts(m)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]view.Concept{}
	for _, c := range concepts {
		byID[c.ID] = c
	}
	writable, ok := byID["plugin-manager.switches"]
	if !ok {
		t.Fatalf("the writable card is gone: %+v", concepts)
	}
	if writable.Apply == nil {
		t.Error("the switch-point card should be writable")
	}
	gun, ok := byID["plugin-manager.footguns"]
	if !ok {
		t.Fatal("the footgun should have its own card")
	}
	if gun.Apply != nil {
		t.Error("the footgun card must not be writable (a write here either fails or bypasses the time limit)")
	}
	for _, item := range gun.Data.(switchesData).Items {
		if item.ID != "demo.gun" {
			t.Errorf("something else got into the footgun card: %s", item.ID)
		}
		// Read-only must say **where to change it**, or the user only learns it will not budge here.
		if !strings.Contains(item.Why, "newgate plugin") {
			t.Errorf("the read-only footgun should say which command changes it: %q", item.Why)
		}
	}
	if n := len(writable.Data.(switchesData).Items); n != 2 {
		t.Errorf("the writable one should have two switch points, got %d", n)
	}
}

// TestViewReportsTheEffectiveState: the drawn `value` is the **currently effective** state,
// decided jointly by the factory state and the user's setting (kill switch -> Off table,
// mode -> On table). Drawn wrong, the user sees a switch opposite to `newgate plugin`.
func TestViewReportsTheEffectiveState(t *testing.T) {
	m := start(t)
	register(t, m, "demo", sw("demo.kill", "safe", true), sw("demo.mode", "quirk", false))

	values := func() map[string]bool {
		t.Helper()
		concepts, err := viewConcepts(m)
		if err != nil {
			t.Fatal(err)
		}
		writable := switchConcept(t, concepts)
		out := map[string]bool{}
		for _, item := range writable.Data.(switchesData).Items {
			out[item.ID] = item.Value
		}
		return out
	}

	// Factory state: kill switch on, mode off.
	got := values()
	if !got["demo.kill"] || got["demo.mode"] {
		t.Fatalf("the factory state is drawn wrong: %v", got)
	}

	// The user turns the kill switch off: one entry in state, and the UI must follow.
	st := store.LoadState()
	if err := SetSwitch(st, sw("demo.kill", "safe", true), false, 0, false); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
	if got := values(); got["demo.kill"] {
		t.Errorf("after turning it off the UI still says it is on: %v", got)
	}
}

// TestViewApplyWritesWithABaseline: saving goes the "compare baseline -> atomic write" way.
// A stale baseline must report a **conflict** (not overwrite the other change) - exactly
// the scene when web and CLI edit the same state.json at once.
func TestViewApplyWritesWithABaseline(t *testing.T) {
	m := start(t)
	register(t, m, "demo", sw("demo.kill", "safe", true))
	concepts, err := viewConcepts(m)
	if err != nil {
		t.Fatal(err)
	}
	c := switchConcept(t, concepts)
	base := c.Data.(switchesData).Base

	rev, err := c.Apply(json.RawMessage(`{"demo.kill":false}`), base)
	if err != nil {
		t.Fatalf("save failed: %v", err)
	}
	if rev == base {
		t.Error("after writing it should return a fresh baseline (the front end can keep editing without a page reload)")
	}
	if !Off(store.LoadState(), "demo.kill") {
		t.Error("state does not contain the 'off' setting")
	}

	// Write again with the **old** baseline: "the CLI edited the same file while you watched".
	// It must report a conflict carrying both originals, and never overwrite.
	_, err = c.Apply(json.RawMessage(`{"demo.kill":true}`), base)
	var conflict *view.Conflict
	if !errors.As(err, &conflict) {
		t.Fatalf("a stale baseline should report a conflict, got %v", err)
	}
	if conflict.Yours == "" || conflict.Theirs == "" {
		t.Error("a conflict must carry both sides as is (so the user knows what they lost)")
	}
	if conflict.Path == "" || !strings.HasSuffix(conflict.Path, "state.json") {
		t.Errorf("a conflict should name the file: %q", conflict.Path)
	}
}

// TestViewApplyRejectsAnUnknownSwitch: the UI's switch point is gone (new artifact,
// module removed). **Error** instead of skipping: a silent skip and the user thinks it took.
func TestViewApplyRejectsAnUnknownSwitch(t *testing.T) {
	m := start(t)
	register(t, m, "demo", sw("demo.kill", "safe", true))
	concepts, _ := viewConcepts(m)
	_, err := switchConcept(t, concepts).Apply(
		json.RawMessage(`{"demo.gone":true}`), store.Revision(paths.StateFile()))
	if err == nil {
		t.Fatal("an unknown switch point should error")
	}
	if !strings.Contains(err.Error(), "demo.gone") {
		t.Errorf("the error should name which one: %v", err)
	}
}

// TestViewReportsModulesWithNoSwitchesAtAll is the **skeleton distribution** case: with
// no switch point reported, the two switch cards are deliberately omitted (an empty card
// reads as "not loaded"), but the module list must be reported - it answers another
// question (which modules make up this build), and that build needs it most after an upgrade
//
// (docs/08). Ratchet on that early return: the list came later, easy to leave after it.
func TestViewReportsModulesWithNoSwitchesAtAll(t *testing.T) {
	m := start(t)

	concepts, err := viewConcepts(m)
	if err != nil {
		t.Fatal(err)
	}
	var table view.Concept
	for _, c := range concepts {
		if c.ID == "plugin-manager.modules" {
			table = c
		}
		if c.ID == "plugin-manager.switches" || c.ID == "plugin-manager.footguns" {
			t.Errorf("with not a single switch point it must not report %s (an empty card looks like 'failed to load')", c.ID)
		}
	}
	if table.ID == "" {
		t.Fatal("with no switch points the module list is not reported either - a skeleton distro would show nothing at all")
	}
	if table.Kind != view.KindTable {
		t.Errorf("the module list should be a table, got %q", table.Kind)
	}
	if table.Apply != nil {
		t.Error("is the module list writable? which modules are installed is decided by the artifact, it cannot be changed")
	}

	data := table.Data.(view.Table)
	if len(data.Columns) == 0 {
		t.Fatal("the module list has no columns")
	}
	columns := map[string]bool{}
	for _, col := range data.Columns {
		columns[col.ID] = true
	}
	if len(data.Rows) != len(m.Modules()) {
		t.Errorf("the list should have %d rows (one per module), got %d", len(m.Modules()), len(data.Rows))
	}
	for _, row := range data.Rows {
		if row.Cells["module"].Text == "" || row.Cells["type"].Text == "" {
			t.Errorf("every row should have a module name and a category: %+v", row.Cells)
		}
		// A module with no switch points shows a dash, not 0: "none at all" is not "0", and
		// the latter is not a possible state (registering means at least one).
		if row.Cells["switches"].Text == "" {
			t.Errorf("the switch-point cell is empty: %+v", row.Cells)
		}
		for id := range row.Cells {
			if !columns[id] {
				t.Errorf("cell %q has no matching column - the front end cannot fetch it, so this cell is blank", id)
			}
		}
	}
}

// switchConcept fetches the writable switch card **by ID**, **not by index**.
//
// The index used to work because that card happened to be produced first. The module list
// broke that: the index-based version grabbed a table and died on an inscrutable type
// assertion. The ID is the concept's stable identity (see lib/view's Concept.ID) - the
// production order is a ledger implementation detail tests should not depend on.
func switchConcept(t *testing.T, concepts []view.Concept) view.Concept {
	t.Helper()
	for _, c := range concepts {
		if c.ID == "plugin-manager.switches" {
			return c
		}
	}
	t.Fatal("the writable switch card is gone")
	return view.Concept{}
}

// TestViewDeclaresWhichCardsChangeOnTheirOwn is the ledger ratchet for these cards' `Live`.
//
// The two switch cards declare it: their values **are changed from the terminal**
// (`newgate plugin ... on/off`) and those with a limit **revert by themselves** - TTL is
// judged on read (Remaining, query.go; no one writes state.json), so a frozen page is stale.
// The module list does not: that is a fact of **this build**, fixed at compile time.
//
// Why an assertion: a lost `Live` raises nothing, those cards just stop refreshing - silently.
func TestViewDeclaresWhichCardsChangeOnTheirOwn(t *testing.T) {
	m := start(t)
	register(t, m, "demo",
		sw("demo.mode", "safe", false),
		sw("demo.kill", "footgun", true),
	)
	concepts, err := viewConcepts(m)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"plugin-manager.modules":  false, // which modules make up this build: never changes by itself
		"plugin-manager.switches": true,
		"plugin-manager.footguns": true,
	}
	seen := map[string]bool{}
	for _, c := range concepts {
		w, expected := want[c.ID]
		if !expected {
			t.Fatalf("an unexpected card showed up: %s", c.ID)
		}
		seen[c.ID] = true
		if c.Live != w {
			t.Errorf("the Live of %s should be %v, got %v", c.ID, w, c.Live)
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("missing %s - it should have been produced", id)
		}
	}
}
