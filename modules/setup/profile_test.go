package setup

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// These cases cover only **the wizard's own knowledge**: what the three written files
// look like, whether others' providers move on update, whether junk in base URLs is
// stripped, and the "is there usable config" check (it decides whether to re-ask).
//
// All pure functions or store I/O, fenced into a temp dir by testkit.Sandbox - never
// touching the real ~/.config/newgate.

func seedSandbox(t *testing.T) {
	t.Helper()
	testkit.Sandbox(t)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
}

// TestApplyWritesAChainThatResolves is this feature's core promise: **a config given
// one base, one key, one model resolves a candidate on every tier**.
//
// One empty tier is symptom enough ("some request suddenly has no candidate") - and
// the user cannot see that bootstrap never wrote it.
func TestApplyWritesAChainThatResolves(t *testing.T) {
	seedSandbox(t)
	in := Bootstrap{Provider: "demo", BaseURL: "https://api.example.com/v1", APIKey: "sk-x", Model: "m-1"}
	res, err := Apply(in, Profile{WriteProfile: true, SetDefault: true}, ApplyResult{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Files) == 0 {
		t.Fatal("Apply did not report changing any file")
	}

	pr, err := store.LoadProfile(ProfileName)
	if err != nil {
		t.Fatalf("reading back default: %v", err)
	}
	for _, tier := range domain.Roles {
		c, ok := pr.Roles[tier]
		if !ok || len(c) == 0 {
			t.Fatalf("tier %s has no candidates (the chain is empty)", tier)
		}
		if c[0].Provider != "demo" || c[0].Model != "m-1" {
			t.Fatalf("tier %s = %v, want demo/m-1", tier, c[0])
		}
	}
	if got := store.LoadState().DefaultProfile; got != ProfileName {
		t.Fatalf("global default = %q, want %q", got, ProfileName)
	}
	if !Inspect().HasUsable {
		t.Fatal("the config just written is judged \"unusable\" -- the wizard would ask for the upstream again next time")
	}
}

// TestApplyKeepsOtherProvidersVerbatim is a rule that must not break: other providers
// keep **every byte**, especially api_key_env ones (their plaintext lives in env only).
func TestApplyKeepsOtherProvidersVerbatim(t *testing.T) {
	seedSandbox(t)
	first := &domain.Providers{Providers: map[string]domain.Provider{
		"work": {BaseURL: "https://work.example.com/v1", APIKeyEnv: "NEWGATE_KEY_WORK", Protocol: "openai"},
		"home": {BaseURL: "https://home.example.com/v1", APIKey: "sk-home", Protocol: "anthropic"},
	}}
	if err := store.SaveProviders(first); err != nil {
		t.Fatal(err)
	}
	// Make api_key_env resolve to plaintext: writing it back would put it on disk.
	t.Setenv("NEWGATE_KEY_WORK", "sk-from-env")

	if _, err := Apply(Bootstrap{Provider: "demo", BaseURL: "https://demo.example.com", APIKey: "sk-d", Model: "d-1"},
		Profile{WriteProfile: true, SetDefault: true}, ApplyResult{}); err != nil {
		t.Fatal(err)
	}

	b, err := ioutil.ReadFile(paths.ProvidersFile())
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Providers map[string]struct {
			BaseURL   string `json:"base_url"`
			APIKey    string `json:"api_key"`
			APIKeyEnv string `json:"api_key_env"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("what was written is not valid JSON: %v", err)
	}
	work := doc.Providers["work"]
	if work.BaseURL != "https://work.example.com/v1" || work.APIKeyEnv != "NEWGATE_KEY_WORK" {
		t.Fatalf("work was corrupted: %+v", work)
	}
	if work.APIKey != "" {
		t.Fatalf("the plaintext key of work was written back to disk (it should only live in an env var): %q", work.APIKey)
	}
	if home := doc.Providers["home"]; home.APIKey != "sk-home" || home.BaseURL != "https://home.example.com/v1" {
		t.Fatalf("home was corrupted: %+v", home)
	}
	if demo, ok := doc.Providers["demo"]; !ok || demo.BaseURL != "https://demo.example.com" {
		t.Fatalf("demo was not written right: %+v", demo)
	}
}

// TestApplyLeavesExistingKvAlone: by default an existing default.kv is not overwritten.
func TestApplyLeavesExistingKvAlone(t *testing.T) {
	seedSandbox(t)
	kv := filepath.Join(paths.Mappings(), ProfileName+".kv")
	if err := ioutil.WriteFile(kv, []byte("normal=other/keep-me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(Bootstrap{Provider: "demo", BaseURL: "https://d.example.com", Model: "m"},
		Profile{WriteProfile: true, SetDefault: false}, ApplyResult{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ioutil.ReadFile(kv)
	if string(b) != "normal=other/keep-me\n" {
		t.Fatalf("the existing default.kv was overwritten: %q", b)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("it did not overwrite but did not say so either -- the user will think the model was pointed over")
	}
}

// TestApplyKvWinsOverStaleJson: after .kv is written, a same-named .json must not linger as valid.
func TestApplyKvWinsOverStaleJson(t *testing.T) {
	seedSandbox(t)
	old := filepath.Join(paths.Mappings(), ProfileName+".json")
	if err := ioutil.WriteFile(old, []byte(`{"roles":{"normal":"old/x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(Bootstrap{Provider: "demo", BaseURL: "https://d.example.com", Model: "m"},
		Profile{WriteProfile: true, SetDefault: true}, ApplyResult{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("the old default.json is still there -- it is now an inert old binding, exactly the most deceptive kind")
	}
	pr, err := store.LoadProfile(ProfileName)
	if err != nil {
		t.Fatal(err)
	}
	if got := pr.Roles["normal"][0]; got.Provider != "demo" || got.Model != "m" {
		t.Fatalf("what was read back is the old binding: %+v", got)
	}
}

// TestNormalizeBase: pasted addresses carry tails; forwarding only joins base + suffix.
func TestNormalizeBase(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com/v1/":                 "https://api.example.com/v1",
		"https://api.example.com/v1/chat/completions": "https://api.example.com/v1",
		"https://api.example.com":                     "https://api.example.com",
		" https://api.example.com/v1/messages ":       "https://api.example.com/v1",
		"https://ark.example.com/api/coding/v3/":      "https://ark.example.com/api/coding/v3",
		"https://gw.example.com/v1/models":            "https://gw.example.com/v1",
	}
	for in, want := range cases {
		if got := normalizeBase(in); got != want {
			t.Errorf("normalizeBase(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSuggestedProvider: the name derived from base must be usable as-is, clashes avoided.
func TestSuggestedProvider(t *testing.T) {
	cases := map[string]string{
		"https://api.rvcompute.com:60000/v1": "rvcompute",
		"https://api.deepseek.com/v1":        "deepseek",
		"https://openrouter.ai/api/v1":       "openrouter",
		"http://127.0.0.1:9/v1":              "127-0-0-1",
		"https://www.example.co.uk":          "example",
	}
	for in, want := range cases {
		if got := SuggestedProvider(in, nil); got != want {
			t.Errorf("SuggestedProvider(%q) = %q, want %q", in, got, want)
		}
	}
	if got := SuggestedProvider("https://api.deepseek.com", map[string]bool{"deepseek": true}); got != "deepseek-2" {
		t.Errorf("a name collision was not avoided: %q", got)
	}
}

// TestParseModelNames: all three real shapes parse (newapi's name, Ollama's models, bare array).
func TestParseModelNames(t *testing.T) {
	cases := map[string][]string{
		`{"data":[{"id":"gpt-5"},{"id":"deepseek-chat"}]}`: {"deepseek-chat", "gpt-5"},
		`{"data":[{"id":"1","name":"my-model"}]}`:          {"my-model"},
		`{"models":[{"name":"llama3"}]}`:                   {"llama3"},
		`["a","b","a"]`:                                    {"a", "b"},
		`{"object":"list","data":[]}`:                      nil,
		`not json`:                                         nil,
	}
	for in, want := range cases {
		got := parseModelNames([]byte(in))
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("parseModelNames(%s) = %v, want %v", in, got, want)
		}
	}
}

// TestModelURLs: try with and without /v1; a pasted /models is not appended again.
func TestModelURLs(t *testing.T) {
	if got := modelURLs("https://api.deepseek.com"); len(got) != 2 || got[0] != "https://api.deepseek.com/v1/models" {
		t.Errorf("base without /v1: %v", got)
	}
	if got := modelURLs("https://gw.example.com/v1"); len(got) != 1 || got[0] != "https://gw.example.com/v1/models" {
		t.Errorf("base with /v1: %v", got)
	}
	if got := modelURLs("https://gw.example.com/v1/models"); len(got) != 1 || got[0] != "https://gw.example.com/v1/models" {
		t.Errorf("already /models: %v", got)
	}
}

// TestUsableRejectsPlaceholders: a placeholder config from `newgate init` has **all its
// files**, but cannot run. Treating it as "set up" skips the only step that makes it run.
func TestUsableRejectsPlaceholders(t *testing.T) {
	seedSandbox(t)
	if _, err := store.Init(false); err != nil {
		t.Fatal(err)
	}
	if Inspect().HasUsable {
		t.Fatal("the placeholder config was judged usable")
	}
	// Only a real key makes it usable (placeholder provider and model names remain, key set).
	provs, err := store.LoadProviders()
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range provs.Providers {
		p.APIKey = "sk-real"
		p.APIKeyEnv = ""
		provs.Providers[name] = p
	}
	if err := store.SaveProviders(provs); err != nil {
		t.Fatal(err)
	}
	if !Inspect().HasUsable {
		t.Fatal("a config that has a key is still judged unusable -- the wizard would pointlessly ask for the upstream again")
	}
}

// TestPortOf: 0 / negative falls back to 8899 (the wizard hands it to takeover and Start).
func TestPortOf(t *testing.T) {
	if portOf(0) != domain.ProxyPort || portOf(-1) != domain.ProxyPort || portOf(9000) != 9000 {
		t.Fatal("portOf does not converge correctly")
	}
}
