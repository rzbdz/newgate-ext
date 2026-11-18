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

// heavyChainOnly 测试桩：heavy 档指向 given 上游，其他档为空——count_tokens
// 的转发只看主循环档（heavy）链头。
// mainLoopChainOnly 造一条「只有主力档（normal）有候选」的链。
// count_tokens 补的就是主力档链头（forward.mainLoopHead），所以别的档位
// 一律返回 nil——它要是去问了别的档位，这个测试就该红。
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

// ctPost 经代理发一个 count_tokens 请求（不带 model 字段——正是日志里
// 「请求体顶层没有 model 字符串字段」的形态）。
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

// TestCountTokensLazyProbeThenLocal 上游没有 count_tokens（聚合器实测
// 形态：/messages 200、/messages/count_tokens 404）。gate 层面的 lazy
// probe：第一发试转发、撞 404、学到「没有」退回本地粗估；此后不再白跑
// （上游零命中），客户端拿到的始终是本地 200。
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
		"/v1/messages/count_tokens", // 第三发：已学到，不该再撞
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
	// 学到的状态本身也要对
	if ok, known := dialect.Supports("test-prov", "real-model-1", dialect.CapCountTokens); !known || ok {
		t.Fatalf("should have learned an explicit unsupported, got (ok=%v known=%v)", ok, known)
	}
}

// TestCountTokensForwardedWhenSupported 上游有 count_tokens（原生
// anthropic 端点）：转发拿真值，model 字段按 mid 链头补上。
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

	// 学到「支持」后再来一发，仍是真值
	code, b = ctPost(t, front, "/v1/messages/count_tokens")
	if code != 200 {
		t.Fatalf("the second request should keep forwarding, got %d", code)
	}
	if !strings.Contains(string(b), "42") {
		t.Fatalf("the second request is still the real value, got %s", b)
	}
}

// TestCountTokensTransientErrorNotLearned 429 是「现在不行」不是「没有」：
// 不学，本次退回本地粗估；下一发继续试转发。
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

// TestCountTokensAlreadyLearnedSkipsUpstream 已探明支持的上游直接转发、
// 不再走本地粗估（对照：LazyProbe 用例里已探明不支持的直接本地）。
func TestCountTokensAlreadyLearnedSkipsUpstream(t *testing.T) {
	dialect.Reset()
	t.Cleanup(dialect.Reset)
	t.Cleanup(func() { testChain = nil })

	// 上游挂了（连接被拒也无所谓——根本不该连它）
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("already learned 'unsupported', so the upstream must not be touched again")
	}))
	upURL := up.URL
	up.Close() // 故意关掉：连不上也不影响本地粗估

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

// TestCountTokensRespectsClientModel 客户端自己带了 model 字段就尊重它，
// 不拿 mid 链头覆盖（count_tokens 按点名模型的 tokenizer 算才对）。
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

// TestAPIHelloAnsweredLocally Claude Code 的连通性探针（HEAD/GET，无 body）
// 本地应 200——掉进 model 解析会刷 400 日志（2026-09-09 实抓）。
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

// TestBodylessProbesAre404NotFailures 本机推理服务的探测（hermes 对 127.0.0.1 的
// base_url 会问 /api/tags、/v1/props、/version …）本地答 404，且不计入失败数——
// 以前它们掉进 model 解析，每一发一条「错误 400」+ 失败计数（2026-11-04 实抓）。
// 带 body 却缺 model 的 POST 仍然是 400：那是真的形状不对。
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
