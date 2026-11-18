package codex

import modules "github.com/rzbdz/newgate/component"

// Client is the stable identity the Codex family exposes to cross-cutting components.
//
// Same as the opencode one: cross-cutting behavior (upstream quirk
// patches, client x model semantics) only needs the AgentID, and should
// not depend on the takeover implementation or the config file's structure.
type Client struct {
	AgentID string
}

// Capability identifies the unique Codex client family.
var Capability = modules.NewCapability[Client]("client-family.codex")
