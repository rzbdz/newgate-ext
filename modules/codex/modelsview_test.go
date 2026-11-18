package codex

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// This family covers the **write side** of that mapping table: what
// actually happened on disk after one press of "apply" in the UI.
//
// It is the only place this card can destroy things (the user's table, and the rows
// he hand-wrote in it), and its failure modes are all **silent**: written empty, mode wiped,
// someone else's fresh change overwritten -- all three show in the UI only as "save succeeded".
//
// Why it is pushed down to this layer rather than the browser layer (a rule the user
// set 2026-09-21): **UI tests mainly cover the BFF and the layer below it, and e2e is touched
// as little as possible** -- that browser check takes minutes per run, while these are pure
// functions and file I/O, millisecond scale. What really only the browser can verify (did the
// UI change after clicking the button) stays in ui_check; everything else comes down here.

// editOf assembles a payload the UI sends back: `{items:[{id, values:{slug,tier}}]}`.
func editOf(rows ...[2]string) json.RawMessage {
	type item struct {
		ID     string            `json:"id"`
		Values map[string]string `json:"values"`
	}
	items := make([]item, 0, len(rows))
	for _, r := range rows {
		items = append(items, item{ID: r[0], Values: map[string]string{"slug": r[0], "tier": r[1]}})
	}
	b, _ := json.Marshal(map[string]any{"items": items})
	return b
}

func readFile(t *testing.T) Models {
	t.Helper()
	b, err := os.ReadFile(ModelsFile())
	if err != nil {
		return Models{}
	}
	var m Models
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("cannot read the copy on disk: %v", err)
	}
	return m
}

// TestApplyWritesTheWholeTableAndKeepsTheMode: whole-table
// write-back, and the mode cell must not be wiped.
//
// mode is **another matter** (it says how takeover writes config.toml),
// and saving this table has nothing to do with it -- the symptom of wiping it
// is "after editing the mapping, the mode quietly went back to takeover", and
// that Note in the UI changes with it, so the user only thinks they misclicked.
func TestApplyWritesTheWholeTableAndKeepsTheMode(t *testing.T) {
	testkit.Sandbox(t)
	if err := SetMode(ModeRename); err != nil {
		t.Fatal(err)
	}
	if _, err := applyModels(editOf([2]string{"gpt-9-a", "heavy"}, [2]string{"gpt-9-b", "light"}), store.Revision(ModelsFile())); err != nil {
		t.Fatal(err)
	}
	m := readFile(t)
	if m.Mode != ModeRename {
		t.Errorf("saving that table changed the mode to %q - it does not belong to this table, it must not be touched", m.Mode)
	}
	if len(m.Models) != 2 || m.Models[0].Slug != "gpt-9-a" || m.Models[1].Tier != "light" {
		t.Errorf("the table on disk is not the one submitted by the UI: %+v", m.Models)
	}
}

// TestApplyRejectsRowsThatWouldSilentlyDoNothing pins the three "stored but does not count".
//
// None of the three makes the UI error, and the
// symptom is always "added, gone, and no reason given":
//
//	empty name  -> matches nothing;
//	empty tier  -> effectiveModels drops it, so that row disappears by itself after saving;
//	two rows with the same name -> the later one never gets a turn, while both are drawn in the UI.
func TestApplyRejectsRowsThatWouldSilentlyDoNothing(t *testing.T) {
	cases := []struct {
		name string
		rows [][2]string
	}{
		{"name empty", [][2]string{{"", "heavy"}}},
		{"tier empty", [][2]string{{"gpt-9-a", ""}}},
		{"two rows with the same name", [][2]string{{"gpt-9-a", "heavy"}, {"gpt-9-a", "light"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			testkit.Sandbox(t)
			if _, err := applyModels(editOf(c.rows...), store.Revision(ModelsFile())); err == nil {
				t.Fatal("this row should be rejected (it would silently do nothing), yet it was saved")
			}
			if _, err := os.Stat(ModelsFile()); err == nil {
				t.Error("a rejected save must not touch the copy on disk")
			}
		})
	}
}

// TestApplyRefusesToClobberAFileChangedUnderneath: when someone else (a hand edit,
// another tab) changed that file, answer with a conflict **and write nothing**.
//
// This is the same rule as other modules' CAS, but one more layer is necessary
// here: that table is **hand-writable by the user** (it lives in his config
// directory), so "the command line just changed it" is not hypothetical.
func TestApplyRefusesToClobberAFileChangedUnderneath(t *testing.T) {
	testkit.Sandbox(t)
	if _, err := applyModels(editOf([2]string{"gpt-9-a", "heavy"}), ""); err != nil {
		t.Fatal(err)
	}
	// Take an **outdated** baseline (from before the file was created) and save again.
	stale := store.RevisionOf([]byte("{}"))
	if _, err := applyModels(editOf([2]string{"gpt-9-b", "light"}), stale); err == nil {
		t.Fatal("a stale baseline should be refused, not overwrite someone else's copy")
	}
	m := readFile(t)
	if len(m.Models) != 1 || m.Models[0].Slug != "gpt-9-a" {
		t.Errorf("a rejected save changed the copy on disk: %+v", m.Models)
	}
}

// TestApplyKeepsUnknownKeys: replace only the models
// section; other keys in the file are left as is.
//
// Today there is only mode, and this test is written for **the future**:
// when a key is added to that file (say some version's migration
// marker), nobody should lose it because "the mapping table was saved".
func TestApplyKeepsUnknownKeys(t *testing.T) {
	testkit.Sandbox(t)
	seed := `{"mode":"rename","something_else":{"a":1}}`
	if err := os.WriteFile(ModelsFile(), []byte(seed), 0o660); err != nil {
		t.Fatal(err)
	}
	if _, err := applyModels(editOf([2]string{"gpt-9-a", "heavy"}), store.Revision(ModelsFile())); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(ModelsFile())
	if !strings.Contains(string(b), "something_else") {
		t.Errorf("saving that table lost an unknown key:\n%s", b)
	}
}
