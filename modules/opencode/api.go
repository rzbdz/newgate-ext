package opencode

import modules "github.com/rzbdz/newgate/component"

// Client is the stable identity the OpenCode family exposes to cross-cutting components.
// Cross-cutting behavior needs only AgentID; it must not depend on the takeover implementation or
// the config file shape.
type Client struct {
	AgentID string
}

// Capability identifies the one OpenCode client family.
var Capability = modules.NewCapability[Client]("client-family.opencode")
