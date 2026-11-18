package domain

import "testing"

// TestProviderURL 双 base 的选择规则：Anthropic 方言（/messages*）走
// anthropic_url，其余走 base_url；没配 anthropic_url 就是老样子——
// 两种方言同一个 base。
//
// 现场（火山方舟，2026-09-15）：openai 在 /api/coding/v3，anthropic 在
// /api/coding，打错了是 istio-envoy 的空 body 404。
func TestProviderURL(t *testing.T) {
	agg := Provider{BaseURL: "https://agg.example.com/v1"} // 两方言同 base
	dual := Provider{
		BaseURL:      "https://ark.example.com/api/coding/v3",
		AnthropicURL: "https://ark.example.com/api/coding",
	}
	dualV1 := Provider{
		BaseURL:      "https://ark.example.com/api/coding/v3",
		AnthropicURL: "https://ark.example.com/api/coding/v1", // 已经带 /v1
	}
	dualSlash := Provider{
		BaseURL:      "https://ark.example.com/api/coding/v3/",
		AnthropicURL: "https://ark.example.com/api/coding/",
	}

	cases := []struct {
		name   string
		p      Provider
		suffix string
		want   string
	}{
		{"同 base：openai", agg, "/chat/completions", "https://agg.example.com/v1/chat/completions"},
		{"同 base：anthropic", agg, "/messages", "https://agg.example.com/v1/messages"},
		{"同 base：count_tokens", agg, "/messages/count_tokens", "https://agg.example.com/v1/messages/count_tokens"},
		{"分家：openai 走 v3", dual, "/chat/completions", "https://ark.example.com/api/coding/v3/chat/completions"},
		{"分家：anthropic 走 /api/coding + /v1", dual, "/messages", "https://ark.example.com/api/coding/v1/messages"},
		{"分家：count_tokens 同路", dual, "/messages/count_tokens",
			"https://ark.example.com/api/coding/v1/messages/count_tokens"},
		{"写了 /v1 不重复补", dualV1, "/messages", "https://ark.example.com/api/coding/v1/messages"},
		{"尾部斜杠不双写", dualSlash, "/messages", "https://ark.example.com/api/coding/v1/messages"},
	}
	for _, c := range cases {
		if got := c.p.URL(c.suffix); got != c.want {
			t.Errorf("%s: URL(%q) = %q，想要 %q", c.name, c.suffix, got, c.want)
		}
	}

	// Base 是「这次发给哪个上游」——插件靠它判断自己认不认这家（如
	// deepseek 插件看 base 里有没有 "deepseek"），所以它必须是真那个 base，
	// 不带路径。
	if got := dual.Base("/messages"); got != "https://ark.example.com/api/coding" {
		t.Errorf("Base(/messages) = %q", got)
	}
	if got := dual.Base("/chat/completions"); got != "https://ark.example.com/api/coding/v3" {
		t.Errorf("Base(/chat/completions) = %q", got)
	}
}

// TestIsAnthropicPath 判据是客户端发来的路径，不是 provider 的 protocol：
// 聚合网关实测 provider 标 openai 也照样收 /v1/messages。
func TestIsAnthropicPath(t *testing.T) {
	for _, c := range []struct {
		suffix string
		want   bool
	}{
		{"/messages", true},
		{"/messages/count_tokens", true},
		{"/chat/completions", false},
		{"/models", false},
		{"/messagesx", false}, // 前缀像但不是同一个端点
		{"", false},
	} {
		if got := IsAnthropicPath(c.suffix); got != c.want {
			t.Errorf("IsAnthropicPath(%q) = %v，想要 %v", c.suffix, got, c.want)
		}
	}
}
