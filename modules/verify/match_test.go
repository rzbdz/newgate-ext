package verify

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		name      string
		requested string
		predicted string
		family    string
		want      Match
	}{
		{"separators do not count", "claude-haiku-4-5", "claude-haiku-4.5", "claude", MatchOK},
		{"dated snapshot", "deepseek-v4-flash", "deepseek-v4-flash-1017", "deepseek", MatchDated},
		{"one digit is another model", "gpt-5", "gpt-5.5", "gpt", MatchFamily},
		{"same family", "gpt-5.5", "gpt-5.6-terra", "gpt", MatchFamily},
		{"different family", "kimi-k3", "deepseek-v4.1-flash", "deepseek", MatchMismatch},
		{"no prediction", "gpt-5.5", "", "", MatchUnknown},
		{"prefix but not a date", "qwen3.8-max", "qwen3.8-max-preview", "qwen", MatchFamily},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.requested, c.predicted, c.family); got != c.want {
				t.Fatalf("Classify(%q, %q, %q) = %q, want %q",
					c.requested, c.predicted, c.family, got, c.want)
			}
		})
	}
}

func TestWarnings(t *testing.T) {
	for _, m := range []Match{MatchFamily, MatchMismatch, MatchUnknown, MatchError} {
		if !m.Warning() {
			t.Errorf("%q should be a warning", m)
		}
	}
	for _, m := range []Match{MatchOK, MatchDated} {
		if m.Warning() {
			t.Errorf("%q should not be a warning", m)
		}
	}
}
