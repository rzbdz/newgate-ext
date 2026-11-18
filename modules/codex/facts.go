package codex

import (
	"os"
	"path/filepath"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
)

// facts are the runtime facts this module hands the kernel (see confighook.AgentFacts).
//
// Codex is an npm-managed Node tool. Generic OnPath is still the first
// test; recognizing nvm's standard version bin directory here is because the daemon's
// PATH is not the interactive shell's PATH that started it: measured 2026-09-22,
// the daemon's PATH had no `/root/.nvm/versions/node/v24.13.0/bin`, and codex
// is exactly `/root/.nvm/versions/node/v24.13.0/bin/codex`. This is not turning OnPath
// into "guess a file": only the Codex module of this distribution, which knows the npm
// channel, recognizes this directory, and other clients still go through the kernel's
// generic test. It does not call `npm prefix -g` or a shell: the card status is polled
// often, and file probing must be cheap and spawn no external process; the installer
// still inherits the daemon's PATH and reports honestly when npm is not found.
type facts struct{}

var _ agentapi.AgentFacts = facts{}

func (facts) Installed() bool {
	return Agent().OnPath() || codexOnNVMPath()
}

func codexOnNVMPath() bool {
	nvmDir := os.Getenv("NVM_DIR")
	if nvmDir == "" {
		nvmDir = filepath.Join(paths.Home(), ".nvm")
	}
	matches, err := filepath.Glob(filepath.Join(nvmDir, "versions", "node", "*", "bin", "codex"))
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
// string (the kernel then falls back to the descriptor's default). **No fallback here** --
// "unchanged means default" has exactly one implementation (confighook.TierOf in the kernel).
//
// It is asked in two places: the moment takeover writes config.toml
// (tierOf in takeover.go), and the current value this card shows
// in the UI. One implementation guarantees the two cannot diverge.
func (facts) SlotTier(s agentapi.Slot) string { return slotsOf().Tier(s) }
