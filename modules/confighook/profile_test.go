package confighook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/store"
)

// "I want exactly this" and "I picked nothing and it happens to be the default" are
// two **states**, different in config: only the first has a record in state.json.
//
// Seen for real (2026-09-29) on the web first screen: clicking "use ds" while auto was
// ds wrote a record, yet the UI said "follow auto" and the highlight left that card; the
// user asked why clicking ds selected auto.
func TestStoredChoiceCountsEvenWhenItMatchesTheDefault(t *testing.T) {
	sandbox(t)
	// Two tier files: SetDefaultProfile validates that this one exists (pointing at a
	// non-existent name is a user typo and should be reported, so create them here).
	dir := filepath.Join(os.Getenv("NEWGATE_HOME"), "mappings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"ds", "ark"} {
		if err := os.WriteFile(filepath.Join(dir, n+".kv"), []byte("normal = smt/"+n+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetDefaultProfile("ds", false); err != nil {
		t.Fatal(err)
	}
	p := AgentProfile{AgentID: "claude"}

	if p.IsOverride() {
		t.Error("with nothing ever set it must not report 'set separately' - it should be the one following the default")
	}
	if err := p.Write("ds"); err != nil {
		t.Fatal(err)
	}
	if !p.IsOverride() {
		t.Error("after explicitly choosing ds (and the default is also ds) it **must** report 'set separately':" +
			"otherwise the UI would describe it as 'following the default', and what the user meant to pin looks unpinned")
	}
	if got := p.Stored(); got != "ds" {
		t.Errorf("the stored one should be ds, got %q", got)
	}
	// The reverse must hold too: change the default, and the pinned client **must not** follow.
	if err := store.SetDefaultProfile("ark", false); err != nil {
		t.Fatal(err)
	}
	if got := p.Active(); got != "ds" {
		t.Errorf("after pinning, changing the default should leave this one on ds, got %q", got)
	}
	// Empty string = let go, back to following; only then should "set apart" stop.
	if err := p.Write(""); err != nil {
		t.Fatal(err)
	}
	if p.IsOverride() {
		t.Error("after releasing it must no longer report 'set separately'")
	}
	if got := p.Active(); got != "ark" {
		t.Errorf("after releasing it should follow the default ark, got %q", got)
	}
}
