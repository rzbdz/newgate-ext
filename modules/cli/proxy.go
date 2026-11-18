package cli

import (
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
)

// proxyState is the proxy's process info + self-reported state.
//
// The document shape and client are in modules/gateway/controlplane (a shared leaf): reading
// the daemon's self-reported state is not a UI-only capability, and any module can read it
// -- that is the premise for metrics / breaker / probe moving back into their own modules.
//
// **Only forwarding is kept here**: the UI parses no field of that document
// (how the breaker table sorts, which bindings are delisted, are breaker's
// knowledge; see Available / Rank on controlplane.Doc). The UI uses it only
// to answer process questions like "is the daemon there, what is the pid".
func proxyState() (*controlplane.Info, *controlplane.Doc) { return controlplane.State() }

// notifyProxy makes the running proxy reread config
// immediately (forwarded to the control-plane leaf).
//
// Why this step belongs to the UI: a command finishing its config change must take effect
// **immediately**, and the UI is the side initiating the change (see Host.NotifyProxy).
func notifyProxy() { controlplane.Notify() }
