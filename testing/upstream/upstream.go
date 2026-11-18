// Package upstream is the "fake upstream": an in-process httptest server that
// impersonates an OpenAI/Anthropic-compatible gateway, for Go integration tests.
//
// # Why it exists
//
// Before this the only fake upstream was python's mock/fake_upstream.py: its own
// process, a fixed port (mock/e2e_claude.sh uses 18081), python3 needed, bash
// only, records via /__mock/requests plus a re-parse. A Go test wanting an object
// that answers like a real upstream hand-wrote httptest stubs, one per package;
// no one had every dialect detail (SSE chunk order, DeepSeek thinking mode's
// strictest rules), so the behaviors most worth pinning in integration were not.
//
// A line-by-line port of the python version: routes, bodies, SSE chunk order,
// strict rules, control plane all copied; only "process + fixed port + HTTP for
// records" became httptest + in-process records (Requests/Reset). The python
// version **stays** - the bash e2e still uses it (mock/e2e_claude.sh and
// mock/e2e_reasoning_affinity.sh), and nothing here touches mock/; python is
// the only truth of both sides - read its passage before changing this.
// Usage
//
//	s := upstream.New(t)                       // auto-closed at test end
//	resp, _ := s.Client().Post(s.URL()+"/v1/messages", "application/json", body)
//	// ...assert on resp...
//	rec := s.Requests()[0]                     // what the proxy actually sent
//
// Three divergences (noted next to the code; do not call them bugs):
//
//   - Records use Go's http.Header (canonical case) and raw bytes; python stores
//     lowercased headers + a parsed dict. GET /__mock/requests still emits
//     python's shape (lowercase headers, parsed body, epoch seconds) for old scripts.
//   - Streaming responses have no Content-Length and no chunking
//     (Transfer-Encoding: identity + Connection: close), byte-for-byte the same
//     shape as python's raw stream; Go's default adds chunked to HTTP/1.1 responses
//     of unknown length, a framing the bash e2e never sees (see (*Server).stream).
//   - python throws and drops the connection on an unparsable request body (JSON
//     that is not a dict); here it degrades to {"__unparsed__": original}, no hang.
package upstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Constants: byte-for-byte identical to the literals in mock/fake_upstream.py.
// Cite these when asserting, do not copy them again - a copy is one more truth to drift.
const (
	// StrictReasoningMessage is the message text in python's STRICT_REASONING_ERR:
	// the behavior captured live in 2026-09 on new-api talking to official
	// DeepSeek. To assert "the strict rule really blocked it" match it as a
	// substring - the status code may be another 400, this sentence is the identity.
	StrictReasoningMessage = "The `reasoning_content` in the thinking mode must be passed back to the API. (request id: mock-e2e-strict)"

	// ThinkingText / ThinkingSignature are the fixed thinking block in the
	// non-streaming anthropic response. thinkcache's backfill assertions use them.
	ThinkingText      = "MOCK-THINKING-ORIGINAL"
	ThinkingSignature = "mock-sig"

	// ToolUseID / ToolName are the tool_use block appended when the request
	// carries tools. The id is fixed so e2e can assert which turn thinkcache backfilled.
	ToolUseID = "toolu_mock_1"
	ToolName  = "Read"

	// MessageID / ChatCompletionID are the response ids of the two dialects.
	MessageID        = "msg_mock"
	ChatCompletionID = "chatcmpl-mock"

	// DefaultChunkDelay is the default streaming chunk gap (python's 0.05s).
	DefaultChunkDelay = 50 * time.Millisecond
	// SlowChunkDelay is the chunk gap when the body carries mock_slow (python's
	// 0.5s). mock_slow is for cases where "one stream must cross a restart
	// window": the default 4 chunks x 0.5s ~ 2s lets the handover land mid-stream.
	SlowChunkDelay = 500 * time.Millisecond
)

// Server is an in-process fake upstream.
//
// One Server, one record table: New starts one per test (random port), and
// tests share no state - so /__mock/reset only means something within one test.
type Server struct {
	http *httptest.Server

	mu        sync.Mutex
	records   []Record      // 收到过的请求，按时间顺序
	failCode  int           // 非 0 = 武装了「下一发失败」（python 的 NEXT_FAIL）
	delay     time.Duration // 流式块间隔
	slowDelay time.Duration // mock_slow 时的块间隔
}

// New starts a fake upstream and closes it when the test ends.
//
// The address comes from httptest (random port on 127.0.0.1), so parallel
// tests cannot interfere, and there is no need for python's "curl-probe until
// the port is up" polling (see mock/e2e_claude.sh ch. 1) - it is already listening.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{delay: DefaultChunkDelay, slowDelay: SlowChunkDelay}
	s.http = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// URL is its root address (from httptest, like http://127.0.0.1:PORT). No
// trailing slash; join paths yourself: s.URL() + "/v1/messages".
func (s *Server) URL() string { return s.http.URL }

// Client is an http.Client that **does not go through a proxy**.
//
// Why not http.DefaultClient: it honors HTTP_PROXY/HTTPS_PROXY, and the session
// running this repo's tests often travels through newgate's proxy itself (root's
// claude is taken over by a PATH shim) - with a proxy in the environment, your
// requests to the fake upstream go through the real gateway first and all the
// assertions go haywire. httptest's own client has a nil Transport.Proxy, immune
// by construction. (mock/e2e_claude.sh's fake claude does the same thing.)
func (s *Server) Client() *http.Client { return s.http.Client() }

// Close shuts the fake upstream down at once. Cleanup calls it too; repeats are harmless.
//
// Explicit use: simulating "the upstream is down" - after Close, connections to
// that address fail outright, which tests whether the gateway's fallback moves
// to the next station; harsher than FailNext (which is a clean error response).
func (s *Server) Close() { s.http.Close() }

// Record is one request received.
//
// Correspondence with python's record (python stores a dict):
//
//	Path    python's rec["path"], the raw URI from the request line, **query included**
//	        (Claude Code sends things like /v1/messages?beta=true). Assert with
//	        strings.HasSuffix(r.Path, "/v1/messages"), not equality against a bare path.
//	Header  Go's http.Header, canonical case, Get case-insensitive; python stores
//	        lowercased headers. Read with rec.Header.Get("Authorization").
//	Body    raw bytes. python stores the parsed dict; bytes are kept here because
//	        this repo cares about byte-level evidence of "what the proxy actually
//	        sent" (field order, unknown keys, big ints live in the bytes). Fields: rec.JSON().
//	At      python stores epoch seconds (float); the Go side gives time.Time.
type Record struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
	At     time.Time
}

// JSON decodes Body into a field map, for asserting "which field did the proxy rewrite".
//
// Parsing goes through json.Number: the default float64 rewrites big integers
// into scientific notation (the "big int becomes ...92" trap in CLAUDE.md holds
// in records too). Returns an error when body is not an object or not JSON at all.
func (r Record) JSON() (map[string]any, error) {
	var fields map[string]any
	dec := json.NewDecoder(bytes.NewReader(r.Body))
	dec.UseNumber()
	if err := dec.Decode(&fields); err != nil {
		return nil, err
	}
	return fields, nil
}

// Requests returns all requests received so far, in time order.
//
// The slice is a copy, but Record's Header / Body are shared refs - read, do not modify.
func (s *Server) Requests() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, len(s.records))
	copy(out, s.records)
	return out
}

// Count is the number of requests received. Handier than len(Requests()) when counting shots.
func (s *Server) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

// Last is the last record; ok=false when no request has arrived yet.
func (s *Server) Last() (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.records) == 0 {
		return Record{}, false
	}
	return s.records[len(s.records)-1], true
}

// Reset clears the request records and disarms FailNext.
//
// Clearing the failure injection too is python's behavior (/__mock/reset clears
// both RECORDED and NEXT_FAIL), copied - "fails after Reset" is not a bug here.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = nil
	s.failCode = 0
}

// FailNext makes the next single request return status code, then disarms.
//
// "Disarms after use" is deliberate: only if the injection hits one shot can a
// test say "first failed, second worked" - every failover/fallback assert on
// the chain rests on it. code 0 = disarmed (0 is falsy: if NEXT_FAIL["code"]).
//
// Two easy traps (both old python behavior, copied over):
//   - failed shots **still enter the record table** (record then judge): retry is assertable;
//   - count_tokens takes the failure too - the failure check runs before it.
func (s *Server) FailNext(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failCode = code
}

// SetChunkDelay adjusts the delay between streaming chunks (0 = as fast as possible).
//
// Default is DefaultChunkDelay (50ms, python's value). 0 is for protocol-only
// cases that do not want to wait; a "proxy does not buffer the response" case
// must **raise** it - a buffering impl holds chunk one until the stream is out.
func (s *Server) SetChunkDelay(d time.Duration) {
	if d < 0 {
		d = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delay = d
}

// SetSlowChunkDelay adjusts the mock_slow chunk gap (default SlowChunkDelay).
//
// It is on its own dial, not changed by SetChunkDelay, because "handover does not
// cut in-flight streams" cases need normal streams fast, the restart-crossing one slow.
func (s *Server) SetSlowChunkDelay(d time.Duration) {
	if d < 0 {
		d = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slowDelay = d
}

// ---- routing ----

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.serveGet(w, r)
	case http.MethodPost:
		s.servePost(w, r)
	default:
		// python's BaseHTTPRequestHandler answers 501 (HTML error page) to
		// unimplemented methods. Status copied, body switched to JSON - callers are Go/curl.
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "unsupported method"})
	}
}

func (s *Server) serveGet(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/__mock/requests":
		writeJSON(w, http.StatusOK, s.wireRecords())
	case path == "/__mock/fail":
		// python: int(q.get("code", ["500"])[0]), default 500. For a non-integer
		// python throws and drops the connection; here it is a clear 400 (deliberate).
		code := 500
		if raw := r.URL.Query().Get("code"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "code must be an integer"})
				return
			}
			code = n
		}
		s.FailNext(code)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "next_fail": code})
	case strings.HasSuffix(path, "/v1/models"):
		// python compares for equality here (== "/v1/models"); the suffix match lets
		// prefixed variants (like /a/claude/p/ds/v1/models) hit the fake too - a superset.
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": []any{}})
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such path"})
	}
}

func (s *Server) servePost(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/__mock/reset" {
		// python handles reset before reading the body too: this shot is not recorded.
		s.Reset()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		// The client hung up halfway. python would wait forever per Content-Length;
		// here it is a plain 400, so a test never hangs reading the response.
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "failed to read the request body: " + err.Error()})
		return
	}
	body := decodeBody(raw)

	// Record first, judge failure after (same order as python): the failed shot keeps evidence too.
	s.appendRecord(Record{
		Method: r.Method,
		Path:   r.URL.RequestURI(),
		Header: r.Header.Clone(),
		Body:   raw,
		At:     time.Now(),
	})

	if code := s.takeFail(); code != 0 {
		writeJSON(w, code, map[string]any{"error": map[string]any{
			"message": fmt.Sprintf("mock forced %d", code),
		}})
		return
	}

	// anthropic's private endpoint: native anthropic upstreams have it, aggregators
	// often do not (api.rvcompute.com: 404). newgate forwards for the real value or
	// estimates locally by capability - the fake has it so forwarding is testable.
	// Judged **before** strict checks: count_tokens has no model, no thinking rules.
	if strings.HasSuffix(path, "/count_tokens") {
		writeJSON(w, http.StatusOK, map[string]any{"input_tokens": 42})
		return
	}

	if strictReasoningViolation(body) {
		writeJSON(w, http.StatusBadRequest, strictReasoningError())
		return
	}

	model := modelOf(body)
	anthropic := strings.HasSuffix(path, "/messages")
	if truthy(body["stream"]) {
		s.stream(w, r, model, anthropic, truthy(body["mock_slow"]), truthy(body["tools"]))
		return
	}
	if anthropic {
		writeJSON(w, http.StatusOK, anthropicMessage(model, truthy(body["tools"])))
		return
	}
	writeJSON(w, http.StatusOK, chatCompletion(model))
}

// ---- record table ----

func (s *Server) appendRecord(rec Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, rec)
}

// takeFail consumes the armed failure code (disarms after use).
func (s *Server) takeFail() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	code := s.failCode
	s.failCode = 0
	return code
}

// delays snapshots the two chunk gaps. python reads module-level constants at
// _stream entry, so a change mid-stream does not affect a stream already
// emitting - same timing copied, so concurrent SetChunkDelay never speeds up a stream midway.
func (s *Server) delays(slow bool) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slow {
		return s.slowDelay
	}
	return s.delay
}

// wireRecord is the wire shape of /__mock/requests - **copied from python's dict**
// (path/method/headers/body/at) so bash scripts and curl see exactly the same.
// Differences from Go's Record: headers are a lowercased single-value map, body is
// parsed JSON (else {"__unparsed__": original}, python's fallback), time is epoch seconds.
type wireRecord struct {
	Path    string            `json:"path"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    any               `json:"body"`
	At      float64           `json:"at"`
}

func (s *Server) wireRecords() []wireRecord {
	s.mu.Lock()
	records := make([]Record, len(s.records))
	copy(records, s.records)
	s.mu.Unlock()

	// Empty records must marshal as [], not null: python gives [], and the one-liners
	// in e2e_claude.sh do for x in r directly - null makes them TypeError on the spot.
	out := make([]wireRecord, 0, len(records))
	for _, rec := range records {
		headers := make(map[string]string, len(rec.Header))
		for key, values := range rec.Header {
			if len(values) == 0 {
				continue
			}
			// python's dict comprehension lets the later duplicate key win; keep it consistent.
			headers[strings.ToLower(key)] = values[len(values)-1]
		}
		out = append(out, wireRecord{
			Path:    rec.Path,
			Method:  rec.Method,
			Headers: headers,
			Body:    decodeBody(rec.Body),
			At:      float64(rec.At.UnixNano()) / float64(time.Second),
		})
	}
	return out
}

// ---- small helpers ----

func writeJSON(w http.ResponseWriter, code int, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Give the length explicitly (python's _json does too): no close-framing for non-streams.
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(code)
	_, _ = w.Write(raw)
}

// decodeBody parses the request body; unparsable returns {"__unparsed__": original}
// (python's fallback).
//
// Use json.Decoder + UseNumber, never json.Unmarshal's float64: it rewrites big
// ints into scientific notation (see CLAUDE.md); dump them as is, like python.
func decodeBody(raw []byte) map[string]any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{} // python: json.loads(raw or b"{}")
	}
	var parsed map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&parsed); err != nil {
		return map[string]any{"__unparsed__": string(raw)}
	}
	return parsed
}

// truthy is python's truthiness: None/False/0/""/[]/{} are false. Request bodies
// are JSON, so only so many types land in any.
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case json.Number:
		n, err := typed.Float64()
		return err != nil || n != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

// modelOf reads the request's model (python: body.get("model", "unknown")). Only
// "key absent" or a null value falls back to "unknown" - an empty string echoes
// as is, same as python (the response echoes it, so assertions read the real model).
func modelOf(body map[string]any) string {
	if model, ok := body["model"].(string); ok {
		return model
	}
	return "unknown"
}
