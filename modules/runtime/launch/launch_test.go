package launch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
)

// sandboxStore lays out a minimal config in a NEWGATE_HOME sandbox: one provider, two
// profiles - glm declares windows, tiny does not.
func sandboxStore(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("providers.json", `{"providers":{"smt-glm":{"base_url":"https://x/v1","api_key":"k"}}}`)
	write("mappings/glm.json", `{
		"name": "glm", "priority": 20,
		"context_window": 1000000, "auto_compact_window": 500000,
		"roles": {"heavy": "smt-glm/glm-5.3", "mid": "smt-glm/glm-5.3",
			"light": "smt-glm/glm-4.5-air", "vision": "smt-glm/glm-4.5-air"}
	}`)
	// tiny with only heavy would leave the main slot (normal after the four-tier change) with
	// no candidate, and the chain falls to the next profile - so tiny gets an explicit normal
	// (which also leaves an assertion point that the main tier's real name follows the profile).
	write("mappings/tiny.json", `{"name":"tiny","priority":30,
		"roles":{"heavy":"smt-glm/glm-4-plus","normal":"smt-glm/glm-4-plus"}}`)
}

// TestBuildInjectWindowEnv: injecting window declarations. Claude Code does not know the
// real model name we inject (glm-5.3), takes it for "unknown" and compacts at a 200k
// window - only context_window/auto_compact_window in the profile produce env (measured
// 2026-09 on binary 2.1.265: MAX_CONTEXT_TOKENS applies only to unknown models,
// AUTO_COMPACT_WINDOW has top priority, window=min(both)). The descriptor can cap them.
// testAgent is the kernel tests' **synthetic** client definition: the real claude
// definition lives in the distribution (client integration is a product choice, moved
// 2026-09-20). The window env **names** now also come from the definition
// (agentapi.Agent.ContextWindowEnv / AutoCompactEnv), so synthetic names can test it:
// "declared -> injected under the defined name".
func testAgent() *agentapi.Agent {
	return &agentapi.Agent{
		ID:               "test-client",
		Bin:              []string{"test-client"},
		Dialect:          "anthropic",
		BaseURLEnv:       "TEST_BASE_URL",
		AuthEnv:          "TEST_API_KEY",
		ContextWindowEnv: "TEST_MAX_CONTEXT_TOKENS",
		AutoCompactEnv:   "TEST_AUTO_COMPACT_WINDOW",
		// Three slots map one-to-one to tiers: assertions then read "a slot gets its tier",
		// with no need to know any client's slot naming (claude's opus/fable/haiku is its own).
		Slots: []agentapi.Slot{
			{Name: "heavy", Tier: "heavy", EnvVar: "TEST_HEAVY_MODEL"},
			{Name: "normal", Tier: "normal", EnvVar: "TEST_NORMAL_MODEL"},
			{Name: "light", Tier: "light", EnvVar: "TEST_LIGHT_MODEL"},
		},
	}
}

func TestBuildInjectWindowEnv(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := testAgent()

	t.Run("a profile that declares windows -> both env vars are injected", func(t *testing.T) {
		inject, _ := buildInject(a, nil, st, "glm", "")
		if got := inject["TEST_MAX_CONTEXT_TOKENS"]; got != "1000000" {
			t.Errorf("MAX_CONTEXT_TOKENS = %q, want 1000000", got)
		}
		if got := inject["TEST_AUTO_COMPACT_WINDOW"]; got != "500000" {
			t.Errorf("AUTO_COMPACT_WINDOW = %q, want 500000", got)
		}
	})

	t.Run("an undeclaring profile -> nothing injected", func(t *testing.T) {
		inject, _ := buildInject(a, nil, st, "tiny", "")
		if _, has := inject["TEST_MAX_CONTEXT_TOKENS"]; has {
			t.Error("tiny declares no context_window, so MAX_CONTEXT_TOKENS must not be injected")
		}
		if _, has := inject["TEST_AUTO_COMPACT_WINDOW"]; has {
			t.Error("tiny declares no auto_compact_window, so AUTO_COMPACT_WINDOW must not be injected")
		}
	})

	t.Run("a missing profile -> fail-open, nothing injected", func(t *testing.T) {
		inject, _ := buildInject(a, nil, st, "ghost", "")
		if _, has := inject["TEST_MAX_CONTEXT_TOKENS"]; has {
			t.Error("no window env var should appear when the profile does not exist")
		}
	})

	t.Run("only context_window -> only one env var", func(t *testing.T) {
		// Both take effect independently: report only the real window, let the client pick compact.
		dir := os.Getenv("NEWGATE_HOME")
		if err := os.WriteFile(filepath.Join(dir, "mappings", "glm.json"),
			[]byte(`{"name":"glm","priority":20,"context_window":1000000,
				"roles":{"heavy":"smt-glm/glm-5.3"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		inject, _ := buildInject(a, nil, st, "glm", "")
		if got := inject["TEST_MAX_CONTEXT_TOKENS"]; got != "1000000" {
			t.Errorf("MAX_CONTEXT_TOKENS = %q, want 1000000", got)
		}
		if _, has := inject["TEST_AUTO_COMPACT_WINDOW"]; has {
			t.Error("without auto_compact_window nothing should be injected")
		}
	})
}

// TestBuildInjectWindowCap: the client's caps (ContextWindowCap / AutoCompactCap),
// no matter how big the profile declares, and **say so** when it exceeds.
//
// Live (2026-11-12): a 1M window pushed the session to the last cell, and past 400 even
// /compact could not be sent - the compaction request itself exceeded. A cap leaves
// room for compaction; silently swapping the 1M gives "config right, /context 512K" - warn.
func TestBuildInjectWindowCap(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := testAgent()
	a.ContextWindowCap = 512000
	a.AutoCompactCap = 400000

	t.Run("a profile declaring 1M -> capped to 512K/400K, with a warn", func(t *testing.T) {
		inject, warns := buildInject(a, nil, st, "glm", "")
		if got := inject["TEST_MAX_CONTEXT_TOKENS"]; got != "512000" {
			t.Errorf("MAX_CONTEXT_TOKENS = %q, want it capped at 512000", got)
		}
		if got := inject["TEST_AUTO_COMPACT_WINDOW"]; got != "400000" {
			t.Errorf("AUTO_COMPACT_WINDOW = %q, want it capped at 400000", got)
		}
		if len(warns) != 1 {
			t.Errorf("capping 1M to 512K must come with a warn line, got %v", warns)
		}
	})

	t.Run("a profile declaring no window -> the cap counts as declared, compact gets no fallback", func(t *testing.T) {
		inject, warns := buildInject(a, nil, st, "tiny", "")
		if got := inject["TEST_MAX_CONTEXT_TOKENS"]; got != "512000" {
			t.Errorf("with no declared window the client cap 512000 should be used, got %q", got)
		}
		if _, has := inject["TEST_AUTO_COMPACT_WINDOW"]; has {
			t.Error("no injection when compact is undeclared -- the client computes its compaction point from its own window")
		}
		if len(warns) != 0 {
			t.Errorf("no large window was declared, so no warn is expected, got %v", warns)
		}
	})

	t.Run("a profile declaring less than the cap -> left as is", func(t *testing.T) {
		dir := os.Getenv("NEWGATE_HOME")
		if err := os.WriteFile(filepath.Join(dir, "mappings", "glm.json"),
			[]byte(`{"name":"glm","priority":20,"context_window":200000,
				"auto_compact_window":150000,"roles":{"heavy":"smt-glm/glm-5.3"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		inject, warns := buildInject(a, nil, st, "glm", "")
		if got := inject["TEST_MAX_CONTEXT_TOKENS"]; got != "200000" {
			t.Errorf("MAX_CONTEXT_TOKENS = %q, should be 200000", got)
		}
		if got := inject["TEST_AUTO_COMPACT_WINDOW"]; got != "150000" {
			t.Errorf("AUTO_COMPACT_WINDOW = %q, should be 150000", got)
		}
		if len(warns) != 0 {
			t.Errorf("it does not exceed the cap, so it must not warn, got %v", warns)
		}
	})
}

// TestBuildInjectModelNames: the two slot naming modes (settled 2026-09-09):
// default dynamic - slots keep tier names, --set-profile applies to running sessions at
// once; explicit --profile pins - inject real model names, showing what is really used.
func TestBuildInjectModelNames(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := testAgent()

	t.Run("default (dynamic): slots = tier names, base URL has no /p/", func(t *testing.T) {
		inject, _ := buildInject(a, nil, st, "glm", "")
		if got := inject["TEST_BASE_URL"]; got != "http://127.0.0.1:8899/a/test-client" {
			t.Errorf("BASE_URL = %q", got)
		}
		// Four-tier change (2026-09-16): the opus slot = the main tier normal, fable is heavy
		if got := inject["TEST_NORMAL_MODEL"]; got != "normal" {
			t.Errorf("in dynamic mode the slot should be the tier name, NORMAL_MODEL = %q", got)
		}
		if got := inject["TEST_HEAVY_MODEL"]; got != "heavy" {
			t.Errorf("HEAVY_MODEL = %q, should be heavy", got)
		}
		if got := inject["TEST_LIGHT_MODEL"]; got != "light" {
			t.Errorf("LIGHT_MODEL = %q, should be light", got)
		}
	})

	t.Run("explicit profile (pinned): slots = real model names, base URL carries /p/", func(t *testing.T) {
		inject, _ := buildInject(a, nil, st, "glm", "glm")
		if got := inject["TEST_BASE_URL"]; got != "http://127.0.0.1:8899/a/test-client/p/glm" {
			t.Errorf("BASE_URL = %q", got)
		}
		if got := inject["TEST_NORMAL_MODEL"]; got != "glm-5.3" {
			t.Errorf("pinned mode should use real names, NORMAL_MODEL = %q", got)
		}
		if got := inject["TEST_LIGHT_MODEL"]; got != "glm-4.5-air" {
			t.Errorf("LIGHT_MODEL = %q, should be glm-4.5-air", got)
		}
	})

	t.Run("pinned to another profile: real names follow the selected profile", func(t *testing.T) {
		// The default chain head is glm, --profile tiny: the real name must be tiny's, else
		// the UI shows what does not match what runs
		inject, _ := buildInject(a, nil, st, "tiny", "tiny")
		if got := inject["TEST_NORMAL_MODEL"]; got != "glm-4-plus" {
			t.Errorf("NORMAL_MODEL = %q, should be glm-4-plus from tiny", got)
		}
		if _, has := inject["TEST_MAX_CONTEXT_TOKENS"]; has {
			t.Error("the window should follow the selected profile (tiny, which declares none)")
		}
	})
}

// TestModelSuffixSurvivesBothModes: the model-name suffix a slot declares (the client's
// capability marker) must ride along in **both naming modes**, and **only** on model names.
//
// Why a separate lock (2026-09-28): the marker lands on the injected model name, and that
// injection has **two places** - BuildEnv (dynamic: tier name) and buildInject's pinned
// branch (real model name). Users named the latter as priority (`newgate claude
// --profile=xx`), and it is exactly the later-added place: dynamic mode right, pinned
// mode lost the marker - "after switching profile the 1M context is gone", config normal.
//
// The marker itself is **synthetic** (`@1m`): the kernel knows no vendor's real marker -
// the client module supplies it (this distribution's modules/claudecode). Tested here:
// "declared -> appended, both modes, not numbers".
func TestModelSuffixSurvivesBothModes(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := testAgent()
	const mark = "@1m"
	for i := range a.Slots {
		a.Slots[i].ModelSuffix = mark
	}

	t.Run("dynamic mode: tier names carry the marker", func(t *testing.T) {
		inject, _ := buildInject(a, nil, st, "glm", "")
		if got := inject["TEST_NORMAL_MODEL"]; got != "normal"+mark {
			t.Errorf("NORMAL_MODEL = %q, should be normal%s", got, mark)
		}
		if got := inject["TEST_LIGHT_MODEL"]; got != "light"+mark {
			t.Errorf("LIGHT_MODEL = %q, should be light%s", got, mark)
		}
	})

	t.Run("pinned mode: real model names carry the marker (the --profile=xx path)", func(t *testing.T) {
		inject, _ := buildInject(a, nil, st, "glm", "glm")
		if got := inject["TEST_NORMAL_MODEL"]; got != "glm-5.3"+mark {
			t.Errorf("NORMAL_MODEL = %q, should be glm-5.3%s", got, mark)
		}
		if got := inject["TEST_LIGHT_MODEL"]; got != "glm-4.5-air"+mark {
			t.Errorf("LIGHT_MODEL = %q, should be glm-4.5-air%s", got, mark)
		}
	})

	// Window declarations are **numbers** (from strconv.Itoa), and appending a marker writes
	// an unparseable value - the failure looks like "the window declaration silently died,
	// back to 200k", as if this injection did not exist. So number vars never go through Apply.
	t.Run("window declarations do not carry the marker", func(t *testing.T) {
		inject, _ := buildInject(a, nil, st, "glm", "glm")
		if got := inject["TEST_MAX_CONTEXT_TOKENS"]; got != "1000000" {
			t.Errorf("MAX_CONTEXT_TOKENS = %q, should be 1000000 (without the marker)", got)
		}
		if got := inject["TEST_AUTO_COMPACT_WINDOW"]; got != "500000" {
			t.Errorf("AUTO_COMPACT_WINDOW = %q, should be 500000 (without the marker)", got)
		}
	})
}

// TestBuildInjectOverridesInherited: execReal's env allowlist ensures injected values beat
// stale same-name variables in the user's shell - the most common cause of "switch did
// nothing". Only buildInject's output is verified here; the override is covered by e2e.
func TestBuildInjectOverridesInherited(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := testAgent()

	t.Setenv("TEST_AUTO_COMPACT_WINDOW", "666") // the user has set a different value themselves
	inject, _ := buildInject(a, nil, st, "glm", "")
	if got := inject["TEST_AUTO_COMPACT_WINDOW"]; got != "500000" {
		t.Fatalf("the profile declaration should win over an old value in the user environment: %q", got)
	}
}

// TestBuildInjectWarnsWhenConfigUnreadable: an unreadable config must leave a word behind.
//
// This path is fail-open: no config means "slots keep tier names", the proxy still routes
// by tier, so the call still starts. But it also **drops** two things - the real model
// name, and the profile's window (else Claude Code compacts at a 200k assumption). The
// latter is a behavior change the user feels, and before 2026-09-18 this if had no else,
// so the whole injection was skipped silently, saying nothing.
func TestBuildInjectWarnsWhenConfigUnreadable(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := testAgent()

	// Break providers.json: Load fails at its first step.
	p := filepath.Join(os.Getenv("NEWGATE_HOME"), "providers.json")
	if err := os.WriteFile(p, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	inject, warns := buildInject(a, nil, st, "glm", "glm")
	if len(warns) == 0 {
		t.Fatal("when the config cannot be read it must warn, or the user only sees 'the window did not take effect' without knowing why")
	}
	// fail-open: slots are still injected (tier names), just not the real model names.
	if got := inject["TEST_NORMAL_MODEL"]; got != "normal" {
		t.Errorf("when the config cannot be read the slot should stay the tier name normal, got %q", got)
	}
	if _, has := inject["TEST_MAX_CONTEXT_TOKENS"]; has {
		t.Error("when the config cannot be read there should be no window declaration")
	}
}
