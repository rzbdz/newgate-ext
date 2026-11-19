package forward

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rzbdz/newgate-ext/lib/logx"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlpath"
	"github.com/rzbdz/newgate-ext/modules/gateway/dialect"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate-ext/modules/gateway/metrics"
	"github.com/rzbdz/newgate-ext/modules/gateway/policy"
	"github.com/rzbdz/newgate-ext/modules/gateway/probe"
	"github.com/rzbdz/newgate-ext/modules/gateway/protocol"
	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
	"github.com/rzbdz/newgate-ext/modules/gateway/rewrite"
	schema "github.com/rzbdz/newgate-ext/modules/gateway/rewrite/schema"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	"github.com/rzbdz/newgate-ext/modules/gateway/thinkcache"
	"github.com/rzbdz/newgate-ext/modules/runtime/daemon"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Server owns the proxy data plane's listener, hot config snapshot and graceful handover state.
// It is a runtime resource, not a component port; the daemon/CLI controls its lifecycle explicitly.
type Server struct {
	Port int
	// Host is the listen address; empty = loopback (see the comment on domain.State.Host: opening
	// it up is an explicit decision, and the default does not move).
	Host   string
	Logger *log.Logger
	// Watch holds the config snapshot. The hot path does one atomic pointer read, never touching
	// the filesystem. Config changes **need no restart**: after the watcher swaps the page, new
	// requests use the new config, and running sessions are untouched (each agent reads its binding).
	Watch *store.Watcher

	// Filters are the injected data-plane policy contributors (admission, outcome verdicts,
	// control-plane self-reporting, shutdown flush; see modules/gateway/policy).
	//
	// The data plane **knows no policy at all**: it only knows it has these four decision
	// points; who answers them and by what rules is someone else's business. Empty ledger =
	// minimal system (all candidates usable, swap by mechanism, no accounting).
	Filters *policy.Registry

	srv      *http.Server
	ln       net.Listener // graceful handoff passes it to the new process as an fd
	requests uint64
	failures uint64
	started  time.Time

	// mux is the data plane's own handler (7 patterns, the catch-all is the forwarding);
	// when root is non-nil, use it as this port's root handler.
	//
	// Why this seam exists: **a port may carry more than one service** (a web UI wants to
	// share the same port). Who decides "who owns this request" must not be answered by the
	// data plane -- so only a slot is left here, and the daemon entry installs the root
	// handler into it. The data plane therefore knows neither the mount table nor any tenant
	// (direction_test.go pins this).
	//
	// The default (root == nil) is the data plane serving the port itself, i.e. how it looked
	// before shared ports existed: single-machine tests and the testing/system harness take it.
	mux  http.Handler
	root http.Handler

	stopOnce sync.Once
	stopCh   chan struct{}

	// pendingQuirks are upstream quirks hit **on the forwarding path, not yet persisted** (see
	// probe's PendingQuirks). The field lives on Server, not in probe: persistence happens at
	// **shutdown** (see Shutdown), and the data plane is what knows "this machine is leaving".
	//
	// Why not "persist as soon as hit": that writes the disk on every upstream 4xx, and 4xx is
	// a perfectly normal outcome on the hot path (shape 400, bad params, quota) -- cost and
	// benefit out of proportion. Why not "leave it as is": that is exactly the always-reproducible
	// window the user reported on 2026-09-22 -- after a restart the table is empty, and the
	// first Codex → GLM shot hits the 400 all over again.
	pendingQuirks probe.PendingQuirks

	// The drain state of a graceful handover (used by the **old** process after the handover):
	//   once draining=1 this process's pid/lock have been rewritten under the new process --
	//   no exit path may clear them any more; the main loop (where Serve returns) must wait
	//   for in-flight requests to flow through, or Shutdown closing the listener returns the
	//   main function at once and the process vanishes -- draining would equal no draining.
	drainOnce sync.Once
	drainCh   chan struct{}
	drainFlag int32
}

// New builds a not-yet-listening Server, so config, logs and the policy ledger are fully
// visible before any startup side effect.
//
// host empty = bind loopback only (default). The bind address is a startup argument, not hot
// config: changing the listen address means re-binding, which is not hot-path work (like Port).
func New(host string, port int, lg *log.Logger, w *store.Watcher, filters *policy.Registry) *Server {
	if filters == nil {
		filters = policy.New()
	}
	return &Server{Host: host, Port: port, Logger: lg, Watch: w, Filters: filters,
		stopCh: make(chan struct{}), drainCh: make(chan struct{})}
}

// RequestStop asks for a shutdown from outside (used by control endpoints). Idempotent;
// repeated calls are harmless. Silent no-op when stopCh is nil (test stubs built from &Server{}).
func (s *Server) RequestStop() {
	s.stopOnce.Do(func() {
		if s.stopCh != nil {
			close(s.stopCh)
		}
	})
}

// StopRequested listens for shutdown requests. Receiving one means someone (usually another
// user's `newgate stop`) asked this process to exit.
func (s *Server) StopRequested() <-chan struct{} { return s.stopCh }

// Draining is whether this process is in the graceful-handover drain window (socket already
// handed over). Exit paths there must not clear pid/lock -- they are the new process's.
func (s *Server) Draining() bool { return atomic.LoadInt32(&s.drainFlag) == 1 }

// Drained is the signal that in-flight requests finished draining. Only meaningful while Draining.
func (s *Server) Drained() <-chan struct{} { return s.drainCh }

// snap is the current config snapshot. Without a watcher it degrades to reading disk (test path).
func (s *Server) snap() *store.Snapshot {
	if s.Watch != nil {
		return s.Watch.Current()
	}
	sn, err := store.Load()
	if err != nil {
		return nil
	}
	return sn
}

func (s *Server) logf(format string, a ...interface{}) {
	if s.Logger != nil {
		s.Logger.Printf(format, a...)
	}
}

// outcome is the data plane's only entry to report an upstream outcome: one verdict + counters
// + log material. All four call sites (connect failure, status code, stream interruption,
// success) go through it, so no site misses the metrics.
//
// It does **mechanism** only: hand "what happened this time" to the registered policy as-is,
// then execute the verdict. Whether to keep this ledger and what trail to leave is all
// policy's business -- the data plane knows none of it, counter names included.
func (s *Server) outcome(o policy.Outcome) policy.Verdict {
	v := s.Filters.Judge(o)
	for _, key := range v.Metrics {
		metrics.Default.Inc(key)
	}
	return v
}

// advance is the kernel's swap mechanism: the response has not started writing and the chain
// has another stop.
//
// This is the only default direction, and **policy can only stop it** (Verdict.Stop) -- there
// is no case where policy asks to advance and the mechanism wants to stop (the old failure
// table was checked in full: apart from the "other 4xx" policy spot, swapping always equals
// this formula). So composing verdict and mechanism is a single OR.
func advance(o policy.Outcome, v policy.Verdict) bool {
	return !o.ResponseStarted && !o.IsLast && !v.Stop
}

// verifyProbeAttempts / verifyProbeTimeout are the "diagnose before the gate" shape: at most
// two consecutive minimal probes, and **any single 200 means this binding is still alive**.
//
// Why "any one succeeds" and not "both succeed": the question is "can it still get through",
// and one success is conclusive proof it can, while one failure may just be queueing/jitter.
// The user's own words were "even intermittent counts" -- this path guards precisely against
// "it works but got pulled".
//
// Why the timeout is deliberately short: this is time spent **waiting on the user's behalf**.
// The real request already failed and the user is waiting for fallback; a slower diagnosis
// trades an already unhappy request for a more accurate conclusion. 8s is enough for one
// max_tokens=4 minimal request (a normal upstream answers in hundreds of ms).
const (
	verifyProbeAttempts = 2
	verifyProbeTimeout  = 8 * time.Second
)

// probe is the "active probing" capability the data plane lends to policy (policy.Env.Probe).
//
// The field (measured 2026-09-17, logs and health.json both kept): smt-deepseek was pulled
// several times, the reason "real traffic failed repeatedly" each time, while every manual
// `newgate probe` was fluent -- the passive path failed on a **first-byte timeout** (that
// classifier chain stuffed 126KB of system into a tight 12s budget), while probe sends a
// minimal request and never touches that edge. With conflicting evidence the active,
// controllable, repeatable one is harder; and pulling costs asymmetrically (the user is
// silently swapped to another model, waiting out a 60s+ cooldown), so the gate must first
// demand active evidence.
//
// This capability belongs to the data plane: it reads the current config snapshot (provider
// base, key, dialect) and reuses the data plane's probe implementation. Policy merely
// **borrows** it -- the previous version had policy demanding a callback (`SetVerifier`),
// the direction was backwards.
//
// fail-closed: no provider config, empty key, probe error -- all return false. Diagnosis
// **must not** become a get-out-of-jail card for a bad binding; it should only catch misjudgments.
func (s *Server) probe(provider, model string) bool {
	snap := s.snap()
	if snap == nil || snap.Providers == nil {
		return false
	}
	p, ok := snap.Providers.Providers[provider]
	if !ok || p.Key() == "" {
		return false
	}
	for i := 0; i < verifyProbeAttempts; i++ {
		status, latency, err := probe.One(p, model, verifyProbeTimeout)
		s.logf("[probe] %s", i18n.T(
			"pre-gate diagnosis {provider}/{model} attempt {attempt}/{total}: HTTP {status} {err} ({latency})",
			i18n.A{"provider": provider, "model": model, "attempt": i + 1,
				"total": verifyProbeAttempts, "status": status, "err": err,
				"latency": latency.Round(time.Millisecond)}))
		if err == nil && status == 200 {
			return true
		}
	}
	return false
}

// serverEnv is the runtime capability the data plane hands to policy (policy.Env).
//
// Timing matters: Server.Start installs it **when the service comes up**, not at assembly
// time -- because what it gives is exactly "this thing alive right now" (logs, probing under
// the current config snapshot). See the three-phase rule in docs/02-component-framework.md.
type serverEnv struct{ s *Server }

// Logf is policy's log outlet. The message is **given whole by policy** (the data plane adds
// no prefix): whoever owns the knowledge names it; words like "health table" are not ours.
func (e serverEnv) Logf(format string, args ...any) { e.s.logf(format, args...) }

func (e serverEnv) Probe(provider, model string) bool { return e.s.probe(provider, model) }

// State is the config snapshot the data plane is serving right now. Nil when the config cannot
// be read: the same fail-open direction as every switch lookup (a contributor reads nil as
// "nothing set").
func (e serverEnv) State() *domain.State {
	if sn := e.s.snap(); sn != nil {
		return sn.State
	}
	return nil
}

// Handler builds the data plane's handler on this port.
//
// Anyone mounting another service on the same port (the daemon entry) **asks it for one
// first**, wraps it as the fallback in their own routing, then installs it back with SetRoot.
// Idempotent: repeated calls return the same one (built once and kept) -- the fallback and
// the port service must be the same thing.
func (s *Server) Handler() http.Handler {
	if s.mux == nil {
		mux := http.NewServeMux()
		mux.HandleFunc(controlpath.Status, s.handleStatus)
		mux.HandleFunc(controlpath.Metrics, s.handleMetrics)
		mux.HandleFunc(controlpath.Health, s.handleHealth)
		mux.HandleFunc(controlpath.Stop, s.handleControlStop)
		mux.HandleFunc(controlpath.Upgrade, s.handleControlUpgrade)
		mux.HandleFunc("/v1/models", s.handleModels)
		// catch-all: the data plane. A path reaching here is either the data plane's own
		// (/v1, /a) or one others explicitly handed over (those nobody on the port claimed).
		mux.HandleFunc("/", s.handleProxy)
		s.mux = mux
	}
	return s.mux
}

// SetRoot swaps this port's root handler. **Must be called before Start** (after Start,
// http.Server already holds the old one; changes have no effect and nobody tells you).
func (s *Server) SetRoot(h http.Handler) { s.root = h }

// rootHandler is what Start really hands to http.Server.
//
// A separate function for one reason: make "who gets the port" **testable** -- tests and
// Start call the same path (otherwise a test would listen and dial itself, testing something else).
func (s *Server) rootHandler() http.Handler {
	if s.root != nil {
		return s.root
	}
	return s.Handler()
}

func (s *Server) Start() error {
	// The runtime capability policy needs is delivered here (see probe for the probe's shape).
	s.Filters.BindEnv(serverEnv{s: s})
	// Load what was probed (dialect capabilities, upstream quirks) back from disk. Say a word
	// when it cannot be read: otherwise the symptom is "after a restart every upstream has to
	// hit the 404/400 again to be learned", and nobody knows why.
	if err := probe.LoadCachedCapabilities(); err != nil {
		s.logf("[probe] %s", i18n.T("capability cache load failed (will re-probe this run): {err}", i18n.A{"err": err}))
	}
	handler := s.rootHandler()

	ln, err := s.listen()
	if err != nil {
		return err
	}
	s.started = time.Now()
	s.srv = &http.Server{Handler: handler}
	s.ln = ln
	s.signalReady() // the handed-over process: tell the parent the socket is connected
	s.logf("[proxy] %s", i18n.T("listening on {host}:{port}", i18n.A{"host": s.bindHost(), "port": s.Port}))
	return s.srv.Serve(ln)
}

// listen acquires the listening socket: an fd handed over by the parent process wins (the
// graceful upgrade path), otherwise listen ourselves. On inheritance the fd number is read
// from NEWGATE_LISTENER_FD and unset right away -- env vars must not leak into anything this
// process spawns later (its own upgrade child, or a plain Spawn on the lazy path), or fd3
// would be used as a socket.
func (s *Server) listen() (net.Listener, error) {
	if fdStr := os.Getenv("NEWGATE_LISTENER_FD"); fdStr != "" {
		os.Unsetenv("NEWGATE_LISTENER_FD")
		fd, err := strconv.Atoi(fdStr)
		if err != nil {
			return nil, i18n.Ef(err, "NEWGATE_LISTENER_FD={value} is not an fd number", i18n.A{"value": strconv.Quote(fdStr)})
		}
		f := os.NewFile(uintptr(fd), "inherited-listener")
		ln, err := net.FileListener(f)
		if err != nil {
			return nil, i18n.Ef(err, "inheriting listener fd {fd} failed", i18n.A{"fd": fd})
		}
		if _, ok := ln.(*net.TCPListener); !ok {
			ln.Close()
			return nil, i18n.E("inherited fd {fd} is not a TCP listener", i18n.A{"fd": fd})
		}
		// the port number follows the real thing: inherited, s.Port may just echo the parent's
		if a, ok := ln.Addr().(*net.TCPAddr); ok {
			s.Port = a.Port
		}
		return ln, nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", s.bindHost(), s.Port))
	if err != nil {
		return nil, i18n.Ef(err, "listening on {host}:{port} failed",
			i18n.A{"host": s.bindHost(), "port": s.Port})
	}
	return ln, nil
}

// bindHost is the address this listen actually uses: unset means loopback.
func (s *Server) bindHost() string {
	if s.Host == "" {
		return "127.0.0.1"
	}
	return s.Host
}

// signalReady is the other half of the graceful-handover handshake: write one byte to the pipe
// the parent gave us before entering the accept loop. A no-op without a handover (normal Spawn).
func (s *Server) signalReady() {
	fdStr := os.Getenv("NEWGATE_READY_FD")
	if fdStr == "" {
		return
	}
	os.Unsetenv("NEWGATE_READY_FD")
	if fd, err := strconv.Atoi(fdStr); err == nil {
		if f := os.NewFile(uintptr(fd), "ready-pipe"); f != nil {
			_, _ = f.Write([]byte{1})
			_ = f.Close()
		}
	}
}

func (s *Server) Shutdown() {
	// Policies with an unflushed ledger are called once here (before the listener closes: after
	// that the process may exit and that ledger is lost). A failed flush is reported per "not
	// silent", but does not block shutdown.
	s.Filters.Flush(s.logf)
	// Same for upstream quirks learned on the forwarding path (see flushPendingQuirks).
	s.flushPendingQuirks()
	if s.srv != nil {
		_ = s.srv.Close()
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	snap := s.snap()
	if snap == nil {
		s.fail(w, 400, i18n.T("cannot read config — run `newgate doctor`", nil))
		return
	}
	st := snap.State
	tc := thinkcache.Default.Stats()
	doc := map[string]interface{}{
		"ok":              true,
		"default_profile": st.DefaultProfile,
		"active":          st.Active,
		"port":            s.Port,
		"requests":        atomic.LoadUint64(&s.requests),
		"failures":        atomic.LoadUint64(&s.failures),
		"uptime_s":        int(time.Since(s.started).Seconds()),
		"tiers":           domain.Roles,
		// For `newgate restart` to probe: supports graceful handover (socket handoff).
		// An old daemon has no such field -- restart falls back to stop+start when it is
		// missing, and never blindly POSTs /__newgate/upgrade (the catch-all would forward it upstream).
		"handoff": true,
		// Reasoning-content cache: report counts only, never content
		"thinkcache": map[string]interface{}{
			"entries":    tc.Entries,
			"bytes":      tc.Bytes,
			"max_bytes":  tc.MaxBytes,
			"hits":       tc.Hits,
			"misses":     tc.Misses,
			"puts":       tc.Puts,
			"evictions":  tc.Evictions,
			"unparsable": tc.Unparsable,
		},
	}
	// Policy's own state sections merge into the **top level** (not tucked into an extra
	// object): older CLIs read those top-level keys, and moving them would show an empty table
	// during the handover upgrade window. Collisions error at registration (policy.Registry.validate),
	// so nothing overwrites anything here.
	for key, value := range s.Filters.Doc() {
		doc[key] = value
	}
	writeJSON(w, 200, doc)
}

type probeHealthObservation struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Status    int    `json:"status"`
	LatencyMs int64  `json:"latency_ms"`
	Context   int    `json:"context_bytes"`
	Error     string `json:"error,omitempty"`
}

// handleHealth receives `newgate probe`'s active probing conclusions and hands them to the
// registered policy. The data plane only does **I/O and auth**: it turns "what was probed"
// into neutral observations; what that conclusion means and whether to pull a card is all
// policy's business (`policy.Controller.ObserveProbes`).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, 405, map[string]interface{}{"ok": false, "error": i18n.T("only POST is accepted", nil)})
		return
	}
	snap := s.snap()
	if snap == nil || snap.State.ControlToken == "" ||
		r.Header.Get("Authorization") != "Bearer "+snap.State.ControlToken {
		writeJSON(w, 403, map[string]interface{}{"ok": false, "error": i18n.T("control token mismatch", nil)})
		return
	}
	var req struct {
		Observations []probeHealthObservation `json:"observations"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 64*1024))
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": i18n.T("invalid request JSON", nil)})
		return
	}
	threshold := snap.State.Timeouts.ClassifierFirstByte()
	obs := make([]policy.ProbeObservation, 0, len(req.Observations))
	for _, o := range req.Observations {
		if o.Provider == "" || o.Model == "" {
			continue
		}
		obs = append(obs, policy.ProbeObservation{
			Provider: o.Provider, Model: o.Model, Status: o.Status,
			Latency:      time.Duration(o.LatencyMs) * time.Millisecond,
			ContextBytes: o.Context, Error: o.Error, SlowAfter: threshold,
		})
	}
	opened := 0
	for _, ack := range s.Filters.ObserveProbes(obs) {
		if ack.Opened {
			opened++
		}
		if ack.Note != "" {
			s.logf("%s", ack.Note)
		}
	}
	doc := map[string]interface{}{"ok": true, "opened": opened}
	for key, value := range s.Filters.Doc() {
		doc[key] = value
	}
	writeJSON(w, 200, doc)
}

// handleMetrics gateway counters (read-only, binds 127.0.0.1 only). The data source for
// `newgate metrics`; counters go back to zero when the daemon restarts.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{
		"uptime_s": int(time.Since(s.started).Seconds()),
		"metrics":  metrics.Default.Snapshot(),
	})
}

// handleControlStop is the control endpoint: for users who "can read the shared config but
// cannot send a signal" (daemon.Stop's fallback path on EPERM).
//
// Auth: the ControlToken from state.json (Bearer). The endpoint binds 127.0.0.1 only, and
// the token is kept as secret as the upstream keys in the same file -- a process that can
// read it has long since passed this layer of protection.
func (s *Server) handleControlStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, 405, map[string]interface{}{"ok": false, "error": i18n.T("only POST is accepted", nil)})
		return
	}
	tok := ""
	if snap := s.snap(); snap != nil {
		tok = snap.State.ControlToken
	}
	// Constant-time comparison: no local process guesses the token bit by bit from response timing
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if tok == "" || subtle.ConstantTimeCompare([]byte(got), []byte(tok)) != 1 {
		writeJSON(w, 403, map[string]interface{}{"ok": false, "error": i18n.T("control token mismatch", nil)})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "bye": true})
	s.logf("[proxy] %s", i18n.T("control stop requested by {addr}, exiting", i18n.A{"addr": r.RemoteAddr}))
	// First let the 200 flush out of the buffer, then trigger the exit
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.RequestStop()
	}()
}

// handleControlUpgrade graceful upgrade (a Go version of nginx upgrade): hand the listening
// socket to the new binary; once attached, the old process drains in-flight requests and exits.
//
// Why it exists: the session developing newgate itself runs through the proxy, and the
// stop/start cut would break the Claude Code using it mid-flight. On the handover path:
//   - in-flight requests (SSE long streams included) **run to completion** here, not cut off;
//   - new connections are accepted by the new process at once, clients notice nothing;
//   - no shim removed, no takeover state touched -- just a different process serving on.
//
// Auth shares its source with /__newgate/stop (ControlToken).
func (s *Server) handleControlUpgrade(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, 405, map[string]interface{}{"ok": false, "error": i18n.T("only POST is accepted", nil)})
		return
	}
	tok := ""
	if snap := s.snap(); snap != nil {
		tok = snap.State.ControlToken
	}
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if tok == "" || subtle.ConstantTimeCompare([]byte(got), []byte(tok)) != 1 {
		writeJSON(w, 403, map[string]interface{}{"ok": false, "error": i18n.T("control token mismatch", nil)})
		return
	}
	if s.ln == nil {
		writeJSON(w, 503, map[string]interface{}{"ok": false, "error": i18n.T("listener handle not ready yet", nil)})
		return
	}
	info, err := daemon.SpawnHandoff(s.Port, s.ln)
	if err != nil {
		// Handoff failed: this process keeps the socket to itself, the service is unaffected
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	// pid/lock move under the new process at once -- status must point at the future while draining
	if err := daemon.AdoptRuntime(info); err != nil {
		_ = syscall.Kill(info.PID, syscall.SIGKILL)
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	s.logf("[proxy] %s", i18n.T("graceful handoff: socket handed to new process pid {pid}, draining in-flight requests", i18n.A{"pid": info.PID}))
	// **Persist what was learned before handing off**, and the order cannot flip: after the
	// swap this process only has "stream the in-flight requests out" left, and the entry it
	// holds (upstream quirks hit in the hours before the version switch) is known only to it --
	// the new process's Start has already run, so what it read from disk is the **old** copy.
	// Persist afterwards and this entry only takes effect when "the next new process comes up",
	// i.e. "the first shot after the upgrade" still hits the 400 -- exactly the one the user
	// reported on 2026-09-22. (Hit for real on 2026-09-23: the swap-first version still gave
	// process B a 400 on its first shot.)
	s.flushPendingQuirks()
	writeJSON(w, 200, map[string]interface{}{"ok": true, "new_pid": info.PID})
	// Drain: let in-flight requests (SSE streams included) end naturally, cap 10 minutes, then exit.
	// Two key points:
	//   - use Shutdown (wait for in-flight), never srv.Close() (a hard cut);
	//   - the main loop waits on Drained() where Serve returns -- otherwise the listener
	//     closing returns the main function at once, the process vanishes, and no amount of
	//     waiting here helps.
	// Exit is always os.Exit: skip the RemoveLock deferred in Serve (pid/lock now belong to
	// the new process and must not be deleted).
	go func() {
		atomic.StoreInt32(&s.drainFlag, 1)
		time.Sleep(100 * time.Millisecond) // let the 200 flush out first
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if s.srv != nil {
			_ = s.srv.Shutdown(ctx)
		}
		s.drainOnce.Do(func() { close(s.drainCh) })
		// Persist only after the drain: in-flight requests during it may learn new quirks, and those
		// shots are the ones most likely to hit around the handover (see flushPendingQuirks --
		// on the upgrade path this is the only place that calls it).
		s.flushPendingQuirks()
		s.logf("[proxy] %s", i18n.T("drain complete, exiting (socket continues under pid {pid})", i18n.A{"pid": info.PID}))
		os.Exit(0)
	}()
}

// handleModels lets opencode discover the semantic tiers we expose.
//
// Dynamic role keys contributed by modules (omo-sisyphus / cat-deep) are listed too: they are
// model ids a client can name, and hiding them only makes opencode report "model does not exist".
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	var data []map[string]interface{}
	seen := map[string]bool{}
	add := func(id string) {
		if id == "" || seen[id] {
			return // built-in aliases (normal→mid) are in the role table too and collide with a tier name
		}
		seen[id] = true
		data = append(data, map[string]interface{}{
			"id": id, "object": "model", "owned_by": "newgate",
		})
	}
	// A client module may answer this list for its own URL (special.ModelLister says why).
	// Nobody answering means the default: tier + dynamic role keys, independent of client.
	tgt := parseTarget(r.URL.Path)
	var st *domain.State
	if snap := s.snap(); snap != nil {
		st = snap.State
	}
	if ids, _, ok := special.ListModels(tgt.TaskCreate, tgt.Profile, st); ok {
		for _, id := range ids {
			add(id)
		}
		writeJSON(w, 200, map[string]interface{}{"object": "list", "data": data})
		return
	}
	for _, tier := range domain.Roles {
		add(tier)
	}
	for _, extra := range domain.ExtraRoles() {
		add(extra.Key)
	}
	writeJSON(w, 200, map[string]interface{}{"object": "list", "data": data})
}

// Target is the routing intent parsed out of the URL path.
//
// The path grammar:
//
//	/a/<agent>[/p/<profile>]/v1/...   with an agent identity
//	/v1/...                           compatible: use the global default profile
//
// Why a path and not a header: neither claude nor opencode lets us put custom headers into
// their requests, but the baseURL is ours, written at bootstrap. The path scheme is fully
// stateless -- no session to register, no token lifetime, and a proxy restart changes nothing.
type Target struct {
	TaskCreate string // empty = unspecified
	Profile    string // non-empty = explicit override for this call, global state untouched
	Suffix     string // path suffix forwarded to the upstream, like /chat/completions
}

func parseTarget(p string) Target {
	var t Target
	for {
		switch {
		case strings.HasPrefix(p, "/a/"):
			rest := p[3:]
			if i := strings.IndexByte(rest, '/'); i < 0 {
				t.TaskCreate, p = rest, ""
			} else {
				t.TaskCreate, p = rest[:i], rest[i:]
			}
		case strings.HasPrefix(p, "/p/"):
			rest := p[3:]
			if i := strings.IndexByte(rest, '/'); i < 0 {
				t.Profile, p = rest, ""
			} else {
				t.Profile, p = rest[:i], rest[i:]
			}
		default:
			t.Suffix = strings.TrimPrefix(p, "/v1")
			return t
		}
	}
}

// pinnedProfiles narrows the profile set down to the named one (not named = return as-is).
//
// Naming (the `/p/<name>` in the URL) means "use this one for this call". That used to be
// honored only after resolving, by truncating the slice (steps[:1]), and it had a gap: when
// **the named profile has no usable candidate at all** (its only binding broken, or disabled),
// the chain head already stands on someone else's profile, and the truncation leaves exactly
// "silently swapped to someone else" -- the thing naming most wants to forbid. Field
// 2026-11-04: ds's only binding was broken, `--profile=ds` requests either 404ed (a lookup
// defect) or, after that was fixed, quietly ran on another profile.
//
// So the narrowing happens **before building the chain**: no second profile's candidates get
// in, and the later truncation only enforces "no swap inside the same profile either". A
// missing name returns an empty set -- the caller reports "this profile has no usable
// candidate" instead of falling back to the global chain.
func pinnedProfiles(profiles []*domain.Profile, name string) []*domain.Profile {
	for _, p := range profiles {
		if p.Name == name {
			// New slice: the snapshot's copy is shared, and must not be changed by a caller's append.
			return []*domain.Profile{p}
		}
	}
	return nil
}

// testChain is for test injection only, bypassing disk config and real upstreams. The
// production path is always nil. It is what keeps unit tests off the network -- see
// docs/10-testing-security.md, "tests do not go out; going out is a failure".
var testChain func(tier string) []resolve.Step

// handleCountTokens answers count_tokens locally (the rough-estimate fallback).
//
// Claude Code asks for token counts periodically (the context meter and the auto-compact
// threshold both rely on it), but not every upstream has this endpoint -- measured on an
// aggregator (2026-09, api.rvcompute.com): /messages 200 while /messages/count_tokens 404.
// So it first tries to forward for the real value (forwardCountTokens, learned per provider),
// and only comes here when that does not take: a rough estimate from request body bytes / 4
// (English ~4 chars/token; Chinese UTF-8 ~3 bytes/char, 1 char/token, so the estimate runs
// high -- for a context meter, compressing early is harmless).
//
// Per-round real counts come from the usage in the /messages response, and never pass here.
func (s *Server) handleCountTokens(w http.ResponseWriter, reqID uint64, body []byte) {
	n := len(body) / 4
	s.logf("[proxy] %s", i18n.T("#{req} count_tokens ({bytes} bytes) -> local rough estimate {n} tokens", i18n.A{"req": reqID, "bytes": len(body), "n": n}))
	writeJSON(w, 200, map[string]interface{}{"input_tokens": n})
}

// forwardCountTokens forwards count_tokens to the upstream for the real value. True when it took.
//
// Why it is worth it: locally we only have a rough estimate; the upstream's tokenizer is the
// real value, and both the context meter and the auto-compact threshold rely on it. But it is
// a private anthropic-dialect endpoint, missing from openai-dialect upstreams and many
// aggregators, so it is learned per (provider, model), a lazy probe at the gate level:
//
//	never probed → one shot (that is itself the probe); 404/405 = explicitly absent,
//	          record it, then fall back to the local estimate without burning requests;
//	          2xx = present, record it, then always take the real value
//	connect failure / 429 / 401 → not learned ("not now" ≠ "not there"), this shot goes local
//
// Model injection: a count_tokens request carries no model field, so fill in the **main tier**
// chain head -- Claude Code's main loop runs on the opus slot (after four-tiering, opus = the
// normal tier, 2026-09-16; it used to be heavy), so the count uses the tokenizer that will
// handle this conversation. No chain walk, no circuit breaking: a failed count is not upstream illness.
func (s *Server) forwardCountTokens(w http.ResponseWriter, r *http.Request,
	body []byte, tgt Target, reqID uint64) bool {

	head, ok := s.mainLoopHead(tgt)
	if !ok {
		return false
	}
	if ctOK, known := dialect.Supports(head.Binding.Provider, head.Binding.Model, dialect.CapCountTokens); known && !ctOK {
		return false // probed: this upstream has no count_tokens
	}

	// Fill in the model field (requests usually carry none); if present, respect the client's
	var out []byte
	if m, has := rewrite.TopLevelString(body, "model"); has && m != "" {
		out = body
	} else {
		nb, err := rewrite.InsertTopLevelRaw(body, "model", []byte(strconv.Quote(head.Binding.Model)))
		if err != nil {
			return false
		}
		out = nb
	}

	target := head.Provider.URL("/messages/count_tokens")
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(out))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	setAuth(req.Header, head.Provider)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		if r.Context().Err() == nil {
			s.logf("[proxy] %s", i18n.T("#{req} count_tokens forward failed ({err}); falling back to local rough estimate", i18n.A{"req": reqID, "err": err}))
		}
		return false
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 404 || resp.StatusCode == 405:
		if dialect.MarkUnsupported(head.Binding.Provider, head.Binding.Model, dialect.CapCountTokens) {
			s.logf("[proxy] %s", i18n.T(
				"#{req} learned {name} has no count_tokens endpoint (upstream {code}); falling back to local rough estimate, no retry",
				i18n.A{"req": reqID, "name": head.Binding, "code": resp.StatusCode}))
		}
		metrics.Default.Inc("count_tokens.probe_404")
		return false
	case resp.StatusCode >= 400:
		s.logf("[proxy] %s", i18n.T("#{req} count_tokens upstream {code}; falling back to local rough estimate", i18n.A{"req": reqID, "code": resp.StatusCode}))
		return false
	}
	dialect.Mark(head.Binding.Provider, head.Binding.Model, dialect.CapCountTokens)
	metrics.Default.Inc("count_tokens.forwarded")

	rb, _ := ioutil.ReadAll(io.LimitReader(resp.Body, 1<<20))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Newgate-Route", "count_tokens -> "+head.Binding.String())
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(rb)
	s.logf("[proxy] %s", i18n.T("#{req} count_tokens -> {name} upstream true value: {value}", i18n.A{"req": reqID, "name": head.Binding, "value": trim(string(rb))}))
	return true
}

// pinnedProfile is the profile this call may not leave: the one named in the URL (`/p/<name>`),
// or -- before any of this existed -- the chain head whenever fallback is off.
//
// The second half is the 2026-11-19 fix. `newgate fallback off` used to mean only "truncate the
// chain to its head after building it", and that cannot express "do not leave this profile":
// when every candidate of the head profile was delisted by the health table, the builder walked
// into the next profile and the truncation happily kept **that** head. The user asked for
// claude, had fallback off, and got DeepSeek with no error anywhere -- reported on 2026-11-19,
// and the reason the resolver can only be told about it here: this is the only place that knows
// both the URL and the switch.
//
// It returns "" when neither applies, which the resolver treats as "no pin".
func pinnedProfile(tgt Target, st *domain.State) string {
	if tgt.Profile != "" {
		return tgt.Profile
	}
	if gatewaystate.FallbackOff(st) {
		// The chain head is what `active` is; the caller passes it as Opts.Active, and the
		// resolver's own head-pinning (Profile.Pinned, a config flag) is a different thing.
		return st.ActiveFor(tgt.TaskCreate)
	}
	return ""
}

// mainLoopHead is the main-loop tier (normal) chain head (with the full provider record).
// count_tokens carries no model, so forwarding fills it from here. Uses PrimaryBinding
// (ignoring circuit breaking): the first in the config is good enough for counting; no fallback.
func (s *Server) mainLoopHead(tgt Target) (resolve.Step, bool) {
	if testChain != nil {
		if steps := testChain("normal"); len(steps) > 0 {
			return steps[0], true
		}
		return resolve.Step{}, false
	}
	snap := s.snap()
	if snap == nil {
		return resolve.Step{}, false
	}
	active := tgt.Profile
	if active == "" {
		active = snap.State.ActiveFor(tgt.TaskCreate)
	}
	profiles := snap.Profiles
	if tgt.Profile != "" {
		profiles = pinnedProfiles(snap.Profiles, tgt.Profile)
	}
	b, ok := resolve.PrimaryBinding("normal", profiles, snap.Providers, active)
	if !ok {
		return resolve.Step{}, false
	}
	p, ok := snap.Providers.Providers[b.Provider]
	if !ok {
		return resolve.Step{}, false
	}
	return resolve.Step{Profile: active, Binding: b, Provider: p}, true
}

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	atomic.AddUint64(&s.requests, 1)
	metrics.Default.Inc("requests.total")

	// /api/hello: Claude Code's connectivity probe (HEAD/GET, no body). It asks "is the API
	// base alive" -- we are its API, so a local 200; falling into the model parsing below only
	// spams a run of 400 logs (observed 2026-09-09).
	if strings.HasSuffix(r.URL.Path, "/api/hello") {
		w.WriteHeader(200)
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		s.fail(w, 400, i18n.Ef(err, "cannot read request body: {err}", nil).Error())
		return
	}
	_ = r.Body.Close()

	tgt := parseTarget(r.URL.Path)

	// The identity-prefixed `/a/<agent>[/p/<profile>]/v1/models` and the bare `/v1/models` are
	// the same thing: list the tiers you can name. The mux mounts only the bare path on
	// handleModels, so the prefixed one lands here and got a 400 for having no model field.
	// After opencode's baseURL gained `/a/opencode` (2026-10-29; it had been missing, so the
	// gateway did not recognize it and chose a chain by global default), its /models takes
	// exactly this path. The list is client-independent, so hand it to the same handler.
	if tgt.Suffix == "/models" && r.Method == http.MethodGet {
		s.handleModels(w, r)
		return
	}

	// A GET / HEAD with no body is not a model call: answer 404 locally, **do not count a failure**.
	//
	// Observed 2026-11-04 while hooking up hermes: for a loopback base_url it probes which
	// local inference service this is (`/api/v1/models`, `/api/tags`, `/v1/props`, `/props`,
	// `/version`; see hermes' model_metadata.detect_local_server_type), one round per session.
	// These used to fall into the model parsing below, recording an "error 400: request body has
	// no top-level model" and a failure count per shot -- so status's err count grew with hermes'
	// session count, looking like the data plane was failing. They ask "do you have this
	// endpoint", and the honest answer is 404: the prober uses it to conclude "not ollama /
	// lm-studio", exactly what it expects from an ordinary OpenAI-compatible service.
	//
	// Only "GET/HEAD with an empty body" qualifies: requests with a body still take the path
	// below -- a POST missing model is a genuinely bad shape, and that 400 + count is deserved.
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && len(body) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{
			"error": map[string]interface{}{
				"type":    "not_found",
				"message": "[newgate] " + i18n.T("{method} {path} is not an endpoint newgate serves", i18n.A{"method": r.Method, "path": r.URL.Path}),
			},
		})
		return
	}

	// count_tokens: an Anthropic-protocol private endpoint, used by Claude Code for the context
	// meter. It must be caught **before** the model check -- some clients send it with no model
	// field. If the upstream has the endpoint, forward for the real value (learned per provider,
	// see forwardCountTokens); if not (an aggregator 404), fall back to the local rough estimate.
	if strings.HasSuffix(r.URL.Path, "/count_tokens") {
		reqID := atomic.LoadUint64(&s.requests)
		metrics.Default.Inc("count_tokens.total")
		if s.forwardCountTokens(w, r, body, tgt, reqID) {
			return
		}
		metrics.Default.Inc("count_tokens.local")
		s.handleCountTokens(w, reqID, body)
		return
	}

	// No JSON round trip. Read model out of the raw bytes only, and later replace only that span.
	inModel, ok := rewrite.TopLevelString(body, "model")
	if !ok || inModel == "" {
		// Include method and path: for this class of "unrecognized shape" request, without the
		// path there is no way to tell which client and which endpoint sent it.
		s.fail(w, 400, i18n.T("request body has no top-level model string field ({method} {path})",
			i18n.A{"method": r.Method, "path": r.URL.Path}))
		return
	}
	norm := protocol.NormalizeRole(inModel)
	snap := s.snap()
	if snap == nil {
		s.fail(w, 400, i18n.T("cannot read config — run `newgate doctor`", nil))
		return
	}
	st := snap.State
	stream0 := rewrite.TopLevelBool(body, "stream")
	reqID := atomic.LoadUint64(&s.requests)
	// Log on entry -- otherwise "the request never arrived" and "it arrived and is waiting on
	// the upstream" look the same in the log. req= is the client request body byte count: it is
	// how you reconcile with the upstream's logs (a 300k compact-sized input) to locate "the same
	// request"; without it only reqID remains, which does not match across systems.
	s.logf("[proxy] %s", i18n.T("#{req} <- {model} stream={stream} req={bytes} bytes  start", i18n.A{"req": reqID, "model": inModel, "stream": stream0, "bytes": len(body)}))
	if gatewaystate.DebugActive(st) {
		s.logf("[proxy] %s", i18n.T(
			"#{req} client request {method} {path}{headers}\n    body({bytes} bytes): {body}",
			i18n.A{"req": reqID, "method": r.Method, "path": r.URL.Path,
				"headers": headerDump(r.Header), "bytes": len(body),
				"body": truncate(string(redact(body)), 4000)}))
	}

	// ---- per-agent routing + fallback chain ----
	// each agent has its own chain head; the /p/ in the URL overrides it for this call
	active := tgt.Profile
	if active == "" {
		active = st.ActiveFor(tgt.TaskCreate)
	}
	// Naming (/p/<name>) narrows the profile set to that one before the chain is built;
	// see pinnedProfiles.
	profiles := snap.Profiles
	if tgt.Profile != "" {
		profiles = pinnedProfiles(snap.Profiles, tgt.Profile)
	}
	suffix := tgt.Suffix
	stream := stream0

	var steps []resolve.Step
	var skips []resolve.Skip
	tier := norm
	toolOrigin, hasToolOrigin := thinkcache.Default.ContinuationOrigin(body)
	// The special routing plugin contributes a structured decision before normal parsing. The hot
	// path knows no plugin names and does not explain why it reroutes; tier, chain-head override,
	// timeouts and observability are all declared by the plugin.
	req := &special.Request{InModel: inModel, Tier: norm, Stream: stream0, Agent: tgt.TaskCreate}
	// special short-circuiter (special.Responder): some plugins can **answer for the upstream**
	// directly, sending not a single byte -- e.g. naked (modules/claudecode/classifier-naked.go)
	// short-circuits the Bash classifier into an approval. Asked before building the chain: a
	// successful short-circuit means there is no "chain" at all. The hot path knows no concrete
	// plugin; it only executes the verdict and leaves the trail.
	if resp, pluginName, short := special.Respond(body, req, st); short {
		s.writeShortCircuit(w, r, reqID, pluginName, resp,
			special.RespondNote(pluginName, st), active)
		return
	}

	route, routed := special.Route(body, req, st)
	routeTier := ""
	if routed {
		routeTier = route.Tier
	}
	if routeTier != "" {
		opts := resolve.Opts{
			Active:    active,
			Available: s.Filters.Admit,
			Rank:      func(provider, model string) int { return s.Filters.Rank(provider, model, len(body)) },
			MaxSteps:  st.Chain.Attempts(),
			Pinned:    pinnedProfile(tgt, st),
		}
		var rs []resolve.Step
		applied := false
		switch {
		case testChain != nil:
			rs = testChain(routeTier)
		case route.Head != nil:
			rs, skips, applied = resolve.OverrideChain("special:"+route.Plugin,
				*route.Head, routeTier,
				profiles, snap.Providers, opts)
			if !applied && route.OverrideFailNote != "" {
				s.logf("[proxy] #%d special_treatment %s: %s",
					reqID, route.Plugin, route.OverrideFailNote)
			}
		default:
			rs, skips = resolve.BuildChain(routeTier, profiles, snap.Providers, opts)
		}
		if len(rs) > 0 {
			steps, tier = rs, routeTier
			note := route.Note
			if applied && route.OverrideNote != "" {
				note = route.OverrideNote
			}
			if note != "" {
				s.logf("[proxy] #%d special_treatment %s: %s",
					reqID, route.Plugin, note)
			}
			if key := route.MetricKey(); key != "" {
				metrics.Default.Inc(key)
			}
		} else {
			// Fall back to normal parsing when the plugin's chain is empty: a routing
			// plugin cannot cut off a request.
			routeTier = ""
			routed = false
		}
	}
	if routeTier == "" {
		if testChain != nil {
			steps = testChain(norm)
		} else {
			// A tier name takes the tier chain; a concrete model name is mapped back to its
			// tier, with the named model first (docs/04-configuration.md) -- this backs
			// "the tool UI shows real model names".
			steps, skips, tier = resolve.ResolveRequest(norm, active, profiles, snap.Providers, resolve.Opts{
				Active:    active,
				Available: s.Filters.Admit,
				Rank:      func(provider, model string) int { return s.Filters.Rank(provider, model, len(body)) },
				MaxSteps:  st.Chain.Attempts(),
				Pinned:    pinnedProfile(tgt, st),
			})
		}
	}
	// Short-circuit: this call only walks the chain head, no swapping (2026-09-28).
	//
	// Two sources, cut at the same place -- what they want as a **result** is identical (send
	// only the first step, no swap, no waiting on the timeout budget), and splitting them would
	// make the three corollaries below proved twice:
	//
	//   - `--profile=xx` (tgt.Profile non-empty): the user named a profile, meaning "use this
	//     one". Before, it only pinned the profile as the chain head, while the tail still got
	//     others attached by profile priority -- when the named one went down it **silently
	//     swapped to another profile**, so config, the real name in the UI and the upstream
	//     actually used all disagreed. The criterion adds no state: launch writes `/p/<name>`
	//     into the base URL only for `--profile=xx`, parseTarget reads it out, so "has /p/"
	//     means "the user named one".
	//
	//     Since 2026-11-04 the profile set is narrowed to the named one before the chain is
	//     built (see pinnedProfiles), so the truncation here only enforces "no swap inside the
	//     same profile either". Both are needed: narrowing without truncating = fallback inside
	//     the named profile still swaps models; truncating without narrowing = when the named
	//     profile is wiped out, what gets truncated is someone else's chain head.
	//
	//   - `newgate fallback off` (global switch): turn off all fallback chains globally.
	//
	// The position is not arbitrary, three reasons:
	//
	//  1. **After the routing/chain-fetch block at 882-942**: the two `testChain != nil`
	//     branches are in that block too, so putting this earlier lets the test-injected chain
	//     bypass it -- those tests would then not test this. The truncation sits at the **only**
	//     exit, so every route (routing chain, override chain, tier chain, injected chain) passes it.
	//  2. **Before the deadline check below**: the budget condition (`i > 0`) only holds while
	//     the chain has a next step; with one step left i is always 0 -- so "ignore all timeouts"
	//     is not a separate branch but **implied**. Neither `st.Chain.Budget()` nor `st.Timeouts`
	//     changes by a word (the latter is the per-attempt transport timeout, which should keep
	//     working as usual; see `tr.ResponseHeaderTimeout` below).
	//  3. **Never write into st**: `st := snap.State` (841) is a **pointer** to the shared
	//     snapshot the watcher owns. `st.Chain.MaxAttempts = 1` here would change the live
	//     config of every concurrent request and every agent -- a data race plus cross-request
	//     contamination. The truncation touches only the local slice.
	//
	// Also why `Opts.MaxSteps = 1` cannot stand in: on that path `OverrideChain` (a routing
	// plugin gave an override binding) first truncates the tier chain to 1, then **inserts the
	// override step at the front**, yielding 2 steps and fallback firing anyway. Slice
	// truncation has no such gap.
	//
	// A trail is mandatory (not silent): the dropped steps and the reason go into the log, so
	// the user can answer "why was nobody swapped in this time".
	//
	// **An empty `why` means nothing was cut**: this variable is not a formatting intermediate,
	// it is the only criterion for "cut or not". Writing `switch { case ...: log }` and leaving
	// `steps = steps[:1]` outside the switch reads like "truncation" but is actually **every
	// multi-stop request walking only the head** -- that is how it was written wrong once on
	// 2026-09-28: the three "nobody swapped" cases below stayed green anyway (that is the result
	// they want), and only the TestFallbackOnByDefaultStillSwaps control went red. That is why
	// it exists: three greens do not prove the switch is wired, only that truncation happened.
	if len(steps) > 1 {
		var why string
		switch {
		case tgt.Profile != "":
			why = i18n.N(
				"#{req} profile {profile} is pinned, dropping {n} fallback step: {dropped}",
				"#{req} profile {profile} is pinned, dropping {n} fallback steps: {dropped}",
				len(steps)-1, i18n.A{"req": reqID, "profile": tgt.Profile, "n": len(steps) - 1,
					"dropped": formatSteps(steps[1:])})
		case gatewaystate.FallbackOff(st):
			why = i18n.N(
				"#{req} fallback is off, dropping {n} chain step: {dropped}",
				"#{req} fallback is off, dropping {n} chain steps: {dropped}",
				len(steps)-1, i18n.A{"req": reqID, "n": len(steps) - 1,
					"dropped": formatSteps(steps[1:])})
		}
		if why != "" {
			s.logf("[proxy] %s", why)
			steps = steps[:1]
		}
	}
	if len(steps) == 0 {
		s.logf("[proxy] %s", i18n.T("#{req} no usable candidate; skipped: {skips}", i18n.A{"req": reqID, "skips": fmtSkips(skips)}))
		// A pinned call whose profile has nothing usable is **not** "unknown model" and not
		// "the tier has no candidate": the tier plainly has candidates, they are delisted right
		// now, and the only way out is the switch that paused the table. Say so -- the 2026-11-19
		// incident was a user reading "everything is fine, fallback off" while being served by
		// another profile's model.
		if pin := pinnedProfile(tgt, st); pin != "" {
			reason := "run `newgate tier " + tier + "` to see why each candidate was skipped"
			if gatewaystate.FallbackOff(st) {
				// Deliberately **not** naming any concrete strategy or switch here: the data plane
				// must not know policy names (see modules/gateway/direction_test.go). "Some policy
				// rejected them" plus the pointer at `newgate tier` is the whole truth this layer
				// owns; which one, and how to turn it off, is the control plane's business.
				reason = "fallback is off, so this call may not leave profile " + pin +
					"; `newgate tier " + tier + "` names the policy that skipped each candidate"
			}
			s.fail(w, 503, i18n.T(
				"profile {profile} has no usable candidate for {model} right now; {reason}",
				i18n.A{"profile": pin, "model": norm, "reason": reason}))
			return
		}
		if tier == "" {
			s.fail(w, 404, i18n.T(
				"model {model} is neither a known tier ({tiers}) nor bound in any profile; run `newgate tier` for available bindings",
				i18n.A{"model": norm, "tiers": strings.Join(domain.Roles, ", ")}))
		} else if norm != tier {
			// The name was recognized (it is bound to a tier), but that tier chain has no usable
			// candidate right now. The old code said "not in any profile's bindings" here -- field
			// 2026-11-04: ds, the only one bound to deepseek-flash, was in circuit open; seeing that
			// line the user went and checked the config, which was right, while the real cause (circuit
			// open) sat in the skip reason on the line above.
			s.fail(w, 404, i18n.T(
				"model {model} is bound to tier {tier}, but that tier has no usable candidate under profile {profile}; run `newgate tier {tier}` to see why each candidate was skipped",
				i18n.A{"model": norm, "tier": tier, "profile": active}))
		} else {
			s.fail(w, 404, i18n.T(
				"tier {tier} has no usable candidate under profile {profile} (tiers: {tiers}); run `newgate tier {tier}` to see why each candidate was skipped",
				i18n.A{"tier": tier, "profile": active, "tiers": strings.Join(domain.Roles, ", ")}))
		}
		return
	}
	deadline := start.Add(time.Duration(st.Chain.Budget()) * time.Millisecond)

	var lastMsg string
	var lastCode int
	var trail []string // for X-Newgate-Chain
	for i, a := range steps {
		isLast := i == len(steps)-1
		// The response rewriter for this shot (see special.ClaimEgress below). Reset at every
		// stop: a different upstream means a different request shape, so the previous
		// stop's claim does not apply to this one.
		var egress special.Egress
		if i > 0 && time.Now().After(deadline) {
			metrics.Default.Inc("chain.budget_exhausted")
			s.logf("[proxy] %s", i18n.T("#{req} chain budget {budget} ms exhausted, stopping at step {step}",
				i18n.A{"req": reqID, "budget": st.Chain.Budget(), "step": i}))
			trail = append(trail, "budget-exhausted")
			isLast = true
		}
		// Pure byte surgery: replace only the top-level model value, keep every other byte as is
		newBody, merr := rewrite.ReplaceTopLevelString(body, "model", a.Binding.Model)
		if merr != nil {
			s.fail(w, 500, i18n.Ef(merr, "rewriting model failed: {err}", nil).Error())
			return
		}
		if hasToolOrigin {
			candidate := &special.Request{
				InModel: inModel, Tier: tier, Model: a.Binding.Model,
				Provider: a.Binding.Provider, BaseURL: a.Provider.Base(suffix),
				Protocol: a.Provider.Protocol, Path: suffix, Stream: stream,
				Agent: tgt.TaskCreate, State: st, Quirks: quirk.Default,
			}
			if res := special.RebaseToolLoop(newBody, toolOrigin.Provider, toolOrigin.Model,
				candidate, pluginOff(st)); len(res.Notes) > 0 {
				for _, note := range res.Notes {
					s.logf("[proxy] #%d special_treatment %s", reqID, note)
				}
				if res.Changed {
					newBody = res.Body
				}
				for _, event := range res.Events {
					if key := event.MetricKey(); key != "" {
						metrics.Default.Inc(key)
					}
				}
			}
		}

		// tool schema patching: rewrite only the tools value, and only when something really needs
		// patching; messages / system / cache_control stay untouched byte for byte.
		if gatewaystate.RepairEnabled(st) {
			if toolsRaw, ok := rewrite.TopLevelRaw(newBody, "tools"); ok {
				repaired, changes, rerr := schema.Repair(toolsRaw)
				switch {
				case rerr != nil:
					s.logf("[proxy] %s", i18n.T("#{req} tools repair skipped (parse failed, sending as-is): {err}", i18n.A{"req": reqID, "err": rerr}))
				case len(changes) > 0:
					if nb, serr := rewrite.ReplaceTopLevelRaw(newBody, "tools", repaired); serr == nil {
						newBody = nb
						s.logf("[proxy] %s", i18n.N(
							"#{req} set \"required\": [] on {n} tool to pass strict validators (semantically a no-op): {changes}",
							"#{req} set \"required\": [] on {n} tools to pass strict validators (semantically a no-op): {changes}",
							len(changes), i18n.A{"req": reqID, "changes": changes}))
					} else {
						s.logf("[proxy] %s", i18n.T("#{req} tools write-back failed, sending as-is: {err}", i18n.A{"req": reqID, "err": serr}))
					}
				}
			}
		}

		// special_treatment: per-upstream quirk patches (gateway/special). The split against
		// schema patching -- that side is generic patching every strict validator needs, this side
		// is "only this upstream needs it", claimed by the plugin's own Match. (Routing the
		// classifier onto the light chain is not here -- see special.Route above.)
		if gatewaystate.SpecialEnabled(st) {
			patched := &special.Request{
				InModel:  inModel,
				Tier:     tier,
				Model:    a.Binding.Model,
				Provider: a.Binding.Provider,
				// Report against the base this request will actually use -- for an upstream whose dialects
				// split (provider.anthropic_url) the plugin must see the real one.
				BaseURL:  a.Provider.Base(suffix),
				Protocol: a.Provider.Protocol,
				Path:     suffix,
				Stream:   stream,
				Agent:    tgt.TaskCreate,
				State:    st,
				Quirks:   quirk.Default,
			}
			res := special.Apply(newBody, patched, pluginOff(st))
			counted := map[string]bool{}
			for _, n := range res.Notes {
				s.logf("[proxy] #%d special_treatment %s", reqID, n)
			}
			for _, event := range res.Events {
				key := event.MetricKey()
				if key != "" && !counted[key] {
					counted[key] = true
					metrics.Default.Inc(key)
				}
			}
			if res.Changed {
				newBody = res.Body
			}
			// Response side: ask whether this shot's response needs rewriting (e.g. the request side
			// downgraded a custom tool to function, so the response side must turn those function_call
			// entries back into custom_tool_call). Asked **after the request rewrite**: the claimer
			// decides which entries to change from what was just applied, so it must see the final
			// request. The original `body` goes along too -- which entries to change is usually judged
			// only from the copy the client sent (see special.Egressor).
			egress = special.ClaimEgress(patched, body, pluginOff(st))
		}

		target := a.Provider.URL(suffix)
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		// Attach the client's context: cancelling with ESC in opencode aborts the upstream request
		// at once, instead of letting it run the whole response and bill for it
		req, rerr := http.NewRequestWithContext(r.Context(), r.Method, target,
			bytes.NewReader(newBody))
		if rerr != nil {
			s.fail(w, 500, rerr.Error())
			return
		}
		s.dump(reqID, i, body, newBody)
		if gatewaystate.DebugActive(st) {
			s.logf("[proxy] %s", i18n.T(
				"#{req} sending upstream {target}\n    body({bytes} bytes, delta vs original {delta}): {body}",
				i18n.A{"req": reqID, "target": target, "bytes": len(newBody),
					"delta": len(newBody) - len(body),
					"body":  truncate(string(redact(newBody)), 4000)}))
		}
		copyHeaders(req.Header, r.Header)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Del("Content-Length")
		setAuth(req.Header, a.Provider)

		// Streaming cannot have a total timeout (long responses get cut), but the first-byte wait
		// must be bounded, or an upstream playing dead hangs forever. ResponseHeaderTimeout
		// covers exactly the response headers. The tight cap goes **only to the classifier** (it
		// blocks the interactive path; hanging = a frozen session, and the system marker pins it);
		// everything else -- including a 500KB+ non-streaming large input like /compact -- gets
		// the standard 150s: a big prefill is legitimately slow, and a blanket tight timeout only
		// strangles one stop after another (lesson observed 2026-09-09). All hot-reload from
		// state.json's timeouts; watch the false-kill rate in `newgate metrics`.
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.ResponseHeaderTimeout = st.Timeouts.FirstByte()
		if routed && route.FirstByteTimeout > 0 {
			tr.ResponseHeaderTimeout = route.FirstByteTimeout
		}
		client := &http.Client{Transport: tr, Timeout: func() time.Duration {
			if stream {
				return 0
			}
			return st.Timeouts.Total()
		}()}

		attemptStart := time.Now()
		resp, derr := client.Do(req)
		attemptTTFT := time.Since(attemptStart)
		// Report the route string by the **actually sent** body model -- a special plugin may have
		// switched mid to light (claude-bg's classifier), and logging the chain step's model sends
		// people looking in the wrong direction (2026-09-09: log said glm-5.3, dump said air).
		sentModel := a.Binding.Model
		if m, has := rewrite.TopLevelString(newBody, "model"); has && m != "" {
			sentModel = m
		}
		routeStr := fmt.Sprintf("%s -> %s/%s", inModel, a.Binding.Provider, sentModel)

		if derr != nil {
			// The client left on its own (ESC, closed window, client-side timeout) is not the upstream's
			// fault.
			//
			// The consequence of not having this gate was measured: one cancellation taken as an
			// smt-claude connect failure → one failure recorded → the chain walks on to
			// smt-deepseek, still with the already-dead context, so **every candidate fails
			// instantly** and one ESC opens the circuit on every provider along the chain (the log shows
			// three providers "temporarily pulled" together). Real requests then have no candidate
			// and get a 502 -- the user sees "everything died after I cancelled" with no way back.
			//
			// So: record no failure, trip no circuit, do not walk the chain, and write no 502
			// (nobody is on the other side any more).
			if cerr := r.Context().Err(); cerr != nil {
				metrics.Default.Inc("client.cancel")
				s.logf("[proxy] %s", i18n.T("#{req} {route} client cancelled during connect ({err}); stopping the whole chain",
					i18n.A{"req": reqID, "route": routeStr, "err": cerr}))
				return
			}
			if strings.Contains(derr.Error(), "timeout awaiting response headers") {
				if stream {
					metrics.Default.Inc("timeout.first_byte.stream")
				} else {
					metrics.Default.Inc("timeout.first_byte.non_stream")
				}
			}
			o := policy.Outcome{
				Provider: a.Binding.Provider, Model: a.Binding.Model,
				Binding: a.Binding.String(), Kind: policy.ConnectionFailed,
				IsLast: isLast, RequestBytes: len(body),
			}
			v := s.outcome(o)
			if v.Attribute {
				atomic.AddUint64(&s.failures, 1)
			}
			hint := ""
			if strings.Contains(derr.Error(), "timeout awaiting response headers") {
				waitLimit := st.Timeouts.FirstByte()
				if routed && route.FirstByteTimeout > 0 {
					waitLimit = route.FirstByteTimeout
				}
				hint = i18n.T("  (first byte exceeded {limit} — upstream stalling or queued)", i18n.A{"limit": waitLimit})
			}
			s.logf("[proxy] %s", i18n.T("#{req} {route} connect failed: {err}{note}{hint}", i18n.A{"req": reqID, "route": routeStr, "err": derr, "note": v.Note, "hint": hint}))
			lastMsg, lastCode = i18n.T("upstream {provider} connect failed: {err}", i18n.A{"provider": a.Binding.Provider, "err": derr}), 502
			trail = append(trail, fmt.Sprintf("%s(conn)", a.Binding))
			if advance(o, v) {
				metrics.Default.Inc("chain.step_failed")
				s.logf("[proxy] %s", i18n.T("advancing to next chain step: {step}", i18n.A{"step": steps[i+1]}))
				continue
			}
			s.fail(w, 502, lastMsg)
			return
		}

		// Settled: hand this response to the client
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			// The upstream spoke. Read the whole error text out as evidence (it is the verdict's input
			// and the record to persist), then hand "walk the chain or not / keep this ledger or not /
			// what trail to leave" to the registered policy in **one** call.
			//
			// Before 2026-09-17 this was two branches judging separately: the transferable one kept
			// the ledger unconditionally, the settled one first exempted shape errors. The inconsistent
			// criteria meant the same reasoning-passback 400 pulled the card mid-chain but not at the
			// chain end -- the mid-chain shot pulled a still usable deepseek. Now there is one
			// criterion, and it is a pure function that can be exhaustively tested.
			eb, _ := ioutil.ReadAll(io.LimitReader(resp.Body, 256*1024))
			_ = resp.Body.Close()
			o := policy.Outcome{
				Provider: a.Binding.Provider, Model: a.Binding.Model,
				Binding: a.Binding.String(), Kind: policy.RejectedStatus,
				Status: resp.StatusCode, Body: eb,
				IsLast: isLast, FallbackOn400: st.Chain.FallbackOn400,
				RequestBytes: len(body),
			}
			v := s.outcome(o)
			if v.Attribute {
				atomic.AddUint64(&s.failures, 1)
			}
			s.learnQuirks(reqID, a.Binding.Provider, a.Binding.Model, resp.StatusCode, eb)
			if advance(o, v) {
				s.logf("[proxy] %s", i18n.T("{route} -> {status}{note}  upstream said: {body}",
					i18n.A{"route": routeStr, "status": resp.StatusCode, "note": v.Note,
						"body": trim(string(eb))}))
				// The advance-along-chain shot must leave claim evidence too. A shape 400 going to a
				// laxer upstream is the only path that can succeed, so "verdict: keep going" is its most
				// common ending; no trail means the client gets a 200 while the log cannot show the
				// middle provider refused (the 2026-09-17 silent downgrade had exactly this shape).
				//
				// Here generic err-* evidence is **deliberately not written** (a whole-body dump would add
				// a few MB per fallback to disk/logs), so base is passed empty -- the dedicated log still
				// fires, anchored on the tag and criterion name, not on a file.
				if ev := v.Evidence; ev != nil {
					s.recordShapeEvidence(reqID, a.Binding.String(), ev, "", body, newBody, eb,
						r.Header, resp.Header, routeStr)
				}
				s.logf("[proxy] %s", i18n.T("advancing to next chain step: {step}", i18n.A{"step": steps[i+1]}))
				metrics.Default.Inc("chain.step_failed")
				lastMsg, lastCode = trim(string(eb)), resp.StatusCode
				trail = append(trail, fmt.Sprintf("%s(%d)", a.Binding, resp.StatusCode))
				continue
			}
			base, evErr := s.saveErrEvidence(reqID, resp.StatusCode, body, newBody, eb,
				r.Header, resp.Header, routeStr)
			if evErr != nil {
				s.logf("[proxy] %s", i18n.T(
					"#{req} upstream {code}, writing evidence failed (the directory is creatable but files are not writable; check permissions/disk — CLAUDE.md §3.1 records this trap): {err}",
					i18n.A{"req": reqID, "code": resp.StatusCode, "err": evErr}))
			} else {
				s.logf("[proxy] %s", i18n.T("#{req} upstream {code}, full evidence saved to {base}.*", i18n.A{"req": reqID, "code": resp.StatusCode, "base": base}))
			}
			// The final shot uses the **same** trail implementation as the advance-along-chain one (see
			// recordShapeEvidence): the generic err-* evidence was just written here, so its base name
			// goes along as the anchor for the dedicated log.
			if ev := v.Evidence; ev != nil {
				s.recordShapeEvidence(reqID, a.Binding.String(), ev, base, body, newBody, eb,
					r.Header, resp.Header, routeStr)
			}
			s.logf("[proxy] %s", i18n.T("#{req} upstream raw: {body}", i18n.A{"req": reqID, "body": truncate(string(redact(eb)), 2000)}))
			s.logf("[proxy] %s", i18n.T("#{req} body we sent ({bytes} bytes): {body}", i18n.A{"req": reqID,
				"bytes": len(newBody), "body": truncate(string(redact(newBody)), 4000)}))

			for k, vs := range resp.Header {
				if respHopHeaders[http.CanonicalHeaderKey(k)] || http.CanonicalHeaderKey(k) == "Content-Length" {
					continue
				}
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.Header().Set("X-Newgate-Route", routeStr)
			w.Header().Set("X-Newgate-Profile", a.Profile)
			w.Header().Set("X-Newgate-Chain", chainHeader(trail, a))
			w.Header().Set("X-Newgate-Evidence", filepath.Base(base))
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(eb)
			return
		}
		ok := policy.Outcome{
			Provider: a.Binding.Provider, Model: a.Binding.Model,
			Binding: a.Binding.String(), Kind: policy.Succeeded,
			RequestBytes: len(newBody), TTFT: attemptTTFT,
		}
		s.outcome(ok)
		s.Filters.Observe(ok)
		if i > 0 {
			metrics.Default.Inc("chain.failover")
		}
		if gatewaystate.DebugActive(st) {
			s.logf("[proxy] %s", i18n.T("#{req} upstream response headers {code}{headers}", i18n.A{"req": reqID, "code": resp.StatusCode, "headers": headerDump(resp.Header)}))
		}
		s.logf("[proxy] %s", i18n.T(
			"#{req} {route}  {path}  {status}  first byte {ms} ms  stream={stream}  profile={profile}{failover}",
			i18n.A{"req": reqID, "route": routeStr, "path": suffix, "status": resp.StatusCode,
				"ms": time.Since(start).Milliseconds(), "stream": stream, "profile": a.Profile,
				"failover": map[bool]string{true: i18n.T("  (failover)", nil)}[i > 0]}))

		for k, vs := range resp.Header {
			if respHopHeaders[http.CanonicalHeaderKey(k)] {
				continue // Transfer-Encoding / Connection are managed by Go's own server
			}
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.Header().Set("X-Newgate-Route", routeStr)
		w.Header().Set("X-Newgate-Profile", a.Profile)
		w.Header().Set("X-Newgate-Chain", chainHeader(trail, a))
		if i > 0 {
			// A swap must be announced explicitly, never silent (docs/05-gateway.md)
			// HTTP headers carry latin-1 only and mangle Chinese -- ASCII only, details in the log
			w.Header().Set("X-Newgate-Failover", fmt.Sprintf("%s -> %s (upstream %d; see: newgate logs)",
				steps[0].Profile, a.Profile, lastCode))
		}
		w.WriteHeader(resp.StatusCode)

		// Forward chunk by chunk. Not a byte of the response body changes, and the whole response is
		// never buffered -- **unless** this shot's response was claimed (special.ClaimEgress, asked
		// before sending the request).
		//
		// A claimed shot is split at SSE event boundaries before forwarding: splitting changes only
		// **when** bytes are written, never the bytes (each event goes to the claimer as-is, who
		// leaves it untouched when saying no change). With no claim the original byte-for-byte
		// passthrough fast path runs -- the promise "with no patch, forwarding is forwarding" does
		// not get more expensive because one extension point was added.
		//
		// An observer sits on the side path, recording the reasoning content the upstream emits
		// into thinkcache so the next round can put it back (the client strips it; see
		// modules/deepseek/st-reasoning.go). It is **purely read-only**: it gets the bytes already
		// written to the client, takes no part in forwarding, and misreading at worst costs this
		// round's cache entry.
		flusher, canFlush := w.(http.Flusher)
		ob := thinkcache.NewObserver()
		var nonStream []byte // non-stream: the whole body must be parsed, so buffer it first (bounded)
		buf := make([]byte, 32*1024)
		var chunks int
		var bytesOut int64

		// Is this shot's response claimed (special.ClaimEgress, asked before sending the request)?
		// Only then split SSE events; otherwise it is the original byte-for-byte fast path.
		egressOn := stream && egress != nil
		// Egress rewriting must leave a trail (where "not silent" lands on this path): it changes
		// **bytes coming back from the upstream**, so the user's copy does not match what the
		// upstream gave, and only the log can explain why.
		var egressEvents int
		var sp *sseSplitter
		writeOut := func(p []byte) error {
			_, err := w.Write(p)
			return err
		}
		if egressOn {
			sp = newSSESplitter(w, egress, func(orig, out []byte) { egressEvents++ })
			writeOut = sp.write
		}
		for {
			n, rderr := resp.Body.Read(buf)
			if n > 0 {
				if werr := writeOut(buf[:n]); werr != nil {
					metrics.Default.Inc("client.cancel")
					s.logf("[proxy] %s", i18n.T("#{req} client disconnected (forwarded {chunks} chunks / {bytes} bytes): {err}",
						i18n.A{"req": reqID, "chunks": chunks, "bytes": bytesOut, "err": werr}))
					return
				}
				if stream {
					// The observer gets the upstream's **raw bytes**, not the rewritten ones: thinkcache must
					// put back the upstream's original reasoning, a patched shape is no help, and a broken
					// shape would make the whole round miss the cache.
					ob.Write(buf[:n])
				} else if len(nonStream) < 8<<20 {
					nonStream = append(nonStream, buf[:n]...)
				}
				chunks++
				bytesOut += int64(n)
				if canFlush {
					flusher.Flush() // emit on every read, do not wait for the buffer to fill
				}
			}
			if rderr != nil {
				switch {
				case rderr == io.EOF:
					if sp != nil {
						// Tail: many upstreams' last event has no trailing blank line; not emitting it loses bytes.
						if ferr := sp.flush(); ferr != nil {
							metrics.Default.Inc("client.cancel")
							s.logf("[proxy] %s", i18n.T("#{req} client disconnected (forwarded {chunks} chunks / {bytes} bytes): {err}",
								i18n.A{"req": reqID, "chunks": chunks, "bytes": bytesOut, "err": ferr}))
							return
						}
					}
					if egressEvents > 0 {
						s.logf("[proxy] %s", i18n.N(
							"#{req} response rewritten on the way out ({n} event): upstream bytes differ from what the client got",
							"#{req} response rewritten on the way out ({n} events): upstream bytes differ from what the client got",
							egressEvents, i18n.A{"req": reqID, "n": egressEvents}))
					}
					if !stream && len(nonStream) > 0 {
						ob.ObserveBody(nonStream)
					}
					origin := thinkcache.Origin{
						Profile: a.Profile, Provider: a.Binding.Provider, Model: a.Binding.Model,
					}
					if nb, nk := ob.CommitWithOrigin(thinkcache.Default, origin); nb > 0 {
						// Byte count and key count only, never content. A key is `tool:<id>`, an **identity**,
						// not content -- the only thread tying "this round stored it" to the next round's "put the
						// original back / nothing to put back" (see skipCause in st-reasoning.go). Added 2026-09-18.
						s.logf("[proxy] %s", i18n.N(
							"#{req} recorded {bytes} bytes of reasoning and {n} key {keys} this round; replaying them to the client next round",
							"#{req} recorded {bytes} bytes of reasoning and {n} keys {keys} this round; replaying them to the client next round",
							nk, i18n.A{"req": reqID, "bytes": nb, "keys": keysForLog(ob.Keys())}))
					} else if ntc := ob.ToolCalls(); ntc > 0 {
						// Tool calls but not one byte of reasoning: next round **none** of those history messages
						// can be restored (the plugin invents no placeholder; see the st-reasoning.go header), and
						// that is **the upstream never gave it**, not us losing it. The two cases call for opposite
						// handling (one is accept-only, one is investigate the cache), so this line must exist --
						// otherwise, reading only the patch side's log afterwards, "upstream gave nothing" gets
						// mistaken for a cache failure and someone fixes something that is not broken.
						//
						// Print **the request's thinking config** along with it: DeepSeek does not accept Anthropic's
						// adaptive (Claude Code's default) and emits not one thinking block on receiving adaptive --
						// the request side is fully valid and the log all green, so only printing this value shows
						// "the cache is not broken, the upstream simply did not think".
						s.logf("[proxy] %s", i18n.N(
							"#{req} upstream sent no reasoning this round ({n} tool call, request thinking={thinking}, tool id {keys}); wire layer: {wire}. None of these history messages can be replayed next round (no placeholders are fabricated)",
							"#{req} upstream sent no reasoning this round ({n} tool calls, request thinking={thinking}, tool id {keys}); wire layer: {wire}. None of these history messages can be replayed next round (no placeholders are fabricated)",
							ntc, i18n.A{"req": reqID, "thinking": thinkingTagOf(newBody),
								"keys": keysForLog(ob.Keys()), "wire": ob.Wire()}))
					}
					s.learnStreamFailure(reqID, a.Binding.Provider, a.Binding.Model, ob)
					if stream {
						s.logf("[proxy] %s", i18n.T("#{req} stream ended normally: {chunks} chunks / {bytes} bytes / {ms} ms total",
							i18n.A{"req": reqID, "chunks": chunks, "bytes": bytesOut,
								"ms": time.Since(start).Milliseconds()}))
					} else {
						s.logf("[proxy] %s", i18n.T("#{req} non-stream response ended: {bytes} bytes / {ms} ms total",
							i18n.A{"req": reqID, "bytes": bytesOut, "ms": time.Since(start).Milliseconds()}))
					}
				case r.Context().Err() != nil:
					metrics.Default.Inc("client.cancel")
					s.logf("[proxy] %s", i18n.T("#{req} client cancelled; upstream cut off (saves the remaining tokens)", i18n.A{"req": reqID}))
				default:
					// The response already started writing to the client and the upstream broke mid-way. No
					// swap is possible (written bytes cannot be recalled), but this is real availability trouble
					// -- this used to print one log line, leaving an upstream that breaks mid-stream every time
					// completely invisible in the health ledger.
					o := policy.Outcome{
						Provider: a.Binding.Provider, Model: a.Binding.Model,
						Binding: a.Binding.String(), Kind: policy.StreamCut,
						ResponseStarted: true, IsLast: isLast,
						RequestBytes: len(newBody), TTFT: attemptTTFT,
					}
					v := s.outcome(o)
					if v.Attribute {
						atomic.AddUint64(&s.failures, 1)
					}
					s.logf("[proxy] %s", i18n.T("#{req} upstream stream cut (forwarded {chunks} chunks / {bytes} bytes): {err}{note}",
						i18n.A{"req": reqID, "chunks": chunks, "bytes": bytesOut, "err": rderr, "note": v.Note}))
					// Before breaking, it may also have said "I rejected this shot" (Responses'
					// response.failed is followed by close). That sentence and the break are two things: the
					// break goes into the health ledger (the policy.Outcome above), **the rejection reason**
					// goes into the quirk table -- the latter is what keeps the next shot from hitting it, and
					// it is visible only through the observer (see learnStreamFailure).
					s.learnStreamFailure(reqID, a.Binding.Provider, a.Binding.Model, ob)
				}
				return
			}
		}
	}
}

// learnStreamFailure handles "the rejection the upstream reports **inside the event stream**".
//
// # Why this path must have it
//
// A Responses-dialect upstream with `stream: true` does not use HTTP status codes: it opens the
// stream with 200, then puts the failure into the event stream (`response.failed`). So the data
// plane's status-code-driven learnQuirks (learns only on `status >= 400`) **never fires** on the
// Codex path -- what the user sees is "reproducible, fixes itself after a while": first shot
// hits it, the second is fine? No: it is **always hit**, unless someone happened to probe that
// provider in this process. The line reported on 2026-09-22, "stream disconnected before
// completion: this model always thinks...", is exactly this shape.
//
// The observer (thinkcache.Observer) is the **only** place on this path reading those bytes, so
// the fact "the upstream rejected this shot" can only be asked of it (see Observer.StreamFailure).
//
// # Why the status code is written as 400
//
// `quirk.Table.Learn`'s criterion is "4xx + error text": 4xx means "the upstream rejected your
// request shape" (against 5xx's "the upstream itself is down"), and this failure **is** that
// family -- it just did not use a status code. What really matters is the text, the input the
// signature matches on, passed through one character unchanged.
//
// The learned result is exactly the same as hitting a real 400: `quirk.Default` gets marked at
// once, the next request's `always-thinks` Match turns green, the value the upstream refuses is
// edited in place in the request body (see fixResponsesEffort in modules/thinking/st-always.go);
// and it is recorded in `pendingQuirks`, persisted at shutdown/upgrade, so the next process's
// first shot does not hit it.
func (s *Server) learnStreamFailure(reqID uint64, provider, model string, ob *thinkcache.Observer) {
	msg, failed := ob.StreamFailure()
	if !failed {
		return
	}
	s.logf("[proxy] %s", i18n.T("#{req} {provider}/{model} reported a failure inside the event stream (HTTP 200, so the status code says nothing): {msg}",
		i18n.A{"req": reqID, "provider": provider, "model": model, "msg": trim(msg)}))
	s.learnQuirks(reqID, provider, model, 400, []byte(msg))
}

// flushPendingQuirks writes out the upstream quirks "learned on the forwarding path, not yet
// persisted".
//
// This entry used to live only in memory and was lost on restart, so "the first Codex → GLM
// shot after a restart" always hit the 400 again -- the "reproducible, heals by itself after a
// while" shape the user reported on 2026-09-22 (see Server.pendingQuirks and probe.PendingQuirks).
//
// **Both shutdown paths must call it**, and that is the only reason this function exists on its
// own: `Shutdown` (signal / control endpoint) and **graceful handover** (handing the socket to
// the new process) are two separate pieces of code, and upgrading uses the latter -- fixing only
// the former leaves "the first shot after an upgrade", the most common scenario, storing not a
// single byte, which is exactly the case the user reported (see the handoff block).
//
// A failed persist only reports, it changes no behavior: when the disk cannot be written, the
// worst case is the old "relearn once after restart", not a blocked shutdown (fail-open).
func (s *Server) flushPendingQuirks() {
	if err := s.pendingQuirks.Flush(); err != nil {
		s.logf("[probe] %s", i18n.T("upstream quirks learned from traffic could not be saved; the next restart relearns them: {err}",
			i18n.A{"err": err}))
	}
}

// learnQuirks learns an upstream's quirks from its error text, so the next request carries the
// patch automatically.
//
// Log only on a **new** learning -- one line per request for the same thing is ignored.
// What was learned must be said; that is part of "not silent": the user has to know we are
// adding things to their request from the next one on.
func (s *Server) learnQuirks(reqID uint64, provider, model string, status int, body []byte) {
	for _, what := range quirk.Default.Learn(provider, model, status, body) {
		s.logf("[proxy] %s", i18n.T("#{req} learned {provider}/{model} {what} — applied to later requests automatically (turn off with `newgate st`)",
			i18n.A{"req": reqID, "provider": provider, "model": model, "what": what}))
		// Also **record what was learned in the pending copy**: living only in memory, it would
		// vanish, together with this window, on a restart (see Server.pendingQuirks).
		//
		// Record **all known quirk bits for this target at this moment**, not just the one newly
		// learned: the union goes into the cache; missing a bit costs one extra 400 after a
		// restart, which is recoverable (see probe.MergeQuirks).
		s.pendingQuirks.Record(probe.Target{Provider: provider, Model: model},
			probe.QuirksOf(probe.Target{Provider: provider, Model: model}))
	}
}

// dump persists the "received" and "sent" request bodies when NEWGATE_DUMP=1.
// When investigating "did the proxy break the request", this is the only way to produce
// evidence. Keep only the last 30 sets -- one transcript entry from this session alone can
// run into MBs, and with no cap the disk fills quickly (pruned independently of the err-*
// error evidence; neither deletes the other).
func (s *Server) dump(reqID uint64, attempt int, in, out []byte) {
	if os.Getenv("NEWGATE_DUMP") == "" {
		return
	}
	dir := filepath.Join(paths.Config(), "dump")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	base := filepath.Join(dir, fmt.Sprintf("req-%06d-%d", reqID, attempt))
	var errs []error
	for _, f := range []struct {
		path string
		data []byte
	}{{base + ".in.json", in}, {base + ".out.json", out}} {
		if err := os.WriteFile(f.path, f.data, 0o600); err != nil {
			errs = append(errs, err)
		}
	}
	same := i18n.T("length delta vs original after rewrite: {diff} bytes", i18n.A{"diff": len(out) - len(in)})
	if err := errors.Join(errs...); err != nil {
		s.logf("[proxy] %s", i18n.T("#{req} dump write failed (check permissions/disk): {err}", i18n.A{"req": reqID, "err": err}))
	} else {
		s.logf("[proxy] #%d dump → %s.{in,out}.json  (%s)", reqID, base, same)
	}
	logx.PruneDirBy(dir, "req-", 30)
}

// redact masks anything key-like in requests/responses; logs and dumps both use it.
var secretPat = regexp.MustCompile(`(sk-[A-Za-z0-9_\-]{8,}|Bearer\s+[A-Za-z0-9_\-\.]{8,})`)

func redact(b []byte) []byte {
	return secretPat.ReplaceAll(b, []byte("[REDACTED]"))
}

func headerDump(h http.Header) string {
	var keys []string
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		v := strings.Join(h[k], ", ")
		if k == "Authorization" || k == "X-Api-Key" || k == "Api-Key" {
			v = "[REDACTED]"
		}
		sb.WriteString("\n      ")
		sb.WriteString(k)
		sb.WriteString(": ")
		sb.WriteString(v)
	}
	return sb.String()
}

// saveErrEvidence persists the full evidence when an upstream reports an error. This is the
// only thing that can show "did the proxy break the request", so it has no switch.
//
// **Return the error and the caller must report it honestly** (changed 2026-09-18): this used
// to be four `_ =`, while the call site logged unconditionally "full evidence saved". So when
// the write failed the user saw "saved" with not one byte on disk -- in CLAUDE.md §3.1's
// multi-user permission pit, that symptom's own words are "400 evidence 'saved' but never
// written". **Claiming it was saved when it was not is worse than not saving**: whoever
// investigates believes the evidence is there, spends time digging through directories, and
// then doubts they misremembered the path.
func (s *Server) saveErrEvidence(reqID uint64, status int, inBody, outBody, respBody []byte,
	reqHdr http.Header, respHdr http.Header, routeStr string) (string, error) {
	dir := filepath.Join(paths.Config(), "dump")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	base := filepath.Join(dir, fmt.Sprintf("err-%03d-req%06d", status, reqID))
	meta := i18n.T("route: {route}\nstatus: {status}\n\n--- client request headers ---{reqHeaders}\n\n--- upstream response headers ---{respHeaders}\n",
		i18n.A{"route": routeStr, "status": status,
			"reqHeaders": headerDump(reqHdr), "respHeaders": headerDump(respHdr)})
	var errs []error
	for _, f := range []struct {
		path string
		data []byte
	}{
		{base + ".client-sent.json", redact(inBody)},
		{base + ".we-sent.json", redact(outBody)},
		{base + ".upstream-said.json", redact(respBody)},
		{base + ".meta.txt", []byte(meta)},
	} {
		if err := os.WriteFile(f.path, f.data, 0o600); err != nil {
			errs = append(errs, err)
		}
	}
	// Prune only the err- prefix group: req-* also lives in the dump directory, each with its
	// own cap (see PruneDirBy(dir,"req-",30) at dump), and an empty prefix would delete the
	// other's files too -- error evidence would be squeezed out by req-* seconds after landing.
	logx.PruneDirBy(dir, "err-", 20)
	return base, errors.Join(errs...)
}

// saveShapeEvidence stores the scene in a **dedicated directory** when a request shape is
// rejected; it takes no part in the dump directory's req-*/err-* rolling cleanup -- these
// 400s are rare and fatal, and once lost cannot be reproduced (one transcript entry from this
// session can run into MBs, the dump directory fills in a few dozen sets, and this kind of
// 400 comes only after a long while, gone before the next chance). Today only the DeepSeek
// criterion claims it (modules/deepseek/shape.go), but the directory name comes from the
// detector's own name, so adding a criterion automatically adds an evidence directory,
// without touching the forwarding path.
//
// Layout: dump/<tag>-<criterion>/req-<id>-<unixnano>/, holding what the client sent, what we
// sent, what the upstream said, plus a per-item audit (which assistant messages got fields
// patched). Both name segments come from the policy's Evidence (today the tag is "shape-400"
// and the criterion is deepseek, so the path is byte-identical to before 2026-09-18). The cap
// is on total bytes only (512MB, a few hundred scenes); past that the oldest go -- normal
// investigation never reaches this, so it equals "never delete".
//
// Both name segments pass a whitelist filter before entering the path (see shapeDirName):
// policy is code other modules registered, and a name with `/` or `..` would let this
// function write outside the config directory. Same as saveErrEvidence: such a scene "cannot
// be reproduced once lost", so a failed write must be said (changed 2026-09-18, from five
// `_ =` plus an unconditional "scene archived").
func (s *Server) saveShapeEvidence(reqID uint64, ev *policy.Evidence, inBody, outBody, respBody []byte,
	reqHdr http.Header, respHdr http.Header, routeStr string) (string, error) {
	dir := filepath.Join(paths.Config(), "dump", shapeDirName(ev.Tag, ev.Subject),
		fmt.Sprintf("req-%06d-%d", reqID, time.Now().UnixNano()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	meta := i18n.T("route: {route}\nstatus: 400\ntag: {tag}\ndetector: {detector}\n\n--- client request headers ---{reqHeaders}\n\n--- upstream response headers ---{respHeaders}\n",
		i18n.A{"route": routeStr, "tag": ev.Tag, "detector": ev.Subject,
			"reqHeaders": headerDump(reqHdr), "respHeaders": headerDump(respHdr)})
	var errs []error
	for _, f := range []struct {
		name string
		data []byte
	}{
		{"client-sent.json", redact(inBody)},
		{"we-sent.json", redact(outBody)},
		{"upstream-said.json", redact(respBody)},
		{"audit.txt", []byte(special.AuditResponse(outBody))},
		{"meta.txt", []byte(meta)},
	} {
		if err := os.WriteFile(filepath.Join(dir, f.name), f.data, 0o600); err != nil {
			errs = append(errs, err)
		}
	}
	pruneShapeEvidence(filepath.Dir(dir), 512<<20)
	return dir, errors.Join(errs...)
}

// recordShapeEvidence is the **only** trail implementation for a shot "claimed by a shape
// criterion"; the final and the advance-along-chain paths share it: first the dedicated
// `[tag]` log (criterion name + generic evidence base name), then Evidence.Archive puts the
// scene into a dedicated directory exempt from rolling cleanup.
//
// Why it must be shared (the 2026-09-17 scene, see TestShapeAdvanceKeepsClaimedEvidence in
// forward_test.go): a shape 400 going to a laxer upstream is the only path that can succeed,
// so "verdict: keep along the chain" is precisely its most common ending. The old
// implementation kept this only in the final branch, so the advance shot neither archived nor
// logged -- the client got a 200 while the log could not show the middle provider refused and
// in what shape, exactly the form of that incident.
//
// base is the base name of the generic err-* evidence (just written on the final path); the
// advance-along-chain shot deliberately writes no generic err-* (a whole-body dump would add a
// few MB per fallback to disk/logs, against "affect as little as possible"), so the base is
// empty -- the dedicated log still fires, anchored on the tag and criterion name, not a file.
// Criterion name and tag both come from the registered policy; the forwarding path carries
// them as-is and does not interpret them.
func (s *Server) recordShapeEvidence(reqID uint64, binding string, ev *policy.Evidence, base string,
	inBody, outBody, respBody []byte, reqHdr, respHdr http.Header, routeStr string) {
	if ev == nil {
		return
	}
	// These outcomes must be greppable at a glance. The forwarding path knows no upstream-specific
	// string and does not know whose dialect this 400 is -- the literal `[shape-400]` and the
	// directory prefix come from the policy that claims it (see policy.Evidence).
	baseName := base
	if baseName != "" {
		baseName = filepath.Base(baseName)
	}
	s.logf("[%s] %s", ev.Tag, i18n.T("#{req} {binding} request shape rejected (detector {detector}; evidence {base}.*)",
		i18n.A{"req": reqID, "binding": binding, "detector": ev.Subject, "base": baseName}))
	if !ev.Archive {
		return
	}
	rdir, shErr := s.saveShapeEvidence(reqID, ev, inBody, outBody, respBody, reqHdr, respHdr, routeStr)
	if shErr != nil {
		s.logf("[%s] %s", ev.Tag, i18n.T("#{req} scene NOT saved (check permissions/disk): {err}", i18n.A{"req": reqID, "err": shErr}))
		return
	}
	s.logf("[%s] %s", ev.Tag, i18n.T("#{req} scene archived to {dir}/", i18n.A{"req": reqID, "dir": rdir}))
}

// shapeDirName squashes "tag + criterion name" into a safe directory name: keep only letters,
// digits and `-_.`, replace the rest with `_`, and turn an empty name into `unknown`. Both
// segments enter a file path, and both come from policy **other modules** registered --
// without a guard a registration entry would decide where files get written (one `../` writes
// outside the config directory).
//
// The two segments have different origins, hence two parameters: Tag is the name of **this
// class of outcome** (policy-given, today always "shape-400", so the path
// dump/shape-400-deepseek/ is byte-identical to before 2026-09-18), Subject is **who claimed
// it** (the shape detector's own name).
func shapeDirName(tag, subject string) string {
	var b strings.Builder
	if tag == "" {
		tag = "shape"
	}
	for _, r := range tag {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	b.WriteString("-")
	before := b.Len()
	for _, r := range subject {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == before {
		b.WriteString("unknown")
	}
	return b.String()
}

// pruneShapeEvidence caps the evidence directory by total bytes: past the cap it deletes the
// oldest subdirectories until back under it. More predictable than a count-based cap and safe
// for the disk -- but the cap is generous, so normal investigation never reaches it (see
// saveShapeEvidence).
func pruneShapeEvidence(dir string, maxBytes int64) {
	ents, err := ioutil.ReadDir(dir)
	if err != nil {
		return
	}
	var subdirs []os.FileInfo
	var total int64
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		subdirs = append(subdirs, e)
		total += dirSize(filepath.Join(dir, e.Name()))
	}
	if total <= maxBytes {
		return
	}
	// ioutil.ReadDir is already sorted by name, and subdirectory names carry unixnano, oldest first.
	for _, e := range subdirs {
		if total <= maxBytes {
			break
		}
		p := filepath.Join(dir, e.Name())
		total -= dirSize(p)
		_ = os.RemoveAll(p)
	}
}

func dirSize(dir string) int64 {
	var n int64
	ents, err := ioutil.ReadDir(dir)
	if err != nil {
		return 0
	}
	for _, e := range ents {
		if !e.IsDir() {
			n += e.Size()
		}
	}
	return n
}

// chainHeader describes which steps the chain really walked. ASCII only (headers are latin-1).
func chainHeader(trail []string, final resolve.Step) string {
	all := append(append([]string{}, trail...), final.Binding.String()+"(ok)")
	return strings.Join(all, " -> ")
}

func fmtSkips(skips []resolve.Skip) string {
	var parts []string
	for _, sk := range skips {
		t := sk.Target
		if t == "" {
			t = sk.Profile
		}
		parts = append(parts, t+"="+sk.Reason)
	}
	return strings.Join(parts, "; ")
}

// formatSteps lists the chain's steps on one line, for the "who the short-circuit cut" log.
// It uses Step's own `profile:provider/model` spelling -- the same vocabulary as `newgate tier`
// and X-Newgate-Chain; a second way of naming things has no place in the logs.
func formatSteps(steps []resolve.Step) string {
	parts := make([]string, 0, len(steps))
	for _, s := range steps {
		parts = append(parts, s.String())
	}
	return strings.Join(parts, ", ")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + i18n.T("…(truncated, {n} characters total)", i18n.A{"n": len(r)})
}

// keysForLog narrows thinkcache's key list to the part that belongs in the log.
//
// Keep only `tool:`-prefixed ones: those are **tool call ids**, pure identity strings that tie
// "what this round stored" precisely to the next round's "restored this one / nothing to
// restore". `text:`-prefixed ones are hashes of the content -- they serve as both key and
// digest, and this repo has a hard rule: **no path prints content**, and even a hash should
// not leak into the log to be compared against content elsewhere. So print prefix and count only.
//
// Cap 8: a response usually has 1~3 tool calls, enough; with dozens of parallel calls it still
// will not blow up one log line.
// pluginOff wraps "is this plugin individually switched off" into a func value -- that is the
// shape the special layer's API takes (it knows nothing of domain.State, nor should it). Both
// call sites share this one, so nobody writes the closure twice and misses one later.
//
// The switch state lives in state.json's ModuleConfig["gateway"], and reading it belongs to
// gatewaystate (see that package: gateway switch words stay out of the shared config type).
func pluginOff(st *domain.State) func(string) bool {
	return func(name string) bool { return gatewaystate.PluginOff(st, name) }
}

func keysForLog(keys []string) []string {
	var out []string
	texts := 0
	for _, k := range keys {
		if strings.HasPrefix(k, "tool:") {
			if len(out) < 8 {
				out = append(out, k)
			}
			continue
		}
		texts++
	}
	if texts > 0 {
		out = append(out, i18n.N("+{n} body key", "+{n} body keys", texts, i18n.A{"n": texts}))
	}
	return out
}

// thinkingTagOf extracts the raw thinking.type from the body **actually sent to the upstream**
// for this shot.
//
// Why print it: on 2026-09-18, investigating "why is there no reasoning content", this was the
// key field. DeepSeek does not accept Anthropic's adaptive (Claude Code's default) and emits
// not one thinking block on receiving it -- the request side is fully valid and the log all
// green, so only printing this value shows "the cache is not broken, the upstream simply did
// not think". The return value keeps its quotes; an empty string means **the field is absent**
// (another case again: the request never entered thinking mode at all).
func thinkingTagOf(body []byte) string {
	raw, has := rewrite.TopLevelRaw(body, "thinking")
	if !has {
		return i18n.T("(field absent)", nil)
	}
	t, ok := rewrite.TopLevelString(raw, "type")
	if !ok || t == "" {
		return i18n.T("(unrecognized)", nil)
	}
	return t
}

func trim(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 180 {
		return s[:180] + "…"
	}
	return s
}

func (s *Server) fail(w http.ResponseWriter, code int, msg string) {
	atomic.AddUint64(&s.failures, 1)
	s.logf("[proxy] %s", i18n.T("error {code}: {msg}", i18n.A{"code": code, "msg": msg}))
	// The error body must carry the word newgate and a diagnostic command (docs/05-gateway.md)
	writeJSON(w, code, map[string]interface{}{
		"error": map[string]interface{}{
			"type":    "newgate_error",
			"message": "[newgate] " + msg,
			"hint":    i18n.T("troubleshooting: newgate status / newgate doctor / newgate stop (restores a direct connection)", nil),
		},
	})
}

// The wait parameters for the upstream are not defined here: they are state.json's timeouts
// field (domain.Timeouts), hot-loaded by the watcher -- change the config, it takes effect; no
// rebuild. Defaults are in domain.Timeouts' accessors (streaming first byte 150s / non-streaming
// 12s / non-streaming total timeout 15min).

// respHopHeaders are the hop-by-hop headers that must be stripped from a response.
var respHopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Transfer-Encoding": true,
	"Te": true, "Trailer": true, "Upgrade": true, "Proxy-Authenticate": true,
}

var hopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true,
	"Authorization": true, "X-Api-Key": true, // always drop the client's fake key
	"Content-Length": true, "Host": true,
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if hopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func setAuth(h http.Header, p domain.Provider) {
	key := p.Key()
	switch p.Protocol {
	case "anthropic":
		h.Set("x-api-key", key)
		if h.Get("anthropic-version") == "" {
			h.Set("anthropic-version", "2023-06-01")
		}
	default:
		h.Set("Authorization", "Bearer "+key)
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	b, _ := json.MarshalIndent(v, "", "  ")
	_, _ = w.Write(append(b, '\n'))
}

// writeShortCircuit writes special.Responder's answer as this request's final response.
//
// A short-circuit plugin returns a **protocol-level response body** (for an anthropic Messages
// request, a valid messages JSON); this adds HTTP headers, logs, and emits metrics, so an
// "upstream call that never left" still leaves a trail on every observation band -- that is
// where "not silent" lands on the short-circuit path: the response is visible, the log is
// visible, the counter is visible, and the user always knows this shot never reached the upstream.
//
// `profile` is the one this shot **would have taken** (upstream was already worked out after
// the /p/<profile> override). The short-circuit happens before the chain is built, so "which
// chain this shot resolved to" has no answer; but "which profile the caller asked for" does --
// echoing it lets the caller answer "I named ds, why did this shot not go via ds". Before
// 2026-09-23 this was hardcoded `n/a`: a request to `/a/claude/p/ds/...` came back with
// `X-Newgate-Profile: n/a`, which looked like the profile was lost, when in fact a
// short-circuit simply never goes through a chain. After the echo, `n/a` is left only for
// assembly with "not even a default profile" (a skeleton dist without the config-chain part).
func (s *Server) writeShortCircuit(w http.ResponseWriter, _ *http.Request,
	reqID uint64, plugin string, body []byte, note, profile string) {
	metrics.Default.Inc("special." + plugin + ".shortcircuit")
	if note == "" {
		note = i18n.T("request short-circuited by special plugin {plugin} (no upstream call)", i18n.A{"plugin": plugin})
	}
	s.logf("[proxy] %s", i18n.T("#{req} {note} ({bytes} bytes)", i18n.A{"req": reqID, "note": note, "bytes": len(body)}))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Newgate-Route", "naked:"+plugin)
	w.Header().Set("X-Newgate-Chain", plugin)
	if profile == "" {
		profile = "n/a"
	}
	w.Header().Set("X-Newgate-Profile", profile)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
