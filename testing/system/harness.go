// Package system is the in-process **system-level** test: real graph, real forward, fake upstream.
//
// Not end-to-end. End-to-end here is the two shell scripts in mock/ (real binary, real
// process, real takeover config, real restore) - the only place to verify "the built
// thing runs" - see docs/10-testing-security.md's layering table. Here it is **one real
// component graph in one process**: no subprocess, no PATH shim, no systemd. Calling it
// e2e would mislead people into thinking it covers those, so it is "system".
//
// # Why this layer exists
//
// Unit tests stub with httptest - "is the function right"; the shell is black box
// (grep assertions, logs on failure). The missing middle: assert structure directly
// (which provider got what, what the headers say) and real end-to-end data-plane
// behaviour (routing, fallback, thinkcache, streaming framing). Per "stable foundations
// deserve big tests": tier resolution, byte surgery, failover - rarely changed, breaks all.
//
// Each module's own quirks **are not here** - those go back inside the module, using
// testing/testkit to build a minimal graph of it and its dependencies (only the module
// knows what it needs and how to update dynamically). This layer tests "together fine".
//
// Relation to the live daemon: the Harness listens on a **temp port requested from
// the kernel** (not 8899), so it never conflicts - use newgate while it runs.
package system

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/lib/httpx"
	"github.com/rzbdz/newgate-ext/manifest"
	breakerapi "github.com/rzbdz/newgate-ext/modules/breaker"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/forward"
	gwpolicy "github.com/rzbdz/newgate-ext/modules/gateway/policy"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	"github.com/rzbdz/newgate-ext/testing/upstream"
	"github.com/rzbdz/newgate/app"
	modules "github.com/rzbdz/newgate/component"
)

// logSink collects the proxy's own logs and dumps them only on failure.
//
// Why not io.Discard: when a routing assertion fails (404 "no candidate" is typical), the
// evidence is in the proxy's `skip reason: ...` line - which candidate, why skipped. Throw
// it away and the test says "404 is not 200", and a human must reproduce by hand.
//
// Why buffer instead of t.Logf: logs come from server goroutines; writing after the test
// ends panics. This registers cleanup first in Start, so it runs last (t.Cleanup is LIFO),
// when the server is fully stopped. On success these lines are noise, so dump on failure.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *logSink) dump(t *testing.T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buf.Len() == 0 {
		return
	}
	t.Logf("proxy log:\n%s", s.buf.String())
}

func (s *logSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// Harness is one running newgate instance.
type Harness struct {
	t *testing.T

	// ProxyURL is the forwarding service root, like http://127.0.0.1:34567.
	ProxyURL string
	// Upstream is the fake upstream it points at this time.
	Upstream *upstream.Server
	// Env is the isolated config directory (NEWGATE_HOME etc.).
	Env *testkit.Env
	// Components is the actual start order, for asserting dependencies.
	Components []string

	graph   *app.App
	server  *forward.Server
	watcher *store.Watcher
	client  *http.Client
	// health is the health table in the component graph (mounted via RegisterFilter).
	health breakerapi.Breaker
	// sink collects the proxy's own logs, for assertions about "not silent".
	sink *logSink
}

// DefaultGraph is the graph Start installs: this distribution's flagship spec.
//
// The harness used to install the kernel's own graph. Since the kernel ships no
// modules (2026-11-18), "a real graph" is by definition a product graph — this
// distribution's. StartWith remains for a test that wants a different one.
func DefaultGraph() modules.Loader {
	return manifest.Specs()[manifest.DefaultSpec]
}

// Start brings it up: sandbox config -> fake upstream -> real graph -> real forward (temp port).
//
// The port is requested from the kernel (listen :0 then close), so parallel runs on one
// machine never conflict. The port goes into state.json, then forward listens - the order
// cannot reverse, since forward's port comes from config.
//
// It installs DefaultGraph(); pass StartWith to install a different one.
func Start(t *testing.T) *Harness {
	t.Helper()
	return StartWith(t, DefaultGraph())
}

// StartWith is Start verbatim, only the manifest comes from the caller.
//
// Why it exists (2026-09-20): **tests follow owners**. The distribution's modules (DeepSeek
// tail-shape patching etc.) should be asserted by it, and that needs "real graph + real
// forward" - the kernel's facility (side effects only a real graph has: special plugins,
// health table on the data plane, confighook's client directory). Without this entrance the
// distribution would copy the kernel's test facility (drifts) or stuff its modules into the
// kernel's default graph (kernel no longer testable). Both cost more than one function.
func StartWith(t *testing.T, loader modules.Loader) *Harness {
	t.Helper()

	env := testkit.Sandbox(t)
	up := upstream.New(t)

	// Buffer the proxy's logs and dump on failure (registered first -> cleanup last).
	sink := &logSink{}
	t.Cleanup(func() {
		if t.Failed() {
			sink.dump(t)
		}
	})

	// 1. Lay down a default config, then point the upstream at the fake one.
	if _, err := store.Init(false); err != nil {
		t.Fatalf("system: initializing sandbox config: %v", err)
	}
	pointAtUpstream(t, up.URL())

	// 2. The port must be obtained before writing state, or forward's listen port and the
	//    config disagree (clients find us by the port in state).
	port := freePort(t)
	st := store.LoadState()
	st.Port = port
	if err := store.SaveState(st); err != nil {
		t.Fatalf("system: writing state.json: %v", err)
	}

	// 3. Real component graph. Its side effects are what we test: gateway registers
	//    special plugins, confighook builds the client directory, config wires roles.
	graph, err := app.New(context.Background(), loader)
	if err != nil {
		t.Fatalf("system: assembling the component graph: %v", err)
	}
	t.Cleanup(func() { _ = graph.Stop(context.Background()) })

	// 4. The real forward service. The watcher matches production (1 second poll), so the
	//    config hot-reload path is covered here too.
	//
	//    The policy ledger comes from **the real graph**'s (not a new policy.New()), so
	//    contributor registration really goes "module Start -> gateway.RegisterFilter ->
	//    data plane", and the health table is production's. forward is only injected - it
	//    registers nothing, only reads the ledger (see the [shape-400] branch in forward.go).
	//
	//    Read via policy.Default() (the instance gateway installs in Start), not
	//    modules.MustGet(graph.Context(), gatewayapi.Capability): the latter gives a
	//    *port, while the data plane wants the ledger. Different from the health table
	//    because the **ledger** itself is the insertion point (see modules/gateway/policy).
	watcher, err := store.NewWatcher(time.Second)
	if err != nil {
		t.Fatalf("system: configuring the watcher: %v", err)
	}
	t.Cleanup(watcher.Close)
	watcher.Start()

	health := modules.MustGet(graph.Context(), breakerapi.Capability)
	server := forward.New("", port, log.New(sink, "", 0), watcher, gwpolicy.Default())
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Start() }()
	t.Cleanup(func() {
		server.Shutdown()
		select {
		case err := <-serveErr:
			// After Shutdown, Serve returning http.ErrServerClosed is normal.
			if err != nil && !strings.Contains(err.Error(), "Server closed") {
				t.Errorf("system: the forwarding service exited unexpectedly: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Errorf("system: the forwarding service did not exit within 3 seconds")
		}
	})

	h := &Harness{
		t:          t,
		ProxyURL:   "http://127.0.0.1:" + strconv.Itoa(port),
		Upstream:   up,
		Env:        env,
		Components: graph.ComponentNames(),
		graph:      graph,
		server:     server,
		watcher:    watcher,
		health:     health,
		sink:       sink,
		// Explicitly bypass the environment proxy: CI and dev boxes often have HTTP_PROXY,
		// and 127.0.0.1 requests would be hijacked through it (hit locally once).
		client: httpx.LocalClient(15 * time.Second),
	}
	h.waitReady()
	return h
}

// Client returns an HTTP client with the env proxy bypassed, 15 second timeout.
func (h *Harness) Client() *http.Client { return h.client }

// Breaker returns **the graph's** health table (the one the data plane uses).
//
// Why this entrance: shape criteria are registered by modules in Start; a test that calls
// newTable() itself bypasses registration, and the assertion becomes "400 with no detector
// also does not tri", true but meaningless - exactly why the pre-2026-09-17 test spun.
// The real table lets us assert "the criterion is registered, claimed, and Shape reads out".
func (h *Harness) Breaker() breakerapi.Breaker { return h.health }

// Logs returns the logs the proxy has written so far (a snapshot; appends may continue).
//
// For "not silent": when a shape criterion claims a 400, the log must carry the [shape-400]
// line naming **which criterion** claimed it. Asserting only the health counter would let a
// dropped log line slip by - and on site, the log is all a human has.
func (h *Harness) Logs() string { return h.sink.String() }

// WaitForLog polls until a log line appears (no ordering guarantee between the proxy
// writing and the client getting the response, especially on error paths). Timeout -> false.
func (h *Harness) WaitForLog(t *testing.T, sub string) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(h.Logs(), sub) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// Post sends a JSON request to the proxy and returns the response. path starts with /,
// e.g. "/a/claude/v1/messages".
func (h *Harness) Post(path, body string) *http.Response {
	h.t.Helper()
	resp, err := h.post(path, body)
	if err != nil {
		h.t.Fatalf("system: POST %s: %v", path, err)
	}
	return resp
}

// PostRaw is Post but hands network errors back to the caller (for disconnect tests).
func (h *Harness) PostRaw(path, body string) (*http.Response, error) {
	return h.post(path, body)
}

func (h *Harness) post(path, body string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, h.ProxyURL+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer newgate-local")
	return h.client.Do(req)
}

// Get sends a GET.
func (h *Harness) Get(path string) *http.Response {
	h.t.Helper()
	resp, err := h.client.Get(h.ProxyURL + path)
	if err != nil {
		h.t.Fatalf("system: GET %s: %v", path, err)
	}
	return resp
}

// ReadBody reads and closes an empty response body, returning a string.
func ReadBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("system: reading the response body: %v", err)
	}
	return string(b)
}

// waitReady polls until the proxy answers. The port listened before this, so only the HTTP
// layer is awaited - but socket and handler differ by milliseconds, so polling avoids flakes.
func (h *Harness) waitReady() {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := h.client.Get(h.ProxyURL + "/__newgate/status")
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("system: the proxy was not ready within 5 seconds (%s)", h.ProxyURL)
}

// pointAtUpstream points every provider at the fake upstream.
//
// base_url carries /v1 (openai dialect appends /chat/completions); anthropic_url does not
// - the established semantics of the two fields, also verifying that "the client's dialect
// picks the base" works at the integration layer.
func pointAtUpstream(t *testing.T, url string) {
	t.Helper()
	providers, err := store.LoadProviders()
	if err != nil {
		t.Fatalf("system: reading providers.json: %v", err)
	}
	if len(providers.Providers) == 0 {
		t.Fatal("system: no provider in the default config")
	}
	for name, p := range providers.Providers {
		p.BaseURL = url + "/v1"
		p.AnthropicURL = url
		p.APIKey = "sk-fake-" + name
		// A key read from an env var cannot be set here; we would judge "no key" and refuse.
		p.APIKeyEnv = ""
		// Must write back into the map: range's value is a copy; editing p edits the copy -
		// the symptom is every candidate judged "provider has no api_key", 404 instead of forward.
		providers.Providers[name] = p
	}
	if err := store.SaveProviders(providers); err != nil {
		t.Fatalf("system: writing providers.json: %v", err)
	}
}

// freePort asks the kernel for a currently free port. There is a TOCTOU window (someone
// may grab it before re-listen), but parallel tests each take one; in practice no clash.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("system: asking for an idle port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}
