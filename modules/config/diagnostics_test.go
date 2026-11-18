package config

import (
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// Pins the 2026-10-28 item: the per-agent "set on its own" criterion is whether there
// is a record in state.Active, not "is it different from the global default". The latter
// would hide "ds explicitly chosen for claude while the default happens to be ds too"
// -- later the user changes the global default thinking claude follows, and it does not.
func TestStatusShowsExplicitOverrideEvenWhenItEqualsDefault(t *testing.T) {
	testkit.Sandbox(t)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProfile(&domain.Profile{Name: "ds", Roles: map[string]domain.Candidates{
		"normal": {{Provider: "relay", Model: "model-x"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(&domain.State{
		DefaultProfile: "ds",
		Port:           8899,
		Active:         map[string]string{"claude": "ds"},
	}); err != nil {
		t.Fatal(err)
	}

	lines := reporter{}.Status()
	if len(lines) != 1 {
		t.Fatalf("status lines = %d, want 1", len(lines))
	}
	if !strings.Contains(lines[0].Value, "claude → ") {
		t.Fatalf("status value %q does not show the explicit claude override", lines[0].Value)
	}

	// Without a record it must not turn "follows the default" into "set on its own" out of nothing.
	if err := store.ClearActiveProfile("claude"); err != nil {
		t.Fatal(err)
	}
	if got := (reporter{}).Status()[0].Value; strings.Contains(got, "claude → ") {
		t.Fatalf("status value %q shows an override that is not in state.Active", got)
	}
}
