package opencodeomo

import (
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/lib/view"
)

// TestSlotsCardExplainsAnAbsentRegistry: before any omo takeover, **still emit
// a card** stating the reason and how to generate one.
//
// Hiding it from the list would read as "this module is not in this build" -
// it is really "never taken over once" (same sentence as the CLI's omoRegistry: Concept.Broken).
func TestSlotsCardExplainsAnAbsentRegistry(t *testing.T) {
	isolate(t)

	concepts, err := omoConcepts("")
	if err != nil {
		t.Fatalf("a missing registry should not error (that is a normal state, not a failure): %v", err)
	}
	if len(concepts) != 1 {
		t.Fatalf("should produce one card, actual %d", len(concepts))
	}
	c := concepts[0]
	if c.Broken == "" {
		t.Fatal("without a registry it must say why (Broken), otherwise the UI shows an empty card")
	}
	// The reason needs two things: where the file is and how to make it. "Unreadable" blocks.
	if !strings.Contains(c.Broken, SlotsFile()) {
		t.Errorf("the reason should name the file (%s): %q", SlotsFile(), c.Broken)
	}
	if !strings.Contains(c.Broken, "newgate on opencode") {
		t.Errorf("the reason should say how to generate one: %q", c.Broken)
	}
}

// TestSlotsCardIsWritableAndKeepsOneWriteSemantics is this card's write ratchet.
//
// It replaces the pre-2026-09-20 "this card must be read-only" assertion - that
// decision changed (users move slots on the web page), but **its intent did not**:
// only one semantics for writing slot ownership. So it does not just assert
// "Apply is not nil" but that it **uses omoUse's criterion** (validateBindingValue).
func TestSlotsCardIsWritableAndKeepsOneWriteSemantics(t *testing.T) {
	isolate(t)
	if err := WriteOmoSlots(&OmoSlots{
		Version: 1, Mode: "current",
		Slots: []OmoSlot{
			{Key: "omo-sisyphus", Kind: "agent", Name: "sisyphus", Current: "normal", Suggested: "heavy"},
			{Key: "omo-librarian", Kind: "agent", Name: "librarian", Current: "light", Suggested: "light"},
		},
		Overrides: map[string]string{"omo-sisyphus": "mid"},
	}); err != nil {
		t.Fatal(err)
	}

	concepts, err := omoConcepts("")
	if err != nil {
		t.Fatal(err)
	}
	c := concepts[0]
	if c.Broken != "" {
		t.Fatalf("there is a registry yet it says it cannot be read: %s", c.Broken)
	}
	if c.Apply == nil {
		t.Fatal("this card should be writable (the user moves slots to other tiers on the web page)")
	}
	if c.Kind != view.KindToggles {
		t.Fatalf("Kind should be toggles (one selector per row), actual %q", c.Kind)
	}
	data, ok := c.Data.(omoData)
	if !ok {
		t.Fatalf("Data should be omoData, actual %T", c.Data)
	}

	// One row for mode plus one per slot, and **every row's id is a stable identity**.
	byID := map[string]omoToggle{}
	for _, it := range data.Items {
		if it.ID == "" || it.Label == "" {
			t.Errorf("item missing id or label: %+v", it)
		}
		if it.Kind != "select" {
			t.Errorf("item %q should be select, actual %q", it.ID, it.Kind)
		}
		if it.Value == "" && len(it.Options) == 0 {
			t.Errorf("item %q has neither a value nor choices, so the UI shows a dead dropdown", it.ID)
		}
		byID[it.ID] = it
	}
	if _, ok := byID["mode"]; !ok {
		t.Error("the mode item is missing: current/suggested is the only switch on this card that affects every slot")
	}
	if _, ok := byID["omo-sisyphus"]; !ok {
		t.Error("the omo-sisyphus item is missing")
	}
	// The values must include empty (= follow the default) and the four ladder
	// tiers, or the user can neither move it elsewhere nor undo it (empty = clear).
	opts := map[string]bool{}
	for _, o := range byID["omo-sisyphus"].Options {
		opts[o] = true
	}
	for _, want := range []string{"", "heavy", "normal", "mid", "light", "vision"} {
		if !opts[want] {
			t.Errorf("the choices are missing %q", want)
		}
	}
	// The override must **sit on the value** (not hide in why): the first thing a
	// user sees on this card is "I changed this slot".
	if got := byID["omo-sisyphus"].Value; got != "mid" {
		t.Errorf("an overridden slot should show the override as its value, actual %q", got)
	}
	if got := byID["omo-librarian"].Value; got != "" {
		t.Errorf("the one without an override should be empty (= follow the default), actual %q", got)
	}

	// ---- write semantics: the same criterion as omoUse ----
	if _, err := c.Apply([]byte(`{"omo-sisyphus":"not a binding"}`), ""); err == nil {
		t.Error("a bad value should be rejected (validateBindingValue is the same one the CLI uses)")
	}
	if _, err := c.Apply([]byte(`{"mode":"whatever"}`), ""); err == nil {
		t.Error("mode only accepts current/suggested, anything else should be rejected")
	}
	if _, err := c.Apply([]byte(`{"omo-sisyphus":"@omo-librarian","mode":"suggested"}`), ""); err != nil {
		t.Fatalf("a legal @reference should be accepted: %v", err)
	}
	after := ReadOmoSlots()
	if after.Overrides["omo-sisyphus"] != "@omo-librarian" {
		t.Errorf("the override was not written: %+v", after.Overrides)
	}
	if after.Mode != "suggested" {
		t.Errorf("mode was not written: %q", after.Mode)
	}
	// The live record (computed at takeover) must not be touched by this card.
	if after.Slots[0].Current != "normal" || after.Slots[0].Suggested != "heavy" {
		t.Errorf("the UI must not change Was/Current/Suggested: %+v", after.Slots[0])
	}
	// Empty = clear the override (the only natural way to say "undo").
	if _, err := c.Apply([]byte(`{"omo-sisyphus":""}`), ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadOmoSlots().Overrides["omo-sisyphus"]; ok {
		t.Error("an empty value should delete the override")
	}
}

// TestSuggestedDifferenceIsVisible pins that the "differs from suggested"
// criterion **is still visible on the new card**.
//
// Same as the CLI (`Suggested != "" && Suggested != Current`): both UIs must give the
// same answer. **Never** compare effective - override makes the two differ, the card
// shouts, nobody reads. Toggle rows have no color, so the signal lands in why.
func TestSuggestedDifferenceIsVisible(t *testing.T) {
	differs := OmoSlot{Key: "omo-a", Kind: "agent", Name: "a", Current: "normal", Suggested: "heavy"}
	same := OmoSlot{Key: "omo-b", Kind: "agent", Name: "b", Current: "light", Suggested: "light"}
	none := OmoSlot{Key: "omo-c", Kind: "category", Name: "c", Current: "mid"}

	if !suggestsDifferently(differs) {
		t.Error("a value that differs from the suggestion should be judged as differing")
	}
	for _, s := range []OmoSlot{same, none} {
		if suggestsDifferently(s) {
			t.Errorf("%s should not be judged as differing (the check compares against current)", s.Key)
		}
	}

	isolate(t)
	if err := WriteOmoSlots(&OmoSlots{
		Version: 1, Mode: "current",
		Slots: []OmoSlot{differs, same, none},
	}); err != nil {
		t.Fatal(err)
	}
	concepts, err := omoConcepts("")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]omoToggle{}
	for _, it := range concepts[0].Data.(omoData).Items {
		byID[it.ID] = it
	}
	if !strings.Contains(byID["omo-a"].Why, "differs") {
		t.Errorf("a slot that deviates from the suggestion should say so in why, actual %q", byID["omo-a"].Why)
	}
	for _, k := range []string{"omo-b", "omo-c"} {
		if strings.Contains(byID[k].Why, "differs") {
			t.Errorf("%s does not deviate, so it should not say differs: %q", k, byID[k].Why)
		}
	}
	// All three live values must be in why (now/effective/suggested) - none of the
	// three columns' information may be lost to the new shape.
	for _, want := range []string{"agent/a", "normal", "heavy"} {
		if !strings.Contains(byID["omo-a"].Why, want) {
			t.Errorf("why is missing the live value %q: %q", want, byID["omo-a"].Why)
		}
	}
}
