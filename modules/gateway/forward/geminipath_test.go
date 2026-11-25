package forward

import "testing"

// These pin the dialect translation: the model we route on is exactly the model we rewrite,
// and nothing else in the path moves.

func TestParseGeminiPath(t *testing.T) {
	cases := []struct {
		suffix string
		model  string
		action string
		wantOK bool
	}{
		{"/v1beta/models/heavy:generateContent", "heavy", "generateContent", true},
		{"/v1beta/models/heavy:streamGenerateContent", "heavy", "streamGenerateContent", true},
		{"/v1beta/models/smt-gemini/gemini-3-pro:countTokens", "", "", false}, // a slash is not our shape
		{"/v1beta/models/heavy", "heavy", "", true},
		{"/v1beta/models/:generateContent", "", "", false},
		{"/v1beta/models", "", "", false},
		{"/chat/completions", "", "", false},
		{"/v1beta/openapi.json", "", "", false},
	}
	for _, c := range cases {
		got, ok := parseGeminiPath(c.suffix)
		if ok != c.wantOK {
			t.Fatalf("%s: ok=%v want %v", c.suffix, ok, c.wantOK)
		}
		if !ok {
			continue
		}
		if got.Model != c.model || got.Action != c.action {
			t.Fatalf("%s: got %+v, want model=%q action=%q", c.suffix, got, c.model, c.action)
		}
	}
}

func TestWithGeminiModel(t *testing.T) {
	cases := []struct{ in, model, want string }{
		{"/v1beta/models/heavy:generateContent", "gemini-3-pro-preview",
			"/v1beta/models/gemini-3-pro-preview:generateContent"},
		{"/v1beta/models/heavy:streamGenerateContent", "gemini-3.1-pro-preview",
			"/v1beta/models/gemini-3.1-pro-preview:streamGenerateContent"},
		{"/v1beta/models/heavy", "gemini-3-pro", "/v1beta/models/gemini-3-pro"},
		// Not a Gemini path: never touched, even though a model was given.
		{"/chat/completions", "x", "/chat/completions"},
		// Empty model = nothing to write.
		{"/v1beta/models/heavy:generateContent", "", "/v1beta/models/heavy:generateContent"},
	}
	for _, c := range cases {
		if got := withGeminiModel(c.in, c.model); got != c.want {
			t.Fatalf("%s + %s: got %s want %s", c.in, c.model, got, c.want)
		}
	}
}

func TestGeminiStreams(t *testing.T) {
	cases := []struct {
		action, query string
		want          bool
	}{
		{"streamGenerateContent", "", true},
		{"generateContent", "alt=sse", true},
		{"generateContent", "key=1&alt=sse", true},
		{"generateContent", "", false},
		{"countTokens", "alt=json", false},
	}
	for _, c := range cases {
		if got := geminiStreams(geminiCall{Action: c.action}, c.query); got != c.want {
			t.Fatalf("%s?%s: got %v want %v", c.action, c.query, got, c.want)
		}
	}
}

func TestParseTargetKeepsV1Beta(t *testing.T) {
	got := parseTarget("/a/gemini/v1beta/models/heavy:generateContent")
	if got.TaskCreate != "gemini" {
		t.Fatalf("agent = %q", got.TaskCreate)
	}
	if got.Suffix != "/v1beta/models/heavy:generateContent" {
		t.Fatalf("suffix = %q (a /v1beta path must not lose its /v1)", got.Suffix)
	}
	// Ordinary OpenAI-style paths still lose the /v1, as before.
	if s := parseTarget("/a/codex/v1/chat/completions").Suffix; s != "/chat/completions" {
		t.Fatalf("openai suffix = %q", s)
	}
}
