package opencodeomo

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/config/roleprov"
)

func init() {
	roleprov.Register(omoRolesProvider{})
}

// isolate points both the config directory and the target file directory at a temp directory:
// takeover writes files, and not one may land on the user's real config.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEWGATE_HOME", dir)
	t.Setenv("NEWGATE_TARGET_DIR", target)
	t.Setenv("HOME", filepath.Join(dir, "home"))
	return target
}

const sampleOmo = `{
  "$schema": "x",
  "agents": {
    "sisyphus": {"model": "smt/gpt-5.6-sol", "variant": "max",
                 "fallback_models": ["smt/gpt-5.6-terra", "smt/claude-sonnet-5"]},
    "librarian": {"model": "smt/gpt-5.6-luna", "fallback_models": []}
  },
  "categories": {
    "deep": {"model": "smt/gpt-5.6-terra", "variant": "high"},
    "quick": {"model": "smt/gemini-3.1-flash-lite-preview"}
  }
}`

func writeTarget(t *testing.T, target, name, content string) string {
	t.Helper()
	p := filepath.Join(target, name)
	if err := ioutil.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func readJSONFile(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	b, err := ioutil.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func node(t *testing.T, root map[string]interface{}, section, name string) map[string]interface{} {
	t.Helper()
	s, _ := root[section].(map[string]interface{})
	n, _ := s[name].(map[string]interface{})
	if n == nil {
		t.Fatalf("%s.%s is not in the file", section, name)
	}
	return n
}

// TestTakeoverKeepsSlotIdentity: takeover must **keep slot identity**: previously every slot was
// translated into a build-derived tier name (sisyphus and librarian looked the same from then on);
// now each must keep its own name.
func TestTakeoverKeepsSlotIdentity(t *testing.T) {
	target := isolate(t)
	omo := writeTarget(t, target, "oh-my-openagent.json", sampleOmo)
	oc := writeTarget(t, target, "opencode.json", `{"model":"anthropic/claude-opus-5"}`)

	if _, err := ApplyAll(8899); err != nil {
		t.Fatal(err)
	}

	root := readJSONFile(t, omo)
	if got := node(t, root, "agents", "sisyphus")["model"]; got != "newgate/omo-sisyphus" {
		t.Errorf("the slot identity of sisyphus was lost: %v", got)
	}
	if got := node(t, root, "agents", "librarian")["model"]; got != "newgate/omo-librarian" {
		t.Errorf("the slot identity of librarian was lost: %v", got)
	}
	if got := node(t, root, "categories", "deep")["model"]; got != "newgate/cat-deep" {
		t.Errorf("the categories key prefix is wrong: %v", got)
	}
	// fallback_models converges to the same key: the chain is covered by newgate on the server side
	// (the original list is recorded in the registry)
	fb, _ := node(t, root, "agents", "sisyphus")["fallback_models"].([]interface{})
	if len(fb) != 1 || fb[0] != "newgate/omo-sisyphus" {
		t.Errorf("fallback_models should collapse into one entry per key, got %v", fb)
	}

	// These ids must be registered in opencode.json's provider, otherwise opencode does not recognize
	// them
	provs, _ := readJSONFile(t, oc)["provider"].(map[string]interface{})
	models, _ := provs["newgate"].(map[string]interface{})["models"].(map[string]interface{})
	for _, id := range []string{"omo-sisyphus", "omo-librarian", "cat-deep", "cat-quick"} {
		if _, ok := models[id]; !ok {
			t.Errorf("the newgate provider of opencode is missing model id %s", id)
		}
	}
}

// TestRegistryRecordsCurrentAndSuggested: the registry must record both "current" and "suggested":
// current guarantees takeover does not change behavior, suggested is an option for the user (the
// variant strength counts).
func TestRegistryRecordsCurrentAndSuggested(t *testing.T) {
	target := isolate(t)
	writeTarget(t, target, "oh-my-openagent.json", sampleOmo)
	writeTarget(t, target, "opencode.json", `{}`)

	if _, err := ApplyAll(8899); err != nil {
		t.Fatal(err)
	}
	reg := ReadOmoSlots()
	if reg == nil {
		t.Fatal("the registry was not written")
	}

	cases := []struct{ key, was, variant, current, suggested string }{
		// opus build + variant max: current is normal (the old behavior), suggested is heavy
		{"omo-sisyphus", "smt/gpt-5.6-sol", "max", "normal", "heavy"},
		// luna is light work: current is light, and without a variant no change is suggested
		{"omo-librarian", "smt/gpt-5.6-luna", "", "light", "light"},
		{"cat-deep", "smt/gpt-5.6-terra", "high", "mid", "mid"},
		{"cat-quick", "smt/gemini-3.1-flash-lite-preview", "", "light", "light"},
	}
	for _, c := range cases {
		sl, ok := reg.SlotOf(c.key)
		if !ok {
			t.Fatalf("there is no %s in the registry", c.key)
		}
		if sl.Was != c.was || sl.Variant != c.variant {
			t.Errorf("%s recorded the wrong original form: was=%q variant=%q", c.key, sl.Was, sl.Variant)
		}
		if sl.Current != c.current {
			t.Errorf("%s current tier = %q, want %q", c.key, sl.Current, c.current)
		}
		if sl.Suggested != c.suggested {
			t.Errorf("%s suggested tier = %q, want %q", c.key, sl.Suggested, c.suggested)
		}
		if sl.Default != c.current {
			t.Errorf("%s default must equal the current setting (no behavior change), got %q", c.key, sl.Default)
		}
	}
	// The original fallback list must be kept on record (it helps when the user wants to restore an
	// explicit chain)
	if sl, _ := reg.SlotOf("omo-sisyphus"); len(sl.WasFallbacks) != 2 {
		t.Errorf("the original fallback list was not recorded: %v", sl.WasFallbacks)
	}
}

// TestRetakeoverIsIdempotent: repeated takeover must not stack up, nor forget "what model it used
// to be": on the second takeover the disk already holds our newgate/omo-sisyphus, and the
// original model name can only be recovered from the backup.
func TestRetakeoverIsIdempotent(t *testing.T) {
	target := isolate(t)
	omo := writeTarget(t, target, "oh-my-openagent.json", sampleOmo)
	writeTarget(t, target, "opencode.json", `{}`)

	for i := 0; i < 3; i++ {
		if _, err := ApplyAll(8899); err != nil {
			t.Fatalf("takeover #%d: %v", i+1, err)
		}
	}
	root := readJSONFile(t, omo)
	if got := node(t, root, "agents", "sisyphus")["model"]; got != "newgate/omo-sisyphus" {
		t.Errorf("a repeated takeover corrupted the model name: %v", got)
	}
	sl, _ := ReadOmoSlots().SlotOf("omo-sisyphus")
	if sl.Was != "smt/gpt-5.6-sol" || sl.Variant != "max" {
		t.Errorf("re-takeover lost the original form: was=%q variant=%q", sl.Was, sl.Variant)
	}
	if sl.Current != "normal" || sl.Suggested != "heavy" {
		t.Errorf("tier drifted after re-takeover: current=%q suggested=%q", sl.Current, sl.Suggested)
	}
}

// TestOldTakeoverKeepsBehaviour: when a file taken over by an old version (newgate/heavy) is taken
// over again, the current must be kept as is -- an upgrade must not quietly change the user's
// current model choice.
//
// The original model name can only be recovered from backups/original/ at this point (the copy on
// disk was rewritten long ago), which also verifies that recovery path.
func TestOldTakeoverKeepsBehaviour(t *testing.T) {
	target := isolate(t)
	old := `{"agents":{"sisyphus":{"model":"newgate/heavy","variant":"max"}}}`
	omo := writeTarget(t, target, "oh-my-openagent.json", old)
	writeTarget(t, target, "opencode.json", `{}`)

	// The original backup left by the old version's takeover (the real name is in it)
	orig := filepath.Join(os.Getenv("NEWGATE_HOME"), "backups", "original")
	if err := os.MkdirAll(orig, 0o700); err != nil {
		t.Fatal(err)
	}
	pristine := `{"agents":{"sisyphus":{"model":"smt/gpt-5.6-sol","variant":"max"}}}`
	if err := ioutil.WriteFile(filepath.Join(orig, "oh-my-openagent.json"), []byte(pristine), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ApplyAll(8899); err != nil {
		t.Fatal(err)
	}
	sl, _ := ReadOmoSlots().SlotOf("omo-sisyphus")
	if sl.Current != "heavy" || sl.Default != "heavy" {
		t.Errorf("the tier written by the old takeover should carry over unchanged, got current=%q default=%q", sl.Current, sl.Default)
	}
	if sl.Was != "smt/gpt-5.6-sol" {
		t.Errorf("the original model name should be recovered from the backup, got %q", sl.Was)
	}
	// But now the omo-sisyphus key carries heavy, and the user can change it later on its own
	if got := node(t, readJSONFile(t, omo), "agents", "sisyphus")["model"]; got != "newgate/omo-sisyphus" {
		t.Errorf("the old file should be upgraded to slot keys too: %v", got)
	}
}

// TestModeSuggestedSwitches: the mode switch: suggested takes the suggestion, current is the
// default.
func TestModeSuggestedSwitches(t *testing.T) {
	target := isolate(t)
	writeTarget(t, target, "oh-my-openagent.json", sampleOmo)
	writeTarget(t, target, "opencode.json", `{}`)
	if _, err := ApplyAll(8899); err != nil {
		t.Fatal(err)
	}

	reg := ReadOmoSlots()
	if got := reg.SlotBinding("omo-sisyphus"); got != "normal" {
		t.Fatalf("the default (current) should be normal, got %q", got)
	}
	// Overrides win over the mode
	reg.Overrides = map[string]string{"omo-sisyphus": "@mid"}
	if got := reg.SlotBinding("omo-sisyphus"); got != "@mid" {
		t.Fatalf("the override should win, got %q", got)
	}
	delete(reg.Overrides, "omo-sisyphus")
	reg.Mode = "suggested"
	if got := reg.SlotBinding("omo-sisyphus"); got != "heavy" {
		t.Fatalf("suggested mode should give heavy, got %q", got)
	}
}

// TestRolesReachTheResolver: the role keys a module registers must really resolve into chains --
// this is the end-to-end acceptance of "the omo plugin registers its own sub agent key mechanism
// with the framework": the framework side (core/domain + resolve) knows nothing about omo
// throughout, it only looks at the registry.
func TestRolesReachTheResolver(t *testing.T) {
	target := isolate(t)
	writeTarget(t, target, "oh-my-openagent.json", sampleOmo)
	writeTarget(t, target, "opencode.json", `{}`)
	if _, err := ApplyAll(8899); err != nil {
		t.Fatal(err)
	}

	if errs := roleprov.Refresh(); len(errs) > 0 {
		t.Fatalf("Refresh: %v", errs)
	}
	defer domain.SetExtraRoles(nil)

	if !domain.IsKnownRole("omo-sisyphus") || !domain.IsKnownRole("cat-deep") {
		t.Fatal("the slot key was not registered as a role key - a model sent by the client would be looked up as a concrete model name")
	}
	if !domain.IsKnownRole("heavy") || !domain.IsKnownRole("normal") {
		t.Fatal("built-in tiers should be recognized too (same role table)")
	}

	// A profile that writes only normal: omo-sisyphus's default is normal, and the whole chain should
	// follow
	prio := 10
	profiles := []*domain.Profile{{
		Name: "head", Priority: &prio,
		Roles: map[string]domain.Candidates{"normal": {{Provider: "p1", Model: "opus"}}},
	}}
	provs := &domain.Providers{Providers: map[string]domain.Provider{
		"p1": {APIKey: "k"},
	}}
	steps, _ := resolve.BuildChain("omo-sisyphus", profiles, provs,
		resolve.Opts{Active: "head"})
	if len(steps) != 1 || steps[0].Binding.Model != "opus" {
		t.Fatalf("omo-sisyphus should follow the candidates of normal, got %+v", steps)
	}
}

// TestRegistryIsReadableByOtherUsers: the registry must be **group-readable**.
//
// Field report (2026-09-16): root's CLI takeover succeeded and omo-slots.json was written, yet the
// claude user running the daemon registered not one role key -- the file landed as 0600 and another
// user could not read it. The symptom was "the file is clearly there and the daemon says it does
// not know this model".
func TestRegistryIsReadableByOtherUsers(t *testing.T) {
	target := isolate(t)
	writeTarget(t, target, "oh-my-openagent.json", sampleOmo)
	writeTarget(t, target, "opencode.json", `{}`)
	if _, err := ApplyAll(8899); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(os.Getenv("NEWGATE_HOME"), "omo-slots.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o060 == 0 {
		t.Fatalf("registry permissions %o: the group cannot read it, so another user running the daemon sees no slot keys at all", perm)
	}
}

// TestSuggest: the two rules of the suggested tier: the model name gives the build, and variant
// moves one step on the ladder.
func TestSuggest(t *testing.T) {
	cases := []struct {
		model, variant string
		want           string
	}{
		{"smt/claude-opus-5", "max", "heavy"},
		{"smt/claude-opus-5", "high", "normal"},
		{"smt/claude-sonnet-5", "max", "normal"},
		{"smt/claude-sonnet-5", "", "mid"},
		{"smt/gpt-5.6-luna", "max", "mid"},
		{"smt/gpt-5.6-luna", "low", "light"},
		{"smt/gemini-3.1-pro-preview", "max", "vision"}, // vision is orthogonal, no up/down tier
		{"smt/whatever-unknown", "max", ""},             // no rule matched: no suggestion
	}
	for _, c := range cases {
		got, why := Suggest(c.model, c.variant)
		if got != c.want {
			t.Errorf("Suggest(%q, %q) = %q (%s), want %q", c.model, c.variant, got, why, c.want)
		}
		if c.want == "" && why == "" {
			t.Errorf("even a negative recommendation must say why: %q", c.model)
		}
	}
}

// TestTakeoverCarriesAgentIdentity pins the 2026-10-29 snode1 field case: the baseURL written into
// opencode.json by takeover must carry `/a/opencode`. The gateway recognizes the client only from
// the path (`/a/<agent>[/p/<profile>]/v1/...`, see the kernel's forward.parseTarget), so without it
// the request is treated as anonymous and takes the global default -- the profile picked separately
// for opencode in the UI never takes effect.
func TestTakeoverCarriesAgentIdentity(t *testing.T) {
	target := isolate(t)
	p := writeTarget(t, target, "opencode.json", `{"$schema":"x"}`)
	if _, err := ApplyOpencode(p, 8899, nil); err != nil {
		t.Fatal(err)
	}
	prov := node(t, readJSONFile(t, p), "provider", ProviderID)
	opts, _ := prov["options"].(map[string]interface{})
	got, _ := opts["baseURL"].(string)
	if want := "http://127.0.0.1:8899/a/opencode/v1"; got != want {
		t.Fatalf("baseURL = %q, want %q (without /a/opencode the gateway routes by the global default)", got, want)
	}
}
