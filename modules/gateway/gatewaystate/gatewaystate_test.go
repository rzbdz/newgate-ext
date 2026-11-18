package gatewaystate

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

func st(m map[string][]byte) *domain.State {
	return &domain.State{ModuleConfig: m}
}

func raw(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestParseFallsBackToFactory with no such section, or broken JSON, both count as factory
// state. The direction is fail-open: if it cannot be read, **do not turn people's things off**
// (off makes DeepSeek start answering 400).
func TestParseFallsBackToFactory(t *testing.T) {
	for _, c := range []struct {
		name string
		st   *domain.State
	}{
		{"nil state", nil},
		{"empty ModuleConfig", st(nil)},
		{"the JSON of this section is broken", st(map[string][]byte{Key: []byte("{not json")})},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !RepairEnabled(c.st) {
				t.Fatal("schema repair should default to on")
			}
			if !SpecialEnabled(c.st) {
				t.Fatal("the special layer should default to on")
			}
			if PluginOff(c.st, "deepseek") {
				t.Fatal("nothing configured means no plugin disabled")
			}
		})
	}
}

// TestFallbackDefaultsOn never stated = **on** (swapping as before).
//
// This guards the fail-open half: no such section, broken JSON, or a key simply absent from an
// old file must all land on "swap as before". Landing on "off" would make a machine **silently
// stop swapping** after an upgrade, with the symptom showing only on the day a provider really
// dies -- requests keep returning 200 the rest of the time and nobody goes looking (exactly why
// that line in status exists; see modules/gateway/status.go).
func TestFallbackDefaultsOn(t *testing.T) {
	for _, c := range []struct {
		name string
		st   *domain.State
	}{
		{"nil state", nil},
		{"empty ModuleConfig", st(nil)},
		{"the JSON of this section is broken", st(map[string][]byte{Key: []byte("{not json")})},
		{"this section exists, but fallback was never written", st(map[string][]byte{Key: raw(t, Config{SchemaRepair: boolp(true)})})},
	} {
		t.Run(c.name, func(t *testing.T) {
			if FallbackOff(c.st) {
				t.Fatal("with no stated preference it should be 'switch as before' - especially when it cannot be read, fallback must not be turned off")
			}
		})
	}
}

// TestFallbackRoundTrip walks the write side through a real store: after `newgate fallback off`,
// **another process** (the daemon) must read it as off too.
//
// Why it deserves its own case: this switch's write end and read end live in two processes,
// with only this JSON text between them, so nobody notices the moment they diverge. So this
// asserts two things -- the parsed boolean, and **the shape of that JSON**: with a wrong field
// name or nesting the former can be accidentally right via "default on", the latter cannot
// (`FallbackOff` only reads `Gateway.fallback`; written elsewhere it still reports "off", while
// the real read path and the display layer see the same Parse and nobody can tell).
func TestFallbackRoundTrip(t *testing.T) {
	testkit.Sandbox(t)
	if _, err := store.Init(false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if FallbackOff(store.LoadState()) {
		t.Fatal("the factory state should be 'switch as before'")
	}

	if err := SetFallback(false); err != nil {
		t.Fatalf("SetFallback(false): %v", err)
	}
	after := store.LoadState()
	if !FallbackOff(after) {
		t.Fatal("after disabling it still reads as on - the CLI and the daemon would diverge")
	}
	var got struct {
		Fallback *bool `json:"fallback"`
	}
	if err := json.Unmarshal(after.ModuleConfig[Key], &got); err != nil {
		t.Fatalf("cannot decode the gateway section: %v (raw %s)", err, after.ModuleConfig[Key])
	}
	if got.Fallback == nil || *got.Fallback {
		t.Fatalf("wrong on-disk shape: gateway section = %s, want fallback:false", after.ModuleConfig[Key])
	}
	// One more guard: writing this switch must not touch the other pointer fields in the same
	// section. Config's three *bool look alike, and `c.Fallback = &on` mistyped to another is an
	// easy mistake that shows **only on the next read** (symptom: "I turned off fallback, why
	// did schema repair disappear").
	if !RepairEnabled(after) || !SpecialEnabled(after) {
		t.Fatal("writing fallback flipped other switches in the same section")
	}

	if err := SetFallback(true); err != nil {
		t.Fatalf("SetFallback(true): %v", err)
	}
	if FallbackOff(store.LoadState()) {
		t.Fatal("after setting it back to on it is still off")
	}
}

// TestParseReadsLegacyKeys old files (top-level typed fields) must still be readable.
//
// This guards a **silent** behavior reversal: store preserves unknown top-level keys
// losslessly, so old keys neither disappear on their own nor are seen by the new read path.
// Without reading them, a machine that explicitly ran `special_treatment: false` goes back to
// all-on after upgrading -- what the user turned off quietly returns.
//
// ⚠️ This test passing has a **precondition**: those keys must really no longer be typed
// fields on `domain.State`. store.LoadState reflects json tags into its "known fields" table,
// and **known keys get parsed structurally and never reach ModuleConfig**, so legacyConfig
// would never see them. Measured and hit on 2026-09-18: the field-deleting regex missed debug,
// so debug's legacy migration **silently stopped working** (old value unreadable, new value not
// written into the old field, state.json's top level kept a live-looking debug:true that the
// gateway no longer read). So each legacy key added below must be confirmed absent from
// domain.State.
func TestParseReadsLegacyKeys(t *testing.T) {
	off := false
	old := st(map[string][]byte{
		legacy.SchemaRepair:     raw(t, false),
		legacy.SpecialTreatment: raw(t, off),
		legacy.SpecialOff:       raw(t, []string{"deepseek", "glm"}),
		legacy.Debug:            raw(t, true),
		legacy.DebugUntil:       raw(t, "2099-01-01T00:00:00Z"),
	})
	if RepairEnabled(old) {
		t.Fatal("the old key schema_repair=false was not read")
	}
	if SpecialEnabled(old) {
		t.Fatal("the old key special_treatment=false was not read")
	}
	if !PluginOff(old, "deepseek") || !PluginOff(old, "glm") {
		t.Fatal("the old key special_treatment_off was not read")
	}
	if PluginOff(old, "thinking") {
		t.Fatal("a plugin that was never disabled was judged disabled")
	}
	if !DebugActive(old) {
		t.Fatal("the old key debug=true plus an unexpired debug_until was not read")
	}
	if got := DebugUntilDisplay(old); got != "2099-01-01T00:00:00Z" {
		t.Fatalf("the old key debug_until was not read: %q", got)
	}
}

// TestLegacyKeysAreNotTypedFields is the **guard** for the one above: legacy keys must not be
// typed fields on domain.State.
//
// Why it deserves its own case: with the field still there, the failure mode is extremely
// subtle -- structural parsing eats the key, ModuleConfig does not have it, legacyConfig cannot
// read it, and everything "looks normal" while the user's setting is silently lost. Asking the
// struct via reflection is more reliable than trusting memory.
func TestLegacyKeysAreNotTypedFields(t *testing.T) {
	typ := reflect.TypeOf(domain.State{})
	for _, key := range []string{
		legacy.SchemaRepair, legacy.SpecialTreatment, legacy.SpecialOff,
		legacy.Debug, legacy.DebugUntil,
	} {
		for i := 0; i < typ.NumField(); i++ {
			tag := strings.SplitN(typ.Field(i).Tag.Get("json"), ",", 2)[0]
			if tag == key {
				t.Fatalf("domain.State still has the typed field %q: store parses it structurally, "+
					"so the old-key migration silently fails (unreadable, and not left in ModuleConfig)."+
					"the field must be deleted first for this package to read it.", key)
			}
		}
	}
}

// TestLegacyFillsOnlyUnsetFields per-field fallback: a field **set** in the new section wins;
// only unset ones read the legacy key.
//
// Both directions must be guarded:
//   - not set → read the legacy key. Otherwise the step in a two-step move (new section built
//     first, legacy keys moved later) silently drops the setting.
//   - set → ignore the legacy key. Otherwise, after the user puts a switch back to factory
//     state, the old value flips it back.
func TestLegacyFillsOnlyUnsetFields(t *testing.T) {
	t.Run("a field set in the new section wins", func(t *testing.T) {
		on := true
		s := st(map[string][]byte{
			Key:                     raw(t, Config{SpecialTreatment: &on}),
			legacy.SpecialTreatment: raw(t, false),
		})
		if !SpecialEnabled(s) {
			t.Fatal("the new section explicitly wrote true, but the old key's false flipped it back")
		}
	})
	t.Run("a field not set in the new section falls back to the old key", func(t *testing.T) {
		// The new section has only schema_repair written (exactly the real shape of a two-step move).
		s := st(map[string][]byte{
			Key:                     raw(t, Config{SchemaRepair: boolp(true)}),
			legacy.Debug:            raw(t, true),
			legacy.DebugUntil:       raw(t, "2099-01-01T00:00:00Z"),
			legacy.SpecialTreatment: raw(t, false),
		})
		if !DebugActive(s) {
			t.Fatal("the new section never set debug, so it should fall back to the old key - otherwise the old setting is silently lost")
		}
		if SpecialEnabled(s) {
			t.Fatal("the new section never set special_treatment, so it should fall back to the old key")
		}
	})
}

func boolp(v bool) *bool { return &v }

// TestUpdateWritesNewAndDropsLegacy write side: the value lands in the new section, legacy
// keys go at once.
func TestUpdateWritesNewAndDropsLegacy(t *testing.T) {
	testkit.Sandbox(t)
	if _, err := store.Init(false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// First build a state carrying legacy keys.
	s := store.LoadState()
	s.ModuleConfig[legacy.SpecialTreatment] = raw(t, false)
	s.ModuleConfig[legacy.SchemaRepair] = raw(t, true)
	if err := store.SaveState(s); err != nil {
		t.Fatal(err)
	}
	// During the migration the old value is readable.
	if SpecialEnabled(store.LoadState()) {
		t.Fatal("before the write the old key's false should be read")
	}

	if err := SetSpecialPlugin("deepseek", false); err != nil {
		t.Fatal(err)
	}

	after := store.LoadState()
	if _, still := after.ModuleConfig[legacy.SpecialTreatment]; still {
		t.Fatal("the old key was not removed - it would sit in state.json forever and make people think it still applies")
	}
	if _, still := after.ModuleConfig[legacy.SchemaRepair]; still {
		t.Fatal("the old key schema_repair was not removed")
	}
	if PluginOff(after, "deepseek") != true {
		t.Fatal("the new section was not written")
	}
	// The old value came along into the new section: the write read false first, so it must survive.
	if SpecialEnabled(after) {
		t.Fatal("the old key's false was dropped during migration - something the user turned off came back")
	}
}
