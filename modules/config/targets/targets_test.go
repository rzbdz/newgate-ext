package targets

import (
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

func seed(t *testing.T) {
	t.Helper()
	testkit.Sandbox(t)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProviders(&domain.Providers{Providers: map[string]domain.Provider{
		"relay": {BaseURL: "https://relay.example/v1", Protocol: "openai"},
	}}); err != nil {
		t.Fatal(err)
	}
	for name, model := range map[string]string{"one": "same-model", "two": "other-model"} {
		// two's normal shares a binding with one: after dedup it should appear only once.
		roles := map[string]domain.Candidates{
			"normal": {{Provider: "relay", Model: model}},
			"mid":    {{Provider: "relay", Model: "same-model"}},
		}
		if err := store.SaveProfile(&domain.Profile{Name: name, Roles: roles}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEnumerateDedupesAndSorts(t *testing.T) {
	seed(t)
	rows, uniq, err := Enumerate(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2*len(domain.Roles) {
		t.Fatalf("rows = %d, want %d (two profiles × every role)", len(rows), 2*len(domain.Roles))
	}
	want := []Target{
		{Provider: "relay", Model: "other-model"},
		{Provider: "relay", Model: "same-model"},
	}
	if len(uniq) != len(want) {
		t.Fatalf("uniq = %v, want %v", uniq, want)
	}
	for i := range want {
		if uniq[i] != want[i] {
			t.Errorf("uniq[%d] = %v, want %v", i, uniq[i], want[i])
		}
	}
	// Sorting: profile name order, then tier order (heavy before light).
	if rows[0].Profile != "one" || rows[0].Role != "heavy" {
		t.Errorf("first row = %s/%s, want one/heavy", rows[0].Profile, rows[0].Role)
	}
}

func TestEnumerateOneAndUnknown(t *testing.T) {
	seed(t)
	rows, uniq, err := Enumerate(Options{Only: "two"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Profile != "two" {
			t.Errorf("Only=two leaked profile %q", r.Profile)
		}
	}
	if len(uniq) != 2 {
		t.Errorf("uniq = %v, want two targets", uniq)
	}
	if _, _, err := Enumerate(Options{Only: "nope"}); err == nil {
		t.Fatal("an unknown profile should be an error, not an empty round")
	}
}

func TestEnumerateOnlyTargetSkipsProfiles(t *testing.T) {
	seed(t)
	want := Target{Provider: "relay", Model: "manual"}
	rows, uniq, err := Enumerate(Options{OnlyTarget: &want})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want none (the target was named, not discovered)", rows)
	}
	if len(uniq) != 1 || uniq[0] != want {
		t.Errorf("uniq = %v, want [%v]", uniq, want)
	}
}
