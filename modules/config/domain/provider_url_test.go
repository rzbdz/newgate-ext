package domain

import "testing"

// TestProviderURL: the rule for choosing between the two bases: the Anthropic
// dialect (/messages*) goes to anthropic_url, everything else to base_url;
// without anthropic_url it is the old way -- both dialects on the same base.
//
// The scene (Volcengine Ark, 2026-09-15): openai is at /api/coding/v3, anthropic
// at /api/coding, and hitting the wrong one gives an istio-envoy empty-body 404.
func TestProviderURL(t *testing.T) {
	agg := Provider{BaseURL: "https://agg.example.com/v1"} // both dialects on the same base
	dual := Provider{
		BaseURL:      "https://ark.example.com/api/coding/v3",
		AnthropicURL: "https://ark.example.com/api/coding",
	}
	dualV1 := Provider{
		BaseURL:      "https://ark.example.com/api/coding/v3",
		AnthropicURL: "https://ark.example.com/api/coding/v1", // already carries /v1
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
		{"same base: openai", agg, "/chat/completions", "https://agg.example.com/v1/chat/completions"},
		{"same base: anthropic", agg, "/messages", "https://agg.example.com/v1/messages"},
		{"same base: count_tokens", agg, "/messages/count_tokens", "https://agg.example.com/v1/messages/count_tokens"},
		{"split: openai goes to v3", dual, "/chat/completions", "https://ark.example.com/api/coding/v3/chat/completions"},
		{"split: anthropic goes to /api/coding + /v1", dual, "/messages", "https://ark.example.com/api/coding/v1/messages"},
		{"split: count_tokens takes the same path", dual, "/messages/count_tokens",
			"https://ark.example.com/api/coding/v1/messages/count_tokens"},
		{"a written /v1 is not added twice", dualV1, "/messages", "https://ark.example.com/api/coding/v1/messages"},
		{"a trailing slash is not doubled", dualSlash, "/messages", "https://ark.example.com/api/coding/v1/messages"},
	}
	for _, c := range cases {
		if got := c.p.URL(c.suffix); got != c.want {
			t.Errorf("%s: URL(%q) = %q, want %q", c.name, c.suffix, got, c.want)
		}
	}

	// Base is "which upstream this is sent to" -- plugins rely on it to decide
	// whether they recognize this provider (e.g. the deepseek plugin looks
	// for "deepseek" in base), so it must be that real base, without a path.
	if got := dual.Base("/messages"); got != "https://ark.example.com/api/coding" {
		t.Errorf("Base(/messages) = %q", got)
	}
	if got := dual.Base("/chat/completions"); got != "https://ark.example.com/api/coding/v3" {
		t.Errorf("Base(/chat/completions) = %q", got)
	}
}

// TestIsAnthropicPath: the criterion is the path the client sent, not the provider's protocol:
// measured on an aggregate gateway, a provider marked openai still receives /v1/messages.
func TestIsAnthropicPath(t *testing.T) {
	for _, c := range []struct {
		suffix string
		want   bool
	}{
		{"/messages", true},
		{"/messages/count_tokens", true},
		{"/chat/completions", false},
		{"/models", false},
		{"/messagesx", false}, // prefix looks similar but it is not the same endpoint
		{"", false},
	} {
		if got := IsAnthropicPath(c.suffix); got != c.want {
			t.Errorf("IsAnthropicPath(%q) = %v, want %v", c.suffix, got, c.want)
		}
	}
}
