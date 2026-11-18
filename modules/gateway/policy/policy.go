// Package policy is the decision points the data plane **carves out for itself**.
//
// # Why it is needed
//
// For one forwarding flow there are four places that "must make a decision":
//
//	A chain build  may this binding enter the candidates now? in what position?
//	B outcome      walk the chain or not? does this shot count against this binding? what trail?
//	C control      how do I report my own state? how do I take probe conclusions?
//	D shutdown     whose ledger is still unflushed before we exit?
//
// Before 2026-09-18 modules/breaker answered all four, and gateway **called it directly**:
// `forward.Server` had a `Health breaker.Breaker` field, and the hot path read
// `breaker.Input{Kind: breaker.KindConnError}`, `res.Verdict.Bucket`, `case breaker.BucketShape:`.
// That is, **a policy's name** appeared in gateway's source -- it treated breaker as a lib
// rather than "a component that plugs things into me".
//
// This layer turns the direction of the capability around: gateway reads the four decision
// points out of its own state machine, leaves one registerable port for each, and whoever has
// an opinion registers. The criterion is mechanically checkable --
//
//	command grep -rn "breaker" modules/gateway/forward/ modules/gateway/module.go
//
// must be 0 lines (`modules/breaker/status` is a wire leaf, allowed to be imported directly
// by the note in docs/03-architecture.md §2, and does not count).
//
// # The kernel gives mechanism, not policy
//
// **With no contributors = the minimal system**, and all four ports have explicit kernel defaults:
//
//	A all candidates usable, no reordering (consistent with resolve.Opts' nil semantics)
//	B stop = the response has started || no next stop on the chain; no ledger, no trail
//	C one section less on the control plane; probe conclusions taken, nothing to say
//	D nothing to do
//
// One boundary is worth writing into stone: **policy can only stop, never force an advance**.
// The whole old classification table was checked: apart from "other 4xx" (retryable but
// stopped), swapping always equals "the response has not been written + there is a next stop",
// regardless of which ledger is kept -- so "stop" is the only direction of interference, and
// the reverse cannot occur. A zero Verdict means "no opinion" and so lands on the kernel
// default; that is consistent with the repo's fail-open hard requirement (see CLAUDE.md §4):
// an erroring/unclaimed contributor must never break the whole chain.
package policy

import (
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"time"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Filter is a contributor's identity.
//
// Isomorphic to special.Plugin's Name()/Why(): the name goes into logs and dedup, and Why
// answers "why does this exist". Without it, a contributor going wrong leaves only "something
// returned Stop".
type Filter interface {
	Name() string
	Why() string
}

// OutcomeKind is "what actually happened on this attempt" -- **facts only, no judgment**.
//
// The data plane says "the connection failed", "the upstream returned 500", "the response has
// started writing", without explaining what that means; the explanation is in the contributor's
// Judge. This boundary matches the old modules/breaker's Kind, only the names became neutral:
// no policy's vocabulary should appear in the kernel.
type OutcomeKind string

const (
	// Succeeded got a 2xx response header.
	Succeeded OutcomeKind = "succeeded"
	// ConnectionFailed failed at the connect stage (dial/DNS/TLS/reset/EOF, or a first-byte timeout).
	// A client cancelling itself is **not here**: the data plane does not hand it to policy (see
	// the note on Outcome).
	ConnectionFailed OutcomeKind = "connection_failed"
	// RejectedStatus the upstream returned a status code >= 400.
	RejectedStatus OutcomeKind = "rejected_status"
	// StreamCut the response already started writing to the client and the upstream broke mid-way.
	StreamCut OutcomeKind = "stream_cut"
)

// Outcome is all the facts the kernel hands to policy. **No hidden input**: the same Outcome
// always yields the same Verdict, so every policy can be exhaustively tested from one table.
type Outcome struct {
	Provider string
	Model    string
	// Binding is the display form of provider/model, for the log only.
	Binding string
	Kind    OutcomeKind
	// Status the upstream status code, present only for RejectedStatus.
	Status int
	// Body the upstream's raw error text (truncated). Present only for RejectedStatus -- the
	// data plane read it anyway for that evidence; the reason changed from "feed breaker" to
	// "feed policy".
	Body []byte
	// ResponseStarted has a response header already gone to the client? No swapping once writing
	// has begun: the client already holds half a response, and another writer would only
	// append garbage.
	ResponseStarted bool
	// IsLast no next stop on the chain (the last candidate, or the total budget is used up).
	IsLast bool
	// FallbackOn400 is the chain.fallback_on_400 switch.
	FallbackOn400 bool
	// RequestBytes the body byte count we sent; latency samples pick the context bucket by it.
	RequestBytes int
	// TTFT time from sending the request to getting the response header.
	TTFT time.Duration
}

// Verdict is policy's answer.
//
// The kernel carries, it does not interpret: Metrics is a counter name, Note is a sentence,
// Evidence.Tag is a log tag and evidence directory prefix -- all **opaque strings**. So no
// upstream's or policy's private vocabulary appears in gateway (the literal `[shape-400]` is
// given by the contributor, see Evidence).
type Verdict struct {
	// Stop do not swap on this shot.
	//
	// The zero value (false) = "no opinion", so it lands on the kernel default: as long as the
	// response has not started writing and the chain has a next stop, keep walking. The
	// fail-open direction (CLAUDE.md §4).
	Stop bool
	// Attribute does this shot count against this binding.
	//
	// It drives the failures count in `newgate status` (today only "the upstream's account" is
	// counted: shape 400s and 4xx for "the request itself is wrong" are not). It is the kernel's
	// question ("whose account"), not the policy's bucket name -- so the kernel carries it as a bool.
	Attribute bool
	// Metrics the counter names to +1; the kernel Incs them as-is.
	Metrics []string
	// Note a log suffix, printed as-is at the end of that line (the contributor supplies the
	// leading space).
	Note string
	// Evidence non-nil = this shot needs evidence (the dedicated log and archive for shape errors).
	Evidence *Evidence
}

// Verdict composition rules (with several contributors).
//
// Written here rather than left to callers because "whose verdict wins" always drifts once
// each side writes it:
//
//	Stop / Attribute  OR       -- any one saying stop stops, saying charge charges
//	Metrics           union    -- deduped, then lexicographic (stable output)
//	Note              concat   -- in contributor registration order
//	Evidence          first non-nil (registration order)
//
// Note the OR direction matches "the kernel default": verdicts only stack toward the more
// conservative side.
func merge(a, b Verdict) Verdict {
	a.Stop = a.Stop || b.Stop
	a.Attribute = a.Attribute || b.Attribute
	a.Metrics = append(a.Metrics, b.Metrics...)
	if b.Note != "" {
		a.Note += b.Note
	}
	if a.Evidence == nil {
		a.Evidence = b.Evidence
	}
	return a
}

// Evidence is the evidence metadata of an outcome that "needs a trail".
//
// Why the whole block comes from the contributor: both words in `[shape-400]` and
// `dump/shape-400-deepseek/` are **upstream dialect** (whose request-shape validation, what the
// criterion is called), and the forwarding path must not contain upstream-specific strings per
// docs/05-gateway.md. The contributor gives the tag; the kernel only logs, persists, and
// stays ignorant.
type Evidence struct {
	// Tag the bracketed tag in the log and the evidence directory prefix, e.g. "shape-400".
	Tag string
	// Subject who claimed it, into the log and the directory name, e.g. "deepseek".
	Subject string
	// Archive also store a scene that takes no part in rolling cleanup.
	//
	// Why this bit exists: these 400s are rare and fatal, the dump directory's rolling cleanup
	// would squeeze them out, so the scene needs its own archive.
	Archive bool
}

// --- Optional interfaces: implement per stage, skip what is missing -------------------------
//
// Reuses the repo's existing "required identity + optional stages" shape (the gateway/special
// one): a contributor implements only the stages it cares about, and the kernel finds them by
// type assertion.

// Admitter is the contributor for phase A (chain building).
//
// **Only one is allowed** (registering a second fails right there). The reason is not
// conservatism: Admit is not a query. When the cooldown expires, the health table issues a
// "half-open trial slot", one slot lets exactly one real request through, and resolve asks
// once per candidate and asks again on the path that overrides the chain - today
// "exactly once" rests on the slot's own TTL fallback (see state.go in modules/breaker).
// Multiple Admitters ANDed together would make "is a slot issued" depend on **another
// unrelated contributor**'s return value: the first issues the slot, the second denies, and
// that binding can neither enter the chain nor is anyone trying it, for the whole TTL.
//
// So this seat is for "who answers admission", not "several parties answering together".
// If several sources really are wanted, they compose one Admitter themselves and register it.
type Admitter interface {
	// Admit says whether this binding can enter the candidates right now. On false, reason
	// is the user-facing skip reason (`newgate tier` prints it verbatim).
	Admit(provider, model string) (ok bool, reason string)
	// Rank is the sort key, smaller first. It only reorders the fallback after the chain head.
	Rank(provider, model string, contextBytes int) int
}

// Adjudicator is the contributor for phase B (outcome).
//
// There may be several; verdicts are composed by the merge rules (see Verdict).
type Adjudicator interface {
	// Judge passes verdict on this one outcome. **It must be pure**: the same Outcome always
	// yields the same Verdict, no locking, no state written. Accounting lives in Observe.
	Judge(Outcome) Verdict
	// Observe is reached only on the successful shot: it records one real observation (a
	// latency sample).
	Observe(Outcome)
}

// Controller is the contributor for phase C (control plane).
type Controller interface {
	// Doc is the field contributed to `/__newgate/status`; the key is how it looks at the
	// document's top level.
	//
	// gateway merges it into the top level of the status document - **deliberately not
	// tucked into an extra object**: older CLIs (new and old binaries mix during a graceful
	// takeover) read the top-level `breakers`, and moving it would show them an empty table
	// during the upgrade window. A key collision fails at **registration time** (the same
	// handling as `confighook.RegisterStateField` claiming the same field), never a silent
	// overwrite.
	//
	// Contract: cheap and side-effect free (asked once at registration, then once per status
	// request).
	Doc() map[string]json.RawMessage

	// ObserveProbes takes in the active conclusions of probes.
	ObserveProbes(obs []ProbeObservation) []ProbeAck
}

// ProbeObservation is one probe conclusion (`newgate probe` sends it back via the control
// endpoint).
type ProbeObservation struct {
	Provider     string
	Model        string
	Status       int
	Latency      time.Duration
	ContextBytes int
	Error        string
	// SlowAfter is the classifier's first-byte threshold: a probe is a tiny 4-token request,
	// and if it is still unfinished past this threshold it does not qualify for the
	// interactive fallback chain, even if it eventually returns 200.
	SlowAfter time.Duration
}

// ProbeAck is a contributor's reply to one probe conclusion (used only for logging and
// counting).
type ProbeAck struct {
	// Opened: this conclusion opened the gate.
	Opened bool
	// Note is one line of plain language to print; empty = nothing to say.
	Note string
}

// Flusher is the contributor for phase D (shutdown): write out not-yet-persisted state
// synchronously.
type Flusher interface {
	Flush() error
}

// Env is the capability the data plane lends contributors **at runtime**.
//
// It is not "configuration" but facts alive right now: the log outlet, and sending one
// minimal probe against the current config snapshot. It exists for "diagnose before
// tripping" - when a binding fails consecutively up to the threshold, probe it once first,
// and if it works, do not delist it (see SetVerifier in modules/breaker).
//
// Why gateway hands it out instead of breaker sending it itself: a probe must read the
// current config snapshot (the provider's base, key, dialect) and reuse the data plane's
// probe implementation. The capability belongs to gateway and is lent to contributors.
type Env interface {
	Logf(format string, args ...any)
	// Probe sends one minimal probe. **fail-closed**: no provider config, empty key, any
	// error - always false. Diagnosis must not be a get-out-of-jail card for a bad binding;
	// it should only stop a misjudgment.
	Probe(provider, model string) bool
}

// EnvBinder is an optional phase: contributors that want runtime capabilities implement it.
type EnvBinder interface {
	BindEnv(Env)
}

// MetricNamer is an optional phase: declare a group and description for your own counters.
//
// Why not put it in Controller: naming on the observability side and fields in the status
// document are two different things, and one day a contributor may produce counters but no
// status section.
type MetricNamer interface {
	// MetricGroup says which group this counter belongs to: the identity (a stable ASCII id,
	// used for sorting/dedup) and the rendering (the human-facing anchor); two empty strings
	// if unknown. Why identity and rendering are split: see modules/gateway/metrics.Group -
	// sorting must not depend on language.
	MetricGroup(key string) (id, label string)
	// MetricHint is the plain-language description of this counter; empty if unknown.
	MetricHint(key string) string
}

// --- Ledger --------------------------------------------------------------

// Registry is the contributor ledger.
//
// It runs in registration order (the **relative order** of filters is decided by
// registration order, same as `component.Registry[T]`): phase A allows only one, and the
// composition rules of phases B/C/D do not care about order (OR / union); only the order in
// which Notes are concatenated depends on it - and Note order is for humans, so stable is
// enough.
type Registry struct {
	mu      sync.RWMutex
	filters []Filter
	tokens  map[string]uint64
	next    uint64
}

// New builds an empty ledger. An empty ledger = the minimal system: all four ports use
// kernel defaults.
func New() *Registry { return &Registry{tokens: map[string]uint64{}} }

// The process-level default ledger. **The shape is copied from InstallDefault/SetDefault in
// gateway/special** (see the notes there): the assembled gateway installs its own ledger as
// the default in Start, and "whoever starts the data plane" can pick it up.
//
// Why this layer is needed: the data plane is started in two places - the daemon's main loop
// (serve.go, which takes port.filters inside the package) and system-level tests
// (testing/system starts a real data plane from the real component graph). If the latter
// New()'ed its own, it would bypass every contributor's registration and turn regressions
// like "is the wiring right" into a no-op (that is exactly how the 2026-09-17 shape
// criterion test went hollow).
//
// It is **not** a back door for business code: the only write entry is
// RegisterFilter/Register, and the only read side is the data plane. A production process
// assembles one gateway, so "which one" is unambiguous; in tests that start several graphs
// in one process, the restore returned by InstallDefault keeps them isolated.
var (
	defaultMu       sync.RWMutex
	defaultRegistry = New()
)

// Default is the ledger currently assembled (an empty ledger when no gateway was assembled).
func Default() *Registry {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultRegistry
}

// SetDefault sets the default ledger directly. On the assembly path, prefer InstallDefault.
func SetDefault(r *Registry) {
	if r == nil {
		panic("policy: nil default registry")
	}
	defaultMu.Lock()
	defaultRegistry = r
	defaultMu.Unlock()
}

// InstallDefault installs an owned default ledger and returns a restore function. The
// restore checks the current instance, so a component that started earlier cannot displace a
// later one's ledger in its own Stop.
func InstallDefault(r *Registry) func() {
	if r == nil {
		panic("policy: nil default registry")
	}
	defaultMu.Lock()
	previous := defaultRegistry
	defaultRegistry = r
	defaultMu.Unlock()
	return func() {
		defaultMu.Lock()
		if defaultRegistry == r {
			defaultRegistry = previous
		}
		defaultMu.Unlock()
	}
}

// Register attaches one contributor.
//
// Duplicate checks run under the write lock: a name collision, or "a second Admitter", fails
// right there. Check and insert are atomic against concurrent registration -
// first-come-first-served is the worst behavior this feature could have (then "who took
// this name" is invisible in the listing).
func (r *Registry) Register(f Filter) (modules.Release, error) {
	if f == nil || f.Name() == "" {
		return nil, i18n.E("policy: contributor must have a name", nil)
	}
	if err := r.validate(f); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.filters {
		if existing.Name() == f.Name() {
			return nil, i18n.E("policy: contributor {name} is already registered",
				i18n.A{"name": strconv.Quote(f.Name())})
		}
	}
	if _, is := f.(Admitter); is {
		for _, existing := range r.filters {
			if _, taken := existing.(Admitter); taken {
				return nil, i18n.E(
					"policy: only one contributor may be an admitter; {name} is already taken by {holder} (it carries side effects, see the Admitter docs)",
					i18n.A{"name": strconv.Quote(f.Name()), "holder": strconv.Quote(existing.Name())})
			}
		}
	}
	r.next++
	token := r.next
	r.tokens[f.Name()] = token
	r.filters = append(r.filters, f)
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.tokens[f.Name()] != token {
			return nil // stale Release: this filter was already withdrawn; Stop must be idempotent
		}
		delete(r.tokens, f.Name())
		for i, existing := range r.filters {
			if existing.Name() == f.Name() {
				r.filters = append(r.filters[:i], r.filters[i+1:]...)
				break
			}
		}
		return nil
	}, nil
}

// validate does the "obvious at a glance" checks before the write lock is taken: status
// document key collisions.
//
// It sits outside the lock because it calls the contributor's Doc(), which may lock; running
// someone else's code under our write lock is asking for lock-order trouble.
func (r *Registry) validate(f Filter) error {
	controller, ok := f.(Controller)
	if !ok {
		return nil
	}
	mine := controller.Doc()
	if len(mine) == 0 {
		return nil
	}
	type other struct {
		name string
		keys []string
	}
	r.mu.RLock()
	var others []other
	for _, existing := range r.filters {
		c, is := existing.(Controller)
		if !is {
			continue
		}
		var keys []string
		for key := range c.Doc() {
			keys = append(keys, key)
		}
		others = append(others, other{name: existing.Name(), keys: keys})
	}
	r.mu.RUnlock()
	for _, o := range others {
		for _, key := range o.keys {
			if _, clash := mine[key]; clash {
				return i18n.E("policy: state document field {key} is already taken by {owner}",
					i18n.A{"key": strconv.Quote(key), "owner": strconv.Quote(o.name)})
			}
		}
	}
	return nil
}

// Filters returns the current contributors (a copy, in registration order).
func (r *Registry) Filters() []Filter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Filter(nil), r.filters...)
}

// Empty means no contributors at all -- the "minimal system".
func (r *Registry) Empty() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.filters) == 0
}

// --- Phase A: admission and ranking --------------------------------------

// Admit asks for admission.
//
// **Every candidate must be asked exactly once** (that is how resolve calls it), because the
// health table has a side effect on this path (issuing half-open trial slots). So this
// **does not short-circuit**: even with multiple sources one day, all are asked and then
// composed - short-circuiting would skip a later contributor and drop its slot.
func (r *Registry) Admit(provider, model string) (bool, string) {
	r.mu.RLock()
	var admitter Admitter
	for _, f := range r.filters {
		if a, ok := f.(Admitter); ok {
			admitter = a
			break
		}
	}
	r.mu.RUnlock()
	if admitter == nil {
		return true, ""
	}
	return admitter.Admit(provider, model)
}

// Rank asks for the sort key; with no contributor it gives the neutral value (equivalent to
// "no reordering").
func (r *Registry) Rank(provider, model string, contextBytes int) int {
	r.mu.RLock()
	var admitter Admitter
	for _, f := range r.filters {
		if a, ok := f.(Admitter); ok {
			admitter = a
			break
		}
	}
	r.mu.RUnlock()
	if admitter == nil {
		return NeutralRank
	}
	return admitter.Rank(provider, model, contextBytes)
}

// NeutralRank is the sort key for "no opinion", the same tier as the neutral value in resolve.
const NeutralRank = 1_000_000

// --- Phase B: outcome verdicts -------------------------------------------

// Judge collects all verdicts and composes them.
//
// A panicking contributor is treated as "no opinion" (fail-open): one bad plugin must never
// take the whole chain's judgment away - the same rule as special.Respond.
func (r *Registry) Judge(o Outcome) Verdict {
	var out Verdict
	for _, f := range r.Filters() {
		adj, ok := f.(Adjudicator)
		if !ok {
			continue
		}
		out = merge(out, judgeSafely(adj, o))
	}
	sort.Strings(out.Metrics)
	out.Metrics = dedupe(out.Metrics)
	return out
}

func judgeSafely(adj Adjudicator, o Outcome) (v Verdict) {
	defer func() {
		if recover() != nil {
			v = Verdict{}
		}
	}()
	return adj.Judge(o)
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// Observe hands the successful observation to every adjudicator.
func (r *Registry) Observe(o Outcome) {
	for _, f := range r.Filters() {
		if adj, ok := f.(Adjudicator); ok {
			safely(func() { adj.Observe(o) })
		}
	}
}

func safely(fn func()) {
	defer func() { _ = recover() }()
	fn()
}

// --- Phase C: control plane ----------------------------------------------

// Doc merges the status fields of every contributor.
//
// Key collisions were already rejected at registration, so the merge here is plain
// concatenation.
func (r *Registry) Doc() map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, f := range r.Filters() {
		controller, ok := f.(Controller)
		if !ok {
			continue
		}
		for key, value := range controller.Doc() {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ObserveProbes fans probe conclusions out to every control-plane contributor.
func (r *Registry) ObserveProbes(obs []ProbeObservation) []ProbeAck {
	var acks []ProbeAck
	for _, f := range r.Filters() {
		controller, ok := f.(Controller)
		if !ok {
			continue
		}
		safely(func() { acks = append(acks, controller.ObserveProbes(obs)...) })
	}
	return acks
}

// MetricGroup / MetricHint ask for observability naming; empty strings when nobody knows
// the counter, and the caller falls back.
func (r *Registry) MetricGroup(key string) (id, label string) {
	for _, f := range r.Filters() {
		if namer, ok := f.(MetricNamer); ok {
			if id, label := namer.MetricGroup(key); id != "" {
				return id, label
			}
		}
	}
	return "", ""
}

func (r *Registry) MetricHint(key string) string {
	for _, f := range r.Filters() {
		if namer, ok := f.(MetricNamer); ok {
			if hint := namer.MetricHint(key); hint != "" {
				return hint
			}
		}
	}
	return ""
}

// --- Phase D: shutdown ---------------------------------------------------

// Flush makes every contributor with a ledger persist. Errors are reported ("never silent")
// but do not block shutdown.
func (r *Registry) Flush(logf func(string, ...any)) {
	for _, f := range r.Filters() {
		flusher, ok := f.(Flusher)
		if !ok {
			continue
		}
		if err := flusher.Flush(); err != nil && logf != nil {
			logf("[policy] %s", i18n.T("flush failed for {name}: {err}", i18n.A{"name": f.Name(), "err": err}))
		}
	}
}

// --- Runtime capabilities ------------------------------------------------

// BindEnv hands runtime capabilities to the contributors that want them.
//
// The moment is **when the data plane itself starts** (Serve time), not assembly time: the
// two things in Env (the log outlet, probing against the current config snapshot) exist only
// once the service is really up. See the three-phase rule in docs/02-component-framework.md.
func (r *Registry) BindEnv(env Env) {
	for _, f := range r.Filters() {
		if binder, ok := f.(EnvBinder); ok {
			safely(func() { binder.BindEnv(env) })
		}
	}
}
