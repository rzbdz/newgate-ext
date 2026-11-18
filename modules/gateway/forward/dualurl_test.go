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

// TestDualURLRoutesByDialect an upstream whose dialects split (Volcengine Ark: the openai
// dialect under /api/coding/v3, the anthropic dialect under /api/coding) -- the forwarding
// layer must pick the base by **the dialect the client speaks this time**.
//
// The field (2026-09-15): claude's /messages hit the openai v3 base, Ark answered an
// istio-envoy empty-body 404, and the user saw "ark does not work" while probe was all green
// (probe only hits the path the provider declares as its protocol).
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
			t.Fatalf("POST %s status %d: %s", path, resp.StatusCode, b)
		}
	}

	// Claude Code shape: /a/claude/ + /v1/messages
	post("/a/claude/v1/messages",
		`{"model":"heavy","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if anthHit != "/api/coding/v1/messages" {
		t.Errorf("the anthropic dialect should use anthropic_url: actually hit %q (want /api/coding/v1/messages)", anthHit)
	}
	if openaiHit != "" {
		t.Errorf("the anthropic dialect must not touch base_url: actually hit %q", openaiHit)
	}

	// count_tokens is an anthropic-dialect endpoint too, so it also takes anthropic_url
	anthHit = ""
	post("/a/claude/v1/messages/count_tokens",
		`{"model":"heavy","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if anthHit != "/api/coding/v1/messages/count_tokens" {
		t.Errorf("count_tokens should use anthropic_url: actually hit %q", anthHit)
	}

	// opencode shape: bare /v1 + /chat/completions, not one byte may go toward anthropic
	anthHit = ""
	post("/v1/chat/completions",
		`{"model":"heavy","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if openaiHit != "/api/coding/v3/chat/completions" {
		t.Errorf("the openai dialect should use base_url: actually hit %q (want /api/coding/v3/chat/completions)", openaiHit)
	}
	if anthHit != "" {
		t.Errorf("the openai dialect must not touch anthropic_url: actually hit %q", anthHit)
	}
}
