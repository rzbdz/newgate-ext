package forward

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/gateway/dialect"
)

// heavyChainOnly is a test stub: heavy points at the given upstream, the other tiers are empty
// -- count_tokens forwarding only looks at the main-loop tier (heavy) chain head.
// mainLoopChainOnly builds a chain where "only the main tier (normal) has candidates".
// count_tokens fills in exactly the main tier's chain head (forward.mainLoopHead), so every
// other tier returns nil -- if it ever asked another tier, this test should go red.
func mainLoopChainOnly(t *testing.T, up *httptest.Server) {
	t.Helper()
	dialect.Reset()
	t.Cleanup(func() {
		dialect.Reset()
		testChain = nil
	})
	testChain = func(role string) []resolve.Step {
		if role != "normal" {
			return nil
		}
		return []resolve.Step{{
			Profile:  "test",
			Binding:  domain.Binding{Provider: "test-prov", Model: "real-model-1"},
			Provider: testProvider(up.URL),
		}}
	}
}

// ctPost sends one count_tokens request through the proxy (without the model field -- which is
// exactly the "request body has no top-level model string field" shape from the logs).
func ctPost(t *testing.T, front *httptest.Server, path string) (int, []byte) {
	t.Helper()
	body := `{"messages":[{"role":"user","content":"take a look at this, and count the tokens while you are at it"}],"tools":[]}`
	resp, err := http.Post(front.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, b
}

// TestCountTokensLazyProbeThenLocal the upstream has no count_tokens (the aggregator's observed
// shape: /messages 200, /messages/count_tokens 404). lazy probe at the gate level: the first
// shot tries to forward, hits 404, learns "not there", falls back to the local rough estimate;
// after that it stops burning requests (zero upstream hits), and the client always gets a 200.
func TestCountTokensLazyProbeThenLocal(t *testing.T) {
	var hits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"Invalid URL"}`))
	}))
	defer up.Close()
	mainLoopChainOnly(t, up)

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	for i, path := range []string{
		"/v1/messages/count_tokens",
		"/a/claude/p/ds/v1/messages/count_tokens",
		"/v1/messages/count_tokens", // third request: already learned, must not hit it again
	} {
		code, b := ctPost(t, front, path)
		if code != 200 {
			t.Fatalf("request %d %s: the client must get 200 (local rough estimate as fallback), got %d: %s",
				i+1, path, code, b)
		}
		var got struct {
			InputTokens int `json:"input_tokens"`
		}
		if err := json.Unmarshal(b, &got); err != nil || got.InputTokens <= 0 {
			t.Fatalf("request %d: the reply is not {input_tokens:N>0}: %s", i+1, b)
		}
	}
	if hits != 1 {
		t.Fatalf("the upstream should be hit only once (lazy probe), got %d - the learned 'unsupported' did not take effect", hits)
	}
	// the learned state itself must be right too
	if ok, known := dialect.Supports("test-prov", "real-model-1", dialect.CapCountTokens); !known || ok {
		t.Fatalf("should have learned an explicit unsupported, got (ok=%v known=%v)", ok, known)
	}
}

// TestCountTokensForwardedWhenSupported the upstream has count_tokens (a native anthropic
// endpoint): forward to take the real value, filling the model field from the mid chain head.
func TestCountTokensForwardedWhenSupported(t *testing.T) {
	var gotModel, gotPath, gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var m map[string]json.RawMessage
		b, _ := ioutil.ReadAll(r.Body)
		_ = json.Unmarshal(b, &m)
		_ = json.Unmarshal(m["model"], &gotModel)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":42}`))
	}))
	defer up.Close()
	mainLoopChainOnly(t, up)

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	code, b := ctPost(t, front, "/v1/messages/count_tokens")
	if code != 200 {
		t.Fatalf("should forward to the upstream and get 200, got %d: %s", code, b)
	}
	var got struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(b, &got); err != nil || got.InputTokens != 42 {
		t.Fatalf("should take the real upstream value 42, got %s", b)
	}
	if gotPath != "/v1/messages/count_tokens" {
		t.Errorf("upstream path = %s", gotPath)
	}
	if gotModel != "real-model-1" {
		t.Errorf("model should be filled from the head of the main-loop tier (heavy), got %q", gotModel)
	}
	if gotAuth != "Bearer sk-real" {
		t.Errorf("auth follows the protocol declared by the provider, got %q", gotAuth)
	}

	// after learning "supported", send one more shot: still the real value
	code, b = ctPost(t, front, "/v1/messages/count_tokens")
	if code != 200 {
		t.Fatalf("the second request should keep forwarding, got %d", code)
	}
	if !strings.Contains(string(b), "42") {
		t.Fatalf("the second request is still the real value, got %s", b)
	}
}

// TestCountTokensTransientErrorNotLearned 429 means "not right now", not "not there": it is not
// learned, this shot falls back to the local rough estimate; the next shot forwards again.
func TestCountTokensTransientErrorNotLearned(t *testing.T) {
	var hits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer up.Close()
	mainLoopChainOnly(t, up)

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	for i := 0; i < 2; i++ {
		code, b := ctPost(t, front, "/v1/messages/count_tokens")
		if code != 200 {
			t.Fatalf("request %d: a 429 should fall back to local 200, got %d: %s", i+1, code, b)
		}
	}
	if hits != 2 {
		t.Fatalf("429 must not be learned as 'unsupported'; both requests should have tried forwarding, got %d hits", hits)
	}
	if _, known := dialect.Supports("test-prov", "real-model-1", dialect.CapCountTokens); known {
		t.Fatal("429 must not be learned as any state")
	}
}

// TestCountTokensAlreadyLearnedSkipsUpstream an upstream already known to support it is
// forwarded directly, no local estimate (contrast: in LazyProbe, the known-unsupported goes local).
func TestCountTokensAlreadyLearnedSkipsUpstream(t *testing.T) {
	dialect.Reset()
	t.Cleanup(dialect.Reset)
	t.Cleanup(func() { testChain = nil })

	// the upstream is down (connection refused does not matter -- it should never be dialed)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("already learned 'unsupported', so the upstream must not be touched again")
	}))
	upURL := up.URL
	up.Close() // closed on purpose: being unreachable does not affect the local rough estimate

	testChain = func(role string) []resolve.Step {
		if role != "normal" {
			return nil
		}
		return []resolve.Step{{
			Profile:  "test",
			Binding:  domain.Binding{Provider: "test-prov", Model: "real-model-1"},
			Provider: domain.Provider{BaseURL: upURL + "/v1", APIKey: "sk-real", Protocol: "openai"},
		}}
	}
	dialect.MarkUnsupported("test-prov", "real-model-1", dialect.CapCountTokens)

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	code, b := ctPost(t, front, "/v1/messages/count_tokens")
	if code != 200 {
		t.Fatalf("should be local 200, got %d: %s", code, b)
	}
	var got struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(b, &got); err != nil || got.InputTokens <= 0 {
		t.Fatalf("wrong local rough-estimate reply: %s", b)
	}
}

// TestCountTokensRespectsClientModel when the client carries its own model field, respect it
// and don't overwrite it with the mid chain head (count_tokens wants the named model's tokenizer).
func TestCountTokensRespectsClientModel(t *testing.T) {
	var gotModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]json.RawMessage
		b, _ := ioutil.ReadAll(r.Body)
		_ = json.Unmarshal(b, &m)
		_ = json.Unmarshal(m["model"], &gotModel)
		_, _ = w.Write([]byte(`{"input_tokens":7}`))
	}))
	defer up.Close()
	mainLoopChainOnly(t, up)

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	body := `{"model":"glm-4.5-air","messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(front.URL+"/v1/messages/count_tokens",
		"application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "7") {
		t.Fatalf("should forward to get the real value: %d %s", resp.StatusCode, b)
	}
	if gotModel != "glm-4.5-air" {
		t.Fatalf("the client's model must not be overwritten, upstream received %q", gotModel)
	}
}

// TestAPIHelloAnsweredLocally Claude Code's connectivity probe (HEAD/GET, no body) should get a
// local 200 -- falling into model parsing only spams 400 logs (observed 2026-09-09).
func TestAPIHelloAnsweredLocally(t *testing.T) {
	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	for _, method := range []string{"HEAD", "GET"} {
		req, _ := http.NewRequest(method, front.URL+"/a/claude/api/hello", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s /api/hello should be local 200, got %d", method, resp.StatusCode)
		}
	}
}

// TestBodylessProbesAre404NotFailures the local inference service's probes (hermes asks
// /api/tags, /v1/props, /version ... for a 127.0.0.1 base_url) get a local 404 and do not count
// as failures -- they used to fall into model parsing: one "error 400" + a failure count per
// shot (observed 2026-11-04). A POST that carries a body but no model is still a 400: bad shape.
func TestBodylessProbesAre404NotFailures(t *testing.T) {
	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	for _, p := range []string{"/a/hermes/api/tags", "/a/hermes/v1/props", "/a/hermes/p/ds/version"} {
		for _, method := range []string{"GET", "HEAD"} {
			req, _ := http.NewRequest(method, front.URL+p, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 404 {
				t.Fatalf("%s %s should be local 404, got %d", method, p, resp.StatusCode)
			}
		}
	}
	if n := atomic.LoadUint64(&srv.failures); n != 0 {
		t.Fatalf("the probe must not count as a failure, got %d", n)
	}

	resp, err := http.Post(front.URL+"/a/hermes/v1/chat/completions", "application/json",
		strings.NewReader(`{"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("a POST without model should still be 400, got %d", resp.StatusCode)
	}
}
