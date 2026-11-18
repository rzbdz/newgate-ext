package forward

import (
	"bytes"
	"encoding/json"
	"log"
	"sync"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/policy"
	"github.com/rzbdz/newgate-ext/modules/gateway/thinkcache"
)

// isolate points the config directory at a temp directory, so tests do not read (or corrupt)
// the real ~/.config/newgate.
//
// Moved here from thinkcache_test.go on 2026-09-20: those three cases asserted **distribution
// module** policy (DeepSeek's verbatim put-back / cross-agent migration), and followed their
// owner to the distribution repo, while the cases this helper still has (compact_test.go)
// need it.
func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	t.Setenv("NEWGATE_TARGET_DIR", dir)
	t.Setenv("HOME", dir)
	if _, err := store.Init(false); err != nil {
		t.Fatalf("initializing the temp config failed: %v", err)
	}
	thinkcache.Default = thinkcache.New(32<<20, 0) // every test starts from an empty cache
}

// newTestServer builds a **minimal system** test Server: not one note on the policy ledger.
//
// That default is deliberate. Everything the data plane itself should do -- forwarding, byte
// fidelity, swapping, logging, evidence persistence -- is fully testable on this surface, with
// **not a single piece of policy semantics**: whether a shape 400 counts against this
// provider, what the threshold is, what the metric is called, are all modules/breaker's
// business and it tests them itself (see breaker/plane_test.go).
//
// Before 2026-09-18 this injected a real health table (`breaker.NewTable()`), so forward's unit
// tests sat on criteria like "what counts as a shape error" and "threshold 2" -- the
// forwarding path knows none of that, it just happened to hold the only policy there was.
// After the inversion a policy is **one cell on the ledger**, and the default cell is empty.
//
// Note it ships **no Watch**, so the handler degrades to `store.Load()` reading the disk
// directly -- where the config comes from is decided by the package-level TestMain
// (modules_test.go) sandbox; see the comment there.
//
// Going through forward.New (instead of building a literal) is required: it creates the
// stopCh/drainCh shutdown channels, and the control-stop test asserts exactly "the channel is
// closed after the token is verified". This once read `&Server{Port: 0, Health: ...}`, and
// only survived because no such assertion existed at that moment.
func newTestServer() *Server {
	return New("", 0, nil, nil, policy.New())
}

// newLoggingTestServer is newTestServer plus proxy logs collected into the returned buffer --
// "not silent is a hard requirement" assertions (what was rewritten, who claimed the
// criterion, where evidence was stored) can only be verified on the log, not from the
// response and status alone.
//
// The buffer returned is **locked**, not a bytes.Buffer: logs are written by the HTTP handler
// goroutine while assertions read from the test goroutine, and "the response body is fully
// read" does not mean the handler has exited -- it still prints those closing lines (like
// stream ended normally) afterwards. A bare bytes.Buffer here is a data race bound to happen
// (measured 2026-09-23 with `go test -race ./...`: `bytes.(*Buffer).String()` against
// `log.(*Logger).output()`), and its red is deceptive too -- it names **one** test that used
// the new log line, looking like that line is at fault.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newLoggingTestServer() (*Server, *syncBuffer) {
	buf := &syncBuffer{}
	s := newTestServer()
	s.Logger = log.New(buf, "", 0)
	return s, buf
}

// scriptedFilter is forward's test policy: it **records every outcome the data plane hands it**
// and answers from a script.
//
// Why write one in tests instead of importing modules/breaker and using its: the import would
// be a cycle (breaker → gateway → forward), and more importantly -- forward's unit tests must
// lock **whether the facts reported by the data plane are right and the verdict executed
// right**, not breaker's ledger semantics. The criterion's truth table lives in breaker's own
// tests, the wiring in testing/system. It is the same layering rule as "do not hardcode
// upstream strings in criteria" (see the shapeTestDetector note in forward_test.go).
type scriptedFilter struct {
	mu       sync.Mutex
	outcomes []policy.Outcome
	docs     map[string]string
	verdict  func(policy.Outcome) policy.Verdict
}

func newScriptedFilter() *scriptedFilter { return &scriptedFilter{} }

func (f *scriptedFilter) Name() string { return "scripted" }
func (f *scriptedFilter) Why() string {
	return "forward unit-test policy: record facts + answer by script"
}

func (f *scriptedFilter) Judge(o policy.Outcome) policy.Verdict {
	f.mu.Lock()
	f.outcomes = append(f.outcomes, o)
	f.mu.Unlock()
	if f.verdict == nil {
		return policy.Verdict{}
	}
	return f.verdict(o)
}

// Observe takes the successful shot; the data plane walks the same Judge path, so nothing
// extra here.
func (f *scriptedFilter) Observe(policy.Outcome) {}

// Doc adds one section to the status document, to lock "a contributor's fields merge into
// the top level".
func (f *scriptedFilter) Doc() map[string]json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.docs) == 0 {
		return nil
	}
	out := map[string]json.RawMessage{}
	for key, value := range f.docs {
		out[key] = json.RawMessage(value)
	}
	return out
}

// seen returns every outcome the data plane handed to policy (a copy).
func (f *scriptedFilter) seen() []policy.Outcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]policy.Outcome(nil), f.outcomes...)
}

// seenOf picks only one provider's outcomes.
func (f *scriptedFilter) seenOf(provider string) []policy.Outcome {
	var out []policy.Outcome
	for _, o := range f.seen() {
		if o.Provider == provider {
			out = append(out, o)
		}
	}
	return out
}

// probeFilter implements stage C only: it takes the probe conclusions, locking "the data plane
// only does I/O and translation".
type probeFilter struct {
	mu   sync.Mutex
	got  []policy.ProbeObservation
	acks []policy.ProbeAck
}

func (f *probeFilter) Name() string { return "probe-recorder" }
func (f *probeFilter) Why() string  { return "forward unit-test policy: record probe observations" }

func (f *probeFilter) Doc() map[string]json.RawMessage { return nil }

func (f *probeFilter) ObserveProbes(obs []policy.ProbeObservation) []policy.ProbeAck {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, obs...)
	return f.acks
}

func (f *probeFilter) observed() []policy.ProbeObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]policy.ProbeObservation(nil), f.got...)
}
