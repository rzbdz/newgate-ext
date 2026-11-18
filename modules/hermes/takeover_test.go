package hermes

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// The hermes takeover family tests three things: what is written is read back by hermes the way
// we mean it (plan), nothing anyone else wrote is touched (the takeover round trip), and
// background sync touches only the list, not the slots (sync).

func bind(provider, model string) domain.Candidates {
	return domain.Candidates{{Provider: provider, Model: model}}
}

func sampleInputs() inputs {
	return inputs{
		Port: 8899,
		Tiers: map[string]string{
			SlotModel: "normal", SlotDelegation: "mid", SlotVision: "vision",
			SlotCompression: "mid", SlotAuxiliary: "light",
		},
		Active: "ds",
		Profiles: []*domain.Profile{
			{Name: "ds", ContextWindow: 1000000, Roles: map[string]domain.Candidates{
				"heavy": bind("smt-deepseek", "deepseek-flash"),
				"mid":   bind("smt-deepseek", "deepseek-flash"),
			}},
			{Name: "complex", Roles: map[string]domain.Candidates{
				"heavy":  bind("smt-claude", "claude-opus-5-5"),
				"light":  bind("smt-codex", "gpt-5.6-luna"),
				"vision": bind("openrouter", "google/gemini-3-pro"),
			}},
			{Name: "has space", Roles: map[string]domain.Candidates{"mid": bind("p", "m")}},
		},
	}
}

func TestCatalogListsTiersAndEachProfilesModels(t *testing.T) {
	c := buildCatalog(sampleInputs())
	keys := []string{}
	for _, e := range c.Entries {
		keys = append(keys, e.K)
	}
	if strings.Join(keys, ",") != "newgate,newgate-ds,newgate-complex" {
		t.Fatalf("lines = %v", keys)
	}
	models := func(i int) []string {
		var out []string
		for _, m := range c.Entries[i].V.(omap) {
			if m.K == "models" {
				for _, x := range m.V.(omap) {
					out = append(out, x.K)
				}
			}
		}
		return out
	}
	if got := strings.Join(models(0), ","); got != strings.Join(domain.Roles, ",") {
		t.Errorf("the follow line should list tiers only: %s", got)
	}
	got := strings.Join(models(2), ",")
	if !strings.Contains(got, "smt-claude/claude-opus-5-5") || !strings.Contains(got, "smt-codex/gpt-5.6-luna") {
		t.Errorf("the pin line must list concrete models: %s", got)
	}
	// A model name containing `/` cannot be pointed at (NormalizeRole takes the last segment);
	// it must not be listed.
	if strings.Contains(got, "gemini-3-pro") {
		t.Errorf("model names containing / must not be listed: %s", got)
	}
	skipped := strings.Join(c.Skipped, "|")
	if !strings.Contains(skipped, "google/gemini-3-pro") || !strings.Contains(skipped, `"has space"`) {
		t.Errorf("skipped ones must be reported: %v", c.Skipped)
	}
}

// Pin that the line's URL carries /p/<profile>: the gateway recognizes "which profile this
// request is pinned to" only from the URL.
func TestPinnedRowsCarryTheProfileInTheURL(t *testing.T) {
	c := buildCatalog(sampleInputs())
	for _, e := range c.Entries[1:] {
		url := ""
		for _, f := range e.V.(omap) {
			if f.K == "base_url" {
				url = f.V.(string)
			}
		}
		want := "/a/hermes/p/" + strings.TrimPrefix(e.K, "newgate-") + "/v1"
		if !strings.HasSuffix(url, want) {
			t.Errorf("base_url of %s = %s, want ...%s", e.K, url, want)
		}
	}
}

// After a full takeover, every cell hermes reads means what we meant (checked by hermes' parser
// -- on a machine without hermes it falls back to this package's read-back check).
func TestFullEditsPointEverythingAtTheProxy(t *testing.T) {
	d := parseDoc(sampleYAML)
	edits, _ := fullEdits(d, sampleInputs())
	for _, e := range edits {
		if _, err := d.put(e); err != nil {
			t.Fatal(err)
		}
	}
	out := d.String()
	if _, err := verifyEdits(sampleYAML, out, edits, ""); err != nil {
		t.Fatalf("verification failed: %v\n%s", err, out)
	}
	r := parseDoc(out)
	for path, want := range map[string]string{
		"model.provider":              "newgate",
		"model.default":               "normal",
		"model.model":                 "normal",
		"model.base_url":              "http://127.0.0.1:8899/a/hermes/v1",
		"model.api_mode":              "chat_completions",
		"model.context_length":        "1000000",
		"delegation.provider":         "newgate",
		"delegation.model":            "mid",
		"delegation.base_url":         "",
		"auxiliary.vision.model":      "vision",
		"auxiliary.vision.base_url":   "",
		"auxiliary.compression.model": "mid",
		"auxiliary.curator.provider":  "newgate",
		"auxiliary.curator.model":     "light",
	} {
		got, ok := r.get(strings.Split(path, ".")...)
		if want == "" {
			s, found := r.lookup(strings.Split(path, ".")...)
			if !found || valueOf(r.lines[s.head]) != `""` {
				t.Errorf("%s should be cleared to \"\"", path)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	// Other people's stuff: not one byte touched.
	for _, keep := range []string{
		`    pirate: "Arrr! Ye be talkin' to Captain Hermes: yo ho ho!"`,
		"  - name: smt-claude",
		"    model: gemini-3-flash-preview", // smart_model_routing is a dead setting, leave it alone
		"  max_iterations: 50",
	} {
		if !strings.Contains(out, keep) {
			t.Errorf("this line must not be touched: %q", keep)
		}
	}
	// Idempotent: computing again yields no edits at all.
	again, _ := fullEdits(r, sampleInputs())
	for _, e := range again {
		if changed, _ := r.put(e); changed {
			t.Errorf("a second takeover must not change %v again", e.Path)
		}
	}
}

// A referenced stale line is not deleted: delete it and hermes cannot resolve a provider next
// round, stalling the whole gateway.
func TestCatalogKeepsAReferencedStaleRow(t *testing.T) {
	src := "model:\n  provider: newgate-old\n  default: x\nproviders:\n  newgate-old:\n    name: newgate-old\n  newgate-gone:\n    name: newgate-gone\n  mine:\n    name: mine\n"
	d := parseDoc(src)
	edits, c := catalogEdits(d, sampleInputs())
	var removed []string
	for _, e := range edits {
		if e.Value == nil {
			removed = append(removed, e.Path[1])
		}
	}
	if strings.Join(removed, ",") != "newgate-gone" {
		t.Errorf("only the unused line should be deleted, actually deleted %v", removed)
	}
	if !strings.Contains(strings.Join(c.Skipped, "|"), "newgate-old") {
		t.Errorf("the kept line must be reported: %v", c.Skipped)
	}
}

// ---------- reading and writing real files: sandbox ----------

func sandbox(t *testing.T) string {
	t.Helper()
	testkit.Sandbox(t)
	dir := t.TempDir()
	t.Setenv("HERMES_HOME", dir)
	if err := ioutil.WriteFile(filepath.Join(dir, "config.yaml"), []byte(sampleYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProviders(&domain.Providers{Providers: map[string]domain.Provider{
		"smt-deepseek": {BaseURL: "https://example.invalid/v1"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProfile(&domain.Profile{Name: "ds", ContextWindow: 500000,
		Roles: map[string]domain.Candidates{"mid": bind("smt-deepseek", "deepseek-flash")}}); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "config.yaml")
}

// takeover → hermes itself changed one cell → off: the cells takeover wrote go back to their
// original form, the one hermes changed stays.
func TestTakeoverRoundTripKeepsWhatHermesWroteMeanwhile(t *testing.T) {
	target := sandbox(t)
	if _, err := (Takeover{}).Apply(8899); err != nil {
		t.Fatal(err)
	}
	if !(Takeover{}).IsTakenOver(target) {
		t.Fatal("after takeover it should be recognizable")
	}
	b, _ := ioutil.ReadFile(target)
	if !strings.Contains(string(b), "newgate-ds:") {
		t.Fatalf("the profile line should be there\n%s", b)
	}
	// During takeover the user changed a key unrelated to us (the `hermes config set` kind).
	b = []byte(strings.Replace(string(b), "  max_turns: 90", "  max_turns: 120", 1))
	if err := ioutil.WriteFile(target, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Takeover{}).Restore(); err != nil {
		t.Fatal(err)
	}
	after, _ := ioutil.ReadFile(target)
	want := strings.Replace(sampleYAML, "  max_turns: 90", "  max_turns: 120", 1)
	// An auxiliary task we added that was not in the original leaves an empty mapping
	// (`goal_judge: {}`) -- hermes reads it the same as absent (defaults deep-merge), so strip
	// those before comparing.
	got := string(after)
	for _, t := range auxTasks {
		got = strings.Replace(got, "  "+t+": {}\n", "", 1)
	}
	if got != want {
		t.Errorf("after restore it should be original + that one user change\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if (Takeover{}).IsTakenOver(target) {
		t.Error("after restore it must no longer be recognized as taken over")
	}
	if _, err := os.Stat(agentapi.OriginalPath(target)); err != nil {
		t.Errorf("the original must be kept (escape hatch): %v", err)
	}
}

// Background sync touches only the list: a main model the user selected in hermes with
// `/model … --global` is not reverted.
func TestSyncLeavesTheSlotsAlone(t *testing.T) {
	target := sandbox(t)
	if _, err := (Takeover{}).Apply(8899); err != nil {
		t.Fatal(err)
	}
	b, _ := ioutil.ReadFile(target)
	b = []byte(strings.Replace(string(b), "  default: normal", "  default: smt-deepseek/deepseek-flash", 1))
	b = []byte(strings.Replace(string(b), "model:\n  provider: newgate\n", "model:\n  provider: newgate-ds\n", 1))
	if err := ioutil.WriteFile(target, b, 0o600); err != nil {
		t.Fatal(err)
	}
	// A profile was added on the newgate side.
	if err := store.SaveProfile(&domain.Profile{Name: "kimi",
		Roles: map[string]domain.Candidates{"mid": bind("kimi", "kimi-k3")}}); err != nil {
		t.Fatal(err)
	}
	if stale, err := staleCatalog(); err != nil || !stale {
		t.Fatalf("after adding a profile the list should be stale (stale=%v err=%v)", stale, err)
	}
	syncOnce()
	after, _ := ioutil.ReadFile(target)
	s := string(after)
	if !strings.Contains(s, "newgate-kimi:") || !strings.Contains(s, "kimi/kimi-k3") {
		t.Errorf("the new profile should be synced in\n%s", s)
	}
	if !strings.Contains(s, "  default: smt-deepseek/deepseek-flash") || !strings.Contains(s, "model:\n  provider: newgate-ds\n") {
		t.Errorf("the main model the user chose must not be changed back\n%s", s)
	}
	if stale, _ := staleCatalog(); stale {
		t.Error("after syncing it should no longer be stale")
	}
}

// The file is missing: do not create it, say one actionable line.
func TestApplyWithoutAConfigDoesNotCreateOne(t *testing.T) {
	testkit.Sandbox(t)
	dir := t.TempDir()
	t.Setenv("HERMES_HOME", dir)
	reps, err := (Takeover{}).Apply(8899)
	if err != nil || len(reps) != 1 || reps[0].Skipped == "" {
		t.Fatalf("it should skip and state the reason: %+v %v", reps, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err == nil {
		t.Error("it must not create a config for hermes")
	}
}

// Someone else changed the file before the write: do not write.
func TestWriteIfUnchangedRefusesARacingWriter(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	_ = ioutil.WriteFile(p, []byte("a: 2\n"), 0o600)
	if err := writeIfUnchanged(p, []byte("a: 1\n"), []byte("a: 3\n")); err == nil {
		t.Error("a stale baseline should be refused")
	}
	b, _ := ioutil.ReadFile(p)
	if string(b) != "a: 2\n" {
		t.Errorf("the file must not be touched: %s", b)
	}
}

// In the real file, auxiliary has scalars mixed in (on this machine 2026-11-04: a dozen
// `display` keys mis-indented into auxiliary). They are not tasks: they must not be edited as
// tasks, and they must not block the whole takeover.
func TestAuxiliaryWithStrayScalarsIsStillTakenOver(t *testing.T) {
	src := strings.Replace(sampleYAML, "  compression:\n", "  compact: false\n  skin: default\n  compression:\n", 1)
	d := parseDoc(src)
	edits, _ := fullEdits(d, sampleInputs())
	for _, e := range edits {
		if _, err := d.put(e); err != nil {
			t.Fatalf("a stray scalar must not block takeover: %v", err)
		}
	}
	out := d.String()
	if !strings.Contains(out, "  compact: false\n  skin: default\n") {
		t.Errorf("those two scalars must be kept as is\n%s", out)
	}
	if _, err := verifyEdits(src, out, edits, ""); err != nil {
		t.Fatal(err)
	}
}

// The /models the proxy side answers and the model table written into the file are the same
// (computed by the same function).
func TestModelsListerMatchesTheCatalog(t *testing.T) {
	sandbox(t)
	ids, ok := modelsLister{}.ListModels(ID, "ds")
	if !ok || strings.Join(ids, ",") != strings.Join(domain.Roles, ",")+",smt-deepseek/deepseek-flash" {
		t.Errorf("pinning ds = %v %v", ids, ok)
	}
	if ids, ok := (modelsLister{}).ListModels(ID, ""); !ok || strings.Join(ids, ",") != strings.Join(domain.Roles, ",") {
		t.Errorf("the follow line should list tiers only: %v", ids)
	}
	if _, ok := (modelsLister{}).ListModels("claude", ""); ok {
		t.Error("other clients are not its business")
	}
	if _, ok := (modelsLister{}).ListModels(ID, "nope"); ok {
		t.Error("a nonexistent profile gets the default list")
	}
}

// Two daemons on the same machine (different ports): only the one the file points at syncs,
// the other does not touch it. Otherwise both rewrite base_url to their own port every ten
// seconds (the bouncing measured 2026-11-04).
func TestSyncOnlyByTheDaemonTheFilePointsAt(t *testing.T) {
	target := sandbox(t)
	if _, err := (Takeover{}).Apply(8899); err != nil {
		t.Fatal(err)
	}
	before, _ := ioutil.ReadFile(target)
	st := store.LoadState()
	st.Port = 18950 // this process is "another" daemon
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProfile(&domain.Profile{Name: "kimi",
		Roles: map[string]domain.Candidates{"mid": bind("kimi", "kimi-k3")}}); err != nil {
		t.Fatal(err)
	}
	syncOnce()
	after, _ := ioutil.ReadFile(target)
	if string(after) != string(before) {
		t.Errorf("the file points at 8899; the daemon on 18950 must not touch it")
	}
}

// TestHermesPythonRejectsANonPythonLauncher is the regression guard for the "background sync
// got stuck" incident of 2026-11-17.
//
// Field evidence: hermes' official installer puts a **shell wrapper** at `~/.local/bin/hermes`
// -- `#!/bin/sh` + `exec …`. `hermesPython` trusted the shebang directly at the time, so it
// took `/bin/sh` as the interpreter and ran a piece of python with it
// (`sh -I -c '<python>' a b c d`) -- and sh reads stdin from the **inherited terminal**, so
// that command never returns. Both takeover and sync hang there, and the timeout branch is
// fail-open, so from outside it merely looks like "the sync did not happen".
//
// The criterion: unless we get an interpreter whose **name looks like python**, return an
// empty string (the caller degrades to the read-back check) rather than running something else
// as the interpreter.
func TestHermesPythonRejectsANonPythonLauncher(t *testing.T) {
	testkit.Sandbox(t)
	dir := t.TempDir()

	writeLauncher := func(body string) string {
		p := filepath.Join(dir, "hermes")
		if err := ioutil.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// The official installer's kind of wrapper: the shebang is `#!/bin/sh`, and exec follows
	// to the real entry. Taking it as the interpreter and running a piece of python with it is
	// exactly the stuck incident of 2026-11-17.
	sh := writeLauncher("#!/bin/sh\nexec \"$HOME/.hermes/hermes-agent/.hermes/bin/hermes\" \"$@\"\n")
	if got := pythonFor(sh); got != "" {
		t.Fatalf("it treated %q as the hermes python interpreter (that would hang verify)", got)
	}

	// A real python entry: recognize it (both shebang spellings must be recognized -- venv
	// uses an absolute path, user installs commonly use `env python3`).
	for _, body := range []string{
		"#!/usr/bin/env python3\n# real launcher\n",
		"#!/usr/bin/python3\n# real launcher\n",
	} {
		p := writeLauncher(body)
		got := pythonFor(p)
		if got == "" {
			t.Fatalf("cannot recognize that kind of shebang: %q", strings.TrimSpace(body))
		}
		if base := filepath.Base(got); !strings.HasPrefix(base, "python") {
			t.Fatalf("it did not return python: %q", got)
		}
	}
}
