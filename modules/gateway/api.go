package gateway

import (
	"time"

	"github.com/rzbdz/newgate-ext/modules/gateway/policy"
	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	modules "github.com/rzbdz/newgate/component"
)

// Request is the set of stable facts a special treatment needs to judge a request.
// It exposes no forwarder internals, so plugins cannot control retries, connections or
// response lifetimes. The contract lives in special (the plugin layer); this forwards names.
type Request = special.Request

// Plugin is the gateway's request-rewrite extension point. Match narrows the scope;
// Apply does byte-level surgery and returns recordable notes; errors are fail-open.
type Plugin = special.Plugin

// Type forwarding for the data plane's policy extension point (the body is in policy;
// the data plane reads four decision points from its own state machine).
type (
	Filter           = policy.Filter
	Admitter         = policy.Admitter
	Adjudicator      = policy.Adjudicator
	Controller       = policy.Controller
	Flusher          = policy.Flusher
	Env              = policy.Env
	EnvBinder        = policy.EnvBinder
	MetricNamer      = policy.MetricNamer
	Outcome          = policy.Outcome
	OutcomeKind      = policy.OutcomeKind
	Verdict          = policy.Verdict
	Evidence         = policy.Evidence
	ProbeObservation = policy.ProbeObservation
	ProbeAck         = policy.ProbeAck
)

// The kinds of outcomes. The data plane states facts, not judgments.
const (
	Succeeded        = policy.Succeeded
	ConnectionFailed = policy.ConnectionFailed
	RejectedStatus   = policy.RejectedStatus
	StreamCut        = policy.StreamCut
)

// Gateway is the gateway module's public control plane: **the port others plug into**.
//
// It exposes no handler, no internal registry, and no policy name - the point of this
// layer. The four insertion points map to data plane decisions (admission, adjudication,
// self-report, flush), see modules/gateway/policy's package comment.
type Gateway interface {
	// RegisterRequestHook inserts a request-rewrite plugin (special treatment).
	RegisterRequestHook(Plugin) (modules.Release, error)

	// RegisterFilter inserts a data plane policy contributor: admission (Admitter, at
	// most one), outcome adjudication (Adjudicator), control-plane self-report
	// (Controller), flush on stop (Flusher), implementing whichever are needed.
	//
	// With no contributors the data plane works as usual - the "minimal system", where
	// all four decision points use kernel defaults (no bookkeeping, no flush).
	RegisterFilter(Filter) (modules.Release, error)

	// ProbeBinding sends one minimal request to **one** binding and feeds the result
	// into the policy layer by the same path as `newgate probe`, returning an outcome.
	//
	// Why on the control-plane port: what a probe needs (provider base, key, dialect,
	// timeout thresholds) only this module has, and the "how to record it" path
	// (policy's ObserveProbes) is only reachable from the data plane side.
	//
	// The only difference from `newgate probe`: that command is **outside** the process
	// and POSTs its result back; this one feeds it directly. Both land on the same
	// function - which is why "web click" and "terminal command" never disagree.
	ProbeBinding(provider, model string) (ProbeOutcome, error)

	// FallbackOn reports whether the fallback chain is on; SetFallback toggles it.
	//
	// **Why on the control-plane port**: `newgate fallback on|off` is a command, and the
	// switch on the web page means **the same thing**. The switch lives in
	// `gatewaystate` (an internal impl package), and "who may change it" is this layer's
	// semantics - the UI must not import an impl package to poke it. Both paths end at
	// the same `gatewaystate.SetFallback`, so terminal and web never disagree.
	//
	// Off = **every request goes to the chain head only**: no switching, no timeout
	// budget. A debugging tool (switching hides upstream faults: a dead provider still
	// answers 200 via someone else), not a token saver; `--profile=xx` is unaffected.
	FallbackOn() bool
	SetFallback(on bool) error

	// Quirks is the "upstream quirks" table (see modules/gateway/quirk).
	//
	// It is exposed because **the criterion must be registered by the module owning the
	// patch**: the "this model always thinks" signature is GLM / DeepSeek / Kimi's own
	// dialect, and the patch lives in modules/thinking, so the signature is registered
	// there too - same rule as `special`'s st-<upstream>.go: no upstream strings in core.
	//
	// The data plane hands the same table to plugins on every request
	// (special.Request.Quirks), so registered criteria take effect on the hot path at once.
	Quirks() *quirk.Table
}

// ProbeOutcome is a single-point probe's human-readable outcome.
//
// It is **not** an alias of probe.Result: that one carries a full probe's account
// (profile / role / dialect capabilities / token counters), while a recheck only answers
// "does this work now, and how fast". One less forwarding layer to guess through.
type ProbeOutcome struct {
	OK      bool
	Status  int
	Latency time.Duration
	Err     string
	// Note is the policy layer's reply ("this one was rescued"). Empty = nothing to say.
	Note string
}

// Capability identifies the single gateway control plane in the process.
var Capability = modules.NewCapability[Gateway]("gateway")
