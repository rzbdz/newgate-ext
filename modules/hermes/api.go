package hermes

import modules "github.com/rzbdz/newgate/component"

// Client is the stable identity the Hermes family exposes to cross-cutting components.
//
// The same shape as codex / opencode: cross-cutting behavior (upstream quirk patches,
// client×model semantics) needs only AgentID and should not depend on the takeover
// implementation or config.yaml's structure.
type Client struct {
	AgentID string
}

// Capability identifies the one Hermes client family.
var Capability = modules.NewCapability[Client]("client-family.hermes")
