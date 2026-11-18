package claudecode

import (
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
)

// Slot -> tier mapping, configurable since 2026-09-21.
//
// # Why it should have been configurable all along
//
// This mapping says "which Claude Code slot belongs to which semantic tier", the meeting
// point of two things: the client's **slot identity** (who ANTHROPIC_DEFAULT_SONNET_MODEL
// is) and the product's **tier ladder** (normal is stronger than mid).
//
//	the former is the client's knowledge -- this module, hardcoded in agent.go, not configurable;
//	the latter is the **user's trade-off** -- the user. "I want the Bash classifier to go through
//	normal, so it does not break whenever Sonnet is down", "drop every subagent one tier".
//
// So the defaults stay in Go (they are the product's factory settings and what someone
// who has never configured anything gets), and what the user changed lives in config.
//
// # The implementation moved to the kernel on 2026-09-21
//
// The read, write, filter, and "keep only what differs from the default" logic
// now lives in `confighook.SlotOverrides` (see the note there: codex needs the same
// table too, and keeping it in this module would only mean copying it, where several
// mistakes inside would not go red). This file is left with **this module's two pieces
// of knowledge**: which key to use in state.json, and which slots this module has.
//
// SlotsKey is the key inside `module_config` in state.json.
const SlotsKey = "claude_slots"

// slotsOf is this module's override table. One per package: it has no state, only two constants.
func slotsOf() agentapi.SlotOverrides {
	return agentapi.SlotOverrides{
		Key:     SlotsKey,
		AgentID: ID,
		Slots:   Agent().Slots,
	}
}

// ReadSlotTiers / WriteSlotTiers / allowedFor / defaultTier / slotTier / slotNote are this
// file's names for those kernel pieces, keeping the short names: call sites are many (UI,
// commands, injection), and writing agentapi.X would fill this screen with package names.
func ReadSlotTiers() (ok, bad map[string]string) { return slotsOf().Read() }

func WriteSlotTiers(m map[string]string) error { return slotsOf().Write(m) }

func allowedFor(slot string) []string { return slotsOf().AllowedFor(slot) }

// profileOf is this client's **chain-head** cell (the read/write implementation is in
// the kernel, see confighook.AgentProfile): which profile its whole chain starts from.
//
// It and the slot table are two things on the same card: first there
// is a chain (which provider each tier binds to), then there is
// "which tier this slot takes". So it is the first cell on the UI.
func profileOf() agentapi.AgentProfile { return agentapi.AgentProfile{AgentID: ID} }

func defaultTier(slot string) (string, bool) { return slotsOf().Default(slot) }

// facts are the **runtime facts** this module hands the kernel (see confighookapi.AgentFacts).
//
// # Why a type, rather than two function fields on the descriptor
//
// The descriptor (Agent) says "what this client is": binary name, dialect, slot table,
// env var names -- those are constant and printable. While "is it installed" and "which
// tier does some slot take right now" are **how it is right now**, changing with disk and
// config. Stuffed into the same struct, the dependency-injection side is gone: the kernel
// just picks up a function pointer that happened to be assigned, and who provides it,
// whether it can be replaced, and how to put a fake in a test all become unanswerable.
//
// The port side is complete: RegisterAgentFacts gets a Release (automatically undone on Stop), and
// with nothing registered the kernel has a hardcoded default (see confighookapi.InstalledDefault).
type facts struct{}

var _ agentapi.AgentFacts = facts{}

// Installed: does this machine have claude.
//
// It goes through the kernel's generic test (look for the name in Bin on PATH,
// **already skipping our own shim directory**, see confighook.Agent.OnPath),
// and **need not be written here**: this client has no particular knowledge.
func (facts) Installed() bool { return Agent().OnPath() }

// SlotTier: which tier this slot takes right now -- the user's change if any, else the empty
// string (the kernel then falls back to the descriptor's default). **No fallback here**
// -- "unchanged means default" has exactly one implementation (confighookapi.TierOf in the
// kernel), and if both sides did it, that question would again need two places to answer.
func (facts) SlotTier(s agentapi.Slot) string { return slotsOf().Tier(s) }

// slotNote says whether this slot was changed and to what; unchanged returns the empty string.
func slotNote(name, def string) string { return slotsOf().Note(name, def) }

// lockReason says whether these two cards make sense right now; they
// do if it returns the empty string (see confighook.NotInstalled).
//
// facts{} must use the same test: this client has no extra knowledge right now (Installed
// is just OnPath), but passing it in means that if real detection is added later (say claude
// installed elsewhere) this card follows -- the symptom of forgetting to pass it is "the card
// is locked, but it is clearly installed", an inconsistency with no visible origin in the UI.
func lockReason() string { return agentapi.NotInstalled(Agent(), facts{}) }
