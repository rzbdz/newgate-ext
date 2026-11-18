package confighook

import (
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate/lib/i18n"
)

// This file is **the one field: which chain a client starts from**.
//
// # Why it is mechanism
//
// The chain head (profile) was global only; the per-agent table (`state.json`'s `active`,
// see domain.State.Active) has existed since M1: `claude` on the pricey one, `opencode`
// on the cheap, each switching its own. But that path **only existed in the CLI**
// (`newgate --set-profile <name> --agent <x>`); the UI showed none of it: you could
// change every slot's tier, but not where its chain starts - the premise for the rest.
//
// Every client needs this field (claude / codex / opencode), so it lives in the kernel:
// stay in one module and the other two just copy it; and two rules in it are **wrong
// without going red**: "empty = follow the global default" (not a hardcoded profile
// name), plus validating before the write that it exists (a typo breaks every request).

// AgentProfile is one client's chain head.
type AgentProfile struct {
	// AgentID is this client's id (`claude` / `codex` / ...).
	AgentID string
}

// Active is the profile this client is **actually on** (the resolved one).
//
// **It always has a value**: never set apart means the global default. It answers which
// chain this client's requests land on, so the human-facing note (Note) uses it.
//
// Do **not** use it for the **value** the UI shows - use Stored; that is the whole point.
func (p AgentProfile) Active() string {
	return store.LoadState().ActiveFor(p.AgentID)
}

// Stored is the choice this client **has saved**; empty = never set, follow the default.
//
// Why the UI wants it, not Active: for "the user picked ds" and "the user picked nothing
// while the global default happens to be ds", the two **return the same string** (`ds`)
// yet differ completely in config (one has a record in state.json, one does not; the
// difference is whether it follows later default changes).
//
// Learned the hard way (2026-09-21): v1 exposed Active, so the field always showed `ds` -
// the "follow default" label never appeared; the user's words were "I clicked apply and
// it still shows no default".
func (p AgentProfile) Stored() string {
	return store.LoadState().Active[p.AgentID]
}

// IsOverride says whether this client was **set apart** (rather than following the default).
//
// The UI uses it to say "follow global default": otherwise the user sees a value in the
// dropdown, takes it as self-set, and later finds it not moving on a default change.
//
// # The criterion is "is there a record", not "does it differ from the default"
//
// Same rule as on the Write side (see the long comment there). When the user explicitly
// picked ds and the global default **also happens to be ds**, the record still matters:
// later default changes will not move it, whereas a client with no record follows.
//
// The wrong criterion's symptom was observed (2026-09-29) on the web first screen:
// clicking "use ds" while auto was also ds did write a record, yet the UI reported
// "follow auto" and the highlight jumped to that card. Write got this right on
// 2026-09-21 for the same reason; this one was missed.
func (p AgentProfile) IsOverride() bool {
	st := store.LoadState()
	v, ok := st.Active[p.AgentID]
	return ok && v != ""
}

// Options is the list of selectable profile names.
//
// When unreadable it returns an empty list (no mappings dir, unreadable dir): the UI
// gets an empty dropdown, not an erroring card - an unreadable directory must not make
// the card vanish (as Concept.Broken in lib/view: show bad news, do not hide things).
func (p AgentProfile) Options() []string {
	names, err := store.ListProfiles()
	if err != nil {
		return nil
	}
	return names
}

// Write sets this client's chain head. **Empty string = back to global default** (key gone).
//
// Same rule as the slot table: a record "same as the default" would make later default
// changes miss some users - users who never configured anything at all.
func (p AgentProfile) Write(profile string) error {
	// Empty string = back to "follow the global default" (that key disappears).
	if profile == "" {
		return store.ClearActiveProfile(p.AgentID)
	}
	// Any other value is **recorded**, even if it happens to equal the global default.
	//
	// This used to read "same name as the global default = never set" (the slot-table
	// rule). That was wrong: this field's value is **the stored one** (see Stored), so
	// after the user explicitly picks `ds` the field shows `ds (default)` again - what
	// they meant to pin looks unpinned, with no UI left to say "this is it".
	//
	// The slot table differs: its value is "which tier runs now", with no second reading.
	return store.SetActiveProfile(p.AgentID, profile)
}

// Note is one sentence for the UI: follows the global default, or was set apart.
func (p AgentProfile) Note() string {
	if p.IsOverride() {
		return i18n.T("set for this client only (other clients follow the global default)", nil)
	}
	return i18n.T("following the global default ({profile})", i18n.A{"profile": p.Active()})
}

// ProfileItemID is that field's key in the UI payload (shape: ProfileItem in view.go).
//
// It must be **impossible** to collide with a slot name: the UI submits all fields at
// once (see kinds/Toggles.svelte) and the slot table looks up by name - a collision
// writes a tier as the profile. Slot names are model keys, so a prefix is enough.
const ProfileItemID = "profile"
