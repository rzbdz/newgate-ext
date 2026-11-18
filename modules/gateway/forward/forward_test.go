package forward

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"context"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/gateway/metrics"
	"github.com/rzbdz/newgate-ext/modules/gateway/policy"
)

// A representative slice of SSE: with tool_call fragments, Unicode, empty data, big integers.
var sseChunks = []string{
	`data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n",
	`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"background_cancel","arguments":""}}]}}]}` + "\n\n",
	`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"id\":\"CJK\"}"}}]}}]}` + "\n\n",
	`data: {"usage":{"total_tokens":9007199254740993}}` + "\n\n",
	"data: [DONE]\n\n",
}

// fakeUpstream emits SSE chunk by chunk with gaps -- to verify the proxy does not buffer.
func fakeUpstream(t *testing.T, gap time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := ioutil.ReadAll(r.Body)
		var m map[string]json.RawMessage
		if err := json.Unmarshal(body, &m); err != nil {
			w.WriteHeader(400)
			return
		}
		// assert that what arrives is the real model name, not the tier name
		var got string
		_ = json.Unmarshal(m["model"], &got)
		if got != "real-model-1" {
			t.Errorf("upstream received model = %q, want real-model-1", got)
		}
		if r.Header.Get("Authorization") != "Bearer sk-real" {
			t.Errorf("upstream received Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		for _, c := range sseChunks {
			_, _ = io.WriteString(w, c)
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(gap)
		}
	}))
}

// TestStreamByteFaithful asserts: the response body equals, byte for byte, what the upstream sent.
func TestStreamByteFaithful(t *testing.T) {
	up := fakeUpstream(t, 0)
	defer up.Close()

	body, arrivals := streamThroughProxy(t, up.URL, 0)
	want := strings.Join(sseChunks, "")
	if body != want {
		t.Errorf("the response body was changed\n want: %q\n got:  %q", want, body)
	}
	if len(arrivals) == 0 {
		t.Fatal("received no chunks at all")
	}
}

// TestStreamNotBuffered asserts: chunks arrive one after another, not all dumped at the end.
// This is the lifeblood of the streaming experience -- with buffering the content test still
// passes, yet to the user it looks frozen.
func TestStreamNotBuffered(t *testing.T) {
	gap := 60 * time.Millisecond
	up := fakeUpstream(t, gap)
	defer up.Close()

	_, arrivals := streamThroughProxy(t, up.URL, gap)
	if len(arrivals) < 2 {
		t.Fatalf("received data only %d times, cannot tell whether it was buffered", len(arrivals))
	}
	first, last := arrivals[0], arrivals[len(arrivals)-1]
	spread := last - first
	// the upstream spread len(sseChunks)*gap in total, and the time span the client observes
	// should be of the same order; if buffered, spread would be near 0
	if spread < gap {
		t.Errorf("looks buffered: the gap between the first and last chunk is only %v (upstream gap %v, %d chunks)",
			spread, gap, len(sseChunks))
	}
	t.Logf("first chunk arrived after %v, first-to-last span %v, %d reads in total - not buffered",
		first, spread, len(arrivals))
}

// streamThroughProxy starts a real proxy instance, sends one streaming request, and returns the
// full response body plus the relative moment each read got data.
func streamThroughProxy(t *testing.T, upstreamURL string, _ time.Duration) (string, []time.Duration) {
	t.Helper()

	srv := newTestServer()
	handler := http.HandlerFunc(srv.handleProxy)

	// Substitute a parser that only knows real-model-1 for the real config:
	// build the route directly, bypassing the config disk read
	testChain = func(role string) []resolve.Step {
		if role != "heavy" {
			return nil
		}
		return []resolve.Step{{
			Profile:  "test",
			Binding:  domain.Binding{Provider: "test-prov", Model: "real-model-1"},
			Provider: testProvider(upstreamURL),
		}}
	}
	defer func() { testChain = nil }()

	front := httptest.NewServer(handler)
	defer front.Close()

	reqBody := `{"model":"newgate/heavy","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(front.URL+"/v1/chat/completions",
		"application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := ioutil.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	if te := resp.Header.Get("Transfer-Encoding"); te != "" {
		t.Errorf("Transfer-Encoding must not be passed through to the client, got %q", te)
	}

	start := time.Now()
	var out bytes.Buffer
	var arrivals []time.Duration
	br := bufio.NewReader(resp.Body)
	buf := make([]byte, 4096)
	for {
		n, err := br.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
			arrivals = append(arrivals, time.Since(start))
		}
		if err != nil {
			break
		}
	}
	return out.String(), arrivals
}

func testProvider(baseURL string) domain.Provider {
	return domain.Provider{BaseURL: baseURL + "/v1", APIKey: "sk-real", Protocol: "openai"}
}

// TestClientCancelAbortsUpstream asserts: when the client disconnects mid-way, the upstream
// request is cancelled too. Without this, a user pressing ESC in opencode still has the
// upstream run the whole response out and bill for it.
func TestClientCancelAbortsUpstream(t *testing.T) {
	upstreamDone := make(chan error, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		for i := 0; i < 50; i++ {
			if _, err := io.WriteString(w, fmt.Sprintf("data: chunk-%d\n\n", i)); err != nil {
				upstreamDone <- err
				return
			}
			if fl != nil {
				fl.Flush()
			}
			select {
			case <-r.Context().Done():
				upstreamDone <- r.Context().Err() // the upstream observed the cancellation
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
		upstreamDone <- nil // ran to completion = the cancellation never propagated
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{
			Profile:  "test",
			Binding:  domain.Binding{Provider: "test-prov", Model: "real-model-1"},
			Provider: testProvider(up.URL),
		}}
	}
	defer func() { testChain = nil }()

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	req, _ := http.NewRequest("POST", front.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"newgate/heavy","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	// read a few chunks then disconnect
	buf := make([]byte, 256)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatalf("could not even read the first chunk: %v", err)
	}
	_ = resp.Body.Close()

	select {
	case err := <-upstreamDone:
		if err == nil {
			t.Error("the upstream ran all 50 chunks - the cancellation did not propagate, which burns tokens for nothing")
		} else {
			t.Logf("upstream observed the cancellation: %v - propagation is correct", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("within 3 seconds the upstream neither finished nor observed the cancellation")
	}
}

// TestNonStreamResponseByteFaithful a non-streaming response must also be returned byte for
// byte, and must not drop or alter the upstream's headers.
func TestNonStreamResponseByteFaithful(t *testing.T) {
	// deliberately odd key order, big integers, Unicode, non-compact whitespace
	upBody := "{\n  \"z\": 1e10,\n  \"a\": 9007199254740993,\n" +
		"  \"txt\": \"café \\\"quotes\\\" and \\\\ backslashes\",\n  \"nil\": null\n}"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Trace", "abc-123")
		w.Header().Set("Eo-Cache-Status", "MISS")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, upBody)
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding:  domain.Binding{Provider: "p", Model: "real-model-1"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"newgate/heavy"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := ioutil.ReadAll(resp.Body)

	if string(got) != upBody {
		t.Errorf("the response body was changed\n want: %q\n got:  %q", upBody, got)
	}
	// the upstream's business headers must pass through
	if resp.Header.Get("X-Upstream-Trace") != "abc-123" {
		t.Error("a custom upstream header was lost")
	}
	if resp.Header.Get("Eo-Cache-Status") != "MISS" {
		t.Error("an upstream cache-status header was lost")
	}
	// our own diagnostic header should be there
	if resp.Header.Get("X-Newgate-Route") == "" {
		t.Error("missing X-Newgate-Route")
	}
}

// TestErrorResponseByteFaithful when the upstream reports an error, its error text must reach
// the client unchanged -- swallowing upstream error messages is the main source of debug
// nightmares.
func TestErrorResponseByteFaithful(t *testing.T) {
	errBody := `{"error":{"message":"Invalid schema for function 'background_cancel': null is not of type \"array\"","type":"invalid_request_error"}}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, errBody)
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding:  domain.Binding{Provider: "p", Model: "real-model-1"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"newgate/heavy"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := ioutil.ReadAll(resp.Body)

	if resp.StatusCode != 400 {
		t.Errorf("the status code should pass through 400, got %d", resp.StatusCode)
	}
	if string(got) != errBody {
		t.Errorf("the upstream error text was changed\n want: %q\n got:  %q", errBody, got)
	}
	if resp.Header.Get("X-Newgate-Evidence") == "" {
		t.Error("4xx should carry X-Newgate-Evidence pointing at the evidence file")
	}
}

// shapeTestFilter is forward's own layer of test policy: it recognizes the 400 that "names
// reasoning_content and wants it passed back verbatim", and keeps a scene for it.
//
// Why write one here in forward's tests instead of importing modules/breaker or
// modules/deepseek: the forwarding path **knows no policy at all** (exactly why the
// 2026-09-18 inversion works -- the criterion is registered into the policy ledger by the
// module that knows the thing, and core only reads the opaque strings in Verdict.Evidence).
// Importing deepseek in a unit test would plug the just-removed coupling back in through the
// tests: change the real criterion's wording and this goes red, for a reason unrelated to
// forward's behavior.
//
// The split is "tests follow whoever knows the thing": the real criterion's truth table is in
// modules/deepseek/shape_test.go, breaker's table turning a shape 400 into a verdict is in
// modules/breaker/plane_test.go, and the **wiring** from a module's Start all the way to the
// forwarding path is in testing/system (real component graph + fake upstream); here we lock
// only forward's contract with the ledger -- what it must do when Evidence is non-empty.
// advance expresses this policy's answer to "swap after a claim or not": zero (false) = "no
// swap" (the chain-end shot, locking the final path), true = "keep along the chain" (breaker's
// real verdict for a shape 400 is exactly this: the same body is wrong no matter who sends it,
// but a laxer upstream is the only path that can succeed). Both endings must leave a trail,
// and the cases lock one side each.
type shapeTestFilter struct{ advance bool }

func (shapeTestFilter) Name() string { return "shape-test" }
func (shapeTestFilter) Why() string {
	return "forward unit test: recognizes the reasoning pass-back 400"
}

func (f shapeTestFilter) Judge(o policy.Outcome) policy.Verdict {
	if o.Kind != policy.RejectedStatus || o.Status != 400 {
		return policy.Verdict{}
	}
	if !bytes.Contains(o.Body, []byte("reasoning_content")) ||
		!bytes.Contains(o.Body, []byte("must be passed back")) {
		return policy.Verdict{}
	}
	// shape error: no accounting, but a trail.
	return policy.Verdict{
		Stop:     !f.advance,
		Evidence: &policy.Evidence{Tag: "shape-400", Subject: "test-shape", Archive: true},
	}
}

func (shapeTestFilter) Observe(policy.Outcome) {}

// TestShapeErrorIsCountedThenLoggedWithEvidence locks all of forward's obligations for an
// "outcome that needs a trail". Once policy gives Evidence, forward must:
//
//  1. pass the upstream's raw text through as-is (not silent: the client sees what it said);
//  2. print the **tag** (`[shape-400]`) and **who claimed it** in the log;
//  3. add a "scene archived" line;
//  4. archive the scene into dump/<tag>-<criterion>/ (a dedicated directory, exempt from the
//     req-*/err-* rolling cleanup).
//
// Field (2026-09-17, health.json + logs): deepseek-flash's 400 was recorded into the
// breaker as an availability failure, and two shots in a row pulled a perfectly usable
// binding, while probe stayed green -- a probe sends a minimal request and never triggers
// "thinking mode requires reasoning passed back". The summary put it plainly: a problem where
// "the same body is equally wrong on any provider" must not be charged to anyone's account.
//
// The accounting half (shape 400 stays out of the availability ledger) is now breaker's
// verdict, verified in modules/breaker/plane_test.go and testing/system; here we verify the
// data plane's obligations only.
func TestShapeErrorIsCountedThenLoggedWithEvidence(t *testing.T) {
	sandboxState(t, `{"port": 0}`)
	metrics.Default.Reset()
	t.Cleanup(func() { metrics.Default.Reset(); testChain = nil })

	reasoningErr := `{"type":"error","message":"The ` + "`reasoning_content`" +
		` in the thinking mode must be passed back to the API."}`
	hits := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, reasoningErr)
	}))
	defer up.Close()

	// Single-stop chain (IsLast): only the chain-end shot takes the "settled" branch, i.e. the
	// path that prints the dedicated log and stores the field evidence. A multi-stop chain misses it.
	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "ds",
			Binding:  domain.Binding{Provider: "ds-shape", Model: "deepseek-flash"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv, logBuf := newLoggingTestServer()
	if _, err := srv.Filters.Register(shapeTestFilter{}); err != nil {
		t.Fatalf("registering the policy: %v", err)
	}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	// three shots in a row: the verdict is constant, so each must leave the same scene.
	for i := 0; i < 3; i++ {
		resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
			strings.NewReader(`{"model":"newgate/heavy"}`))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("request %d status = %d, want 400 (should pass through as is, not another error)", i+1, resp.StatusCode)
		}
		if string(got) != reasoningErr {
			t.Fatalf("request %d upstream text was changed\n want: %q\n got:  %q", i+1, reasoningErr, got)
		}
	}
	if hits != 3 {
		t.Fatalf("upstream was hit %d times, want 3", hits)
	}
	// Not charged to any binding: the verdict gave no Attribute, so the counter must not move.
	if n := atomic.LoadUint64(&srv.failures); n != 0 {
		t.Errorf("an outcome that leaves a trace but is not booked added failures %d times", n)
	}

	// Tag and criterion name must reach the log: shape criteria belong to each upstream
	// separately, and saying only "a shape error was hit" is useless when two upstreams both
	// return 400. The forwarding path knows none of those strings; names are its only clue --
	// which is why it can only print them as-is.
	logs := logBuf.String()
	if !strings.Contains(logs, "[shape-400]") || !strings.Contains(logs, "test-shape") {
		t.Errorf("the log does not say which criterion claimed it:\n%s", logs)
	}
	// The machine tag on the archive line is the evidence directory name `shape-400-<criterion>`:
	// wording follows the language, a directory name does not (that is what stays greppable).
	if !strings.Contains(logs, "shape-400-") {
		t.Errorf("evidence was not written (these 400s are rare and fatal; once lost they cannot be reproduced):\n%s", logs)
	}

	// Evidence lands in a dedicated directory named tag + criterion → a different policy
	// automatically adds a directory, with no change to the forwarding path.
	dir := filepath.Join(os.Getenv("NEWGATE_HOME"), "dump", "shape-400-test-shape")
	ents, err := ioutil.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the evidence directory %s: %v (this kind of scene must be archived separately)", dir, err)
	}
	if len(ents) == 0 {
		t.Fatalf("the evidence directory %s is empty", dir)
	}
	for _, want := range []string{"client-sent.json", "we-sent.json", "upstream-said.json", "audit.txt"} {
		if _, err := os.Stat(filepath.Join(dir, ents[0].Name(), want)); err != nil {
			t.Errorf("the evidence directory is missing %s: %v", want, err)
		}
	}
}

// TestShapeAdvanceKeepsClaimedEvidence locks the trail of **the advance-along-chain shot**:
// when the verdict says "keep going" (the fallback_on_400=true path; a shape verdict should
// try another upstream anyway) and a criterion claimed this 400, the `[shape-400]` log line
// and the dedicated-directory archive **must both happen** -- even if the next stop ends up
// answering 200.
//
// Field (2026-09-17, see docs/11-troubleshooting.md): a shape 400 silently downgraded to
// another provider, the client got a 200, and the log could not show it. The old
// implementation kept the trail in the final branch, and the advance is precisely its most
// common ending -- so it neither archived nor logged, and the "exempt from rolling cleanup,
// never lost" docs promise became an empty phrase.
//
// It also locks the "affect as little as possible" half: the advance path writes **no**
// generic err-* evidence (a whole-body dump would add a few MB per fallback to disk/logs),
// so this asserts no err-400-* in the dump, proving the fix touched only logging and
// archiving, not the verdict.
func TestShapeAdvanceKeepsClaimedEvidence(t *testing.T) {
	sandboxState(t, `{"port": 0, "chain": {"fallback_on_400": true}}`)
	t.Cleanup(func() { testChain = nil })

	reasoningErr := `{"type":"error","message":"The ` + "`reasoning_content`" +
		` in the thinking mode must be passed back to the API."}`
	var shapeHits, okHits int
	shape := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		shapeHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, reasoningErr)
	}))
	defer shape.Close()
	okUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer okUp.Close()

	// two-stop chain: the first stop is picky (shape 400) → verdict: keep going → the
	// second takes it.
	testChain = func(role string) []resolve.Step {
		return []resolve.Step{
			{Profile: "ds", Binding: domain.Binding{Provider: "ds-shape", Model: "deepseek-flash"},
				Provider: testProvider(shape.URL)},
			{Profile: "other", Binding: domain.Binding{Provider: "other-ok", Model: "real-model-1"},
				Provider: testProvider(okUp.URL)},
		}
	}
	defer func() { testChain = nil }()

	srv, logBuf := newLoggingTestServer()
	if _, err := srv.Filters.Register(shapeTestFilter{advance: true}); err != nil {
		t.Fatalf("registering the policy: %v", err)
	}
	// The bystander only records facts, proving this shot really ran on the chain that
	// fallback_on_400 opened.
	watcher := newScriptedFilter()
	if _, err := srv.Filters.Register(watcher); err != nil {
		t.Fatalf("registering the observer policy: %v", err)
	}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"newgate/heavy","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(got) != `{"ok":true}` {
		t.Fatalf("should fail over along the chain to the second stop and get 200, got %d %q", resp.StatusCode, got)
	}
	if shapeHits != 1 || okHits != 1 {
		t.Fatalf("upstream hits = shape %d / ok %d, want 1/1 (a real failover must have happened)", shapeHits, okHits)
	}
	var sawFallback400 bool
	for _, o := range watcher.seen() {
		if o.Provider == "ds-shape" && o.Kind == policy.RejectedStatus && o.FallbackOn400 {
			sawFallback400 = true
		}
	}
	if !sawFallback400 {
		t.Fatal("this request did not go over a chain with fallback_on_400=true - the fixture had no effect, so the case cannot lock the failover path")
	}

	// Tag and criterion name must reach the log: shape criteria belong to each upstream
	// separately, the forwarding path knows none of those strings, names are its only clue,
	// so it can only print them as-is.
	logs := logBuf.String()
	if !strings.Contains(logs, "[shape-400]") || !strings.Contains(logs, "test-shape") {
		t.Errorf("the failover path did not say which criterion claimed it (that is the shape of a 'silent downgrade'):\n%s", logs)
	}
	// The machine tag on the archive line is the evidence directory name `shape-400-<criterion>`:
	// wording follows the language, a directory name does not -- that is what stays greppable.
	if !strings.Contains(logs, "shape-400-test-shape") {
		t.Errorf("the failover path did not archive the scene (these 400s are rare and fatal; once lost they cannot be reproduced):\n%s", logs)
	}
	dir := filepath.Join(os.Getenv("NEWGATE_HOME"), "dump", "shape-400-test-shape")
	ents, err := ioutil.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the evidence directory %s: %v (the failover path must archive too)", dir, err)
	}
	if len(ents) != 1 {
		t.Fatalf("one failover should leave exactly one scene, got %d", len(ents))
	}
	for _, want := range []string{"client-sent.json", "we-sent.json", "upstream-said.json", "audit.txt"} {
		if _, err := os.Stat(filepath.Join(dir, ents[0].Name(), want)); err != nil {
			t.Errorf("the evidence directory is missing %s: %v", want, err)
		}
	}
	// The "affect as little as possible" half: the advance path writes no generic err-*
	// whole-body dump.
	dumpDir := filepath.Join(os.Getenv("NEWGATE_HOME"), "dump")
	if all, err := ioutil.ReadDir(dumpDir); err == nil {
		for _, e := range all {
			if strings.HasPrefix(e.Name(), "err-400-") {
				t.Errorf("the failover path dropped a generic err-* evidence (each fallback adds a few MB, violating minimal impact): %s", e.Name())
			}
		}
	}
}

// TestShapeAdvanceWithoutClaimLeavesNoArchive is the reverse case: the first stop returned a
// 400 that **did not match** the shape criterion, the verdict is still "keep going"
// (fallback_on_400=true), but nobody claimed this shot -- so nothing is archived and the
// `[shape-400]` line must not appear either.
//
// It guards the boundary of the trail: evidence is the result of a **criterion claiming**, not
// of "seeing a 400". Loosen the criterion and the dedicated directory fills with ordinary
// validation errors, drowning the "never lost" evidence docs/11-troubleshooting.md promises.
func TestShapeAdvanceWithoutClaimLeavesNoArchive(t *testing.T) {
	sandboxState(t, `{"port": 0, "chain": {"fallback_on_400": true}}`)
	t.Cleanup(func() { testChain = nil })

	schemaErr := `{"error":{"message":"Invalid schema: missing required field 'name'"}}`
	var okHits int
	badSchema := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, schemaErr)
	}))
	defer badSchema.Close()
	okUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer okUp.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{
			{Profile: "bad", Binding: domain.Binding{Provider: "schema-bad", Model: "real-model-1"},
				Provider: testProvider(badSchema.URL)},
			{Profile: "other", Binding: domain.Binding{Provider: "other-ok", Model: "real-model-1"},
				Provider: testProvider(okUp.URL)},
		}
	}
	defer func() { testChain = nil }()

	srv, logBuf := newLoggingTestServer()
	// The criterion is registered, this body just does not match it -- exactly the boundary to lock.
	if _, err := srv.Filters.Register(shapeTestFilter{advance: true}); err != nil {
		t.Fatalf("registering the policy: %v", err)
	}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"newgate/heavy","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(got) != `{"ok":true}` {
		t.Fatalf("a non-shape 400 should continue along the chain and get 200, got %d %q", resp.StatusCode, got)
	}
	if okHits != 1 {
		t.Fatalf("the second stop was hit %d times, want 1 (if the case never reaches the failover path it passes vacuously)", okHits)
	}
	logs := logBuf.String()
	if strings.Contains(logs, "[shape-400]") {
		t.Errorf("the log claims shape evidence although no criterion matched:\n%s", logs)
	}
	if strings.Contains(logs, "scene archived to") {
		t.Errorf("it claims to have stored a scene although no criterion matched:\n%s", logs)
	}
	dir := filepath.Join(os.Getenv("NEWGATE_HOME"), "dump", "shape-400-test-shape")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("it created the dedicated evidence directory %s although no criterion matched (err=%v)", dir, err)
	}
}

// TestShapeDirNameIsPathSafe: both tag and criterion name enter a file path, and both come
// from policy **other modules** registered -- without a guard, a registration entry decides
// where files get written.
func TestShapeDirNameIsPathSafe(t *testing.T) {
	tests := []struct{ tag, subject, want string }{
		{"shape-400", "deepseek", "shape-400-deepseek"},
		{"shape-400", "a.b_c-1", "shape-400-a.b_c-1"},
		{"shape-400", "../escape", "shape-400-.._escape"},
		{"../x", "deepseek", ".._x-deepseek"},
		{"shape-400", "a/b", "shape-400-a_b"},
		{"shape-400", "", "shape-400-unknown"},
		{"", "deepseek", "shape-deepseek"},
		{"shape-400", "éé", "shape-400-__"},
	}
	for _, tt := range tests {
		if got := shapeDirName(tt.tag, tt.subject); got != tt.want {
			t.Errorf("shapeDirName(%q, %q) = %q, want %q", tt.tag, tt.subject, got, tt.want)
		}
	}
	// The result must never contain a separator: the anti-pattern for path joining is
	// "a string policy controls".
	for _, name := range []string{"../x", "a/b", "a\\b"} {
		for _, got := range []string{shapeDirName(name, "d"), shapeDirName("shape-400", name)} {
			if strings.ContainsAny(got, `/\`) {
				t.Errorf("shapeDirName returned %q which still contains a path separator", got)
			}
		}
	}
}

// TestOtherClientErrorDoesNotBlameTheProvider locks the **minimal system** side: with not one
// note on the ledger, the data plane's whole action for a 400 is **pass it through** -- no
// swap, no accounting, no trail, no evidence.
//
// On 2026-09-17 this test pointed the other way (asserting "a non-shape 400 must open the gate
// after two shots", reasoning "otherwise a genuinely schema-broken provider never gets pulled").
// Why it flipped:
//
//   - A 400 means "your request is wrong", and whether the upstream or the request is at fault
//     is **not a conclusion the data plane can draw**; pulling on a 400 lets one patch's blind
//     spot decide who gets pulled -- exactly the shape of the 2026-09-17 reasoning-passback
//     incident;
//   - "this upstream is simply unreachable" is not a conclusion the passive path can draw;
//     `newgate probe` is the authority: a probe sends a minimal valid request, a provider with
//     a wrong base or version gets pulled by probe on the spot, and it never misjudges because
//     of the shape of one round of the user's conversation.
//
// Deciding whose account this 400 goes on and whether to leave a trail is **policy's** job
// (today's copy is breaker/plane.go's Judge, truth table in plane_test.go next to it). The
// data plane has exactly one obligation: report the facts faithfully, then execute the verdict.
func TestOtherClientErrorDoesNotBlameTheProvider(t *testing.T) {
	schemaErr := `{"error":{"message":"Invalid schema: missing required field 'name'"}}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, schemaErr)
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding:  domain.Binding{Provider: "schema-bad", Model: "real-model-1"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv, logBuf := newLoggingTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	for i := 0; i < 3; i++ {
		resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
			strings.NewReader(`{"model":"newgate/heavy"}`))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 400 || string(got) != schemaErr {
			t.Fatalf("request %d should pass through 400 and the upstream text as is, got %d %q", i+1, resp.StatusCode, got)
		}
	}
	if n := atomic.LoadUint64(&srv.failures); n != 0 {
		t.Errorf("the minimal system booked %d of the 400s as upstream failures - no policy means no verdict, the kernel books nothing by default", n)
	}
	// Nobody wants the scene, so the archive line must not appear. The assertion is on the **source
	// language text** (source is the identity path at runtime, stable on CI with LANG=C),
	// not a translation.
	if logs := logBuf.String(); strings.Contains(logs, "scene archived to") {
		t.Errorf("nobody asked for a scene, yet the data plane stored one:\n%s", logs)
	}
}

// TestMinimalSystemForwardsEveryOutcomeToNobody asserts the "minimal system" is usable: with
// an empty policy ledger, successful forwarding still happens (everything admitted, swap by
// mechanism, no accounting).
//
// It guards policy's fallback direction. The four decision points' kernel defaults are "all
// candidates usable", "keep going if the response has not started and there is a next stop",
// "one section less on the control plane" and "nothing to do" -- write any one of them
// backwards (say, by default keeping candidates out of the chain) and the gateway becomes
// "with no breaker installed not one request gets out", a far worse failure than a broken breaker.
func TestMinimalSystemForwardsEveryOutcomeToNobody(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding:  domain.Binding{Provider: "p", Model: "real-model-1"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv := newTestServer()
	if !srv.Filters.Empty() {
		t.Fatal("newTestServer should give an empty ledger (minimal system)")
	}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"newgate/heavy"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := ioutil.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(got) != `{"ok":true}` {
		t.Fatalf("minimal-system forward failed: %d %q", resp.StatusCode, got)
	}
}

// TestClientCancelDuringConnectDoesNotBurnTheChain asserts: when the client cancels at the
// **connect stage**, do not retry along the chain, do not record a failure, **and do not hand
// this shot to any policy**.
//
// This is how it used to break (logs from 10.0.50.11): one cancellation taken as an smt-claude
// connect failure, the chain swapped to smt-deepseek -- but the context was already dead, so
// every candidate failed instantly and one ESC opened all three providers' breakers along the
// chain. Real requests then had no candidate and got a 502, with no way to trace the source.
//
// "Do not hand it to policy" is the extra safety this inversion added: in the old
// implementation the data plane judged "this is not the upstream's fault", so a cancellation
// **never** reached the accounting step. After the inversion the call belongs to policy, and
// a policy charging every ConnectionFailed would burn the chain again -- so the data plane
// simply does not report it: nobody is on the other side any more, and no one needs to know.
func TestClientCancelDuringConnectDoesNotBurnTheChain(t *testing.T) {
	hit := make(chan string, 8)
	// The upstream plays dead: it hangs on receiving a request. A cap as backstop, so a
	// failed assertion does not hang the whole test.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- r.URL.Path
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer up.Close()

	provs := []string{"prov-a", "prov-b", "prov-c"}
	testChain = func(role string) []resolve.Step {
		var out []resolve.Step
		for _, p := range provs {
			out = append(out, resolve.Step{
				Profile:  "test",
				Binding:  domain.Binding{Provider: p, Model: "real-model-1"},
				Provider: testProvider(up.URL),
			})
		}
		return out
	}
	defer func() { testChain = nil }()

	srv := newTestServer()
	watcher := newScriptedFilter()
	// Charge everything -- simulate the "greediest" policy. The data plane must not even ask it.
	watcher.verdict = func(policy.Outcome) policy.Verdict {
		return policy.Verdict{Attribute: true}
	}
	if _, err := srv.Filters.Register(watcher); err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", front.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"newgate/heavy","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")

	done := make(chan struct{})
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(ioutil.Discard, resp.Body)
			resp.Body.Close()
		}
		close(done)
	}()

	select {
	case <-hit: // the first candidate has been hit
	case <-time.After(3 * time.Second):
		t.Fatal("the upstream received no request")
	}
	cancel()
	<-done
	time.Sleep(150 * time.Millisecond) // let the handler finish

	// not one candidate further down the chain should be hit
	select {
	case p := <-hit:
		t.Fatalf("the client already cancelled, yet the chain retried the next candidate (%s)", p)
	default:
	}
	if n := atomic.LoadUint64(&srv.failures); n != 0 {
		t.Errorf("a client cancellation was booked as %d upstream failures", n)
	}
	// The point: a cancellation must not appear in any policy's input at all.
	for _, o := range watcher.seen() {
		t.Errorf("a client cancellation was reported to the policy as %q (%s) - nobody is on the other end anymore,"+
			"nobody needs to know about this request; reporting it would turn 'one ESC burns the whole chain' into"+
			"the policy's decision", o.Kind, o.Binding)
	}
}
