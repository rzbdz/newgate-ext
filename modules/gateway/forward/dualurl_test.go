package forward

import (
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
)

// TestDualURLRoutesByDialect 两种方言分家的上游（火山方舟：openai 方言在
// /api/coding/v3，anthropic 方言在 /api/coding）——转发层必须按**客户端这次
// 说的方言**选 base。
//
// 现场（2026-09-15）：claude 的 /messages 打到了 openai 的 v3 base，方舟回
// istio-envoy 的空 body 404，用户看到的是「ark 跑不通」，而 probe 全绿
// （probe 只打 provider 声明协议的那条路）。
func TestDualURLRoutesByDialect(t *testing.T) {
	var openaiHit, anthHit string
	record := func(dst *string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			*dst = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`)
		}
	}
	openai := httptest.NewServer(record(&openaiHit))
	defer openai.Close()
	anth := httptest.NewServer(record(&anthHit))
	defer anth.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding: domain.Binding{Provider: "ark", Model: "ark-code-latest"},
			Provider: domain.Provider{
				BaseURL:      openai.URL + "/api/coding/v3",
				AnthropicURL: anth.URL + "/api/coding",
				APIKey:       "sk-real", Protocol: "openai"}}}
	}
	defer func() { testChain = nil }()

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	post := func(path, body string) {
		t.Helper()
		resp, err := http.Post(front.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			b, _ := ioutil.ReadAll(resp.Body)
			t.Fatalf("POST %s 状态 %d: %s", path, resp.StatusCode, b)
		}
	}

	// Claude Code 形态：/a/claude/ + /v1/messages
	post("/a/claude/v1/messages",
		`{"model":"heavy","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if anthHit != "/api/coding/v1/messages" {
		t.Errorf("anthropic 方言该走 anthropic_url：实际打到 %q（期望 /api/coding/v1/messages）", anthHit)
	}
	if openaiHit != "" {
		t.Errorf("anthropic 方言不该碰 base_url：实际打到 %q", openaiHit)
	}

	// count_tokens 也是 anthropic 方言的端点，同样走 anthropic_url
	anthHit = ""
	post("/a/claude/v1/messages/count_tokens",
		`{"model":"heavy","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if anthHit != "/api/coding/v1/messages/count_tokens" {
		t.Errorf("count_tokens 该走 anthropic_url：实际打到 %q", anthHit)
	}

	// opencode 形态：裸 /v1 + /chat/completions，一个字节都不该往 anthropic 那边去
	anthHit = ""
	post("/v1/chat/completions",
		`{"model":"heavy","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if openaiHit != "/api/coding/v3/chat/completions" {
		t.Errorf("openai 方言该走 base_url：实际打到 %q（期望 /api/coding/v3/chat/completions）", openaiHit)
	}
	if anthHit != "" {
		t.Errorf("openai 方言不该碰 anthropic_url：实际打到 %q", anthHit)
	}
}
