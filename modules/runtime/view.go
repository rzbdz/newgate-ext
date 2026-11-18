package runtime

// This file is takeover state's face for the web UI: **which clients go through newgate**.
//
// Why runtime reports it: takeover is this module's output - which file `on/off`
// rewrites, what goes into PATH, what intent state.json holds, are all this module's
// vocabulary. If the UI read those files it would have to know the shim/config split
// (see the takeover package), which is what "whoever writes it reports it" avoids.
//
// # The four-state criterion has one copy
//
// "Taken over or not" is not a boolean but **four** states - intent and reality each
// true or false, and both kinds of mismatch map to a real fault scene:
//
//	wanted, present     = takeover in effect
//	wanted, absent      = declared but not in effect - the tool connects direct, **silently**
//	unwanted, present   = turned off but not released - the user thinks direct, still proxied
//	unwanted, absent    = direct (normal, the user never wanted it taken over)
//
// Both mismatches are of the "looks perfectly normal" kind, so the criterion lives in
// place (phaseOf), feeding the CLI summary and this table - two UIs differing on one
// disk state is worse than one not showing it.

import (
	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/runtime/takeover"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// takeoverPhase is which of four states an agent is in right now.
//
// Four, not two: "taken over or not" sounds binary, but intent and reality each have
// truth values, and **both mismatches map to a real fault scene** (the CLI summary's
// note always called them "two symmetric faults", yet only one was lit - 2026-09-20).
type takeoverPhase int

const (
	// phaseDirect is not taken over, not meant to be. Normal, not an error.
	phaseDirect takeoverPhase = iota
	// phaseActive is takeover in effect: the reality on disk matches the user's intent.
	phaseActive
	// phasePending is wanted but not in effect on disk.
	//
	// This is **the dangerous one**: the tool connects direct, silently (requests bypass
	// newgate), while everything looks normal. Usually a missed `newgate on` after start.
	phasePending
	// phaseStale is explicitly turned off but still on disk.
	//
	// Cause: a **failed release** (the permission trap: files written under a umask another
	// user cannot write, see CLAUDE.md §3.1 - `Off` records intent first, then acts).
	//
	// Less dangerous than the last (traffic still goes through the gateway, the safe side),
	// but equally **invisible**: the user thinks it is direct, and all looks fine. So warn, not bad.
	phaseStale
	// phaseAbsent: **this tool is not on this machine**.
	//
	// It outranks the other three: with no tool, "is it taken over" does not apply - while
	// the traces on disk (the rewritten config, our shim) **remain**, so without a separate
	// judgment a long-uninstalled tool shows ✓ forever. Measured 2026-09-21: `which
	// opencode` was empty while status said "opencode ✓" - opencode uses config, and that
	// cell judged whether our rewritten ~/.config/opencode/*.json exists.
	//
	// The criterion is `which`'s (see confighook.Agent.Installed), **excluding our own
	// shim** - not excluded, it is always true, degrading into the very bug being fixed.
	phaseAbsent
)

// phaseOf is the **only** criterion for the four states (CLI status and web table).
//
// `Wanted` defaults to true (no stance counts as wanting it, see
// domain.State.TakeoverWanted), so "no stance" never lands in phaseStale - it only
// comes from an **explicit** off. Without this, upgraded old machines would misreport.
func phaseOf(s takeover.Status) takeoverPhase {
	switch {
	case !s.Installed:
		return phaseAbsent
	case s.Active && !s.Wanted:
		return phaseStale
	case s.Active:
		return phaseActive
	case s.Wanted:
		return phasePending
	default:
		return phaseDirect
	}
}

// takeoverConcepts is takeover as of this moment.
//
// Reports **every** agent (not just anomalies): the table answers "who goes through
// newgate", and "claude does, opencode does not" are two halves of one answer -
// anomalies only, and the user cannot tell whether the other tool was taken over.
func takeoverConcepts() ([]view.Concept, error) {
	return []view.Concept{{
		ID:    "runtime.takeover",
		Kind:  view.KindTable,
		Title: i18n.T("Who goes through newgate", nil),
		Data:  takeoverTable(takeover.List()),
	}}, nil
}

// takeoverTable lays a set of takeover states out as a table.
//
// It is a **pure function** (feed it states), because what must be pinned is
// machine-independent: how the four states translate, and whether the dangerous one has
// color. The environment half (`takeover.List()`) is guarded by the takeover package's tests.
func takeoverTable(rows []takeover.Status) view.Table {
	table := view.Table{
		Columns: []view.Column{
			{ID: "agent", Label: i18n.T("agent", nil)},
			{ID: "state", Label: i18n.T("State", nil)},
			// The mechanism name (shim / config) is a machine tag: also the value in
			// `newgate doctor` and the takeover package, so translating it invents vocabulary.
			{ID: "mechanism", Label: i18n.T("Mechanism", nil)},
			{ID: "detail", Label: i18n.T("How", nil)},
		},
		Rows: []view.Row{},
	}
	for _, s := range rows {
		text, tone := stateCell(s)
		table.Rows = append(table.Rows, view.Row{Cells: map[string]view.Cell{
			"agent":     {Text: s.Agent},
			"state":     {Text: text, Tone: tone},
			"mechanism": dashIfEmpty(string(s.Mechanism)),
			"detail":    dashIfEmpty(s.Detail),
		}})
	}
	return table
}

// stateCell turns the four states into plain words plus a semantic color.
func stateCell(s takeover.Status) (string, string) {
	switch phaseOf(s) {
	case phaseActive:
		return i18n.T("taken over", nil), view.ToneOK
	case phasePending:
		// The dangerous half is **not in effect**, so the wording must say that, not just
		// "wanted" - the latter reads as if it already worked.
		return i18n.T("declared but not in effect", nil), view.ToneBad
	case phaseStale:
		// Also say **not in effect**, and **which half**: the user turned it off, so "still
		// connected" is the surprise. Tone is warn, not bad - traffic still goes through the
		// gateway, the safe side (see phaseStale).
		return i18n.T("turned off but still in effect", nil), view.ToneWarn
	case phaseAbsent:
		// Not drawn as an error: not installed is no fault, just absent here. But it also
		// **must not** read "direct" ("it is here, just not taken over"). So: its own words,
		// no color - Tone only has ok/warn/bad, and any of them says "something happened".
		return i18n.T("not installed", nil), ""
	default:
		return i18n.T("direct", nil), ""
	}
}

// dashIfEmpty gives an empty value an explicit placeholder.
//
// An empty cell and "this cell is empty" look alike on screen but mean different things:
// the former reads like "something broke here" or "it did not load". A dash says "none".
func dashIfEmpty(s string) view.Cell {
	if s == "" {
		return view.Cell{Text: "-"}
	}
	return view.Cell{Text: s}
}
