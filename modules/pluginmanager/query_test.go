package pluginmanager

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
)

// stateWith builds a state snapshot carrying only the plugin_manager field.
func stateWith(t *testing.T, cfg Config) *domain.State {
	t.Helper()
	raw, err := cfg.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &domain.State{ModuleConfig: map[string][]byte{StateKey: raw}}
}

func TestOffDefaultsToFalse(t *testing.T) {
	// Untouched = not off. This is the foundation of kill switch semantics: a patch
	// that is on by default must not be disabled by "no configuration".
	for _, st := range []*domain.State{nil, {}, stateWith(t, Config{})} {
		if Off(st, "deepseek.tail-shape") {
			t.Fatalf("a switch point never set must not be 'off': %+v", st)
		}
	}
}

func TestOffHonoursExplicitSetting(t *testing.T) {
	st := stateWith(t, Config{Off: map[string]Entry{"deepseek.tail-shape": {}}})
	if !Off(st, "deepseek.tail-shape") {
		t.Fatal("an explicitly disabled switch point should be 'off' (empty Until = no time limit)")
	}
	if Off(st, "deepseek.inject-thinking") {
		t.Fatal("the one that was not disabled must not be affected")
	}
}

func TestOffExpiresLazily(t *testing.T) {
	// Lazy expiry: no background scan, the read compares times. Expired = never set.
	st := stateWith(t, Config{Off: map[string]Entry{
		"deepseek.tail-shape":      {Until: time.Now().Add(-time.Second)},
		"deepseek.inject-thinking": {Until: time.Now().Add(time.Hour)},
	}})
	if Off(st, "deepseek.tail-shape") {
		t.Fatal("an expired setting should be treated as never set")
	}
	if !Off(st, "deepseek.inject-thinking") {
		t.Fatal("an unexpired setting should still be there")
	}
}

func TestOffAndOnAreSeparateTables(t *testing.T) {
	// Two independent tables: Off reads Off, On reads On. A Path is in only one,
	// picked by the CLI from Switch.Default - so neither hot-path function needs the registry.
	st := stateWith(t, Config{On: map[string]Entry{"gateway.passthrough": {}}})
	if Off(st, "gateway.passthrough") {
		t.Fatal("a setting in the On table must not be seen by Off()")
	}
	if !On(st, "gateway.passthrough") {
		t.Fatal("On() should see settings in the On table")
	}
	if On(st, "deepseek.tail-shape") {
		t.Fatal("a mode never set must not be 'on'")
	}
}

func TestParseNeverFails(t *testing.T) {
	// fail-open: bad JSON always reads as "nothing was ever set". Never disable a
	// feature over a broken config - worse than not working, because it is silent.
	for _, raw := range [][]byte{
		nil, {}, []byte("not json"), []byte(`{"off": "not a map"}`),
		[]byte(`{"off": {"a": 123}}`), []byte(`[]`),
	} {
		st := &domain.State{ModuleConfig: map[string][]byte{StateKey: raw}}
		if Off(st, "deepseek.tail-shape") {
			t.Fatalf("a broken config must not be interpreted as 'off': %q", raw)
		}
	}
}

func TestConfigRoundTrips(t *testing.T) {
	want := Config{
		Off: map[string]Entry{"deepseek.tail-shape": {Until: time.Unix(1800000000, 0).UTC()}},
		On:  map[string]Entry{"gateway.passthrough": {}},
	}
	raw, err := want.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := Parse(raw)
	if !got.Off["deepseek.tail-shape"].Until.Equal(want.Off["deepseek.tail-shape"].Until) {
		t.Fatalf("Until did not round trip: %+v", got.Off)
	}
	if _, ok := got.On["gateway.passthrough"]; !ok {
		t.Fatalf("the On table did not round trip: %+v", got.On)
	}
	// The unlimited one must not turn into "present" through omitempty.
	if !got.On["gateway.passthrough"].Until.IsZero() {
		t.Fatal("no time limit should be a zero Until")
	}
}

func TestPruneDropsExpiredOnly(t *testing.T) {
	cfg := Config{
		Off: map[string]Entry{
			"a.x": {Until: time.Now().Add(-time.Minute)},
			"b.y": {Until: time.Now().Add(time.Minute)},
			"c.z": {},
		},
	}
	got := cfg.Prune()
	if _, ok := got.Off["a.x"]; ok {
		t.Fatal("an expired entry should be cleared")
	}
	if _, ok := got.Off["b.y"]; !ok {
		t.Fatal("an unexpired entry must not be cleared")
	}
	if _, ok := got.Off["c.z"]; !ok {
		t.Fatal("an entry with no time limit must not be cleared")
	}
}

func TestRemaining(t *testing.T) {
	until := time.Now().Add(time.Minute).Round(time.Second)
	st := stateWith(t, Config{
		Off: map[string]Entry{"deepseek.tail-shape": {Until: until}},
		On:  map[string]Entry{"gateway.passthrough": {}},
	})
	if got := Remaining(st, "deepseek.tail-shape"); !got.Equal(until) {
		t.Fatalf("Remaining = %v, want %v", got, until)
	}
	// Unlimited returns the zero value (callers decide whether to show "42s left").
	if got := Remaining(st, "gateway.passthrough"); !got.IsZero() {
		t.Fatalf("no time limit should return the zero value, got %v", got)
	}
	if got := Remaining(st, "no such entry"); !got.IsZero() {
		t.Fatalf("never set should return the zero value, got %v", got)
	}
}

// TestQueryIsPure guards this package's top discipline: Off/On run on the forwarding
// hot path on every request, so they **only read the config snapshot passed in** - no
// lock, no IO, no registry lookup, no file.
//
// Why check imports instead of review: once broken, the symptom is "one more lock in
// the forwarding loop" or "CLI changed the config but the daemon reads another copy" -
// both found very late. It is also a **seam**: a shared config layer governing a fleet
// later (shared overlay + machine-local, local first) merges values into the snapshot
// and these two functions do not change; let query.go read files and it is welded shut.
func TestQueryIsPure(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "query.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing query.go: %v", err)
	}
	allowed := map[string]bool{
		strconv.Quote("time"): true,
		strconv.Quote("github.com/rzbdz/newgate-ext/modules/config/domain"): true,
	}
	for _, imp := range f.Imports {
		if !allowed[imp.Path.Value] {
			t.Fatalf("query.go gained an import %s - hot-path queries must be pure functions:"+
				"read only the *domain.State passed in, do not consult registries, do not open files, do not read environment variables."+
				"see the comment on TestQueryIsPure (it is also a seam left for the shared config layer).",
				imp.Path.Value)
		}
	}
}

// TestOffIsDeterministic: a thousand calls on one snapshot must agree, and not modify it.
func TestOffIsDeterministic(t *testing.T) {
	st := stateWith(t, Config{Off: map[string]Entry{"deepseek.tail-shape": {}}})
	before, _ := json.Marshal(st.ModuleConfig)
	for i := 0; i < 1000; i++ {
		if !Off(st, "deepseek.tail-shape") {
			t.Fatalf("the result changed on attempt %d", i)
		}
	}
	after, _ := json.Marshal(st.ModuleConfig)
	if string(before) != string(after) {
		t.Fatal("Off() modified the snapshot passed in")
	}
}
