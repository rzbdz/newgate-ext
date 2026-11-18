package forward

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/gateway/metrics"
)

// This group of tests locks the short-circuit in forward.go (2026-09-28): **two unrelated
// routes end up with the same result here** -- send the chain head only, never swap.
//
//   - `--profile=xx`: the `/p/<name>` segment in the URL path (launch writes it only
//     when named); before this, it only pinned the profile as the chain head, while the
//     tail still got others attached by profile priority. When the named one went down it
//     **silently swapped to another profile** -- config, UI and the actual run: three things.
//   - `newgate fallback off`: turn off every fallback chain globally.
//
// The criterion is "was the second stop knocked on" (hit count), not "what came back": the
// status code can be the same either way, while "was someone swapped in" only the upstream knows.
//
// **This test proves this truncation, not the field event "the upstream is down and nobody is
// swapped in"**: truncation happens before `s.Filters.Admit` (that code sits inside
// resolve.ResolveRequest), so a candidate a policy judged unusable never gets a turn here.
// The real event (a provider really down, the breaker really pulling its card) is locked by
// mock/e2e_claude.sh -- the upstream there is really broken, so the assertion cannot pass by
// accident of "nobody was available to swap to".

const fallbackShapeBody = `{"type":"error","message":"The ` + "`reasoning_content`" +
	` in the thinking mode must be passed back to the API."}`

// fallbackRig is the scene for this group of tests: two fake upstreams + one front end.
type fallbackRig struct {
	srv   *Server
	logs  *syncBuffer
	front *httptest.Server
	hits1 *int64
	hits2 *int64
}

// newFallbackRig starts a two-stop chain: the first stop answers 400 with the shape error
// "thinking mode requires reasoning passed back verbatim", the second answers a normal 200.
//
// Both stops must be **really started httptest servers**, not faked with functions: the only
// meaningful criterion here is "was the second stop knocked on", and only a real server counts.
//
// shapeTestFilter{advance: true} keeps the first shot's verdict consistent with the field
// (whether a shape 400 may swap is policy's call, not the data plane's guess); it is inert
// for the second stop's 200. Note it **cannot influence** the two truncated cases: `isLast`
// is computed from len(steps) at the top of the loop body, so with one step left it is always
// true and `advance` becomes irrelevant to the verdict.
func newFallbackRig(t *testing.T) *fallbackRig {
	t.Helper()

	var h1, h2 int64
	up1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&h1, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, fallbackShapeBody)
	}))
	t.Cleanup(up1.Close)

	up2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&h2, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(up2.Close)

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{
			{Profile: "ds",
				Binding:  domain.Binding{Provider: "ds-shape", Model: "deepseek-flash"},
				Provider: testProvider(up1.URL)},
			{Profile: "other",
				Binding:  domain.Binding{Provider: "other-ok", Model: "real-model-1"},
				Provider: testProvider(up2.URL)},
		}
	}
	// testChain is a **package-level variable**, and store.Load() re-reads the disk per request.
	// Miss this reset and any later case gets this two-stop fake chain -- its two providers do
	// not exist in anyone else's sandbox, and the symptom is "another test mysteriously swapped
	// in someone else". (TestClientCancelAbortsUpstream leaked exactly that way; the line it
	// was missing is added here.)
	//
	// This must be t.Cleanup, **never defer**: defer would run when this helper returns, i.e.
	// before rig.post -- testChain would then be nil every time, every case would fall back to
	// the real resolve.ResolveRequest, and that sandbox has no profiles.json, so the symptom is
	// a 404 "tier heavy has no usable candidate under profile ...".
	t.Cleanup(func() { testChain = nil; metrics.Default.Reset() })

	srv, logs := newLoggingTestServer()
	if _, err := srv.Filters.Register(shapeTestFilter{advance: true}); err != nil {
		t.Fatalf("registering the policy: %v", err)
	}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	t.Cleanup(front.Close)
	return &fallbackRig{srv: srv, logs: logs, front: front, hits1: &h1, hits2: &h2}
}

// post sends one ordinary OpenAI-dialect request and returns the response and raw body.
func (r *fallbackRig) post(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(r.front.URL+path, "application/json",
		strings.NewReader(`{"model":"newgate/heavy","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(body)
}

// TestFallbackOnByDefaultStillSwaps: with no switch installed it **still swaps**.
//
// This is the control for the other two: without it, "the second stop was never knocked"
// could just mean this fake chain was never wired up (testChain did not take effect, the model
// name did not land on a tier), and both those greens are idle. So this default shot must
// prove the chain is **alive**.
func TestFallbackOnByDefaultStillSwaps(t *testing.T) {
	sandboxState(t, `{"port": 0, "chain": {"fallback_on_400": true}}`)
	metrics.Default.Reset()
	rig := newFallbackRig(t)

	resp, body := rig.post(t, "/v1/chat/completions")
	if resp.StatusCode != 200 || body != `{"ok":true}` {
		t.Fatalf("by default it should fail over to the second stop and return its 200 as is; got %d %q\n%s", resp.StatusCode, body, rig.logs.String())
	}
	if n := atomic.LoadInt64(rig.hits1); n != 1 {
		t.Errorf("the first stop was hit %d times, want 1", n)
	}
	// a down upstream retries knock the same stop again, so only assert "was knocked".
	if n := atomic.LoadInt64(rig.hits2); n == 0 {
		t.Error("the second stop was never hit - the chain is not wired, so the rest of this test passes vacuously")
	}
	if resp.Header.Get("X-Newgate-Failover") == "" {
		t.Error("it failed over but there is no X-Newgate-Failover line - the scene cannot be seen from the response")
	}
	logs := rig.logs.String()
	if !strings.Contains(logs, "advancing to next chain step") {
		t.Errorf("it failed over but left no trace:\n%s", logs)
	}
	// the trail should land here: this shot must report "swapped" in the log (path prints it for i>0).
	if !strings.Contains(logs, "(failover)") {
		t.Errorf("the success log line is not marked failover:\n%s", logs)
	}
	if got := metrics.Default.Snapshot()["chain.failover"]; got != 1 {
		t.Errorf("chain.failover = %d, want 1", got)
	}
	if got := metrics.Default.Snapshot()["chain.step_failed"]; got != 1 {
		t.Errorf("chain.step_failed = %d, want 1", got)
	}
}

// TestPinnedProfileStopsAtTheHead: `--profile=xx` walks only that one profile.
//
// The criterion is the `/p/<name>` segment in the URL path -- launch writes it into the base
// URL only when the user names one, and parseTarget reads it out as Target.Profile. So this
// test walks **exactly the same path as `newgate claude --profile=x`**, not a switch of its own.
func TestPinnedProfileStopsAtTheHead(t *testing.T) {
	sandboxState(t, `{"port": 0, "chain": {"fallback_on_400": true}}`)
	metrics.Default.Reset()
	rig := newFallbackRig(t)

	// The /a/claude/ segment only gives this call an agent identity (the field is a process
	// started by `newgate claude`); what pins the profile is the /p/ segment.
	resp, body := rig.post(t, "/a/claude/p/pinned-test/v1/chat/completions")

	if n := atomic.LoadInt64(rig.hits2); n != 0 {
		t.Fatalf("a profile was named yet it failed over: the second stop was hit %d times", n)
	}
	if n := atomic.LoadInt64(rig.hits1); n != 1 {
		t.Errorf("the first stop was hit %d times, want 1", n)
	}
	// This shot must **land on the upstream**: the user wants its raw body, not a 200 from a catch.
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400 (the named one is down, so report it as is)", resp.StatusCode)
	}
	if body != fallbackShapeBody {
		t.Errorf("the upstream text was changed\n want: %q\n got:  %q", fallbackShapeBody, body)
	}
	if got := resp.Header.Get("X-Newgate-Failover"); got != "" {
		t.Errorf("no failover happened yet X-Newgate-Failover was written: %q", got)
	}
	logs := rig.logs.String()
	// The dropped steps and their reasons must both be in the log -- not silent. The format is
	// formatSteps' `profile:provider/model`, so this pins exactly which step that was.
	if !strings.Contains(logs, "profile pinned-test is pinned, dropping 1 fallback step: other:other-ok/real-model-1") {
		t.Errorf("nothing recorded 'who was dropped and why':\n%s", logs)
	}
	if strings.Contains(logs, "advancing to next chain step") {
		t.Errorf("it moved on even though the profile was pinned:\n%s", logs)
	}
	if got := metrics.Default.Snapshot()["chain.failover"]; got != 0 {
		t.Errorf("chain.failover = %d, want 0", got)
	}
}

// TestFallbackOffStopsAtTheHead: `newgate fallback off` turns off **all remaining paths**.
//
// Same truncation point and same result as the previous one; only the criterion differs -- a
// global switch rather than this call's path -- so this walks the plainest URL (no /p/ segment).
func TestFallbackOffStopsAtTheHead(t *testing.T) {
	sandboxState(t, `{"port": 0, "gateway": {"fallback": false}, "chain": {"fallback_on_400": true}}`)
	metrics.Default.Reset()
	rig := newFallbackRig(t)

	resp, body := rig.post(t, "/v1/chat/completions")

	if n := atomic.LoadInt64(rig.hits2); n != 0 {
		t.Fatalf("fallback is off yet it failed over: the second stop was hit %d times", n)
	}
	if n := atomic.LoadInt64(rig.hits1); n != 1 {
		t.Errorf("the first stop was hit %d times, want 1", n)
	}
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400 (turning fallback off means the request hits the upstream directly)", resp.StatusCode)
	}
	if body != fallbackShapeBody {
		t.Errorf("the upstream text was changed\n want: %q\n got:  %q", fallbackShapeBody, body)
	}
	if got := resp.Header.Get("X-Newgate-Failover"); got != "" {
		t.Errorf("no failover happened yet X-Newgate-Failover was written: %q", got)
	}
	logs := rig.logs.String()
	if !strings.Contains(logs, "fallback is off, dropping 1 chain step: other:other-ok/real-model-1") {
		t.Errorf("nothing recorded 'who was dropped and why':\n%s", logs)
	}
	if got := metrics.Default.Snapshot()["chain.failover"]; got != 0 {
		t.Errorf("chain.failover = %d, want 0", got)
	}
}

// TestFallbackOffBeatsTheProfilePin... not needed: both want exactly the same result
// (chain head only); the order only changes the log wording, and that order is two switch
// cases you read at a glance. Instead this locks the other real split -- the **timeout budget**.

// TestFallbackOffIgnoresTheTimeoutBudget: "ignore all timeouts" is not a separate branch, it
// is **implied by** the truncation: the budget gate carries `i > 0`, and with one step left i is 0.
//
// The budget is 1ms: `start` is taken when this request enters the handler, and no upstream
// round trip can come back within a millisecond, so whenever that gate is reachable (a second
// stop exists) the `i > 0` shot must time out and must record `budget-exhausted` in
// X-Newgate-Chain. That is what "1ms" buys: if someone dismantles the truncation this test
// sees it at once; `total_budget_ms: 0` would **test nothing** -- `ChainLimits.Budget()`
// treats `<= 0` as unset and falls back to the default 120000ms (Domain semantics, not this
// file's), leaving this test blind to whether the budget works at all.
//
// The assertion stops at "upstream raw body + only the first stop knocked + this counter is
// 0": if the budget really took effect the second stop would be knocked (line 1022 changes
// only isLast and trail, **without breaking out** of this shot) and the response would become
// the second stop's 200 -- another test's scene; this one only cares that the first stop is
// reported faithfully.
func TestFallbackOffIgnoresTheTimeoutBudget(t *testing.T) {
	sandboxState(t, `{"port": 0, "gateway": {"fallback": false}, "chain": {"fallback_on_400": true, "total_budget_ms": 1}}`)
	metrics.Default.Reset()
	rig := newFallbackRig(t)

	resp, body := rig.post(t, "/v1/chat/completions")
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400 (the budget should only affect whether to wait for the next stop)", resp.StatusCode)
	}
	if body != fallbackShapeBody {
		t.Errorf("the upstream text was changed\n got: %q", body)
	}
	if n := atomic.LoadInt64(rig.hits1); n != 1 {
		t.Errorf("the first stop was hit %d times, want 1 (the budget must not block the first request)", n)
	}
	if n := atomic.LoadInt64(rig.hits2); n != 0 {
		t.Errorf("the second stop was hit %d times - with one step left in the chain the budget gate never applies", n)
	}
	if got := metrics.Default.Snapshot()["chain.budget_exhausted"]; got != 0 {
		t.Errorf("chain.budget_exhausted = %d, want 0 (with one step left this check never applies)", got)
	}
	if trail := resp.Header.Get("X-Newgate-Chain"); strings.Contains(trail, "budget-exhausted") {
		t.Errorf("budget-exhausted appears in X-Newgate-Chain: %q", trail)
	}
}

// A last note for whoever edits this next: the raw 400 here is forwarded as-is to the client,
// which is the 4xx final-branch behavior (`w.Write(eb)`). **Do not assert
// X-Newgate-Evidence**: its value is filepath.Base(base), and saveErrEvidence gives an empty
// base when it cannot write the file, so that header degrades to "." -- an assertion that
// looks like it has content but has none.
