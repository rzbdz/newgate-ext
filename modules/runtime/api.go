package runtime

import (
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	modules "github.com/rzbdz/newgate/component"
)

// Runtime wraps a taken-over client's launch boundary, so the CLI need not know shim/env/argv.
type Runtime interface {
	// Launch injects env and execs the real client. facts are fetched by the caller from the
	// catalog port (agentapi.AgentFacts) - "how this client is now" is its knowledge; we pass it.
	Launch(agent *agentapi.Agent, facts agentapi.AgentFacts, args []string, profile string) int
}

// Control is the **takeover and lifecycle** port: the thing underneath `start` / `on` /
// `off` / `status`, promoted from commands to a public port on 2026-11-17 (control.go's note).
//
// # Why it is a separate port from Runtime
//
// Two reasons, one of principle and one very practical:
//
//   - **Who needs which half** differs. Wrapped launch (wrapper, `newgate claude`) needs
//     only Launch - it never touches the on-disk takeover state; the distribution's setup
//     wizard needs only these four and never launches a client. One wide interface would make
//     every consumer depend on the other half (and on whatever it grows later).
//   - Merging breaks **every existing test stub**: the wrapper's stub implements only Launch,
//     and on Runtime it must add four methods it never calls. A wide interface turns "not my
//     business" into "I must know". Measured 2026-11-17: on Runtime it went red at once.
//
// Both ports come from **the same component** (modules/runtime's service implements both),
// so "takeover and launch are one module" is unchanged - only consumers can now declare
// which half they need.
type Control interface {
	// ---- takeover ----
	//
	// These four are the thing underneath `start` / `on` / `off` / `status`, promoted to a
	// port on 2026-11-17 (see control.go's header). The return types are the ones the
	// commands print - "what one takeover changed" has exactly one shape.

	// TakeOver takes over one agent and records "the user wants it taken over".
	TakeOver(agent string, port int) Result
	// Release frees one agent and records "the user does not want it taken over".
	Release(agent string) Result
	// TakeOverAll takes over every agent not explicitly turned off by the user.
	TakeOverAll(port int) []Result
	// TakeoverStatus lists the takeover state of every known agent.
	TakeoverStatus() []Status

	// ---- lifecycle ----

	// Start brings up the proxy and takes over everything (`newgate start` semantics). Command
	// and wizard share one implementation: same token written first, same "no key, no takeover" gate.
	Start(opts StartOptions) (StartReport, error)
}

// Capability identifies the one client-runtime implementation.
var Capability = modules.NewCapability[Runtime]("runtime")

// ControlCapability identifies the one takeover/lifecycle implementation.
var ControlCapability = modules.NewCapability[Control]("runtime-control")
