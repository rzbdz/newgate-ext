package webdashboard

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// The theme layer pins three things: **registry by token, not order**, **a gone stored
// theme is reported honestly**, **switching hits disk** - all three "no error, wrong UI".

func name(s string) func() string { return func() string { return s } }

// TestThemeRegistryRejectsDuplicates: two themes grabbing one ID **errors**, not the later
// one overwriting.
//
// Why not overwrite: "which won" then depends on assembly order (directory names), which
// nobody can see - the UI just shows wrong colors, and another machine may differ. If
// reported, both module authors look once and know which one to change.
func TestThemeRegistryRejectsDuplicates(t *testing.T) {
	r := newThemeRegistry()
	rel, err := r.RegisterTheme(Theme{ID: "dark", Name: name("Black")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RegisterTheme(Theme{ID: "dark", Name: name("another black")}); err == nil {
		t.Fatal("registering the same ID twice should error")
	}
	// After undo it **can register again** - else testing/testkit installing graphs twice in
	// one process hits what looks like "the theme is broken" on the second one.
	if err := rel(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RegisterTheme(Theme{ID: "dark", Name: name("Black")}); err != nil {
		t.Fatalf("should be able to register again after revoking: %v", err)
	}
}

// TestThemeRegistryNeedsIDAndName: missing either is not a usable theme.
//
// No ID and it cannot be stored in state.json (choosing it is not choosing); no Name and
// the dropdown shows a **blank option** - clicking changes the UI, but nobody knows to what.
func TestThemeRegistryNeedsIDAndName(t *testing.T) {
	r := newThemeRegistry()
	if _, err := r.RegisterTheme(Theme{Name: name("no id")}); err == nil {
		t.Error("missing ID should error")
	}
	if _, err := r.RegisterTheme(Theme{ID: "x"}); err == nil {
		t.Error("missing Name should error")
	}
}

// TestThemeDocDropsAThemeThatIsGone: when the stored theme is **not in this assembly**
// (module disabled, directory deleted), the payload's active is empty.
//
// Both directions matter: empty lets the UI fall back to factory tokens (not clinging to a
// nonexistent ID); and **the on-disk value must not be changed** - the user may have only
// disabled it temporarily; a write-back erases that choice for good, unclicked.
func TestThemeDocDropsAThemeThatIsGone(t *testing.T) {
	testkit.Sandbox(t)
	r := newThemeRegistry()
	if _, err := r.RegisterTheme(Theme{ID: "dark", Name: name("Black")}); err != nil {
		t.Fatal(err)
	}
	if err := setSelectedTheme("gone"); err != nil {
		t.Fatal(err)
	}
	doc := themeDocOf(r)
	if doc.Active != "" {
		t.Errorf("the stored set is gone, active should report empty, actual %q", doc.Active)
	}
	if got := selectedTheme(); got != "gone" {
		t.Errorf("the value on disk must not be changed (it is needed when installing back), actual %q", got)
	}
	if len(doc.Themes) != 1 || doc.Themes[0].ID != "dark" {
		t.Errorf("an installed skin should be reported as usual, actual %+v", doc.Themes)
	}
	// Names are translated here (the backend resolves language; a second guess is a second source).
	if doc.Themes[0].Name != "Black" {
		t.Errorf("the name should have been evaluated, actual %q", doc.Themes[0].Name)
	}
}

// TestThemesEndpoint covers the switch path: read the table, switch, unknown ID reported.
func TestThemesEndpoint(t *testing.T) {
	testkit.Sandbox(t)
	h := newHandler()
	reg, err := h.themes.RegisterTheme(Theme{ID: "dark", Name: name("Black"), Dark: true, CSS: "--bg: #000;"})
	if err != nil {
		t.Fatal(err)
	}
	defer reg()

	get := func() themeDoc {
		t.Helper()
		rec := get(t, h, "/api/themes")
		if rec.Code != http.StatusOK {
			t.Fatalf("reading the skin table should be 200, actual %d: %s", rec.Code, rec.Body)
		}
		var doc themeDoc
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	set := func(body string) int { return post(t, h, "/api/themes", body).Code }

	if got := get(); got.Active != "" || len(got.Themes) != 1 {
		t.Fatalf("at the start it should be \"nothing chosen\" plus one skin, actual %+v", got)
	}
	if code := set(`{"id":"dark"}`); code != http.StatusOK {
		t.Fatalf("switching skins should be 200, actual %d", code)
	}
	if got := get(); got.Active != "dark" {
		t.Errorf("after switching, active should be dark, actual %q", got.Active)
	}
	// Empty id = back to factory tokens. It is this cell's **fourth pre-existing value**, not clear.
	if code := set(`{"id":""}`); code != http.StatusOK {
		t.Fatalf("resetting to factory should be 200, actual %d", code)
	}
	if got := get(); got.Active != "" {
		t.Errorf("after resetting to factory, active should be empty, actual %q", got.Active)
	}
	// Unknown ID: error, not silent fallback - "clicked, nothing happened" and "this theme is
	// gone" are two sentences, and the former makes people think the UI hung.
	if code := set(`{"id":"nope"}`); code != http.StatusBadRequest {
		t.Errorf("an unknown skin should be 400, actual %d", code)
	}
	// A POST with no Content-Type: this door edits disk, same rule as the other write doors.
	// host empty = raw's default path (it exists for "change a Host / Content-Type").
	if code := raw(t, h, http.MethodPost, "/api/themes", `{"id":"dark"}`, loopback, "").Code; code != http.StatusUnsupportedMediaType {
		t.Errorf("without a JSON Content-Type it should be 415, actual %d", code)
	}
}
