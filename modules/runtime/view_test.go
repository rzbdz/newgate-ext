package runtime

import (
	"testing"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/runtime/takeover"
)

// TestTakeoverPhaseIsFourStates pins the four-state criterion.
//
// It is **not** a boolean, and that is why this table exists: intent and reality each
// have truth values, and both **mismatches** map to a real fault scene:
//
//	wanted but not installed = dangerous (the tool connects direct, bypassing newgate)
//	turned off but still on  = surprise (the user thinks direct, it still goes via the gateway)
//
// Both are "looks normal anywhere alone", so they can only be pinned here. The CLI
// summary and the web table both start from phaseOf; a wrong criterion breaks both.
func TestTakeoverPhaseIsFourStates(t *testing.T) {
	// The cases below all describe the four states with **the tool on this machine**, so
	// each carries Installed: true. Not installed is a separate state, at the function's end.
	cases := []struct {
		name string
		s    takeover.Status
		want takeoverPhase
	}{
		{"installed on disk = in effect", takeover.Status{Installed: true, Wanted: true, Active: true}, phaseActive},
		{"requested but not installed = the dangerous kind", takeover.Status{Installed: true, Wanted: true, Active: false}, phasePending},
		{"not requested = direct (normal)", takeover.Status{Installed: true, Wanted: false, Active: false}, phaseDirect},

		// The user explicitly turned it off, but disk was not released (failed release: a
		// common permission trap with multiple users, see CLAUDE.md §3.1). The CLI summary's
		// note always called this one of the "two symmetric faults", yet the code never lit
		// it - added 2026-09-20 (before, it counted as "in effect", so all looked normal).
		//
		// It is **not** tripped by "no stance": Wanted defaults to true (see
		// domain.State.TakeoverWanted), so machines upgraded from old versions get
		// wanted=true and land in phaseActive.
		{"explicitly turned off but still installed on disk = release did not take effect", takeover.Status{Installed: true, Wanted: false, Active: true}, phaseStale},

		// The fifth state: **no such tool on this machine**. It overrides the four above -
		// with no tool, "is it taken over" does not apply.
		//
		// Measured 2026-09-21: `which opencode` was empty, and status said "opencode ✓".
		// opencode uses the config mechanism, and that cell judged whether our rewritten
		// ~/.config/opencode/*.json exists - **what we did**, not **the user has the tool**.
		// All four cases must be tried: whatever intent and reality, absent means absent.
		{"not installed (both wish and reality)", takeover.Status{Installed: false, Wanted: true, Active: true}, phaseAbsent},
		{"not installed (wish only)", takeover.Status{Installed: false, Wanted: true, Active: false}, phaseAbsent},
		{"not installed (reality only)", takeover.Status{Installed: false, Wanted: false, Active: true}, phaseAbsent},
		{"not installed (neither)", takeover.Status{Installed: false, Wanted: false, Active: false}, phaseAbsent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := phaseOf(c.s); got != c.want {
				t.Errorf("phaseOf(%+v) = %v, want %v", c.s, got, c.want)
			}
		})
	}
}

// TestDangerousPhaseIsRed is the visible half of the above: the three states must **look
// different** on screen, and the middle one must be red.
//
// A gray or colorless "declared but not in effect" is invisible on screen - and it is
// exactly the "you think claude goes via newgate, it connects direct" scene.
func TestDangerousPhaseIsRed(t *testing.T) {
	pending := takeover.Status{Agent: "claude", Installed: true, Wanted: true, Active: false}
	_, tone := stateCell(pending)
	if tone != view.ToneBad {
		t.Fatalf("'declared but not in effect' should be %q (the only dangerous fault), got %q", view.ToneBad, tone)
	}
	if _, tone := stateCell(takeover.Status{Agent: "claude", Installed: true, Wanted: true, Active: true}); tone != view.ToneOK {
		t.Errorf("in effect should be %q, got %q", view.ToneOK, tone)
	}
	// The other half (turned off, not released) is **warn, not bad**: traffic still goes
	// through the gateway; bad is for "silent direct". Both red, and red points nowhere.
	stale := takeover.Status{Agent: "claude", Installed: true, Wanted: false, Active: true}
	if text, tone := stateCell(stale); tone != view.ToneWarn {
		t.Errorf("'turned off but still taken over' should be %q, got %q (text %q)", view.ToneWarn, tone, text)
	}
	// Direct is normal: **no color**. Coloring it makes an all-direct table look broken.
	if text, tone := stateCell(takeover.Status{Agent: "opencode", Installed: true}); tone != "" {
		t.Errorf("direct must not be colored (that is the normal state), got tone=%q text=%q", tone, text)
	}
}

// TestTakeoverTableShape is this table's shape ratchet (same as breaker/plugin-manager's):
// cells are indexed by column ID, and a wrong key name raises nothing - the front end blanks.
func TestTakeoverTableShape(t *testing.T) {
	table := takeoverTable([]takeover.Status{
		{Agent: "claude", Mechanism: takeover.MechShim, Wanted: true, Active: true, Detail: "/x/bin/claude → newgate"},
		{Agent: "opencode", Mechanism: takeover.MechConfig},
	})

	if len(table.Columns) == 0 {
		t.Fatal("no columns - the header is gone, the front end has nothing to draw")
	}
	columns := map[string]bool{}
	for _, col := range table.Columns {
		if col.ID == "" || col.Label == "" {
			t.Errorf("a column is missing ID or Label: %+v (ID is the cell index key, Label is what people read)", col)
		}
		columns[col.ID] = true
	}
	if len(table.Rows) != 2 {
		t.Fatalf("two agents should be two rows, got %d", len(table.Rows))
	}
	for _, row := range table.Rows {
		for id, cell := range row.Cells {
			if !columns[id] {
				t.Errorf("cell %q has no matching column - the front end cannot fetch it, so this cell is blank", id)
			}
			switch cell.Tone {
			case "", view.ToneOK, view.ToneWarn, view.ToneBad:
			default:
				t.Errorf("cell %q has tone=%q which is not a known semantic color (the front end treats it as unset)", id, cell.Tone)
			}
			if cell.Text == "" {
				t.Errorf("cell %q is empty - write an explicit placeholder (see dashIfEmpty)", id)
			}
		}
	}
}

// TestTakeoverTableKeepsTheHeaderWhenEmpty: with no agent taken over, the header stays.
//
// An empty table is not an error - "nobody goes through newgate" and "broken" must differ.
func TestTakeoverTableKeepsTheHeaderWhenEmpty(t *testing.T) {
	table := takeoverTable(nil)
	if len(table.Rows) != 0 {
		t.Fatalf("an empty table should have no rows, got %d", len(table.Rows))
	}
	if len(table.Columns) == 0 {
		t.Fatal("an empty table should still have a header, or the front end cannot even show what this table is about")
	}
}
