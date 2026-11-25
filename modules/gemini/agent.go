// Package gemini describes Google's gemini-cli client.
//
// Same class as claudecode / codex / hermes / opencode: it says what this client is, how it
// is installed, and how its traffic is routed through newgate -- and nothing about upstream
// quirks, which belong to their own modules.
//
// # What makes this client different from the other four
//
// It speaks the **native Gemini API**: the model is a path segment,
//
//	POST /v1beta/models/<model>:generateContent
//
// not a top-level `model` field. So this module only has to hand the right base URL and model
// name over; the gateway parses that path (see modules/gateway/forward/geminipath.go). The
// model we inject is a **tier name**, and the data plane replaces it with the concrete model
// its profile resolved -- the same "client names a tier, newgate decides the model" story as
// every other client.
package gemini

import (
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

const ID = "gemini"

// NpmPackage is the package on Google's official channel.
//
// One line of fact, and one that changes, so it lives here rather than in the kernel (same
// reason as codex's NpmPackage): the kernel does not know npm, let alone a vendor's package.
const NpmPackage = "@google/gemini-cli"

// SlotModel is this module's slot name (the override table and the status line use it).
const SlotModel = "model"

// Agent returns the gemini-cli descriptor.
//
// # Why both variables are environment entries
//
// gemini-cli has no settings key for the API base: `GOOGLE_GEMINI_BASE_URL` is its only
// override (checked in 0.37.1's bundle, 2026-11-25), and `GEMINI_API_KEY` selects the
// `gemini-api-key` auth path without any login prompt. So takeover is the **shim** mechanism
// (see runtime/takeover): the shim injects these three variables and execs the real binary.
// That is also why this module needs no Config takeover -- there is no file to rewrite.
func Agent() *agentapi.Agent {
	return &agentapi.Agent{
		Icon:       iconPath,
		ID:         ID,
		Bin:        []string{"gemini"},
		Dialect:    "gemini",
		BaseURLEnv: "GOOGLE_GEMINI_BASE_URL",
		AuthEnv:    "GEMINI_API_KEY",
		Slots: []agentapi.Slot{{
			Name: SlotModel,
			// "heavy" rather than "normal": a coding CLI's main loop is the expensive one, and
			// the Gemini-flavored profiles in this distribution define heavy/mid/light/vision
			// (no "normal"). A default the profile cannot resolve would 404 on the first call.
			Tier: "heavy",
			// gemini-cli's precedence is `--model` → `GEMINI_MODEL` → `settings.model.name`
			// (bundle, 2026-11-25), so an environment entry is enough; nothing to write to disk.
			EnvVar: "GEMINI_MODEL",
			Desc: i18n.T("the model gemini-cli runs on; a tier name here means the profile "+
				"decides the real model, and the data plane rewrites the /v1beta path", nil),
		}},
		Notes: i18n.T("gemini-cli speaks the native Gemini API (/v1beta/models/<model>:…); "+
			"newgate reads the model from that path and rewrites it per chain step", nil),
	}
}
