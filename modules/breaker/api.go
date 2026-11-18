// Package breaker owns every judgment about "can this binding be used right now".
//
// It was promoted from a gateway-internal package to a top-level module (2026-09-17)
// because more places read it than write it: the data plane (gateway/forward) asks during
// chain building, the control endpoint reports probe conclusions, and the CLI renders
// it as `newgate breaker` / `newgate metrics` / `newgate tier`. While it lived in gateway,
// those consumers could only reach it through the process-level singleton `health.Default`
// -- a global mutable variable that polluted tests and could not be swapped at
// assembly. Now it is injected via a capability, and whoever needs it declares it.
//
// This package **depends on no business module**: it only knows the binding key (provider/model),
// status codes, and latency. Upstream-specific knowledge (what response body counts as "bad
// request shape") is contributed by upstream modules via RegisterShapeDetector; see detectors.go.
package breaker

import (
	"time"

	"github.com/rzbdz/newgate-ext/modules/breaker/status"
	modules "github.com/rzbdz/newgate/component"
)

// The facts of ProbeGrade / Status are defined in modules/breaker/status (the wire-contract leaf).
// Aliases are forwarded here, so code in this package and old callers keep using the short names.
type (
	ProbeGrade = status.ProbeGrade
	Status     = status.Status
)

const (
	ProbeFluent      = status.ProbeFluent
	ProbeUsable      = status.ProbeUsable
	ProbeLaggy       = status.ProbeLaggy
	ProbeUnavailable = status.ProbeUnavailable
)

// Breaker is the health-table port. The data plane only reads the first two items
// and only writes Report/ObserveSuccess; the render layer only reads Snapshot.
//
// # Why this interface is "many readers, few writers" (narrowed 2026-09-21)
//
// It once hung three setters: `SetPolicy` / `SetErrorHandler` / `SetVerifier`. All
// three were **ownerless write doors** -- any module holding the health-table capability
// could call them, while **not one** call site in the whole repo was on the interface (they
// were all in BindEnv below and in tests, acting on a concrete *table). In other words: the
// interface was wider than every real use, and the extra part was a door anyone could push.
//
// The extra cost was not hypothetical. `SetVerifier` is the switch for the safety
// diagnostic added before tripping, and one `SetVerifier(nil)` **silently**
// removes it -- no error, no edge on the dependency graph, and code review cannot
// see that anyone touched it. And `SetPolicy` is worse: it has **zero callers**,
// an API reserved for a feature not yet built (putting policies into state.json).
//
// The test is "one capability should have one door,
// and the door belongs to the module that owns it":
//   - the data plane's runtime capability lent to policy ->
//     `policy.EnvBinder.BindEnv` (that seam is owned and directed, with clear timing),
//     and the health table hooks its log outlet and probe back in inside BindEnv;
//   - policy itself -> today always DefaultPolicy; when it needs to be configurable later,
//     open an **owned** door then, rather than leaving a setter anyone can change now.
//
// Those three methods are still there (all on *table); they are just no longer exposed on the port.
type Breaker interface {
	// Available answers whether this binding can enter the
	// candidate chain right now. Called during chain building.
	//
	// It has a side effect: when the cooldown has expired it moves the binding
	// to half-open and issues this round's trial slot. During chain building each
	// candidate is asked once, so "let one real request through" holds naturally here.
	Available(provider, model string) bool
	// Rank is the sort key during chain building: predicted time to first byte, smaller first.
	Rank(provider, model string, contextBytes int) int

	// Report reports the outcome of one upstream interaction and returns the verdict
	// (whether to keep going along the chain). Classification and state transitions are
	// in the implementation; the data plane just describes what happened truthfully.
	Report(provider, model string, in Input) Result
	// ObserveSuccess records only a latency sample, not
	// availability (called after the first byte has arrived).
	ObserveSuccess(provider, model string, contextBytes int, ttft time.Duration)
	// RecordProbe records the conclusion of one active probe
	// and returns the grade and whether it is still tripped.
	RecordProbe(provider, model string, status, contextBytes int,
		latency, slowAfter time.Duration, probeErr string) (ProbeGrade, bool)

	// Snapshot freezes a serializable view of the current state.
	Snapshot() []Status

	// Flush writes out synchronously the latency samples not yet
	// persisted in the throttling window (for graceful exit).
	Flush()
	// UseFile lets the health table survive a graceful restart.
	//
	// It is **not** in the same class as the three setters above, so it stays on the port:
	// it opens "where state is stored", not "lend you a capability" -- without it the
	// health table just does not persist, behavior is predictable, and nobody else will
	// come change it. The narrowing cut only chopped ownerless **capability** injection.
	UseFile(path string) error

	// RegisterShapeDetector contributes one test for "this 4xx is a request-shape problem".
	//
	// The definition of a shape error **belongs to the upstream itself** (see ShapeDetector
	// in shape.go): core only offers the port and the policy "never delist, only count",
	// and the test is registered by the module that knows that provider's validation
	// rules. With no detector registered, 400 is never charged to anyone -- missing one is
	// just one count less, while a false positive lets a real availability failure through.
	//
	// It returns a release handle belonging to this registration
	// only; consumers release in Start/Stop reverse order.
	RegisterShapeDetector(ShapeDetector) (modules.Release, error)
}

// Capability is the health table's identity in the component graph.
var Capability = modules.NewCapability[Breaker]("breaker")

// NewTable builds a purely in-memory health table (no
// persistence). Tests and module assembly both use it.
func NewTable() Breaker { return newTable() }
