// Package controlpath holds path constants for the gateway control-plane endpoints.
//
// Why a separate leaf package (2026-09-18): these endpoints are **registered by gateway's HTTP
// server** (the mux in modules/gateway/forward), while their callers are in the CLI - `newgate
// status/metrics` ask the running daemon over HTTP. Both sides writing the string literals
// means "only one side changed the path" is a silent failure: the server moved, the client
// still hits the old one, and the symptom is "the feature vanished", not a compile error.
//
// It must be a **leaf**: it cannot live in gateway's root package. The
// reason is **import weight** - cli, config, breaker, runtime and gateway's
// own controlplane all need these path constants, and putting them in the
// root package drags the whole gateway implementation into every referrer.
//
// (2026-09-18, this reason corrected twice: first "gateway Need(cli), so cli importing
// gateway back would make a cycle" - that cycle vanished once cli's out-edges were
// cut; later gateway used the unordered Inject edge to ui. Now that edge is back to
// Optional(cli) (a weak dependency, see component.Optional), still no cycle: the one
// direction is gateway → ui, cli has no out-edges, and at the Go level gateway imports
// the cli/extension leaf, not modules/cli. The conclusion never changed, only the reason.)
//
// One rule already has a precedent: **config sharing** (a distribution feature, see
// the note in §1) defines `/__newgate/config` `/__newgate/secrets` in its own leaf.
//
// Relation to those two: the same `/__newgate/` namespace, but **not the same
// server** - config sharing starts its own endpoints, these five are started by
// the data plane. So the constants stay with their owners and are not merged.
package controlpath

const (
	// Status is the daemon state document: pid, port, request counts, breaker table, reasoning
	// cache counters. `newgate status` and every "read daemon memory" command use it.
	Status = "/__newgate/status"
	// Metrics is the counter snapshot (in memory, reset on restart).
	Metrics = "/__newgate/metrics"
	// Health is the port where probe results come back: the CLI runs a probe
	// in its own process and hands the conclusion to the daemon to record.
	Health = "/__newgate/health"
	// Stop is the cross-user stop port. It carries a Bearer token, and
	// is the only way left to stop a process that is "not running".
	Stop = "/__newgate/stop"
	// Upgrade is graceful handoff (socket fd transfer). Old daemons did not register it, so the
	// caller must probe the capability before acting - a blind send is taken as an unknown path.
	Upgrade = "/__newgate/upgrade"
)
