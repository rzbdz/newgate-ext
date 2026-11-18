package confighook

import (
	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate/lib/i18n"
)

// This file is what confighook hands to **each client module's view.go**: it turns
// "which chain this client starts from" into one field on the UI.
//
// # Why it has its own file instead of living in profile.go
//
// profile.go is **data** (how to read, how to write, what an empty string means); here
// the data is arranged into the `view.ToggleItem` shape. Same split as `stateView` in
// modules/config/view.go: shape belongs to the contract package (lib/view), content to
// the owner; the owner's own UI face in one view.go shows what this module contributes.
//
// The direction is allowed: `lib/view` is a **leaf contract** (see its package comment:
// arrows are `modules/* -> lib/view -> component`), so importing it from a business
// module is by design. It pulls in only component and i18n: no HTTP, no frontend.

// ProfileItem builds the "chain head" field.
//
// All three clients (claude / codex / opencode) install this field, so it must have
// **exactly one implementation**: "empty string = back to the global default" must be
// identical in every Apply; implemented as "write an empty string", it leaves a
// `"claude": ""` entry in state.json, the same-value record avoided elsewhere.
func (p AgentProfile) ProfileItem() view.ToggleItem {
	why := i18n.T("Which chain this client starts from. "+
		"The chain decides what each tier resolves to; changing it takes effect the next "+
		"time this client is taken over (newgate on {agent}).",
		i18n.A{"agent": p.AgentID})
	if n := p.Note(); n != "" {
		why += " · " + n
	}
	active := p.Active()
	return view.ToggleItem{
		ID:   ProfileItemID,
		Kind: view.ToggleSelect,
		// The value is **the stored one**, not the resolved one (see AgentProfile.Stored):
		// this field answers "did I set it apart", while `Active()` flattens "following the
		// default to ds" and "I want ds" into the same string - both states look alike
		// on the UI, so the user cannot tell whether this client follows later default changes.
		Label: i18n.T("Profile", nil),
		Value: p.Stored(),
		// Empty string comes first: it is "follow the global default", not a dead option.
		Options: append([]string{""}, p.Options()...),
		// The wording must **name whom it follows**: the user sees "follow default", and only
		// the backend knows who that is. Parentheses won't collide with a profile name (a filename).
		OptionLabels: map[string]string{
			"": i18n.T("{profile} (the global default)", i18n.A{"profile": active}),
		},
		Why: why,
	}
}

// SplitProfile splits the blob the UI returns into "chain head" and **the other fields**.
//
// The UI submits all fields at once (see kinds/Toggles.svelte), and one card carries both
// the chain head and the slot mapping, which land in different places (state.json's
// `active` table vs each client's slot table). All three clients must do the split; its
// silent rule: the head must be **removed** from rest, not left in, because a leftover
// is taken as an unknown slot name, dropped, and the head changed but was never saved.
func SplitProfile(patch map[string]string) (profile string, rest map[string]string) {
	profile = patch[ProfileItemID]
	rest = make(map[string]string, len(patch))
	for k, v := range patch {
		if k != ProfileItemID {
			rest[k] = v
		}
	}
	return profile, rest
}
