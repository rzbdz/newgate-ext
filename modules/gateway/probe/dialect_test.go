package probe

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/gateway/dialect"
)

// fakeBilingual 模拟聚合器（api.rvcompute.com 实测形态）：
// openai 和 anthropic 两个方言都收，但 count_tokens 可以开关。
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

// TestCheckDialectsBilingual openai 声明的 provider 实际 anthropic 也通：
// 探明 openai+anthropic；count_tokens 有就 ✓、没有就明确 ✗。
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

// TestCheckDialectsOpenAIOnly openai 方言独占的上游（DeepSeek 官方形态）：
// anthropic 探明不支持，count_tokens 不用试就记 ✗（/messages 都没有）。
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
	// 不该拿 count_tokens 去撞一个连 /messages 都没有的上游
	for _, p := range paths {
		if p == "/v1/messages/count_tokens" {
			t.Fatal("when anthropic does not work, count_tokens should not be probed either")
		}
	}
}

// TestCheckDialectsAnthropicDeclared 声明 anthropic 的 provider：openai
// 是被探的那个；auth 用声明协议的（与 gate 的 setAuth 一致）。
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

// TestLearnDialectTransientNotLearned 429/超时不学——猜错会让 gate 永久
// 放弃一个本来存在的端点。
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
