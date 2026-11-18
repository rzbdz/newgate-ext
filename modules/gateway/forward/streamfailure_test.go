package forward

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/gateway/probe"
	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
)

// streamFailUpstream is a fake upstream for "a Responses-dialect upstream reporting failure
// inside the stream": **HTTP 200**, then a response.failed. That is the real upstream's shape
// (core/mock/fake_upstream.py's _responses_failed replicates it verbatim), and it is exactly
// why this test exists -- a status-code criterion sees nothing wrong here.
func streamFailUpstream(t *testing.T, message string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, `event: response.created`+"\n"+
			`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`+"\n\n")
		if fl != nil {
			fl.Flush()
		}
		_, _ = io.WriteString(w, `event: response.failed`+"\n"+
			`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","output":[],`+
			`"error":{"code":"invalid_request_error","message":"`+message+`"}}}`+"\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
}

// TestStreamFailureInsideA200IsLearned pins the **only** learning entry on this path.
//
// # The failure it reproduces (the line the user reported on 2026-09-22)
//
// Codex → GLM: the upstream puts the rejection inside the event stream, HTTP status 200. The
// data plane's status-code-driven learnQuirks (learns only on status >= 400) therefore **never
// fires**, the quirk table stays empty forever, and always-thinks' Match never turns green --
// what the user sees is "reproducible, heals by itself after a while" ("heals" because someone
// happened to probe that provider, which is luck, not mechanism).
//
// # Why assert "this entry is in the persisted cache" and not "in the quirk table"
//
// The table lives in memory and is empty after a restart; what the user really wants is **the
// next shot not hitting it**, which rests on persistence (probe-capabilities.json) + the next
// process's LoadCachedCapabilities. So the assertion goes as far as the entry on disk -- the
// in-memory one is only an intermediate stop on this path.
//
// The signature is registered in place: the criterion's home is `modules/thinking` (it owns
// that patch), and forward only **uses** it. Registering it for real would drag
// modules/thinking into this package's test dependencies, a cycle that should not exist.
func TestStreamFailureInsideA200IsLearned(t *testing.T) {
	isolate(t)
	quirk.Default.Reset()
	t.Cleanup(quirk.Default.Reset)

	// The criterion itself ("the error says 'always thinks' → this model cannot disable
	// thinking") is registered by modules/thinking in Start, and the graph TestMain starts
	// has it installed -- this test only **uses** it, registering no second copy (that would
	// collide, by design: RegisterSignature errors at once on a duplicate Label, see
	// gateway/quirk).
	//
	// This line pins "the criterion really is there": the day thinking is removed, this goes
	// red first, rather than letting the assertion below quietly turn from "learned" into a
	// green that **tests nothing**.
	if got := quirk.Default.Learn("probe-check", "probe-check", 400, []byte("始终思考")); len(got) == 0 {
		t.Fatal("the modules/thinking signatures were not installed - this test is a no-op, fix the assembly first")
	}
	quirk.Default.Reset()

	const message = "该模型始终思考，不支持关闭思考；请使用 low、high 或 max。"
	up := streamFailUpstream(t, message)
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{
			Profile:  "test",
			Binding:  domain.Binding{Provider: "smt-glm", Model: "glm-5.3"},
			Provider: testProvider(up.URL),
		}}
	}
	defer func() { testChain = nil }()

	srv, logs := newLoggingTestServer()
	// The persist step at shutdown (a real daemon takes the Shutdown / graceful-handover paths;
	// this is the same function).
	defer srv.flushPendingQuirks()

	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/responses", "application/json",
		strings.NewReader(`{"model":"newgate/heavy","stream":true,"input":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// The first two are **the shape of why this failure is hard to diagnose**: status 200,
	// connection fine.
	if resp.StatusCode != 200 {
		t.Fatalf("the shape of this path is 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "response.failed") {
		t.Fatalf("the failure in the stream was not relayed to the client as is:\n%s", body)
	}

	// learned (in-memory table)
	if !quirk.Default.Has("smt-glm", "glm-5.3", quirk.NoThinkingDisable) {
		t.Fatalf("the failure in the stream was not learned. log:\n%s", logs.String())
	}
	// said it (not silent): the user has to know we add things to requests from the next shot on.
	if log := logs.String(); !strings.Contains(log, "reported a failure inside the event stream") {
		t.Fatalf("the log has no line saying 'the upstream reported a failure inside the stream':\n%s", log)
	}
	if log := logs.String(); !strings.Contains(log, "learned smt-glm/glm-5.3") {
		t.Fatalf("the log has no 'learned' line:\n%s", log)
	}

	// persisted (still recognized after an upgrade/restart)
	srv.flushPendingQuirks()
	disk := probe.DiskQuirks()
	if len(disk) != 1 || disk[0].Provider != "smt-glm" || disk[0].Model != "glm-5.3" {
		t.Fatalf("this entry is not on disk, so a restart would hit the same error again: %v", disk)
	}
}

// TestSuccessfulStreamDoesNotLearn no collateral damage: a normally finished stream learns not
// one byte.
//
// This criterion matters more than the previous one -- if that one breaks it is "did not
// learn" (the user hits it again, recoverable), if this one breaks it is **adding fields to
// every request on the strength of one normal stream**, an unrecoverable silent edit of the
// user's requests. So it asserts both "the table is empty" and "nothing was written to disk".
func TestSuccessfulStreamDoesNotLearn(t *testing.T) {
	isolate(t)
	quirk.Default.Reset()
	t.Cleanup(quirk.Default.Reset)

	// The criterion itself ("when the error says 'always thinks' -> this model cannot turn
	// thinking off") is registered by modules/thinking in Start, and the whole graph that
	// TestMain builds includes it -- this test only **uses** it and does not register its
	// own (registering would collide on purpose: RegisterSignature errors on a duplicate
	// Label, see gateway/quirk).
	//
	// This line pins down "the criterion is really there": the day thinking is removed,
	// this goes red first, instead of the assertions below quietly turning from "learned"
	// into a green that **tests nothing**.
	if got := quirk.Default.Learn("probe-check", "probe-check", 400, []byte("始终思考")); len(got) == 0 {
		t.Fatal("the modules/thinking signatures were not installed - this test is a no-op, fix the assembly first")
	}
	quirk.Default.Reset()

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		for _, ev := range []string{
			`{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`,
		} {
			_, _ = io.WriteString(w, "data: "+ev+"\n\n")
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{
			Profile:  "test",
			Binding:  domain.Binding{Provider: "smt-glm", Model: "glm-5.3"},
			Provider: testProvider(up.URL),
		}}
	}
	defer func() { testChain = nil }()

	srv := newTestServer()
	defer srv.flushPendingQuirks()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/responses", "application/json",
		strings.NewReader(`{"model":"newgate/heavy","stream":true,"input":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if quirk.Default.Has("smt-glm", "glm-5.3", quirk.NoThinkingDisable) {
		t.Fatal("a normally finished stream was learned as a failure - the next request would add fields to a perfectly healthy model")
	}
	srv.flushPendingQuirks()
	if disk := probe.DiskQuirks(); len(disk) != 0 {
		t.Fatalf("a normally finished stream must write nothing to disk: %v", disk)
	}
}
