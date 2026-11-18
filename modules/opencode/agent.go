// Package opencode owns the base OpenCode client descriptor. Add-ons such as
// OMO are separate modules and are composed by the catalog.
package opencode

import (
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

const ID = "opencode"

// NpmPackage is the official npm package (checked the registry's `latest` on 2026-11-17; the bin
// is opencode).
//
// Why not `https://opencode.ai/install`: that script picks a version over the network, edits
// PATH, and handles a pile of platform branches -- it is **for a person to install with**, not for
// another program to call from a wizard. The npm route is the same as claude / codex, and its error
// messages are readable when it fails (see the kernel's install.go).
const NpmPackage = "opencode-ai"

func Agent() *agentapi.Agent {
	return &agentapi.Agent{
		// The icon in the UI (see icon.go: why it lives in this module).
		Icon:    iconPath,
		ID:      ID,
		Bin:     []string{"opencode"},
		Dialect: "openai",
		Notes:   i18n.T("slots are discovered by the config extension module", nil),
	}
}
