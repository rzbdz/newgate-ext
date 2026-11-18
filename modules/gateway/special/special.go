// Package special is the special_treatment layer: a set of pluggable "special treatment"
// plugins.
//
// # Why this layer is needed
//
// The ideal of the semantic naming layer is "forwarding a request should be forwarding"
// (docs/01-product.md). But in reality every upstream has its own quirks: the same
// Anthropic-format request is accepted by official Claude and answered 400 by some DeepSeek
// gateway. These problems share three traits:
//
//  1. they target **one upstream only** and must not become global behavior;
//  2. the fix adds a little to the request rather than changing semantics;
//  3. the day the upstream fixes it, this code should be able to come out cleanly.
//
// So they should not be scattered as ifs on the forwarding hot path, but be a set of
// registered plugins: each explains "which upstream I claim" (Match), "why I exist" (Why) and
// "what I changed" (the notes Apply returns). The hot path only asks them in order.
//
// Relation to schema repair: tool schema repair (rewrite/schema) is a repair **every** strict
// validator needs and is semantically a no-op per the JSON Schema spec, so it is a separate
// layer that applies to all upstreams by default. What lives here are patches "only one
// particular upstream needs".
//
// Three hard rules
//
//	fail-open: a plugin error counts as it never ran, and the request goes out as it was.
//	           Better an upstream error than a patch breaking the whole chain
//	           (docs/05-gateway.md).
//	not silent: whatever was changed must be reported as notes, for the caller to log. The
//	           user can always know which part of their request was touched.
//	pure bytes: always use the rewrite package's byte surgery; never a whole-body JSON
//	           round trip.
package special

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
	"github.com/rzbdz/newgate-ext/modules/gateway/rewrite"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Request is the context of this forward that a plugin can see.
//
// It deliberately has no http.Request: a plugin should see only the request's **semantic
// identity** (who it is sent to, which model), and can neither see nor change headers,
// authentication or the connection. Patches that want to touch those do not belong in this
// layer.
//
// **State does not violate that rule**; the two are different things: http.Request is **this
// connection** (headers, auth, retries, response lifecycle - not the plugin's to touch);
// State is **this config snapshot** (which switch points were turned off, what the timeout
// is). Respond and Route already received it separately, and only Apply and RebaseToolLoop
// did not, so a plugin could not consult its own switch points. Adding it smooths out that
// inconsistency. The convention is the same as above: **read-only**; changing it is not
// propagated back to the hot path, only making later plugins in the same request see an
// inconsistent world.
//
// The chain has one source of truth: the body bytes. r.Model is its **view** after each step -
// when a plugin rewrites the body's model field, the Apply framework syncs it automatically
// (see syncContextModel), and later plugins see the new value at once. A plugin need not and
// should not maintain it by hand: forgetting is the distortion "the upstream switched model
// while the downstream decides on the old one" (captured once on 2026-09-09). The other
// fields (Provider/Tier/Stream/Agent…) come from routing and the request shape, not from the
// body, and do not change along the chain.
//
// The definition lives here rather than at the gateway root because this contract belongs to
// the **plugin layer**: special's implementation uses it, and plugin authors use only it. The
// root api.go aliases it in the other direction, so the gateway root can import special while
// special need not import the root back (which would be a cycle).
type Request struct {
	InModel  string
	Tier     string
	Model    string
	Provider string
	BaseURL  string
	Protocol string
	Path     string
	Stream   bool
	Agent    string

	// State is the config snapshot this request sees; plugins consult their own runtime switch
	// points on it (Off/On in modules/pluginmanager). Like the other fields of this request it is
	// **read-only**, and it **may be nil** (common in tests and old call surfaces) - the switch
	// lookup helpers all treat "nil = not off", so a plugin need not check for nil.
	State *domain.State

	// Quirks is **the gateway-held "upstream quirks" table** (see modules/gateway/quirk).
	//
	// Why it is carried on Request instead of letting plugins import that package and call a
	// package-level function (changed 2026-09-18): a package-level map is a service locator -
	// thinking's st-always calling quirk.Has directly would depend on state the gateway data
	// plane wrote in, and that dependency is invisible in Requires, in the dependency graph and
	// in the ratchet tests (docs/03-architecture.md §5 says outright no new service locators).
	// Now the table is owned by gateway and handed out through its own request contract, so a
	// plugin gets the table **this same request** sees - the same pattern as State.
	//
	// nil is allowed (tests and old call surfaces): the lookup helpers treat "absent = not
	// learned".
	Quirks *quirk.Table
}

// Plugin is the typed capability a module can hang on the request-handling chain.
type Plugin interface {
	Name() string
	Why() string
	Match(*Request) bool
	Apply(body []byte, request *Request) (out []byte, notes []string, err error)
}

// ToolLoopMigrator is an optional routing constraint: some upstreams cannot take over
// another's unclosed reasoning/tool state as is, but can do an explicit lossy rebuild after
// all safe candidates fail. It belongs to a specific upstream's quirk and should not become a
// global fallback rule.
type ToolLoopMigrator interface {
	NeedsToolLoopRebase(originProvider, originModel string, candidate *Request) (bool, string)
	RebaseToolLoop(body []byte, candidate *Request) ([]byte, string, error)
}

// Responder is a plugin's short-circuit point before chain building / forwarding: **answer
// for the upstream** without sending a single byte. Returning (result, true) means this
// request is settled by the plugin; false goes through normal forwarding.
//
// The difference from Apply: Apply changes the request, Responder creates the response - the
// former makes the request "become" another request, the latter makes it "not need sending".
// A short-circuiter is responsible for the response's **complete HTTP/protocol semantics**
// (for an anthropic Messages request it must answer a valid messages response); the caller
// only writes headers and logs.
//
// Nothing upstream-proprietary is hardcoded on the endpoint side: whether "this is the
// classifier" can be recognized is the plugin's business (see
// modules/claudecode/classifier-naked.go). fail-open: a plugin panic counts as it never ran,
// and normal forwarding proceeds.
type Responder interface {
	Respond(body []byte, request *Request, state *domain.State) (result []byte, ok bool)
}

// NoteProvider is a short-circuit plugin's optional partner: write one human-readable line
// for every shot it answers on the upstream's behalf. The hot path writes it verbatim into the
// log line, so **who short-circuited and why** is always visible in the log (where "not
// silent" lands on the short-circuit path).
//
// Why the caller does not compose this sentence: only the plugin knows what this verdict
// means. The hot path guessing (say an if on plugin names) would move plugin-private knowledge
// back into core, which this repo explicitly forbids.
type NoteProvider interface {
	RespondNote(state *domain.State) string
}

// ModelLister is a plugin's answer to `GET …/v1/models`: **under this client and this URL**,
// which models can be named. Returning (ids, true) means it settles it; false = hand back the
// kernel's default list (tiers + dynamic role keys registered by modules).
//
// Why it exists (hermes takeover, 2026-11-04): the default list is client-independent, and
// some clients **replace** their own configured model table with the list they get back -
// hermes's `/model` asks `<base>/models` for every named provider and uses whatever comes
// back. Under a URL pinned to one profile (`/a/<agent>/p/<profile>/v1`) what can be named is
// not only tiers but every concrete model that profile binds; the default list lacks them and
// instead holds omo's keys, which mean nothing to this client. "Which ones should this client
// see" is knowledge of the client module, so it answers and the kernel only asks.
//
// fail-open, same as Responder: if the plugin panics, or was turned off by `newgate st off`,
// it counts as not answering and the default list is returned.
type ModelLister interface {
	ListModels(agent, profile string) (ids []string, ok bool)
}

// RoutePlugin is the special layer's extension point before chain building. A plugin only
// returns a routing intent; validating bindings and constructing the fallback chain remain
// resolve's job.
type RoutePlugin interface {
	Route(body []byte, request *Request, state *domain.State) (RouteDecision, bool)
}

// RouteDecision is the declarative intent a routing plugin hands back to the main router.
// A plugin may suggest the chain head, tier and first-byte timeout, but cannot construct or
// execute a fallback itself.
type RouteDecision struct {
	Plugin           string
	Tier             string
	Head             *domain.Binding
	FirstByteTimeout time.Duration
	Note             string
	OverrideNote     string
	OverrideFailNote string
	Metric           string
}

// MetricKey normalizes plugin-private metrics into the special namespace; if any part is
// missing, no metric is emitted.
func (d RouteDecision) MetricKey() string {
	if d.Plugin == "" || d.Metric == "" {
		return ""
	}
	return "special." + d.Plugin + "." + d.Metric
}

// StatusItem is the structured information a plugin contributes to `newgate status`. The CLI
// only lays it out and knows nothing of concrete mechanisms like classifier or DeepSeek.
type StatusItem struct {
	Label string
	Value string
}

// StatusProvider lets plugins contribute status items without making the CLI know concrete
// plugin types.
type StatusProvider interface {
	Status(state *domain.State) []StatusItem
}

// BindingProvider lets a routing plugin declare bindings it may introduce that are not in the
// profile. metrics uses it to build the complete observation set, without knowing any plugin
// config field.
type BindingProvider interface {
	Bindings(state *domain.State) []domain.Binding
}

// MetricInfo describes a metric's action and troubleshooting hint, so the count keeps its
// meaning.
type MetricInfo struct {
	Action string
	Hint   string
}

// MetricProvider is a plugin's optional metric-metadata port.
type MetricProvider interface {
	Metrics() []MetricInfo
}

// ResponseAuditor lets a plugin inspect the upstream response read-only, to spot protocol
// anomalies without rewriting the response.
type ResponseAuditor interface {
	AuditResponse(body []byte) string
}

// Egress rewrites **the response coming back from the upstream**, one SSE event at a time.
//
// It is symmetric to the request-side Plugin, and it is the only place in this model where
// "the bytes going out are no longer the upstream's own words" - so the rules are hardcoded
// here rather than left to the caller's discipline:
//
//   - **Every event goes in as it is and out as it is.** The caller splits on SSE event
//     boundaries and hands it **this event's raw bytes** (including the `event:` line and the
//     trailing blank line); returning nil means not one character changes. The returned bytes
//     are **a complete replacement for this event**, and the caller writes them out.
//   - **Change only the ones you claim.** Anything not recognized returns nil - even a mere
//     "tidy the format while we are here" is swapping the upstream's bytes for ours, and there
//     is no version number on this path for anyone to notice.
//   - **fail-open: a panic or an empty slice counts as not rewriting.** The same rule as
//     Apply (see the package comment): better to miss one event than to break the whole stream
//     over a patch.
//
// It is **an interface rather than a function**: response rewriting almost always carries
// state (which output items of this shot were changed to what), and state should live on this
// one request - see Egressor.
type Egress interface {
	Egress(event []byte) []byte
}

// Egressor is a plugin's optional partner: who rewrites **this shot's response**.
//
// It is asked once at the request stage (right after the plugin's Apply, before the request is
// sent), and a non-nil return means this shot's response is its to rewrite. Each ask should
// give a **new instance**, because response-rewrite state belongs to this shot alone: plugin
// instances are process-level singletons while several requests run at once, so a new object
// per request needs no lock and cannot carry A's judgment into B's shot.
//
// `original` is **the raw request bytes the client sent** (not the post-Apply copy). Which
// events need rewriting on the response side is often judged only from the original request:
// say the client declared which tools are its own custom type, and Apply already reshaped
// those declarations into what the upstream accepts - working backwards from the rewritten
// body is guesswork, reading the original is fact.
type Egressor interface {
	// Egress claims this shot's response rewriting; nil = this shot is not mine.
	Egress(req *Request, original []byte) Egress
}

// Registry holds the current gateway's plugin set, stable execution order and registration
// ownership tokens.
type Registry struct {
	mu      sync.RWMutex
	plugins []Plugin
	tokens  map[string]uint64
	next    uint64
}

var (
	defaultMu       sync.RWMutex
	defaultRegistry = &Registry{}
)

// NewRegistry creates an empty plugin registry isolated from other application instances.
func NewRegistry() *Registry { return &Registry{} }

// SetDefault sets the registry used by the old call surface; new component assemblies should
// prefer the owned InstallDefault.
func SetDefault(registry *Registry) {
	if registry == nil {
		panic("special: nil default registry")
	}
	defaultMu.Lock()
	defaultRegistry = registry
	defaultMu.Unlock()
}

// InstallDefault installs an owned compatibility registry and returns a restore function.
// The restore checks the current instance, so an old component's rollback cannot overwrite a
// newer owner that started later.
func InstallDefault(registry *Registry) func() {
	if registry == nil {
		panic("special: nil default registry")
	}
	defaultMu.Lock()
	previous := defaultRegistry
	defaultRegistry = registry
	defaultMu.Unlock()
	return func() {
		defaultMu.Lock()
		if defaultRegistry == registry {
			defaultRegistry = previous
		}
		defaultMu.Unlock()
	}
}

func currentRegistry() *Registry {
	defaultMu.RLock()
	registry := defaultRegistry
	defaultMu.RUnlock()
	return registry
}

// Ordered declares execution order constraints by plugin name. This is a dependency graph,
// not numeric priority; adding an unrelated plugin does not quietly change the relative order
// of existing ones.
type Ordered interface {
	Before() []string
	After() []string
}

// Register is the compatibility call surface; in production assembly the Gateway component
// holds the Registry and injects the registration port into other components. Execution order
// comes from Ordered's named dependency graph, not from load order.
func Register(p Plugin) {
	if _, err := currentRegistry().Register(p); err != nil {
		panic(err)
	}
}

// Register adds a plugin to the instance registry and returns an undo handle belonging to
// this registration only.
func (r *Registry) Register(p Plugin) (modules.Release, error) {
	if p == nil || p.Name() == "" {
		return nil, fmt.Errorf("special: plugin name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.plugins {
		if existing.Name() == p.Name() {
			return nil, fmt.Errorf("special: duplicate plugin %s", p.Name())
		}
	}
	next := append(append([]Plugin(nil), r.plugins...), p)
	var orderErr error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				orderErr = fmt.Errorf("%v", recovered)
			}
		}()
		next = orderPlugins(next)
	}()
	if orderErr != nil {
		return nil, orderErr
	}
	if r.tokens == nil {
		r.tokens = make(map[string]uint64)
	}
	r.next++
	token := r.next
	// The tokens table is **not** for tolerating duplicate registration (that was rejected
	// above); it exists so a **stale Release is harmless**: unregister → register the same key
	// again and you get a new token, and an old Release arriving late would delete the new
	// registration. Comparing tokens blocks exactly that.
	// (Reading the code it is easy to think this branch is unreachable - that misses
	// "unregister → register again".)
	r.tokens[p.Name()] = token
	r.plugins = next
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.tokens[p.Name()] != token {
			return nil
		}
		delete(r.tokens, p.Name())
		for i, existing := range r.plugins {
			if existing.Name() == p.Name() {
				r.plugins = append(r.plugins[:i], r.plugins[i+1:]...)
				break
			}
		}
		return nil
	}, nil
}

func orderPlugins(plugins []Plugin) []Plugin {
	byName := make(map[string]Plugin, len(plugins))
	index := make(map[string]int, len(plugins))
	edges := make(map[string]map[string]bool, len(plugins))
	indegree := make(map[string]int, len(plugins))
	for i, plugin := range plugins {
		byName[plugin.Name()] = plugin
		index[plugin.Name()] = i
		edges[plugin.Name()] = map[string]bool{}
	}
	addEdge := func(from, to string) {
		if _, ok := byName[from]; !ok {
			return
		}
		if _, ok := byName[to]; !ok || edges[from][to] {
			return
		}
		edges[from][to] = true
		indegree[to]++
	}
	for _, plugin := range plugins {
		ordered, ok := plugin.(Ordered)
		if !ok {
			continue
		}
		for _, before := range ordered.Before() {
			addEdge(plugin.Name(), before)
		}
		for _, after := range ordered.After() {
			addEdge(after, plugin.Name())
		}
	}

	var ready []string
	for name := range byName {
		if indegree[name] == 0 {
			ready = append(ready, name)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return index[ready[i]] < index[ready[j]] })
	out := make([]Plugin, 0, len(plugins))
	for len(ready) > 0 {
		name := ready[0]
		ready = ready[1:]
		out = append(out, byName[name])
		for next := range edges[name] {
			indegree[next]--
			if indegree[next] == 0 {
				ready = append(ready, next)
				sort.Slice(ready, func(i, j int) bool {
					return index[ready[i]] < index[ready[j]]
				})
			}
		}
	}
	if len(out) != len(plugins) {
		panic(fmt.Sprintf("special: plugin ordering cycle among %v", pluginNames(plugins)))
	}
	return out
}

func pluginNames(plugins []Plugin) []string {
	names := make([]string, 0, len(plugins))
	for _, plugin := range plugins {
		names = append(names, plugin.Name())
	}
	return names
}

// Plugins returns the registered plugins (a copy; callers cannot damage the registry).
func Plugins() []Plugin {
	registry := currentRegistry()
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	out := make([]Plugin, len(registry.plugins))
	copy(out, registry.plugins)
	return out
}

// ListModels asks ModelLister in registration order; the first to settle wins. Returns
// (ids, plugin name, ok).
func ListModels(agent, profile string, state *domain.State) ([]string, string, bool) {
	if state == nil || !gatewaystate.SpecialEnabled(state) {
		return nil, "", false
	}
	for _, p := range Plugins() {
		if gatewaystate.PluginOff(state, p.Name()) {
			continue
		}
		lister, is := p.(ModelLister)
		if !is {
			continue
		}
		var ids []string
		var ok bool
		func() {
			defer func() { _ = recover() }()
			ids, ok = lister.ListModels(agent, profile)
		}()
		if ok {
			return ids, p.Name(), true
		}
	}
	return nil, "", false
}

// Respond asks the short-circuit plugins in registration order; the first able to answer on
// the upstream's behalf wins. Returns (response body, winning plugin name, ok).
func Respond(body []byte, request *Request, state *domain.State) ([]byte, string, bool) {
	if state == nil || !gatewaystate.SpecialEnabled(state) {
		return nil, "", false
	}
	for _, p := range Plugins() {
		if gatewaystate.PluginOff(state, p.Name()) {
			continue
		}
		responder, is := p.(Responder)
		if !is {
			continue
		}
		var out []byte
		var matched bool
		func() {
			// fail-open: a plugin panic counts as it never ran. A short-circuiter decides for
			// the user, and one bad plugin must never take away the whole chain's answer.
			defer func() { _ = recover() }()
			out, matched = responder.Respond(body, request, state)
		}()
		if matched {
			return out, p.Name(), true
		}
	}
	return nil, "", false
}

// RespondNote asks the short-circuit plugin that just took effect "what does this shot mean",
// for the caller to log. If the plugin does not implement NoteProvider (or was unregistered)
// the result is an empty string and the caller logs its own line as usual.
func RespondNote(name string, state *domain.State) string {
	for _, p := range Plugins() {
		if p.Name() != name {
			continue
		}
		if provider, ok := p.(NoteProvider); ok {
			return provider.RespondNote(state)
		}
		return ""
	}
	return ""
}

// Route asks the routing plugins in registration order; the first that explicitly claims the
// request decides.
func Route(body []byte, request *Request, state *domain.State) (RouteDecision, bool) {
	if state == nil || !gatewaystate.SpecialEnabled(state) {
		return RouteDecision{}, false
	}
	for _, p := range Plugins() {
		if gatewaystate.PluginOff(state, p.Name()) {
			continue
		}
		router, ok := p.(RoutePlugin)
		if !ok {
			continue
		}
		if decision, matched := router.Route(body, request, state); matched {
			decision.Plugin = p.Name()
			return decision, true
		}
	}
	return RouteDecision{}, false
}

// Statuses collects the status rows contributed by all enabled plugins.
func Statuses(state *domain.State) []StatusItem {
	if state == nil || !gatewaystate.SpecialEnabled(state) {
		return nil
	}
	var out []StatusItem
	for _, p := range Plugins() {
		if gatewaystate.PluginOff(state, p.Name()) {
			continue
		}
		if reporter, ok := p.(StatusProvider); ok {
			out = append(out, reporter.Status(state)...)
		}
	}
	return out
}

func Bindings(state *domain.State) []domain.Binding {
	if state == nil || !gatewaystate.SpecialEnabled(state) {
		return nil
	}
	var out []domain.Binding
	for _, p := range Plugins() {
		if gatewaystate.PluginOff(state, p.Name()) {
			continue
		}
		if provider, ok := p.(BindingProvider); ok {
			out = append(out, provider.Bindings(state)...)
		}
	}
	return out
}

// MetricHint makes metric descriptions follow the plugin that produces the metric, so the CLI
// need not maintain a switch on plugin names.
func MetricHint(key string) (string, bool) {
	for _, p := range Plugins() {
		provider, ok := p.(MetricProvider)
		if !ok {
			continue
		}
		for _, metric := range provider.Metrics() {
			if key == "special."+p.Name()+"."+metric.Action {
				return metric.Hint, true
			}
		}
	}
	return "", false
}

func AuditResponse(body []byte) string {
	var reports []string
	for _, plugin := range Plugins() {
		if auditor, ok := plugin.(ResponseAuditor); ok {
			if report := auditor.AuditResponse(body); report != "" {
				reports = append(reports, plugin.Name()+":\n"+report)
			}
		}
	}
	return strings.Join(reports, "\n")
}

// ClaimEgress asks the plugins once: who rewrites **this shot's response**.
//
// It is asked once right after Apply finishes and before the request goes out, and the
// instance obtained goes with this shot to the end. Returning nil means nobody claimed, and
// the caller takes the original byte-for-byte pass-through forwarding.
//
// Why ask first (rather than running every event through every plugin): rewriting a response
// means splitting SSE into events, and event framing adds a layer to the "not one byte moves"
// promise. With no claimer the caller must be able to see at a glance that the original fast
// path is fine - so the decision is made **before sending the request**, using the same list
// that real execution uses.
//
// Only the first claim (asked in registration order, same as Respond / Route): a response has
// one outlet, and two parties each rewriting would splice together something neither wrote.
//
// `original` is passed to the claimer as is (see Egressor: it wants the copy the client
// sent).
func ClaimEgress(req *Request, original []byte, off func(name string) bool) Egress {
	for _, p := range Plugins() {
		if off != nil && off(p.Name()) {
			continue
		}
		claimer, ok := p.(Egressor)
		if !ok {
			continue
		}
		var egress Egress
		func() {
			// fail-open: a panicking claimer counts as not claiming, and this shot is forwarded
			// as it is.
			defer func() { _ = recover() }()
			egress = claimer.Egress(req, original)
		}()
		if egress != nil {
			return egress
		}
	}
	return nil
}

// RewriteEvent hands **one SSE event** to its claimer; if it declines, nil is returned.
//
// A panic counts as not rewriting (fail-open, same as Apply): better to miss one event than to
// break the whole stream over a patch.
func RewriteEvent(e Egress, event []byte) []byte {
	if e == nil {
		return nil
	}
	var out []byte
	func() {
		defer func() { _ = recover() }()
		out = e.Egress(event)
	}()
	if len(out) == 0 {
		return nil
	}
	return out
}

// ToolLoopNeedsRebase asks whether the plugin matching candidate needs a lossy rebuild first
// to take over an unclosed tool loop produced by origin.
func ToolLoopNeedsRebase(originProvider, originModel string, candidate *Request,
	off func(name string) bool) (bool, string) {
	for _, p := range Plugins() {
		if off != nil && off(p.Name()) {
			continue
		}
		migrator, ok := p.(ToolLoopMigrator)
		if !ok || !p.Match(candidate) {
			continue
		}
		if needed, why := migrator.NeedsToolLoopRebase(
			originProvider, originModel, candidate); needed {
			return true, p.Name() + ": " + why
		}
	}
	return false, ""
}

// RebaseToolLoop lets a plugin that declared it needs a rebase rewrite the request. Fail
// open: on a plugin error it returns the original body and a visible note, so the upstream
// gives the real error and the patch itself never cuts the chain.
func RebaseToolLoop(body []byte, originProvider, originModel string, candidate *Request,
	off func(name string) bool) Result {
	res := Result{Body: body}
	for _, p := range Plugins() {
		if off != nil && off(p.Name()) {
			continue
		}
		migrator, ok := p.(ToolLoopMigrator)
		if !ok || !p.Match(candidate) {
			continue
		}
		needed, _ := migrator.NeedsToolLoopRebase(originProvider, originModel, candidate)
		if !needed {
			continue
		}
		out, note, err := migrator.RebaseToolLoop(body, candidate)
		if err != nil {
			note := i18n.T("lossy tool loop rebase skipped ({err})", i18n.A{"err": err.Error()})
			res.Notes = []string{p.Name() + ": " + note}
			return res
		}
		if note == "" || out == nil {
			return res
		}
		res.Body, res.Changed = out, true
		res.Notes = []string{p.Name() + ": " + note}
		res.Events = []Event{{Plugin: p.Name(), Action: "tool_loop_rebase", Note: note}}
		return res
	}
	return res
}

// Result is the outcome of one special_treatment pass.
type Result struct {
	Body    []byte   // equals the incoming body when Changed is false
	Notes   []string // like "deepseek: inject thinking…"; each one is written to the log
	Events  []Event
	Changed bool
}

type Event struct {
	Plugin string
	Action string
	Note   string
}

func (e Event) MetricKey() string {
	if e.Plugin == "" {
		return ""
	}
	if e.Action == "" || e.Action == "rewrite" {
		return "special." + e.Plugin
	}
	return "special." + e.Plugin + "." + e.Action
}

// Apply runs every matching plugin in registration order, chaining the rewrites.
//
// The decorator chain's full semantics: the body flows along (res.Body = out) and the context
// follows the body (syncContextModel after each step) - the next plugin gets the latest body
// and r.Model, both already rewritten, with no window where "the body changed but the context
// is stale".
//
// off is used to skip plugins the user turned off individually (nil may be passed).
// A plugin error affects only that plugin: one note is recorded and the body keeps the
// previous plugin's result.
func Apply(body []byte, r *Request, off func(name string) bool) Result {
	res := Result{Body: body}
	for _, p := range Plugins() {
		if off != nil && off(p.Name()) {
			continue
		}
		if !p.Match(r) {
			continue
		}
		out, notes, err := p.Apply(res.Body, r)
		if err != nil {
			note := i18n.T("skipped ({err}); sending the request as-is", i18n.A{"err": err.Error()})
			res.Notes = append(res.Notes, p.Name()+": "+note)
			continue
		}
		if len(notes) == 0 || out == nil {
			continue // this plugin had nothing to backfill this time
		}
		res.Body = out
		res.Changed = true
		for _, n := range notes {
			res.Notes = append(res.Notes, p.Name()+": "+n)
			res.Events = append(res.Events, Event{Plugin: p.Name(), Action: "rewrite", Note: n})
		}
		syncContextModel(res.Body, r)
	}
	return res
}

// syncContextModel keeps the context in step with the body: read the top-level model back out
// of the body into r.Model. This is the only derived field the chain must maintain (the others
// do not come from the body). If it cannot be read, keep the old value - never let a broken
// body drag the context down with it.
func syncContextModel(body []byte, r *Request) {
	if m, has := rewrite.TopLevelString(body, "model"); has && m != "" && m != r.Model {
		r.Model = m
	}
}
