// Package codex owns the base Codex client descriptor (OpenAI's official codex CLI).
//
// This module is in the **client onboarding** class: it states "what codex
// is, where it is installed, how to install it", without mixing in other modules'
// trade-offs about this client. Upstream quirks and cross-cutting semantics each
// get their own module -- consistent with the boundary for claudecode / opencode.
package codex

import (
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

const ID = "codex"

// NpmPackage is the package on the official distribution channel.
//
// It is **one line of fact**, and one that changes (the vendor has changed the package name
// and the channel), so it lives in the client module rather than the kernel: the kernel does
// not know npm, let alone one company's package name (see AgentInstaller in confighook).
const NpmPackage = "@openai/codex"

func Agent() *agentapi.Agent {
	return &agentapi.Agent{
		// The icon in the UI (see icon.go: why it lives in this module).
		Icon:    iconPath,
		ID:      ID,
		Bin:     []string{"codex"},
		Dialect: "openai",
		// The model name and provider go through ~/.codex/config.toml (`model` / `model_providers.*`),
		// with no env var entry -- so the slot below **has no EnvVar**: its value is written
		// into that TOML by takeover, not injected as env (see tierOf in takeover.go).
		//
		// The slot is still registered, rather than letting takeover read a hardcoded tier itself:
		// once registered it shares the same knowledge and the same card as claude's tier mapping
		// -- the user changing a tier need not know "codex's model name is written into a file".
		// The slot table = **every key in codex's config whose value is a model name**. There are
		// only two, settled after checking its ConfigToml field table (codex 0.155, 2026-09-21)
		// -- not because we under-counted: `model` is the main model, `review_model` is the one
		// `codex review` uses, and the rest like `model_reasoning_effort` / `model_verbosity`
		// are **knobs outside the tier ladder** (values are low/high, not model names); stuffing
		// them into the slot table would only put a pile of non-tier values in the dropdown.
		//
		// Claude Code has four because it has four env vars; the difference comes
		// from the two vendors' interface surfaces, not from our trade-offs.
		Slots: []agentapi.Slot{
			{
				Name: "model",
				Tier: "normal",
				Desc: i18n.T("the model codex runs on; written into config.toml by takeover", nil),
			},
			{
				Name: "review_model",
				Tier: "normal",
				Desc: i18n.T("the model `codex review` runs on; separate from the main one", nil),
			},
		},
		Notes: i18n.T("the model and provider live in ~/.codex/config.toml", nil),
	}
}
