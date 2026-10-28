package verify

import "testing"

func TestProfileArgSkipsFlagValues(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"bare profile", []string{"ds"}, "ds"},
		{"flags consume their values", []string{"--provider", "relay", "--model", "m", "--count", "1"}, ""},
		{"profile after flags", []string{"--count", "1", "ds"}, "ds"},
		{"equals form does not consume the next arg", []string{"--provider=relay", "ds"}, "ds"},
		{"boolean flag", []string{"--json", "ds"}, "ds"},
		{"literal separator", []string{"--json", "--", "-weird"}, "-weird"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := profileArg(c.args); got != c.want {
				t.Fatalf("profileArg(%v) = %q, want %q", c.args, got, c.want)
			}
		})
	}
}
