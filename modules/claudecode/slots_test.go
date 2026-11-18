package claudecode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate-ext/lib/view"

	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// The slot mapping is configurable since 2026-09-21. This family tests the three spots
// on that chain most easily broken: the value read must really reach the injection,
// what is written must really be persisted, and a bad value must **loudly** fail to
// take effect (rather than quietly becoming a nonexistent model name sent upstream).

// slotOf gets one slot definition (by name).
func slotOf(t *testing.T, name string) agentapi.Slot {
	t.Helper()
	for _, s := range Agent().Slots {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("there is no slot named %q", name)
	return agentapi.Slot{}
}

// TestASlotFollowsTheConfig: a changed slot takes the new tier when injected.
//
// The assertion is on **BuildEnv**, not TierOf: the result the user sees is that value in
// env, and "TierOf is right but BuildEnv forgot to use it" is exactly what this kind of
// change most easily leaves behind -- the UI shows it changed and the behavior does not.
func TestASlotFollowsTheConfig(t *testing.T) {
	testkit.Sandbox(t)
	writeState(t, map[string]any{SlotsKey: map[string]string{"sonnet": "normal"}})

	a := Agent()
	if got := agentapi.TierOf(facts{}, slotOf(t, "sonnet")); got != "normal" {
		t.Errorf("sonnet should use normal (changed in config), got %q", got)
	}
	env := a.BuildEnv(8899, "tok", facts{})
	// What is injected is the bare tier name (since 2026-11-12
	// no `[1m]` suffix; see TestInjectedModelNamesStayBare).
	if got := env["ANTHROPIC_DEFAULT_SONNET_MODEL"]; got != "normal" {
		t.Errorf("the injected value should be the modified tier, got %q", got)
	}
	// The unchanged ones are unaffected.
	if got := env["ANTHROPIC_DEFAULT_OPUS_MODEL"]; got != "normal" {
		t.Errorf("opus was never configured, so the default normal, got %q", got)
	}
	if got := env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; got != "light" {
		t.Errorf("haiku was never configured, so the default light, got %q", got)
	}
}

// TestInjectedModelNamesStayBare: the injected model name
// is the **bare name**, with no capability marker appended.
//
// On 2026-09-28 every slot was given a `[1m]` suffix to keep large-window models from
// compacting early. The 2026-11-12 scene overturned that: the 1M declaration let the session
// grow to 1.01M tokens, the next request got 400 "Input tokens exceed the model context
// limit", and /compact itself was over the limit too -- the client had no self-rescue
// path. Now the window is declared by `CLAUDE_CODE_MAX_CONTEXT_TOKENS` (capped at 512K),
// and the name stays bare: both the tier name `normal` and the real name `deepseek-chat`.
//
// This test pins two things:
//
//  1. **Neither dynamic mode nor pinned mode carries a suffix**. The two
//     paths splice in two places (BuildEnv and the pinned branch of launch);
//     this pins the descriptor side, the other side is in launch_test.go.
//  2. **The two window-declaration variables are not in BuildEnv**. They
//     are numbers injected by launch, not the slot assembly's business.
func TestInjectedModelNamesStayBare(t *testing.T) {
	testkit.Sandbox(t)
	a := Agent()
	env := a.BuildEnv(8899, "tok", facts{})
	for _, s := range a.EnvSlots() {
		want := agentapi.TierOf(facts{}, s)
		if got := env[s.EnvVar]; got != want {
			t.Errorf("slot %s should inject the bare name %q, got %q", s.Name, want, got)
		}
	}
	if _, there := env[a.ContextWindowEnv]; there {
		t.Errorf("the window declaration must not be in BuildEnv, got %v", env[a.ContextWindowEnv])
	}
}

// TestClaudeCodeDeclaresItsWindowCap: this client caps the window at 512K and compaction at 400K.
//
// The numbers themselves are a product decision (see the comment in agent.go),
// but this is the only defense against the "after 400 not even /compact can be
// sent" failure -- raising it brings that day's scene back, so it is pinned here.
func TestClaudeCodeDeclaresItsWindowCap(t *testing.T) {
	a := Agent()
	if a.ContextWindowCap != 512000 {
		t.Errorf("ContextWindowCap = %d, should be 512000", a.ContextWindowCap)
	}
	if a.AutoCompactCap != 400000 {
		t.Errorf("AutoCompactCap = %d, should be 400000", a.AutoCompactCap)
	}
}

// TestWritingASlotReturnsItToTheDefault: setting a row back to the default
// = that key disappears, rather than leaving a record with the same value.
//
// Leaving an equal-valued record has a real cost: whoever changes the default later
// finds their change not taking effect for some users, and those users never configured
// anything -- that "I changed it and theirs did not" is the hardest class to attribute.
func TestWritingASlotReturnsItToTheDefault(t *testing.T) {
	testkit.Sandbox(t)
	if err := WriteSlotTiers(map[string]string{"sonnet": "heavy"}); err != nil {
		t.Fatal(err)
	}
	if got := ReadSlotTiersOK(t, "sonnet"); got != "heavy" {
		t.Fatalf("writing it should read back, got %q", got)
	}
	// Back to the default (mid).
	if err := WriteSlotTiers(map[string]string{"sonnet": "mid"}); err != nil {
		t.Fatal(err)
	}
	ok, _ := ReadSlotTiers()
	if v, there := ok["sonnet"]; there {
		t.Errorf("after reverting to the default that key should disappear, but %q is still there", v)
	}
	if got := agentapi.TierOf(facts{}, slotOf(t, "sonnet")); got != "mid" {
		t.Errorf("after reverting to the default it should use the default mid, got %q", got)
	}
}

// TestABadTierIsRefusedOnTheWayIn: writing a nonexistent tier must be rejected.
//
// It would be injected as a model name as is. A mistyped tier name (`hevy`) is
// "nonexistent model" upstream, with the symptom that some requests suddenly 400, and
// it is several layers away from the config -- so it must be stopped at the entrance.
func TestABadTierIsRefusedOnTheWayIn(t *testing.T) {
	testkit.Sandbox(t)
	if err := WriteSlotTiers(map[string]string{"sonnet": "hevy"}); err == nil {
		t.Fatal("writing a nonexistent tier should be rejected")
	}
	if got := agentapi.TierOf(facts{}, slotOf(t, "sonnet")); got != "mid" {
		t.Errorf("a rejected write must leave no trace, but it resolves to %q", got)
	}
}

// TestABadTierInTheFileDoesNotBreakTakeover: a hand-broken file must not break injection too.
//
// state.json is meant for human editing, and the consequence of breaking
// this cell is "claude will not start" -- a mistyped tier should not have that
// consequence. So it is filtered on read, falls back to the default, and is
// **reported** (the card's why says so), rather than quietly injecting `hevy`.
func TestABadTierInTheFileDoesNotBreakTakeover(t *testing.T) {
	testkit.Sandbox(t)
	writeState(t, map[string]any{SlotsKey: map[string]string{"sonnet": "hevy", "opus": "light"}})

	if got := agentapi.TierOf(facts{}, slotOf(t, "sonnet")); got != "mid" {
		t.Errorf("a bad value should fall back to the default mid, got %q", got)
	}
	if got := agentapi.TierOf(facts{}, slotOf(t, "opus")); got != "light" {
		t.Errorf("the good line in the same file should still take effect, got %q", got)
	}
	// The bad row must be spoken of in the UI: otherwise it just sits there silently doing nothing.
	if note := slotNote("sonnet", "mid"); note == "" {
		t.Error("a bad value should produce a sentence on the card, but nothing is said now")
	}
	// The good row also has something to say (the user changed it; the UI must show it).
	if note := slotNote("opus", "normal"); note == "" {
		t.Error("the modified line should say it was modified")
	}
	// Unchanged rows say nothing.
	if note := slotNote("haiku", "light"); note != "" {
		t.Errorf("an unmodified line should have no annotation, got %q", note)
	}
}

// ReadSlotTiersOK is a small test helper: read back one cell's value.
func ReadSlotTiersOK(t *testing.T, slot string) string {
	t.Helper()
	ok, _ := ReadSlotTiers()
	return ok[slot]
}

// TestAClientSpecificValueSurvives: the client's own
// value must not be filtered out as a mistyped tier name.
//
// `subagent`'s description says "set it to inherit to hand that back to per-slot resolution" --
// that is **Claude Code's** semantics, and newgate only injects the string as is. The first version
// used only domain.Roles as the test, so `inherit` could not be set in the UI and hand-edited
// values were silently ignored: the description said it could, and following it did nothing.
//
// The exception is declared by the client module itself (agentapi.Slot.Also),
// so this also pins that boundary -- the kernel and the config layer do
// not know the word `inherit`, they only know "this slot declared it".
func TestAClientSpecificValueSurvives(t *testing.T) {
	testkit.Sandbox(t)
	if err := WriteSlotTiers(map[string]string{"subagent": "inherit"}); err != nil {
		t.Fatalf("the value from the client itself should be accepted: %v", err)
	}
	a := Agent()
	if got := agentapi.TierOf(facts{}, slotOf(t, "subagent")); got != "inherit" {
		t.Errorf("it should pass inherit through as is, got %q", got)
	}
	if got := a.BuildEnv(8899, "tok", facts{})["CLAUDE_CODE_SUBAGENT_MODEL"]; got != "inherit" {
		t.Errorf("it should inject as is, got %q", got)
	}
	// This exception belongs only to the slot that declared
	// it: writing inherit on another slot is still a typo.
	if err := WriteSlotTiers(map[string]string{"opus": "inherit"}); err == nil {
		t.Error("opus does not declare inherit, so it should be rejected")
	}
	// It must be in the dropdown too -- otherwise the user sees the description and cannot set it.
	opts := allowedFor("subagent")
	if !contains(opts, "inherit") || !contains(opts, "mid") {
		t.Errorf("the subagent value should be the tier plus inherit, got %v", opts)
	}
	if contains(allowedFor("opus"), "inherit") {
		t.Error("the opus value must not contain inherit")
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// TestTheCardsAreLockedWhenTheClientIsNotThere: on a machine
// without claude, both cards are grayed out entirely.
//
// The user's words: "anything not installed, gray it out entirely". Configuring slots or
// adjusting the classifier for claude takes effect not one byte on a machine without claude
// -- if the card stayed editable, the user would only find out after making useless changes.
//
// The test is on **the reason sentence**, not "was the lock applied": locked
// without a reason and the user just thinks the UI is broken. And what is
// locked is **both** cards -- locking only one leaves the other still editable.
func TestTheCardsAreLockedWhenTheClientIsNotThere(t *testing.T) {
	testkit.Sandbox(t)
	// Both directions must be **fabricated**: whoever runs this
	// test is using claude, so "is it on PATH" depends on the machine --
	// assuming it is absent would make this test always red on a dev box.
	t.Setenv("PATH", t.TempDir()) // an empty directory: nothing is found
	if reason := lockReason(); reason == "" {
		t.Fatal("claude is not on PATH, so these two cards should be greyed out")
	}
	for _, c := range []view.Concept{classifierConcept(), slotsConcept()} {
		if c.Locked == "" {
			t.Errorf("%s should carry a sentence explaining the grey-out", c.ID)
		}
	}
	// After installing (create a real executable with that name) it should no longer be locked.
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if reason := lockReason(); reason != "" {
		t.Errorf("claude is on PATH now, so it should no longer be greyed out: %q", reason)
	}
}
