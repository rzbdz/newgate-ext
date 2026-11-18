package codex

import (
	"encoding/json"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This file is the face codex gives the web UI: **which tier it runs**.
//
// Its shape lines up with claudecode's slot card (the same Kind, the same values),
// because they are the same thing: "which tier this client uses right now". Only one
// difference -- codex has a single slot (its `model` is everything), and that value is
// **written into config.toml**, not injected as an environment variable (see agent.go).
//
// Why this card is worth having: codex's tier lives in `model = "..."` in `~/.codex/config.toml`,
// and that file is written **at takeover time**. That is, "change the tier" has to go through
// `newgate on codex` on the user's side, and before that there was nowhere he could see which tier
// he currently runs -- `newgate agents` is typed in the terminal and has no browser counterpart.

// SlotsKey is the key in state.json's `module_config` (this module's override table).
const SlotsKey = "codex_slots"

// slotsOf is this module's override table (implementation in confighook.SlotOverrides).
func slotsOf() agentapi.SlotOverrides {
	return agentapi.SlotOverrides{
		Key:     SlotsKey,
		AgentID: ID,
		Slots:   Agent().Slots,
	}
}

// conceptID is this card's stable identity.
const conceptID = "codex.model"

// profileOf is the **chain head** cell of this client (the read/write implementation is in
// the kernel, see confighook.AgentProfile): which profile codex's whole chain starts from.
func profileOf() agentapi.AgentProfile { return agentapi.AgentProfile{AgentID: ID} }

// registerView mounts the card. Does nothing when there is no web UI.
//
// `In("Clients")`: same group as claudecode / opencode. The symptom of leaving it out
// is that **the codex entry drops to the top of the sidebar, belonging to no group**
// (measured 2026-09-21, caught by the user) -- groups are reported by contributors,
// and the UI only draws the title it was given, so leaving it out turns nothing red.
func registerView(v view.Service) (modules.Release, error) {
	return v.Register("codex",
		view.Title(func() string { return i18n.T("Codex", nil) }).
			In(func() string { return i18n.T("Clients", nil) }).
			Of(view.FieldClients),
		concepts)
}

func concepts() ([]view.Concept, error) {
	return []view.Concept{modelConcept(), modelsConcept()}, nil
}

func modelConcept() view.Concept {
	a := Agent()
	_, bad := slotsOf().Read()

	// The first cell is the **chain head**; after it come the tiers each slot uses. Order matters:
	// which model a tier finally lands on needs a chain first, and only with a chain is there a tier.
	items := make([]view.ToggleItem, 0, len(a.Slots)+1)
	items = append(items, profileOf().ProfileItem())
	for _, s := range a.Slots {
		why := s.Desc
		if note := slotsOf().Note(s.Name, s.Tier); note != "" {
			why += " · " + note
		}
		items = append(items, view.ToggleItem{
			ID: s.Name, Label: s.Name, Kind: view.ToggleSelect,
			// Where it goes right now: the facts this module hands the kernel (see facts.go),
			// from the same implementation that takeover asks when writing the TOML --
			// what the UI shows and what is actually written into the file cannot split.
			Value: agentapi.TierOf(facts{}, s),
			// The tier + the exception this slot itself recognizes (see agentapi.Slot.Also): values
			// not in the dropdown cannot be set by the user -- while the description says they can.
			Options: slotsOf().AllowedFor(s.Name),
			Why:     why,
		})
	}

	// The unparsable one must be called out separately: it is a **broken
	// file**, and right now it quietly does nothing (the defaults are still
	// right). Without a report, the user would think his change took effect.
	file := "state.json"
	if len(bad) > 0 {
		file = i18n.T("state.json — {key} could not be read, the defaults are in use",
			i18n.A{"key": SlotsKey})
	}
	return view.Concept{
		ID: conceptID, Kind: view.KindToggles,
		Title: i18n.T("Codex model", nil),
		Data:  view.Toggles{File: file, Items: items},
		// Order 20: same position as claudecode's slot card. In this section it is "set once after
		// installing", not "did it change just now", so it sits after the things that change.
		Order: 20,
		Apply: applyModel,
		// The mode sentence and the toggle button are **not on this card**: they
		// moved to the "Codex models" card (see modelsview.go). Reason: the whole meaning of the
		// mode is "which of our tiers a codex model name counts as", and that table really takes
		// effect only in rename mode -- split apart, the user looks at a table without knowing
		// whether it counts right now. The user's words on 2026-09-21 were "I used rename mode,
		// why is there no mapping table to fill in", and that feedback said two things at once:
		// the table is needed, and it and the mode are one thing. On a machine without codex this
		// whole card is greyed out: however right the tier is written, takeover will not happen --
		// that config.toml does not exist at all (criterion in confighook.NotInstalled). facts{}
		// must be passed: this module's Installed recognizes nvm's version bin directory that
		// the generic PATH lookup does not (see facts.go: the daemon's PATH lacks it); leaving
		// it out degrades to "the daemon's PATH has no codex" = the card lies about this machine.
		Locked: agentapi.NotInstalled(a, facts{}),
	}
}

// applyModel writes the slot mapping.
//
// The payload is **all slots** (see kinds/Toggles.svelte: everything at once, not
// incremental), so this is a complete table handed straight to the shared Write -- which
// drops the ones "equal to the default", so changing this line back to the default =
// that key disappears from the config, rather than leaving a redundant same-value record.
//
// No CAS baseline: this table's home is state.json's module_config (one cell per
// module), and it has **no** corresponding raw-text card (unlike claude's slot table,
// which pairs with a config file card). When two tabs change this card at the same time,
// the later write wins -- the cost is that one cell's trade-off, not losing a file.
func applyModel(edit json.RawMessage, _ string) (string, error) {
	var patch map[string]string
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the slot map in this request is not readable: {err}",
			i18n.A{"err": err})
	}
	// Two things share this card: the chain head (one cell) and the slot mapping (several
	// cells). The splitting is done in the kernel (see confighook.SplitProfile): the chain head
	// must be taken out of the slot set, otherwise it is dropped as an unknown slot name, and
	// the symptom is "the chain head was changed but not saved" while the slot side looks fine.
	profile, patch := agentapi.SplitProfile(patch)

	known := map[string]bool{}
	for _, s := range Agent().Slots {
		known[s.Name] = true
	}
	next := map[string]string{}
	for slot, tier := range patch {
		// Unknown slot names are dropped: that is most likely **the snapshot
		// the UI holds being stale** (slots changed after a module upgrade), and
		// writing it only leaves a record in state.json that nobody recognizes.
		if known[slot] {
			next[slot] = tier
		}
	}
	// Write the chain head first, then the slots: if the chain head cannot
	// be written (the profile does not exist) the whole save fails, and not one byte
	// on the slot side should change -- either both configs change, or neither does.
	if err := profileOf().Write(profile); err != nil {
		return "", err
	}
	if err := slotsOf().Write(next); err != nil {
		return "", err
	}
	return "", nil
}

// modeNote says which takeover mode is in use right now (see the three constants in models.go).
//
// The tone is ok rather than warn: both modes are **legitimate choices**, neither
// is "problematic" -- warn would make people think the config is in trouble, and
// then go looking for a fault that is not there. It is a statement, not a warning.
func modeNote() *view.Note {
	text := i18n.T("takeover mode: one tier is written into config.toml", nil)
	if Mode() == ModeRename {
		text = i18n.T("rename mode: codex's own model names map to tiers", nil)
	}
	return &view.Note{Text: text, Tone: view.ToneOK}
}

// modeActions is the mode toggle button -- **it draws only "the other one"**.
//
// The same trade-off as the pair of applies on the tier card (see profileActions
// in config/view.go): drawing the one already in effect as a button gives a button that changes
// nothing when clicked, and standing next to it would drown out the one that really does something.
// One difference: that one is "do / do not", this one is **two symmetric options**, so the button
// text is the mode's **name** (`rename mode`), not "switch to..." -- for a switch with only two
// positions, a name is shorter than an action, and the reader need not do a negation in his head.
func modeActions() []view.Action {
	if Mode() == ModeRename {
		return []view.Action{{
			ID:    "mode-takeover",
			Label: func() string { return i18n.T("takeover mode", nil) },
			Run:   func() (string, error) { return switchMode(ModeTakeover) },
		}}
	}
	return []view.Action{{
		ID:    "mode-rename",
		Label: func() string { return i18n.T("rename mode", nil) },
		Run:   func() (string, error) { return switchMode(ModeRename) },
	}}
}

// switchMode switches modes, and **takes over again right away under the new mode**.
//
// # Why it cannot just record the mode and wait for the next `newgate on codex`
//
// The whole meaning of the mode is **what gets written into config.toml**. Only writing the
// state without taking over equals nothing happening, while the Note on the card has already
// changed -- the user would think he switched, then switch a model in codex and find it does
// nothing. Same rule as the apply on the tier card: pressing it must take effect right away.
//
// # All three outcomes need an answer
//
//   - writing the state fails -> the whole action fails (the UI shows the reason);
//   - codex is not installed / that config.toml is not there -> **record the mode, skip
//     the takeover**. This is not an error: the user may pick it in the UI first and
//     install codex later -- the Skipped that `Apply` returns is exactly this sentence;
//   - takeover fails (the file cannot be written) -> report an error. **But the state has
//     already been written by then**, so what the UI shows and what is in the file disagree
//     -- deliberately: the state says "which one you chose", and what failed is this
//     particular write. Pressing the button again (or `newgate on codex`) catches it up.
func switchMode(mode string) (string, error) {
	if err := SetMode(mode); err != nil {
		return "", err
	}
	if _, err := (Takeover{}).Apply(proxyPort()); err != nil {
		return "", err
	}
	return "", nil
}

// proxyPort is the port takeover writes into base_url. It comes from **the
// same source** as the command line (state.json's port, falling back to the
// default when unreadable), otherwise the mode switched in the UI and the one
// `newgate on codex` writes in the terminal would point at two different ports.
func proxyPort() int {
	if st := store.LoadState(); st != nil && st.Port != 0 {
		return st.Port
	}
	return domain.ProxyPort
}
