package claudecode

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// classifierConcept is the face this module gives the
// web UI: how the Bash classifier is treated right now.
//
// # Why this card is harder than other cards
//
// `naked forever` is the switch that **permanently short-circuits** the safety classifier:
// while on, classifier requests are all approved directly, never asking the upstream.
// In today's product it shows up only in one line of `newgate status` -- and that line is the
// plugin status the gateway **relays** (special.StatusProvider). In the distribution with cli
// removed (dist-dashboard), status and `newgate naked off` both do not exist, so that state is
// **invisible and has no command to turn it off** in that build; the only way is to go edit that
// key in state.json by hand. A card is the difference between "an editor" and "a JSON file".
//
// # Why it parses itself rather than asking the plugin
//
// The plugin path (special.Statuses) returns empty when **the whole layer is off**
// -- and "the layer is off while the key still says forever" is exactly the situation
// that most needs to be seen: turn the layer back on and that door opens again. So
// what is read here is **the config on disk** (one test in ParseNakedConfig, including
// lazy expiry), and then whether it is really in effect is stated separately. This
// is also the same source as the CLI's status line: the same sentence, the same test.
//
// # Why read-only
//
// `newgate naked`'s write carries time semantics (`on` and `forever` are two things,
// see classifier-naked.go), and this version of the UI has no control for picking a
// duration. Giving it a bool switch would quietly downgrade "permanent" to "for a while"
// -- worse than not giving one (same as pluginmanager leaving the footgun read-only).
func classifierConcept() view.Concept {
	st := store.LoadState()
	cfg, active := ParseNakedConfig(st.ModuleConfig[NakedConfigKey])
	naked := ""
	if active {
		if cfg.Mode == "forever" {
			naked = "forever"
		} else {
			naked = "on"
		}
	}
	override := ""
	if o, err := classifierOverride(st); err == nil && o != nil {
		override = o.String()
	}
	items := []clToggle{
		{
			ID: "naked", Label: i18n.T("Naked", nil), Kind: "select",
			Value:   naked,
			Options: []string{"", "on", "forever"},
			Why:     nakedWhy(st, cfg, active),
		},
		{
			ID: "classifier_override", Label: i18n.T("Classifier override", nil), Kind: "text",
			Value: override,
			Why:   overrideWhy(st),
		},
	}
	return view.Concept{
		ID: "claudecode.classifier", Kind: view.KindToggles,
		Title: i18n.T("Claude Code Bash classifier", nil),
		Data:  clData{File: "state.json", Items: items},
		// Live: the limited-window sentence is "how long until it turns itself off" --
		// with the page left open, that number is wrong (and it is about when the safety gate
		// closes). The whole card reads state.json from memory, so asking often costs nothing.
		Live:  true,
		Apply: applyClassifier,
		// On a machine without claude, this card describes the behavior of a command that does not exist.
		Locked: lockReason(),
	}
}

// The shapes of clToggle / clData live in the contract package (`lib/view`'s ToggleItem /
// Toggles): the shape is **Kind's business**, and Kind is defined there. This module once had
// its own copy (a third one), removed 2026-09-21 -- keeping it means adding a field requires
// changing three places, and the symptom of "changing one fewer" is that cell showing
// no value in the frontend, which looks like "the feature just added is not done yet".
type clToggle = view.ToggleItem
type clData = view.Toggles

// nakedWhy puts "what state it is in" and "does it count right now" into one sentence.
func nakedWhy(st *domain.State, cfg NakedConfig, active bool) string {
	state := i18n.T("off — the Bash classifier is asked as usual", nil)
	switch {
	case active && cfg.Mode == "forever":
		state = i18n.T("on (forever) — the classifier is short-circuited and every request logs [naked]", nil)
	case active:
		state = i18n.T("on — it turns itself off in {left}",
			i18n.A{"left": time.Until(cfg.ExpiresAt).Round(time.Second)})
	}
	// "What the config says" and "does it count right now" are two things: when this
	// layer (or this plugin) is off, the config stays and the door is closed, and turning
	// the layer back on opens it again -- without saying so, the user would follow
	// the sentence above to turn off something that is not in effect at all right now.
	if note := layerNote(st); note != "" {
		state += " · " + note
	}
	return state
}

func overrideWhy(st *domain.State) string {
	if _, err := classifierOverride(st); err != nil {
		return i18n.T("classifier_override is not valid: {err}", i18n.A{"err": err})
	}
	return i18n.T("provider/model, or @key to follow another tier. Empty = the Bash classifier rides the light tier.", nil)
}

// layerNote says "does this layer count right now"; when
// it does, returns the empty string (nothing to say).
func layerNote(st *domain.State) string {
	off := []string{}
	if gatewaystate.PluginOff(st, nakedPluginName) {
		off = append(off, nakedPluginName)
	}
	if gatewaystate.PluginOff(st, backgroundPluginName) {
		off = append(off, backgroundPluginName)
	}
	enabled := gatewaystate.SpecialEnabled(st)
	if !enabled {
		return i18n.T("the whole special_treatment layer is off — none of this counts until it is switched back on", nil)
	}
	if len(off) > 0 {
		return i18n.T("{names}: switched off individually, so that setting counts for nothing right now",
			i18n.A{"names": strings.Join(off, ", ")})
	}
	return ""
}

// applyClassifier writes these two cells. Naked mode's three states correspond
// one to one with the CLI (off / 60s window / forever), and it **does not
// downgrade forever into a window** -- that was exactly why this card used to be
// read-only; now that all three states are laid out, the reason no longer holds.
func applyClassifier(edit json.RawMessage, _ string) (string, error) {
	var patch map[string]string
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the classifier settings in this request are not readable: {err}",
			i18n.A{"err": err})
	}
	if v, ok := patch["naked"]; ok {
		switch strings.TrimSpace(v) {
		case "":
			if err := saveNakedConfig(NakedConfig{}); err != nil {
				return "", err
			}
			// When Mode is the empty string ParseNakedConfig judges it "off", but that would write an empty
			// object into state.json; deleting the key outright is cleaner (same as `newgate naked off`).
			if err := deleteNakedConfig(); err != nil {
				return "", err
			}
		case "on":
			if err := saveNakedConfig(NakedConfig{
				Mode: "on", ExpiresAt: time.Now().Add(nakedDefaultTTL),
			}); err != nil {
				return "", err
			}
		case "forever":
			if err := saveNakedConfig(NakedConfig{Mode: "forever"}); err != nil {
				return "", err
			}
		default:
			return "", i18n.E("naked must be off, on or forever, got {value}", i18n.A{"value": v})
		}
	}
	if v, ok := patch["classifier_override"]; ok {
		v = strings.TrimSpace(v)
		s := store.LoadState()
		if v == "" {
			delete(s.ModuleConfig, "classifier_override")
		} else {
			b, err := domain.ParseBindingString(v)
			if err != nil {
				return "", err
			}
			raw, err := json.Marshal(b)
			if err != nil {
				return "", err
			}
			if s.ModuleConfig == nil {
				s.ModuleConfig = map[string][]byte{}
			}
			s.ModuleConfig["classifier_override"] = raw
		}
		if err := store.SaveState(s); err != nil {
			return "", err
		}
	}
	return "", nil
}

// deleteNakedConfig removes the naked-mode key (turns it off).
func deleteNakedConfig() error {
	s := store.LoadState()
	delete(s.ModuleConfig, NakedConfigKey)
	return store.SaveState(s)
}

// The plugin name comes from the plugin itself (`classifierNaked{}.Name()`), not copied as a
// literal: a copy drifts, and the drift means PluginOff asks about a plugin that does not exist
// -- **the failure direction is fail-open**, and that reminder would silently never appear.
var (
	nakedPluginName      = classifierNaked{}.Name()
	backgroundPluginName = background{}.Name()
)

// slotsConcept is this card: **which Claude Code slot takes which tier**.
//
// # Why it deserves a card
//
// The mapping's defaults live in agent.go (factory settings), and before 2026-09-21
// it lived **only** there -- to "make the Bash classifier go through normal
// so a Sonnet outage does not break the whole session" or "drop every subagent
// one tier to save money", the only way was to edit code and rebuild. The user's
// words were "looking at it now, this is just hardcoded in the go code, isn't it".
//
// It is config, not code: a change takes effect at **the next takeover** (env
// is injected at launch, so a running session needs `newgate on claude` again
// -- this is written in the why, or the user would think the click did nothing).
//
// # Why not one card per tier
//
// One row per tier is this card: five rows, read in one glance, and
// changing any row is one dropdown. As five cards, "I want sonnet to
// take the heavy tier" means finding the right one among five places.
func slotsConcept() view.Concept {
	a := Agent()
	_, bad := ReadSlotTiers()
	// The first cell is the **chain head** (which profile this client starts from);
	// the rest are which tier each slot takes. The order matters: before a tier
	// can resolve to any model there must be a chain; chain first, tier second.
	items := make([]clToggle, 0, len(a.Slots)+1)
	items = append(items, profileOf().ProfileItem())
	for _, s := range a.Slots {
		why := s.Desc
		if note := slotNote(s.Name, s.Tier); note != "" {
			why += " · " + note
		}
		items = append(items, clToggle{
			ID: s.Name, Label: s.Name, Kind: "select",
			// Where it goes right now: the facts this module hands the kernel (see
			// facts in slots.go), asking the same implementation as the injection
			// path -- what the UI shows and what is really injected cannot diverge.
			Value: agentapi.TierOf(facts{}, s),
			// The tiers plus this slot's own exception (the `inherit` kind, see agentapi.Slot.Also):
			// without it in the dropdown the user cannot set it -- and the description says they can.
			Options: allowedFor(s.Name),
			Why:     why,
		})
	}
	// The unparseable one must be stated separately: it is a **broken file**,
	// and right now it silently does nothing (the defaults are still right).
	// Without reporting it, the user would think their change took effect.
	file := "state.json"
	if len(bad) > 0 {
		file = i18n.T("state.json — {key} could not be read, the defaults are in use",
			i18n.A{"key": SlotsKey})
	}
	return view.Concept{
		ID: "claudecode.slots", Kind: view.KindToggles,
		Title: i18n.T("Claude Code slots", nil),
		Data:  clData{File: file, Items: items},
		// Order 20: after the classifier card. The classifier is "is the safety
		// gate off right now" (changes, needs watching); here it is "an assignment
		// configured once after install" (rarely touched) -- put the changing one first.
		Order:  20,
		Apply:  applySlots,
		Locked: lockReason(),
	}
}

// applySlots writes the slot mapping.
//
// The payload is **all slots** (see kinds/Toggles.svelte: everything at once, not
// incremental). So what arrives here is a complete table, handed straight to WriteSlotTiers
// -- it drops the ones "equal to the default", so setting a row back to the default =
// that row disappears from config, rather than leaving a redundant equal-valued record.
func applySlots(edit json.RawMessage, _ string) (string, error) {
	var patch map[string]string
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the slot map in this request is not readable: {err}",
			i18n.A{"err": err})
	}
	// There are two things on one card: the chain head (one cell) and the slot mapping
	// (several cells). Unpacking that is in the kernel (see confighook.SplitProfile), because
	// the rule "the chain head must be pulled out of the slot set" is needed by three parties
	// and none may copy it wrong -- kept in, it would be dropped as an unknown slot name, with
	// the symptom "the chain head was changed but not saved", while the slot side looks fine.
	profile, patch := agentapi.SplitProfile(patch)

	known := map[string]bool{}
	for _, s := range Agent().Slots {
		known[s.Name] = true
	}
	next := map[string]string{}
	for slot, tier := range patch {
		// Unknown slot names are dropped: that is most likely **the UI's
		// snapshot being stale** (slots changed after a module upgrade), and
		// writing it would only leave an entry in state.json nobody recognizes.
		if known[slot] {
			next[slot] = tier
		}
	}
	// Write the chain head first, then the slots: when the chain head cannot be
	// written (the profile does not exist) the whole save fails, and not one byte on
	// the slot side should move -- two pieces of config change together or not at all.
	if err := profileOf().Write(profile); err != nil {
		return "", err
	}
	if err := WriteSlotTiers(next); err != nil {
		return "", err
	}
	return "", nil
}
