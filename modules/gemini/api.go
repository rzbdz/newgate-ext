package gemini

import modules "github.com/rzbdz/newgate/component"

// Client is the stable identity the gemini-cli family exposes to cross-cutting components.
//
// Same shape as codex's: a cross-cutting patch only needs the agent id, and must not depend
// on how takeover works.
type Client struct {
	AgentID string
}

// Capability identifies the gemini-cli client family.
var Capability = modules.NewCapability[Client]("client-family.gemini")
