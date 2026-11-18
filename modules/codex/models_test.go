package codex

import (
	"os"
	"testing"

	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// This family pins both halves of "codex uses its own model names":
//
//	which codex name to write for which tier (rename mode, written into config.toml)
//	which tier to recognize which name as (when the request arrives, via dynamic roles)
//
// Both halves use the **same table**, so the most easily broken error is
// "the two sides disagree" -- which makes nothing error, it just makes the
// user pick gpt-5.6-luna in codex and land on some tier nobody can explain.
func seedModels(t *testing.T, body string) {
	t.Helper()
	testkit.Sandbox(t)
	if err := os.WriteFile(ModelsFile(), []byte(body), 0o660); err != nil {
		t.Fatal(err)
	}
}

func TestModeDefaultsToTakeoverAndRoundTrips(t *testing.T) {
	testkit.Sandbox(t)
	// When the file does not exist it is takeover: **changing no one's
	// current state** (that was the only behavior before 2026-09-21, and an
	// old assembly upgraded should not suddenly take over in the new mode).
	if got := Mode(); got != ModeTakeover {
		t.Fatalf("without this file the mode is %q, should be %q", got, ModeTakeover)
	}
	if err := SetMode(ModeRename); err != nil {
		t.Fatal(err)
	}
	if got := Mode(); got != ModeRename {
		t.Fatalf("after writing rename it reads back as %q", got)
	}
	// Unrecognized values are always treated as the **default**, never a third mode in the file.
	if err := SetMode("banana"); err != nil {
		t.Fatal(err)
	}
	if got := Mode(); got != ModeTakeover {
		t.Fatalf("after writing an unknown value the mode is %q, should fall back to %q", got, ModeTakeover)
	}
}

// TestTheTableComesFromTheFileOnceItHasOne pins "the
// table in the UI is what you see is what you get".
//
// The semantics changed once on 2026-09-21: it used to be "factory values overlaid with
// the file" (comfortable to hand-write; one line adds one line), but once it became a table
// in the UI it was wrong -- the user deletes a row on the card and saves, and on read the
// factory value pushes it back: the UI shows the delete succeeded while nothing happened.
//
// So now: if the file **has** a models section, that is everything; only without
// it are the five factory ones used. This also means "the factory ones do not vanish
// just because the file did not copy them" holds only when **that section is absent**.
func TestTheTableComesFromTheFileOnceItHasOne(t *testing.T) {
	// No such section (only mode written) -> the five factory ones as usual.
	seedModels(t, `{"mode":"rename"}`)
	if got := len(effectiveModels()); got != len(defaultModels) {
		t.Errorf("with no models section in the file it should use the factory table (%d entries), got %d", len(defaultModels), got)
	}

	// With the section -> it is everything; factory values no longer participate.
	seedModels(t, `{"models":[{"slug":"gpt-7-nova","tier":"heavy"}]}`)
	got := effectiveModels()
	if len(got) != 1 || got[0].Slug != "gpt-7-nova" {
		t.Fatalf("when the file has models, use it, got %+v", got)
	}
	// An empty array is also "has": the user emptied the table, not "not configured yet".
	seedModels(t, `{"models":[]}`)
	if got := len(effectiveModels()); got != 0 {
		t.Errorf("an empty array means an explicit 'no mapping'; factory values must not come back, got %d entries", got)
	}
}

// TestModelForFallsBackInsteadOfFailing: when the reverse lookup fails,
// return the empty string and the caller falls back to the tier name.
//
// A narrowed table should not make takeover unable to write anything
// -- it just means "this tier has no corresponding codex model".
func TestModelForFallsBackInsteadOfFailing(t *testing.T) {
	// The file has **no** models section -> the factory table applies.
	seedModels(t, `{"mode":"rename"}`)
	if got := modelFor("heavy"); got != "gpt-6-astra" {
		t.Errorf("heavy resolves to %q, should be gpt-6-astra (factory table)", got)
	}
	if got := modelFor("nonexistent-tier"); got != "" {
		t.Errorf("a tier with no matching model should give an empty string (the caller then falls back to the tier name), got %q", got)
	}
	if got := modelFor(""); got != "" {
		t.Errorf("an empty tier should give an empty string, got %q", got)
	}
}

// TestRolesAreContributedOnlyInRenameMode is the **most important** test in this change.
//
// Once these names are registered as dynamic roles, `IsKnownRole` is true, so the path "resolve
// a concrete model name back to its tier, putting the named model first" is short-circuited. And
// that path is what **other clients** use: this machine's smt-codex chain carries gpt-5.6-terra.
// Unconditional registration = quietly changing the behavior of people who did not opt in.
//
// So this test pins "people who did not opt in are unaffected":
// in takeover mode this provider must contribute **not one role**.
func TestRolesAreContributedOnlyInRenameMode(t *testing.T) {
	testkit.Sandbox(t)
	rs, err := rolesProvider{}.Roles()
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 0 {
		t.Errorf("in takeover mode %d roles were contributed, should be 0 - "+
			"registering them would short-circuit the 'concrete model name resolves back to a tier' path, and other people's requests travel over that path", len(rs))
	}

	if err := SetMode(ModeRename); err != nil {
		t.Fatal(err)
	}
	rs, err = rolesProvider{}.Roles()
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) == 0 {
		t.Fatal("in rename mode not a single role was contributed either - then codex's model names would 404")
	}
	byKey := map[string]string{}
	for _, r := range rs {
		byKey[r.Key] = r.Default
	}
	// Check the factory table one by one: **both sides are the same table**, and a mismatch
	// means "whoever was picked in codex, which tier it lands on is unexplainable".
	for _, want := range defaultModels {
		if byKey[want.Slug] != want.Tier {
			t.Errorf("the default tier of role %s is %q, the table says %q", want.Slug, byKey[want.Slug], want.Tier)
		}
	}
}

// TestRenameModeWritesCodexModelNames: the mode's **whole point** is in this one assertion.
//
// takeover mode writes the tier name (codex knows only one model, cannot
// switch); rename mode writes codex's own model names, so every entry in its
// picker is recognized, and every entry is mapped back to a tier when it arrives.
func TestRenameModeWritesCodexModelNames(t *testing.T) {
	testkit.Sandbox(t)
	seedModels(t, `{"mode":"rename","models":[{"slug":"gpt-7-nova","tier":"heavy"},
	                                        {"slug":"gpt-5.6-luna","tier":"normal"}]}`)

	// The slot table was never written -> both slots use the registration default (normal).
	got := slotValue("model")
	if got == "normal" {
		t.Fatal("in rename mode it still writes the tier name - that switch did not happen")
	}
	if got != "gpt-5.6-luna" {
		t.Errorf("the codex model for the normal tier is %q, should be the gpt-5.6-luna entry in the table", got)
	}
	// A tier **not in** the table: falls back to the tier name (no error, and not the empty
	// string). This is the cost of "what you see is what you get", and how it should be --
	// there is no light in the table in the UI, so no codex model name can represent light.
	if got := modelFor("light"); got != "" {
		t.Errorf("light is not in the table, so the reverse lookup should be an empty string, got %q", got)
	}
	// The one in the file is also reverse-lookup-able (it is the only heavy right now).
	if got := modelFor("heavy"); got != "gpt-7-nova" {
		t.Errorf("heavy resolves to %q, should be gpt-7-nova", got)
	}
	// And a tier not in the table gives the empty string (the caller falls back to the tier name). The
	// file only wrote one entry, so light now has **no** corresponding codex model -- this is the cost
	// of "what you see is what you get", and how it should be: the table in the UI has only one row.
	if got := modelFor("light"); got != "" {
		t.Errorf("light is not in the table, so the reverse lookup should be an empty string, got %q", got)
	}
}
