// Package claudecode owns Claude Code's client descriptor and client-only
// behavior. It must not import model-family modules.
package claudecode

import (
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

const ID = "claude"

// NpmPackage is the package on the official distribution channel (verified 2026-11-17
// from the dist-tags of `https://registry.npmjs.org/@anthropic-ai/claude-code`).
//
// Why npm rather than `https://claude.ai/install.sh`: as measured on 2026-11-17, that line returned
// the **official site's HTML** (a 303 KB Next.js page), and `curl | bash` would execute it as
// a script. The npm package is officially published, its `bin` is `claude`, and codex already uses
// the same channel -- two install paths instead of three, one fewer thing for readers to remember.
const NpmPackage = "@anthropic-ai/claude-code"

// Agent returns Claude Code's descriptor.
//
// # Why the window no longer uses the model-name suffix [1m]
//
// From 2026-09-28 to 2026-11-12, this appended `[1m]` to every slot, making Claude
// Code treat it as a 1M-token context. The scene (2026-11-12, deepseek-flash) proved
// that pushed the session to the last cell of the real window: the session grew to
// 1.01M tokens, the next request got 400 "Input tokens exceed the model context limit",
// and /compact could not be sent either -- the compaction request itself carries the
// whole session, equally over the limit. The client then had **no self-rescue path**.
//
// Now the name is injected as is (tier name or real model name), the window is declared by
// CLAUDE_CODE_MAX_CONTEXT_TOKENS and capped at 512K (ContextWindowCap). An unrecognized name
// takes Claude Code's "unknown model" branch, which **forces** auto-compact at the declared
// window (measured on 2.1.322), so 512K is a real ceiling, not just display. The `context-1m`
// beta header is no longer sent either, so the upstream is not told to treat it as 1M.
//
// The compact point is separately capped at 400K (AutoCompactCap): newgate's reasoning
// backfill grows the request again (measured 2026-11-12: 7.9MB -> 9.0MB, about 14%), and
// if compression waited until 500K, after backfill it would already be flush against 512K.
func Agent() *agentapi.Agent {
	return &agentapi.Agent{
		// The icon in the UI (see icon.go: why it lives in this module).
		Icon:    iconPath,
		ID:      ID,
		Bin:     []string{"claude"},
		Dialect: "anthropic",
		Slots: []agentapi.Slot{
			{Name: "fable", Tier: "heavy", EnvVar: "ANTHROPIC_DEFAULT_FABLE_MODEL",
				Desc: i18n.T("the fable tier (the most expensive); also the marker used to spot a third-party model family falling back automatically", nil)},
			{Name: "opus", Tier: "normal", EnvVar: "ANTHROPIC_DEFAULT_OPUS_MODEL",
				Desc: i18n.T("the opus tier (the workhorse); the main loop runs here (Claude Code's default), and so does opusplan in Plan Mode", nil)},
			{Name: "sonnet", Tier: "mid", EnvVar: "ANTHROPIC_DEFAULT_SONNET_MODEL",
				Desc: i18n.T("the sonnet tier; the Bash classifier and /compact summaries run here", nil)},
			{Name: "haiku", Tier: "light", EnvVar: "ANTHROPIC_DEFAULT_HAIKU_MODEL",
				Desc: i18n.T("the haiku tier; background features", nil)},
			// Also: `inherit` is **Claude Code's own** value (follow the parent session), not
			// one of our tiers. Declaring it here is so the configurable slot-mapping path does
			// not filter it out as a misspelled tier name -- the description says it can be set
			// this way, so the UI and config must really accept it (see agentapi.Slot.Also).
			//
			// **No capability marker**: what it injects may be the value `inherit`, and any suffix would
			// make Claude Code look for a model called `inherit...`. A capability marker states "how
			// big a context the model this name points to has", and `inherit` points to no model at all.
			{Name: "subagent", Tier: "mid", EnvVar: "CLAUDE_CODE_SUBAGENT_MODEL", Also: []string{"inherit"},
				Desc: i18n.T("every subagent / agent team / workflow; set it to inherit to hand that back to per-slot resolution", nil)},
		},
		BaseURLEnv: "ANTHROPIC_BASE_URL",
		AuthEnv:    "ANTHROPIC_AUTH_TOKEN",
		// The window declares **names** of two variables (the kernel only handles
		// "if declared in the config, inject it under this name"; the names are the client's
		// knowledge, see the Agent field comment in the kernel's modules/confighook). Without
		// them, Claude Code assumes 200k for an unrecognized model name and compacts early.
		ContextWindowEnv: "CLAUDE_CODE_MAX_CONTEXT_TOKENS",
		AutoCompactEnv:   "CLAUDE_CODE_AUTO_COMPACT_WINDOW",
		// Safety caps: 512K window / 400K compaction point. See the comment on
		// Agent for why -- once a session over 1M is rejected, not even /compact can
		// rescue it, and leaving headroom is the only way to recover automatically.
		ContextWindowCap: 512000,
		AutoCompactCap:   400000,
		UnsetEnv: []string{
			"ANTHROPIC_API_KEY",
			"ANTHROPIC_MODEL",
			"ANTHROPIC_SMALL_FAST_MODEL",
		},
		Notes: i18n.T("the model field in settings.json is left untouched; what a tier means is decided by the env", nil),
	}
}
