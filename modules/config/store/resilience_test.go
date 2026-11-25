package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The scene this file pins (2026-11-25): one bad key in the **default** profile emptied the chain
// head of every agent, while Load() still reported success. Keep serving the last usable version,
// and say loudly what happened.

// loadSandbox is kvSandbox plus the provider table Load() insists on: the point under test is a
// broken profile, not a missing providers.json.
func loadSandbox(t *testing.T) string {
	t.Helper()
	dir := kvSandbox(t)
	if err := os.WriteFile(filepath.Join(dir, "providers.json"), []byte(`{"providers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadSubstitutesLastUsableVersion(t *testing.T) {
	dir := loadSandbox(t)
	good := "desc=good\nheavy=pA/m1\n"
	// Two saves through store.Write: the first creates the file (nothing to snapshot), the
	// second fills the history ring with the version the user meant to keep.
	for i := 0; i < 2; i++ {
		if err := Write(filepath.Join(dir, "mappings", "main.kv"), []byte(good)); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate a hand edit with a key this build does not know (the real outage: the running
	// binary was a release older than the config it was handed).
	if err := os.WriteFile(filepath.Join(dir, "mappings", "main.kv"),
		[]byte("desc=good\nheavy=pA/m1\nhidden-agent=gemini\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap, err := Load()
	if err != nil {
		t.Fatalf("Load must not fail over one profile: %v", err)
	}
	if len(snap.Problems) != 1 || snap.Problems[0].Profile != "main" {
		t.Fatalf("want one problem for main, got %+v", snap.Problems)
	}
	if snap.Problems[0].Used == "" {
		t.Fatalf("want the problem to say where the usable copy came from")
	}
	if len(snap.Profiles) != 1 || snap.Profiles[0].Name != "main" {
		t.Fatalf("the profile must still be there: %+v", snap.Profiles)
	}
	if got := snap.Profiles[0].Roles["heavy"]; len(got) != 1 || got[0].Model != "m1" {
		t.Fatalf("the substituted version is wrong: %+v", got)
	}
}

func TestLoadWithoutBackupReportsAndOmits(t *testing.T) {
	loadSandbox(t)
	// The file never existed before, so there is no history to fall back to.
	if err := os.WriteFile(filepath.Join(os.Getenv("NEWGATE_HOME"), "mappings", "broken.kv"),
		[]byte("bogus=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Problems) != 1 || snap.Problems[0].Profile != "broken" || snap.Problems[0].Used != "" {
		t.Fatalf("problems = %+v", snap.Problems)
	}
	for _, p := range snap.Profiles {
		if p.Name == "broken" {
			t.Fatalf("an unparseable profile with no history must be absent")
		}
	}
}

func TestWatcherKeepsLastGoodProfile(t *testing.T) {
	dir := loadSandbox(t)
	path := filepath.Join(dir, "mappings", "claude.kv")
	if err := os.WriteFile(path, []byte("desc=claude\nheavy=pA/m1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := NewWatcher(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var reported []string
	w.OnError(func(e error) { reported = append(reported, e.Error()) })

	// Hand-break the file the way a typo does (bypasses the history ring on purpose: the
	// in-memory snapshot is the fallback here).
	if err := os.WriteFile(path, []byte("desc=claude\nnot-a-key=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !w.Reload(true) {
		t.Fatalf("a reload with one bad profile must still swap the rest")
	}
	snap := w.Current()
	found := false
	for _, p := range snap.Profiles {
		if p.Name == "claude" {
			found = true
			if got := p.Roles["heavy"]; len(got) != 1 || got[0].Model != "m1" {
				t.Fatalf("kept version is wrong: %+v", got)
			}
		}
	}
	if !found {
		t.Fatalf("the running snapshot lost its profile over a typo: %+v", snap.Profiles)
	}
	if len(reported) == 0 || !strings.Contains(reported[0], "claude") {
		t.Fatalf("the partial failure must be reported, got %v", reported)
	}
}
