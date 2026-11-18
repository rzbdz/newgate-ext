package cli

import (
	"strings"
	"testing"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
)

// Tests for the argv parsing family. They pin the scene of 2026-09-18:
//
//	$ newgate start --force
//	newgate: unknown agent "--force" (known: [claude opencode])
//
// -- **and doctor itself recommends that command** ("config unavailable, refusing
// takeover … force takeover: newgate start --force"). The same pit also had `newgate probe
// --json` (`--json` taken as a profile name). At the time the contract package had **two**
// argument getters: `Positional` skipping options, and `Arg` doing a raw numeric index,
// and all 15 call sites used the wrong one. `Arg` is deleted; only this one remains.
//
// So these cases are not just "function behavior", they are the
// lock on **whether that command can still be typed** from now on.
func TestPositionalSkipsOptions(t *testing.T) {
	tests := []struct {
		name string
		args []string
		i    int
		want string
	}{
		{"only options, no positional argument", []string{"--force"}, 0, ""},
		{"options first, positional arguments after", []string{"--json", "ds"}, 0, "ds"},
		{"positional argument between options", []string{"--a", "one", "--b", "two"}, 1, "two"},
		{"short options are skipped the same way", []string{"-f", "logs"}, 0, "logs"},
		{"index out of range", []string{"only"}, 3, ""},
		{"empty argument", nil, 0, ""},
		// A lone `-` is conventionally a positional (stdin), not an option.
		{"a single dash counts as a positional argument", []string{"-"}, 0, "-"},
		// `--` is the end-of-options marker: it is not itself a positional, and everything after it is.
		{"the terminator itself does not take a position", []string{"--"}, 0, ""},
		{"everything after the terminator is a positional argument", []string{"--", "--force"}, 0, "--force"},
		{"before the terminator it is skipped as usual", []string{"--x", "a", "--", "--y"}, 1, "--y"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cliapi.Positional(tt.args, tt.i); got != tt.want {
				t.Errorf("Positional(%q, %d) = %q, want %q", tt.args, tt.i, got, tt.want)
			}
		})
	}
}

func TestFlagIsExactAndStopsAtTerminator(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"hit", []string{"--force"}, true},
		{"mixed in with positional arguments", []string{"claude", "--force"}, true},
		{"not given means not present", []string{"claude"}, false},
		{"a prefix does not count (that is another option)", []string{"--forcefully"}, false},
		// The value-bearing form belongs to FlagValue; Flag does not expand `=`. Each path
		// handles one shape; mixed together, "--x has a value or not" becomes unanswerable.
		{"the equals form is not its business", []string{"--force=1"}, false},
		{"no more options after the terminator", []string{"--", "--force"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cliapi.Flag(tt.args, "--force"); got != tt.want {
				t.Errorf("Flag(%q, --force) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// `newgate upgrade@v0.9.0` splits into a verb + `@value`: the value
// is left to the command to interpret; the UI does not know it.
func TestSplitAtVerb(t *testing.T) {
	s := &service{}
	if _, err := s.RegisterCommand(fakeCommand{names: []string{"upgrade"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterCommand(fakeCommand{names: []string{"odd@name"}}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"split it and keep the rest of the arguments", []string{"upgrade@v0.9.0", "--check"}, []string{"upgrade", "@v0.9.0", "--check"}},
		{"leading options are skipped as usual", []string{"--profile", "ds", "upgrade@main"}, []string{"--profile", "ds", "upgrade", "@main"}},
		{"without @ nothing changes", []string{"upgrade", "binary"}, []string{"upgrade", "binary"}},
		{"an unknown verb is not split (so unknown commands still error as usual)", []string{"nope@v1"}, []string{"nope@v1"}},
		{"an empty value is not split", []string{"upgrade@"}, []string{"upgrade@"}},
		{"a command whose name already contains @ is dispatched as is", []string{"odd@name"}, []string{"odd@name"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitAtVerb(s, tt.args)
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("splitAtVerb(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}
