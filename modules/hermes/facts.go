package hermes

import (
	"os"
	"path/filepath"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
)

// facts is the runtime facts this module hands to the kernel (see confighook.AgentFacts).
//
// hermes' official installer puts the entry at `~/.local/bin/hermes` (pointing at
// `$HERMES_HOME/hermes-agent/venv/bin/hermes`). The generic OnPath is still the first
// criterion; recognizing these two locations too is the same reason as codex recognizing the
// nvm directory: the daemon's PATH is not the interactive shell's PATH, and `~/.local/bin` is
// exactly the slot that login scripts often add. Look only at files, start no process: the card
// state is polled frequently.
type facts struct{}

var _ agentapi.AgentFacts = facts{}

func (facts) Installed() bool { return realHermes() != "" }

// SlotTier is the tier this slot uses right now: the user's change if there is one, an empty
// string if not (the kernel then falls back to the descriptor default, see confighook.TierOf).
func (facts) SlotTier(s agentapi.Slot) string { return slotsOf().Tier(s) }

// SlotsKey is the key of this module's override table in state.json's `module_config`.
const SlotsKey = "hermes_slots"

func slotsOf() agentapi.SlotOverrides {
	return agentapi.SlotOverrides{Key: SlotsKey, AgentID: ID, Slots: Agent().Slots}
}

// tierFor returns the tier a slot should use right now (looked up by name, same reason as
// codex's tierFor).
func tierFor(name string) string {
	for _, s := range Agent().Slots {
		if s.Name == name {
			return agentapi.TierOf(facts{}, s)
		}
	}
	return ""
}

// realHermes is the real hermes entry on this machine (skipping our own shim); empty string if
// none.
func realHermes() string {
	if p, err := Agent().FindReal(paths.ShimDir()); err == nil {
		return p
	}
	for _, p := range []string{
		filepath.Join(paths.Home(), ".local", "bin", "hermes"),
		filepath.Join(Home(), "hermes-agent", "venv", "bin", "hermes"),
	} {
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}
