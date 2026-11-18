package wrapper

import (
	modules "github.com/rzbdz/newgate/component"
)

// Capability identifies the process-unique shim takeover implementation.
var Capability = modules.NewCapability[Wrapper]("wrapper")

// Wrapper owns the **install/remove-link** half of the "PATH shim" takeover path:
// Install/Uninstall/Installed/Foreign.
//
// The other half - the process invocation when the link is hit - **is not on this
// interface** (changed 2026-09-20): it is a kind of **process entry**, in component/
// entry's ledger (this module declares via entry.RankShim in its Start: claim when
// argv0 is a taken-over client). Still one module, each half on its own port - else
// "who is the entry" would again need the root to name a module, the thing to dismantle.
//
// The byte surgery (symlinks, shell rc) stays in the runtime/injection library;
// this module owns the **policy** - which names are clients, when to claim a call.
type Wrapper interface {
	// Install creates a shim link for a client and returns the link path.
	Install(agentID string) (string, error)

	// Uninstall removes a shim. The name must be a known client and the link ours - a real
	// executable of the same name or the user's own wrapper script is left untouched.
	Uninstall(agentID string) error

	// Installed lists the shim names we installed ourselves.
	Installed() []string

	// Foreign lists entries in the shim directory that we did not install. Show only, no delete.
	Foreign() []string
}
