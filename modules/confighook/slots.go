package confighook

import (
	"encoding/json"
	"slices"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate/lib/i18n"
)

// This file is **that slot -> tier override table every client needs**.
//
// # Why it is mechanism
//
// The slot mapping says "which slot goes to which semantic tier", and it sits where two
// things meet: the client's **slot identity** (who ANTHROPIC_DEFAULT_SONNET_MODEL is)
// and the product's **tier ladder** (normal > mid).
//
//	The former is client knowledge - its module's, hardcoded in agent.go, not configurable.
//	The latter is **the user's trade-off** - "Bash classifier on normal", "every subagent
//	one tier down": code edits cannot do these things.
//
// Before 2026-09-21 this logic lived in the distribution's modules/claudecode (then the
// only client with slot mappings). codex needs the same table - its `model` slot must
// change tier like claude's - so a copy would be its only option. Some of it **fails
// without any red when copied**: differs-only, filter-without-failing, tiers-only.
//
// So the kernel owns it; a client supplies only its state.json key and its slots,
// nothing else.

// SlotOverrides is one client's override table.
//
// One per client, in its own module's package-level var (modules/claudecode: slotsOf).
type SlotOverrides struct {
	// Key is the key in state.json's `module_config` (`claude_slots` / `codex_slots`).
	// One per client, no interference - same spot as language, same reason: state.json's
	// ModuleConfig is where "top-level keys core does not know" are stored as-is (domain.State).
	Key string
	// AgentID is this client's id, used in the human-facing sentence ("takes effect on
	// the next `newgate on X`") - it must name it, else the user won't know whom to redo.
	AgentID string
	// Slots is this client's slot table.
	//
	// A **value**, not a getter: whoever builds it (the module's own slotsOf) reads
	// `Agent().Slots` fresh every time, so "table changed" shows up as "built again" - a
	// closure layer would only add a hop with nothing swappable in it.
	Slots []Slot
}

// Read reads the user's edited slot mapping (slot name -> tier). Empty if never set.
//
// Reading must **filter**: this file is hand-edited, and a mistyped tier (`hevy`) gets
// injected as a model name all the way upstream; the symptom is "some requests suddenly
// 400", several layers from the config. Filtered entries are reported (see Note), not swallowed.
func (o SlotOverrides) Read() (ok map[string]string, bad map[string]string) {
	raw := store.LoadState().ModuleConfig[o.Key]
	if len(raw) == 0 {
		return nil, nil
	}
	var all map[string]string
	if err := json.Unmarshal(raw, &all); err != nil {
		// Unparseable = the whole thing is untrustworthy. Report it (the UI card explains),
		// but **do not** fail takeover: a broken mapping must not stop the client; defaults are fine.
		return nil, map[string]string{"": string(raw)}
	}
	ok = make(map[string]string, len(all))
	for slot, tier := range all {
		if slices.Contains(o.AllowedFor(slot), tier) {
			ok[slot] = tier
			continue
		}
		if bad == nil {
			bad = map[string]string{}
		}
		bad[slot] = tier
	}
	return ok, bad
}

// AllowedFor is what this slot accepts: semantic tiers plus the exceptions this slot
// declares itself (see Slot.Also, e.g. Claude Code's `inherit`).
//
// Unknown slot names get tiers only: such entries should be dropped anyway (usually the
// UI's snapshot is stale); giving them exceptions would endorse a slot that doesn't exist.
func (o SlotOverrides) AllowedFor(slot string) []string {
	for _, s := range o.Slots {
		if s.Name == slot {
			return append(append([]string(nil), domain.Roles...), s.Also...)
		}
	}
	return domain.Roles
}

// Default says which tier this slot has in the factory settings; false for unknown slots.
func (o SlotOverrides) Default(slot string) (string, bool) {
	for _, s := range o.Slots {
		if s.Name == slot {
			return s.Tier, true
		}
	}
	return "", false
}

// Write stores the slot mapping. Empty table = delete the key (back to factory default).
//
// Values must be known tiers: what is written is injected verbatim as a model name, and
// a mistyped tier becomes a model that does not exist upstream, hard to trace back.
func (o SlotOverrides) Write(m map[string]string) error {
	for slot, tier := range m {
		if !slices.Contains(o.AllowedFor(slot), tier) {
			return i18n.E("{slot}: {tier} is not one of the values this slot takes — pick one of {known}",
				i18n.A{"slot": slot, "tier": tier, "known": o.AllowedFor(slot)})
		}
	}
	// Keep only what differs from the default: an entry "same as the default" would make
	// later default changes skip some users who never configured anything.
	diff := map[string]string{}
	for slot, tier := range m {
		if def, known := o.Default(slot); known && def != tier {
			diff[slot] = tier
		}
	}
	s := store.LoadState()
	// Empty on the way in and empty **after subtraction** are the same thing: this client
	// is all defaults right now. Both must delete the key - checking only the first leaves
	// a `{}` in state.json when the last row goes back to default (observed: codex, with
	// one slot, always had this shape). Same-value records, at the file level this time.
	if len(diff) == 0 {
		delete(s.ModuleConfig, o.Key)
		return store.SaveState(s)
	}
	raw, err := json.Marshal(diff)
	if err != nil {
		return i18n.Ef(err, "cannot encode the slot map: {err}", nil)
	}
	if s.ModuleConfig == nil {
		s.ModuleConfig = map[string][]byte{}
	}
	s.ModuleConfig[o.Key] = raw
	return store.SaveState(s)
}

// Tier is the answer handed to the kernel (see AgentFacts.SlotTier): where this slot is now.
//
// Empty string = not changed, use the default. **No fallback here**: the default belongs
// to TierOf; falling back in both places would make "who decided this" a two-place question.
func (o SlotOverrides) Tier(s Slot) string {
	ok, _ := o.Read()
	return ok[s.Name]
}

// Note says whether this slot was changed and to what; empty string if unchanged.
func (o SlotOverrides) Note(name, def string) string {
	ok, bad := o.Read()
	if v, isBad := bad[name]; isBad {
		return i18n.T("the config says {tier}, which is not a tier — the default is in use",
			i18n.A{"tier": v})
	}
	if v := ok[name]; v != "" && v != def {
		// "Takes effect on the next takeover" must be here: injected env vars are given at
		// **startup**, config files are written at **takeover**, so after changing the mapping
		// a running session doesn't change by a byte (see docs/07-clients-runtime.md).
		// Without this line the user just sees "I clicked and nothing happened".
		return i18n.T("set in the config: {tier} (the default is {def}) — "+
			"takes effect the next time {agent} is taken over (newgate on {agent})",
			i18n.A{"tier": v, "def": def, "agent": o.AgentID})
	}
	return ""
}

// NotInstalled is the **reason the card greys** for "not on this machine"; empty if installed.
//
// One criterion: **is it on this machine**. If not, the slot tier setting, however
// correct, takes effect in not one byte - that command simply does not exist. The UI
// greys the whole card (see Concept.Locked), so nobody edits in vain.
//
// The criterion **is supplied by the client** (facts.Installed), falling back to
// a.OnPath() only when absent. Before 2026-09-22 this called a.OnPath() directly and
// bypassed "clients know their own install": codex lives in nvm's version bin dir,
// missing from the daemon's PATH, so the card said "codex is not installed on this
// machine" - **true of the daemon's PATH, false of the machine** - and the user saw
// only the false half; the diverged criteria gave two different UI answers.
//
// Use a.OnPath() rather than querying PATH yourself: it is the **only** general
// criterion (it drops our own shim dirs, see OnPath); another copy is a third answer.
func NotInstalled(a *Agent, facts AgentFacts) string {
	if a == nil || InstalledDefault(a, facts) {
		return ""
	}
	return i18n.T("{agent} is not installed on this machine — nothing on this card takes effect yet. "+
		"Install it with `newgate {agent} -y`.", i18n.A{"agent": a.ID})
}
