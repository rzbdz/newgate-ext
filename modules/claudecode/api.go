package claudecode

import modules "github.com/rzbdz/newgate/component"

// Client is the stable identity the Claude Code family exposes
// to cross-cutting components. Only the AgentID needed for matching is
// passed here, without leaking the client module's plugin implementation.
type Client struct {
	AgentID string
}

// Capability identifies the unique Claude Code client family.
var Capability = modules.NewCapability[Client]("client-family.claudecode")
