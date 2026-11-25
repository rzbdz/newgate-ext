package gemini

import (
	"os"
	"path/filepath"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
)

// facts are the runtime facts this module hands the kernel (see confighook.AgentFacts).
//
// Same shape as codex's, and for the same measured reason: gemini-cli is also an npm-managed
// Node tool, the daemon's PATH is not the interactive shell's PATH that started it, and the
// standard nvm directory is where its binary actually lives. This is not "guess a file" --
// only this module of this distribution, which knows the npm channel, recognizes that
// directory; every other client still goes through the kernel's generic test.
type facts struct{}

var _ agentapi.AgentFacts = facts{}

func (facts) Installed() bool {
	return Agent().OnPath() || geminiOnNVMPath()
}

func geminiOnNVMPath() bool {
	nvmDir := os.Getenv("NVM_DIR")
	if nvmDir == "" {
		nvmDir = filepath.Join(paths.Home(), ".nvm")
	}
	matches, err := filepath.Glob(filepath.Join(nvmDir, "versions", "node", "*", "bin", "gemini"))
	if err != nil {
		return false
	}
	for _, path := range matches {
		if filepath.Clean(filepath.Dir(path)) == filepath.Clean(paths.ShimDir()) {
			continue
		}
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return true
		}
	}
	return false
}

// SlotTier: which tier this slot takes right now -- the user's change if any, else the empty
// string (the kernel then falls back to the descriptor's default). "Unchanged means default"
// has exactly one implementation (confighook.TierOf).
func (facts) SlotTier(s agentapi.Slot) string { return slotsOf().Tier(s) }

// SlotsKey is this module's override table in state.json's module_config.
const SlotsKey = "gemini_slots"

// slotsOf is this module's slot override table (implementation in confighook.SlotOverrides).
func slotsOf() agentapi.SlotOverrides {
	return agentapi.SlotOverrides{
		Key:     SlotsKey,
		AgentID: ID,
		Slots:   Agent().Slots,
	}
}
