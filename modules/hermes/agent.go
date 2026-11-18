// Package hermes describes the Hermes Agent client (Nous Research's Python agent, CLI +
// messaging gateway).
//
// This module belongs to the **client integration** category: it says what hermes is, where it
// is installed, how it is taken over, and does not mix in other modules' trade-offs about this
// client -- the same boundary as claudecode / codex / opencode.
//
// Its biggest difference from the other three is that hermes **can switch models inside a
// session** (`/model`, in both the CLI and the messaging gateway). So takeover serves both
// usages at once (decided by the user on 2026-11-04):
//
//  1. **Follow newgate**: the default model is written as a tier name (`normal`), and which
//     model it actually lands on is decided by newgate's profile -- change the profile and the
//     next request follows, hermes untouched.
//  2. **Name it directly inside hermes**: the `/model` list shows every profile behind newgate
//     and every model it binds (`provider/model`); pick one and you are pinned to it. This list
//     is a snapshot written into config.yaml **at takeover time**: profiles change, so it is
//     rewritten (`newgate hermes sync`), and whether the running messaging gateway restarts is
//     the user's call (see sync.go).
package hermes

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

const ID = "hermes"

// InstallScript is the official installation method (the curl | bash line from the README).
//
// It is a fact that changes, so it lives in the client module (same reason as codex's
// NpmPackage).
const InstallScript = "https://raw.githubusercontent.com/NousResearch/hermes-agent/main/scripts/install.sh"

// Slot names. They are **this module's keys** (the override table in state.json, that card in
// the UI, that line in status all use these names), not paths in config.yaml -- one slot may
// correspond to several places in the file (see slotTargets in takeover.go).
const (
	SlotModel       = "model"
	SlotDelegation  = "delegation"
	SlotVision      = "vision"
	SlotCompression = "compression"
	SlotAuxiliary   = "auxiliary"
)

// Agent returns the Hermes descriptor.
//
// # Where the slot table comes from
//
// = **every key in config.yaml whose value is a model name**, grouped into five by "what work
// it is doing". Checked against hermes 0.13's DEFAULT_CONFIG and the places that read these
// keys (`hermes_cli/config.py`, `agent/auxiliary_client.py`, `tools/delegate_tool.py`,
// 2026-11-04):
//
//	model        model.default                main loop
//	delegation   delegation.model             sub-agents spawned by delegate_task
//	vision       auxiliary.vision.model       image reading
//	compression  auxiliary.compression.model  compressing a long context into a summary
//	auxiliary    auxiliary.<other tasks>.model  titling, approval, session search, web
//	                                           summaries, curator, …
//
// **Not in the table**: `smart_model_routing.cheap_model` and `compression.summary_model` are
// dead config (the former's feature was removed upstream in 424e9f36b, the latter migrated into
// auxiliary.compression at v16→17); no code reads them -- taking them over only creates the
// illusion of "I changed it and nothing happened". `fallback_providers` is left alone too: the
// chain's fallback is done server-side by newgate, and when hermes activates a fallback it
// **does not honor** a named provider's api_mode (run_agent.py re-guesses from the URL), so
// pointing it back at us can pick the wrong dialect.
// Why the dozen-odd tasks under auxiliary count as one slot: they are all "small work outside
// the main loop", and the user expects the same thing of them (cheap, fast); one slot each
// would just put ten dropdowns nobody will change on the card. vision and compression stand
// alone because they are **not** small work: one needs multimodality, the other must read a
// whole context -- their default tier differs from the rest (the same rule as Claude Code
// putting /compact on the sonnet tier).
//
// The slots all have **no EnvVar**: hermes models only honor config.yaml (it does not read
// ANTHROPIC_*-style environment variables); takeover writes the value into the file -- the
// same shape as codex.
func Agent() *agentapi.Agent {
	return &agentapi.Agent{
		Icon:    iconPath,
		ID:      ID,
		Bin:     []string{"hermes"},
		Dialect: "openai",
		Slots: []agentapi.Slot{
			{Name: SlotModel, Tier: "normal",
				Desc: i18n.T("the main loop (model.default); /model can still switch to any newgate model", nil)},
			{Name: SlotDelegation, Tier: "mid",
				Desc: i18n.T("subagents started by delegate_task (delegation.model)", nil)},
			{Name: SlotVision, Tier: "vision",
				Desc: i18n.T("image understanding (auxiliary.vision)", nil)},
			{Name: SlotCompression, Tier: "mid",
				Desc: i18n.T("context compression summaries (auxiliary.compression)", nil)},
			{Name: SlotAuxiliary, Tier: "light",
				Desc: i18n.T("every other auxiliary task (titles, approval, session search, web extract, curator …)", nil)},
		},
		Notes: i18n.T("the model and provider live in ~/.hermes/config.yaml; the messaging gateway re-reads it on every message", nil),
	}
}

// Home is hermes' data directory.
//
// Order: `$HERMES_HOME` → the directory of the file the last takeover wrote → `~/.hermes`.
//
//   - `$HERMES_HOME` first, because that is hermes' own rule
//     (`hermes_constants.get_hermes_home`: environment variable → `~/.hermes`), and its
//     profile mechanism switches on this variable too (`profiles/<name>`).
//   - The middle step is for the **daemon**: its environment is not the user shell's. The file
//     taken over by `HERMES_HOME=… newgate on hermes` is one the daemon would always compute as
//     `~/.hermes` -- so background sync, saves from the web page and the restart button would
//     all act on a file nobody uses, and the taken-over list would never update again. So
//     takeover records which file it wrote (record.Target), and a process without an explicit
//     environment variable honors that.
func Home() string {
	if h := strings.TrimSpace(getenv("HERMES_HOME")); h != "" {
		return h
	}
	if t := loadRecord().Target; t != "" {
		return filepath.Dir(t)
	}
	return filepath.Join(paths.Home(), ".hermes")
}

// ConfigPath is the file hermes reads.
func ConfigPath() string { return filepath.Join(Home(), "config.yaml") }

// getenv is the only indirection in this layer: tests must be able to point HERMES_HOME at a
// sandbox, and **must not** touch the real user's ~/.hermes.
var getenv = os.Getenv
