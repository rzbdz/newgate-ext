package store

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureControlToken the generation and survival of the control
// token: idempotent (the token is not swapped), an ordinary SaveState
// does not wash it away (the path where cmdStart writes state back).
func TestEnsureControlToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	_ = os.MkdirAll(filepath.Join(dir, "mappings"), 0o755)

	s, err := EnsureControlToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ControlToken) < 32 {
		t.Fatalf("the token is too short (%d chars), not enough entropy: %q", len(s.ControlToken), s.ControlToken)
	}

	again, err := EnsureControlToken()
	if err != nil {
		t.Fatal(err)
	}
	if again.ControlToken != s.ControlToken {
		t.Fatalf("the second call changed the token: %s -> %s", s.ControlToken, again.ControlToken)
	}

	// An ordinary Load -> change -> Save (the shape of --set-profile
	// and similar commands) must carry it back to disk as is
	s2 := LoadState()
	s2.DefaultProfile = "other"
	if err := SaveState(s2); err != nil {
		t.Fatal(err)
	}
	if got := LoadState().ControlToken; got != s.ControlToken {
		t.Fatalf("a normal SaveState clobbered the token: %q -> %q", s.ControlToken, got)
	}

	// On-disk form: the token is in state.json (so the daemon's watcher can read it)
	b, err := ioutil.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), s.ControlToken) {
		t.Fatal("the token was not written to disk")
	}
}
