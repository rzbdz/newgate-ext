package probe

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/gateway/dialect"
)

// fakeBilingual models an aggregator (the shape measured on api.rvcompute.com):
// it accepts both the openai and anthropic dialects, but count_tokens can be toggled.
func fakeBilingual(t *testing.T, countTokens bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
		case r.URL.Path == "/v1/messages":
			_, _ = w.Write([]byte(`{"id":"m","type":"message","content":[{"type":"text","text":"ok"}]}`))
		case r.URL.Path == "/v1/messages/count_tokens":
			if !countTokens {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":{"message":"Invalid URL"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"input_tokens":7}`))
		default:
			w.WriteHeader(404)
		}
	}))
}

// TestCheckDialectsBilingual: a provider declared as openai actually speaks anthropic too -
// the probe finds openai+anthropic; count_tokens gets ✓ when present and an explicit ✗ when not.
func TestCheckDialectsBilingual(t *testing.T) {
	dialect.Reset()
	t.Cleanup(dialect.Reset)

	for _, ct := range []bool{true, false} {
		dialect.Reset()
		up := fakeBilingual(t, ct)
		p := domain.Provider{BaseURL: up.URL + "/v1", APIKey: "sk-x", Protocol: "openai"}

		CheckDialects("prov", p, "glm-5.3", 5_000_000_000)

		if ok, known := dialect.Supports("prov", "glm-5.3", dialect.CapOpenAI); !ok || !known {
			t.Errorf("count_tokens=%v: a declared openai provider should be recorded as supported directly", ct)
		}
		if ok, known := dialect.Supports("prov", "glm-5.3", dialect.CapAnthropic); !ok || !known {
			t.Errorf("count_tokens=%v: the anthropic dialect should be found supported by probing", ct)
		}
		got, known := dialect.Supports("prov", "glm-5.3", dialect.CapCountTokens)
		if !known || got != ct {
			t.Errorf("count_tokens=%v: the probe result should be (known, %v), got (%v,%v)", ct, ct, known, got)
		}
		up.Close()
	}
}

// TestCheckDialectsOpenAIOnly: an upstream exclusive to the openai dialect (the official
// DeepSeek shape) - anthropic is found unsupported, and count_tokens is marked ✗ without
// trying (there is not even /messages).
func TestCheckDialectsOpenAIOnly(t *testing.T) {
	dialect.Reset()
	t.Cleanup(dialect.Reset)

	var paths []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/v1/chat/completions" {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer up.Close()

	p := domain.Provider{BaseURL: up.URL + "/v1", APIKey: "sk-x", Protocol: "openai"}
	CheckDialects("prov", p, "deepseek-chat", 5_000_000_000)

	if ok, known := dialect.Supports("prov", "deepseek-chat", dialect.CapAnthropic); ok || !known {
		t.Errorf("anthropic should be found unsupported, got (ok=%v known=%v)", ok, known)
	}
	if ok, known := dialect.Supports("prov", "deepseek-chat", dialect.CapCountTokens); ok || !known {
		t.Errorf("count_tokens should be recorded unsupported as well (there is not even /messages), got (ok=%v known=%v)", ok, known)
	}
	// count_tokens must not be thrown at an upstream that lacks even /messages
	for _, p := range paths {
		if p == "/v1/messages/count_tokens" {
			t.Fatal("when anthropic does not work, count_tokens should not be probed either")
		}
	}
}

// TestCheckDialectsAnthropicDeclared: a provider declaring anthropic - openai
// is the one probed; auth follows the declared protocol (same as setAuth in gate).
func TestCheckDialectsAnthropicDeclared(t *testing.T) {
	dialect.Reset()
	t.Cleanup(dialect.Reset)

	up := fakeBilingual(t, true)
	defer up.Close()
	p := domain.Provider{BaseURL: up.URL + "/v1", APIKey: "sk-x", Protocol: "anthropic"}

	CheckDialects("prov", p, "claude-opus", 5_000_000_000)

	if ok, known := dialect.Supports("prov", "claude-opus", dialect.CapAnthropic); !ok || !known {
		t.Error("a declared anthropic provider should be recorded as supported directly")
	}
	if ok, known := dialect.Supports("prov", "claude-opus", dialect.CapOpenAI); !ok || !known {
		t.Error("the openai dialect should be found supported by probing")
	}
	if ok, known := dialect.Supports("prov", "claude-opus", dialect.CapCountTokens); !ok || !known {
		t.Error("count_tokens should be found supported by probing")
	}
}

// TestLearnDialectTransientNotLearned: 429/timeouts are not learned - a wrong guess would
// make gate abandon an endpoint that actually exists, for good.
func TestLearnDialectTransientNotLearned(t *testing.T) {
	dialect.Reset()
	t.Cleanup(dialect.Reset)

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
	}))
	defer up.Close()
	p := domain.Provider{BaseURL: up.URL + "/v1", APIKey: "sk-x", Protocol: "openai"}

	learnDialect("prov", p, "m", dialect.CapAnthropic, 5_000_000_000)
	if _, known := dialect.Supports("prov", "m", dialect.CapAnthropic); known {
		t.Fatal("429 must not be learned as any state")
	}
}
