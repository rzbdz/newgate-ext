package claudecode

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This family tests the classifier card. Assertions are on **data** (view.Table), not on HTML.
//
// Why it is worth several assertions: `naked forever` is the switch that permanently short-circuits
// the safety classifier, and in the distribution with cli removed this card is the **only**
// place it can be seen. So "the picture is the wrong color when it is on" and "the config is
// still there but this layer does not count right now" must both really hold -- if they are wrong
// nothing errors, it just makes the user think the classifier is still on (or that it is off).

// writeState writes several module configs into the sandbox's
// state.json -- that is what the card reads (store.LoadState).
func writeState(t *testing.T, modules map[string]any) {
	t.Helper()
	st := store.LoadState()
	st.ModuleConfig = map[string][]byte{}
	for key, v := range modules {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		st.ModuleConfig[key] = raw
	}
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
}

// itemOf gets that cell by **id**.
//
// By id, not by position: positions drift as fields come and go, while
// id is the identity this module chooses, and the key the UI uses to
// match on the way back (same reason as Record/Field in lib/view).
func itemOf(t *testing.T, id string) clToggle {
	t.Helper()
	c := classifierConcept()
	if c.ID != "claudecode.classifier" || c.Kind != view.KindToggles {
		t.Fatalf("the identity of this card changed: id=%q kind=%q", c.ID, c.Kind)
	}
	data, ok := c.Data.(clData)
	if !ok {
		t.Fatalf("Data is not clData, it is %T", c.Data)
	}
	for _, it := range data.Items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("this card has no %q cell (it has %d cells now)", id, len(data.Items))
	return clToggle{}
}

func nakedItemOf(t *testing.T) clToggle { t.Helper(); return itemOf(t, "naked") }
func overrideItemOf(t *testing.T) clToggle {
	t.Helper()
	return itemOf(t, "classifier_override")
}

func TestTheNakedRowIsQuietWhenNothingIsConfigured(t *testing.T) {
	testkit.Sandbox(t)
	it := nakedItemOf(t)
	if it.Value != "" {
		t.Errorf("when never configured it should be empty (= off), got %q", it.Value)
	}
	if !strings.Contains(it.Why, i18n.T("off — the Bash classifier is asked as usual", nil)) {
		t.Errorf("the why should make clear it is off, got %q", it.Why)
	}
}

// TestForeverAndAWindowReadDifferently: among the three states, "forever" and "limited"
// must **read differently**. Whether they end by themselves is the most important
// thing on this card -- saying "it will be over in a bit" and "it will never turn off"
// as the same sentence makes the user careless in the state that is actually dangerous.
func TestForeverAndAWindowReadDifferently(t *testing.T) {
	testkit.Sandbox(t)

	writeState(t, map[string]any{NakedConfigKey: NakedConfig{Mode: "forever"}})
	forever := nakedItemOf(t)
	if forever.Value != "forever" {
		t.Errorf("permanent mode should show the value as forever, got %q", forever.Value)
	}
	if !strings.Contains(forever.Why, i18n.T("on (forever) — the classifier is short-circuited and every request logs [naked]", nil)) {
		t.Errorf("permanent mode should say it will not turn itself off, got %q", forever.Why)
	}

	writeState(t, map[string]any{NakedConfigKey: NakedConfig{
		Mode: "on", ExpiresAt: time.Now().Add(90 * time.Second),
	}})
	window := nakedItemOf(t)
	if window.Value != "on" {
		t.Errorf("a timed window should show the value as on, got %q", window.Value)
	}
	if strings.Contains(window.Why, i18n.T("on (forever) — the classifier is short-circuited and every request logs [naked]", nil)) {
		t.Error("a timed window was described as 'permanent' - that makes people think it cannot be turned off")
	}
	if !strings.Contains(window.Why, "turns itself off in") {
		t.Errorf("a timed window should say how long until it turns off, got %q", window.Why)
	}
}

// TestAnExpiredWindowReadsAsOff: the window expires **lazily** (no timer turns
// it off, see the comment on ParseNakedConfig). A window that has run out
// should read as "off" at **any** read point, this card included -- showing
// "auto-off in -6m3s" is exactly the bug hit in the real run of 2026-09-17.
func TestAnExpiredWindowReadsAsOff(t *testing.T) {
	testkit.Sandbox(t)
	writeState(t, map[string]any{NakedConfigKey: NakedConfig{
		Mode: "on", ExpiresAt: time.Now().Add(-time.Minute),
	}})
	it := nakedItemOf(t)
	if it.Value != "" {
		t.Errorf("an expired window should read as empty (= off), got %q", it.Value)
	}
	if strings.Contains(it.Why, "-") && strings.Contains(it.Why, "turns itself off") {
		t.Errorf("an expired window must not still report a countdown: %q", it.Why)
	}
}

// TestTheLayerRowAppearsOnlyWhenItMatters is this card's most valuable test.
//
// "The layer is off while `forever` is still in state.json" is the situation that most
// needs to be seen: the door is closed now, but turning special_treatment back on opens
// it again. The first two rows of that table say "what the config says", and reading
// only them would suggest the classifier is short-circuited right now -- so there must
// be a third row saying "those configs do not count right now". Conversely, when the
// layer is in effect that row should not appear (one "all normal" line is just noise).
//
// This is also the ratchet for "two rows each saying their own thing":
// before the change only the naked row explained the layer state, while the
// reroute row still said "highest priority" -- two versions in one table.
func TestTheLayerNoteAppearsOnlyWhenItMatters(t *testing.T) {
	testkit.Sandbox(t)
	forever := map[string]any{NakedConfigKey: NakedConfig{Mode: "forever"}}

	// Layer on: no such reminder.
	writeState(t, forever)
	if note := layerNote(store.LoadState()); note != "" {
		t.Errorf("while the layer is in effect there should be no such reminder, got %q", note)
	}

	// Whole layer off: write out "nothing configured here
	// counts; turn the layer back on and it all counts".
	writeState(t, map[string]any{
		NakedConfigKey: NakedConfig{Mode: "forever"},
		"gateway":      map[string]any{"special_treatment": false},
	})
	note := layerNote(store.LoadState())
	want := i18n.T("the whole special_treatment layer is off — none of this counts until it is switched back on", nil)
	if note != want {
		t.Errorf("with the whole layer off it should say 'none count, and they all count once turned back on'\n  got %q\n  want %q", note, want)
	}
	// This sentence must really appear **in the cell the
	// user sees**, not live only in a function nobody calls.
	if got := nakedItemOf(t).Why; !strings.Contains(got, want) {
		t.Errorf("that reminder did not land in the why of the naked cell: %q", got)
	}
	// The config itself is unaffected: that cell still shows
	// forever (the value is what is written in config).
	if got := nakedItemOf(t).Value; got != "forever" {
		t.Errorf("turning the layer off must not change the 'what the config says' cell: %q", got)
	}

	// Turn off one plugin alone (layer on): **only that cell** does not count, so the
	// sentence must not be the whole-layer one -- that would negate the other cell (running
	// fine right now) too, and the reader would go turn off a patch that has no problem.
	writeState(t, map[string]any{
		NakedConfigKey: NakedConfig{Mode: "forever"},
		"gateway":      map[string]any{"special_treatment_off": []string{nakedPluginName}},
	})
	note = layerNote(store.LoadState())
	if !strings.Contains(note, nakedPluginName) {
		t.Errorf("disabling a single plugin should name that one, got %q", note)
	}
	if strings.Contains(note, i18n.T("the whole special_treatment layer is off — none of this counts until it is switched back on", nil)) {
		t.Errorf("disabling only one plugin must not be described as 'the whole layer does not count': %q", note)
	}
}

func TestTheOverrideItemReportsBadConfigInsteadOfHidingIt(t *testing.T) {
	testkit.Sandbox(t)

	// Never configured: that cell is empty, and the why says how to write it.
	if got := overrideItemOf(t); got.Value != "" || !strings.Contains(got.Why, "provider/model") {
		t.Errorf("with no reroute configured it should be empty plus a hint on how to write it, got %q / %q", got.Value, got.Why)
	}

	// Broken must be **spoken**: it silently disables
	// the whole reroute (see Status in st-background.go).
	writeState(t, map[string]any{"classifier_override": map[string]any{"provider": "only-provider"}})
	if got := overrideItemOf(t).Why; !strings.Contains(got, "not valid") {
		t.Errorf("a broken config should be stated in the why, got %q", got)
	}
}

// TestTheNakedSwitchActuallyWrites is the **write
// semantics** ratchet after this card became writable.
//
// It is the same implementation as the CLI (saveNakedConfig / ParseNakedConfig),
// so this is also insurance that "two UIs will not give different answers about
// the same key". Each of the three states maps to a CLI action: "" = naked off,
// on = a self-limiting 60-second window, forever = permanent. **Forever is not
// downgraded to a window** -- that was exactly why this card used to be read-only.
func TestTheNakedSwitchActuallyWrites(t *testing.T) {
	testkit.Sandbox(t)

	write := func(v string) {
		t.Helper()
		c := classifierConcept()
		if c.Apply == nil {
			t.Fatal("this card should be writable (naked mode needs a switch)")
		}
		body, _ := json.Marshal(map[string]string{"naked": v})
		if _, err := c.Apply(body, ""); err != nil {
			t.Fatalf("writing %q failed: %v", v, err)
		}
	}

	write("forever")
	cfg, active := ParseNakedConfig(store.LoadState().ModuleConfig[NakedConfigKey])
	if !active || cfg.Mode != "forever" {
		t.Fatalf("forever was not written: %+v active=%v", cfg, active)
	}

	write("on")
	cfg, active = ParseNakedConfig(store.LoadState().ModuleConfig[NakedConfigKey])
	if !active || cfg.Mode != "on" || time.Until(cfg.ExpiresAt) <= 0 {
		t.Fatalf("on should be written as a window not yet expired: %+v active=%v", cfg, active)
	}

	write("")
	if _, on := ParseNakedConfig(store.LoadState().ModuleConfig[NakedConfigKey]); on {
		t.Error("an empty value should turn naked mode off")
	}
	if _, ok := store.LoadState().ModuleConfig[NakedConfigKey]; ok {
		t.Error("when turning it off the key should be deleted, not left as an empty object")
	}

	// Unrecognized values must be stopped, not silently treated as off.
	if _, err := classifierConcept().Apply([]byte(`{"naked":"maybe"}`), ""); err == nil {
		t.Error("an unrecognized naked mode should error")
	}
}
