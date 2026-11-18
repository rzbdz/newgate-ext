package confighook

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/store"
)

// This family tests the **on-disk shape** of the slot -> tier override table.
//
// Why test it separately: it is the "looks right once written" kind of bug. Leave a
// record equal to the default and all looks normal; only whoever changes the default
// later finds it skipped for some users. Irreproducible on site; pinned here.
//
// The sandbox is built by hand (not testing/testkit): **that package imports this one**
// (it assembles the real component graph), so using it here would be a cycle. So this
// does only the one Sandbox job relevant here - point NEWGATE_HOME at a temp dir.
func sandbox(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEWGATE_HOME", home)
}

// testSlots is a two-slot table; the second declares its own exceptions (like `inherit`).
func testSlots() []Slot {
	return []Slot{
		{Name: "main", Tier: "normal"},
		{Name: "sub", Tier: "mid", Also: []string{"inherit"}},
	}
}

func testOverrides() SlotOverrides {
	return SlotOverrides{Key: "test_slots", AgentID: "test", Slots: testSlots()}
}

func stored(t *testing.T, key string) (string, bool) {
	t.Helper()
	raw, ok := store.LoadState().ModuleConfig[key]
	return string(raw), ok
}

// TestWritingAllDefaultsRemovesTheKey: all equal to the default = the key **disappears**.
//
// Learned from the field: "input is an empty table" is not enough - the UI submits **all**
// slots at once, so setting the last row back to the default sends "every row equals the
// default". Without the second half, state.json keeps a `{}`: reading it back is the same
// as never configured, so nothing breaks - just noise no one understands or dares delete.
//
// codex had this shape all along (one slot), so it went red first in browser acceptance.
func TestWritingAllDefaultsRemovesTheKey(t *testing.T) {
	sandbox(t)
	o := testOverrides()

	// Write one first to show the key would appear, else the assert below proves nothing.
	if err := o.Write(map[string]string{"main": "heavy"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := stored(t, o.Key); !ok {
		t.Fatal("a modified slot should be written")
	}

	// Then set them all back to the default: the key should vanish, not leave an empty object.
	if err := o.Write(map[string]string{"main": "normal", "sub": "mid"}); err != nil {
		t.Fatal(err)
	}
	if raw, ok := stored(t, o.Key); ok {
		t.Errorf("once all equal the default that key should disappear, but %q is left", raw)
	}
	// Same for an empty table (this is the original check).
	if err := o.Write(map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if raw, ok := stored(t, o.Key); ok {
		t.Errorf("an empty table should delete the key, but %q is left", raw)
	}
}

// TestOnlyTheChangedEntriesAreWritten: only changed rows are written, values as-is.
func TestOnlyTheChangedEntriesAreWritten(t *testing.T) {
	sandbox(t)
	o := testOverrides()
	if err := o.Write(map[string]string{"main": "normal", "sub": "heavy"}); err != nil {
		t.Fatal(err)
	}
	raw, ok := stored(t, o.Key)
	if !ok {
		t.Fatal("a modified slot should be written")
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got["sub"] != "heavy" {
		t.Errorf("the modified line should be there, got %v", got)
	}
	if _, there := got["main"]; there {
		t.Errorf("a line equal to the default must not be written, got %v", got)
	}
}

// TestABadValueIsRefusedAndLeavesNothingBehind: a mistyped tier name cannot be written.
//
// It is injected verbatim as a model name (claude) or written to config.toml (codex), and
// upstream that is "no such model" - a sudden 400 on some requests, layers from the config.
func TestABadValueIsRefusedAndLeavesNothingBehind(t *testing.T) {
	sandbox(t)
	o := testOverrides()
	if err := o.Write(map[string]string{"main": "hevy"}); err == nil {
		t.Fatal("a nonexistent tier should be rejected")
	}
	if raw, ok := stored(t, o.Key); ok {
		t.Errorf("a rejected write must leave no trace, got %q", raw)
	}
	// Exceptions the client declares are accepted (`inherit` is Claude Code's; store only).
	if err := o.Write(map[string]string{"sub": "inherit"}); err != nil {
		t.Errorf("an exception declared by the slot should be accepted: %v", err)
	}
	// But it belongs only to the slot that declared it.
	if err := o.Write(map[string]string{"main": "inherit"}); err == nil {
		t.Error("main does not declare inherit, so it should be rejected")
	}
}

// TestABadValueOnDiskDoesNotBreakAnything: a hand-broken file must **read** (just not apply).
//
// state.json is hand-edited, and a broken entry here must not mean "the client won't
// start": filter bad values on read, fall back to the default, and **report** the bad
// one (the UI explains it) rather than quietly treating it as unconfigured.
func TestABadValueOnDiskDoesNotBreakAnything(t *testing.T) {
	sandbox(t)
	o := testOverrides()

	// Write a bad value straight to disk (bypassing Write's validation, i.e. hand-edited).
	st := store.LoadState()
	if st.ModuleConfig == nil {
		st.ModuleConfig = map[string][]byte{}
	}
	st.ModuleConfig[o.Key] = []byte(`{"main":"hevy","sub":"light"}`)
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}

	ok, bad := o.Read()
	if _, there := ok["main"]; there {
		t.Error("a bad value must not appear in the usable copy")
	}
	if bad["main"] != "hevy" {
		t.Errorf("a bad value must be reported separately, got %v", bad)
	}
	if ok["sub"] != "light" {
		t.Errorf("the good line in the same file should still take effect, got %v", ok)
	}
	// "Unchanged uses the default" has one implementation (TierOf); this checks the fallback.
	if got := TierOf(nil, Slot{Name: "main", Tier: "normal"}); got != "normal" {
		t.Errorf("a bad value should fall back to the default in the descriptor, got %q", got)
	}
}

// TestTheKeyIsPerClient: two clients write their own keys, without interfering.
//
// This sounds obvious, but it is the premise of "codex also needs such a table": if
// they shared one key, changing codex's tiers would change claude's too.
func TestTheKeyIsPerClient(t *testing.T) {
	sandbox(t)
	a := SlotOverrides{Key: "a_slots", AgentID: "a", Slots: testSlots()}
	b := SlotOverrides{Key: "b_slots", AgentID: "b", Slots: testSlots()}
	if err := a.Write(map[string]string{"main": "heavy"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Write(map[string]string{"main": "light"}); err != nil {
		t.Fatal(err)
	}
	okA, _ := a.Read()
	okB, _ := b.Read()
	if okA["main"] != "heavy" || okB["main"] != "light" {
		t.Errorf("the two tables got crossed: a=%v b=%v", okA, okB)
	}
	// And they really land on two keys.
	if _, ok := store.LoadState().ModuleConfig["a_slots"]; !ok {
		t.Error("the key of a is missing")
	}
	if _, ok := store.LoadState().ModuleConfig["b_slots"]; !ok {
		t.Error("the key of b is missing")
	}
}
