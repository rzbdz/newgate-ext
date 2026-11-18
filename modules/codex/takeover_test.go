package codex

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// Taking over codex = rewriting a **hand-written** TOML. This family tests the three most
// breakable things in that sentence: not one thing someone else wrote may be touched,
// what we wrote must be recognizable as ours, and the escape hatch must really restore.

// sample is a TOML as close to a real user config as possible:
// comments, scattered sections, and its own bookkeeping.
//
// Every item was seen in measurement (`~/.codex/config.toml`, 2026-09-21): `[projects.*]` grows by
// itself each time codex trusts a directory, `[notice.model_migrations]` is its record of renames.
const sample = `# my codex config
model = "gpt-5.6-terra"
model_reasoning_effort = "medium"
personality = "pragmatic"

[projects."/root"]
trust_level = "trusted"

[notice.model_migrations]
"gpt-5.3-codex" = "gpt-5.4"
`

// TestRewriteKeepsEverythingElse: touch only the three lines that
// should change; everything else stays untouched **byte for byte**.
//
// This is the entire reason this implementation exists. A TOML parse-then-serialize
// round trip changes comments, section order and blank line positions -- and that loss
// turns no test red; it only makes the user discover "the comments I wrote are gone".
func TestRewriteKeepsEverythingElse(t *testing.T) {
	out, reps := rewrite(sample, 8899, want{Model: "normal", ContextWindow: 200000})

	for _, keep := range []string{
		"# my codex config",
		`personality = "pragmatic"`,
		`model_reasoning_effort = "medium"`,
		"[projects.\"/root\"]",
		`trust_level = "trusted"`,
		"[notice.model_migrations]",
		`"gpt-5.3-codex" = "gpt-5.4"`,
	} {
		if !strings.Contains(out, keep) {
			t.Errorf("this line must not be touched: %q\n--- got ---\n%s", keep, out)
		}
	}

	if !strings.Contains(out, `model = "normal"`) {
		t.Errorf("model should be rewritten to the tier name\n%s", out)
	}
	if !strings.Contains(out, `model_provider = "newgate"`) {
		t.Errorf("model_provider should point at us\n%s", out)
	}
	if strings.Contains(out, "gpt-5.6-terra") {
		t.Errorf("the original concrete model name should be replaced\n%s", out)
	}
	if len(reps) == 0 {
		t.Error("any change must be recorded - takeover must never be silent")
	}
}

// TestRewriteDoesNotTouchKeysInsideTables: same-named keys
// inside tables must not be changed as if they were top-level.
//
// In TOML keys of different tables are unrelated, and this file genuinely has
// other sections. The first version looked for "a line containing `model`" and would
// treat the rename records in `[notice.model_migrations]` as model names -- the
// symptom being that the user's rename history was quietly replaced with tier names.
func TestRewriteDoesNotTouchKeysInsideTables(t *testing.T) {
	const src = `[projects."/root"]
model = "user-set-this"
trust_level = "trusted"
`
	out, _ := rewrite(src, 8899, want{Model: "normal"})
	if !strings.Contains(out, `model = "user-set-this"`) {
		t.Errorf("the model in the table is not ours, leave it alone\n%s", out)
	}
	// The top level has no model, so it must be added at the very top -- and
	// **not** after a table header (that would make it a key of that table).
	head := strings.SplitN(out, "[projects", 2)[0]
	if !strings.Contains(head, `model = "normal"`) {
		t.Errorf("a missing top-level key should be added before the first table header\n%s", out)
	}
}

// TestRewriteIsIdempotent: take over twice, same result.
//
// Idempotency is not fussiness: `newgate on codex` is meant to be run repeatedly anyway (run
// it again after changing a tier, see "take over again" in the docs). Without idempotency,
// every run adds another `[model_providers.newgate]` section, and a duplicate same-named
// table in TOML is a **parse error** -- the user's next codex start-up fails outright.
func TestRewriteIsIdempotent(t *testing.T) {
	once, _ := rewrite(sample, 8899, want{Model: "normal", ContextWindow: 200000})
	twice, _ := rewrite(once, 8899, want{Model: "normal", ContextWindow: 200000})
	if once != twice {
		t.Errorf("the second takeover changed something\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
	if n := strings.Count(twice, providerTable); n != 1 {
		t.Errorf("that config section should exist once, got %d", n)
	}
	if n := strings.Count(twice, "model_context_window"); n != 1 {
		t.Errorf("the window declaration should be one line, got %d lines", n)
	}
	// Idempotent and it must still really change something -- only when both hold does it count.
	if !strings.Contains(twice, `wire_api = "responses"`) {
		t.Errorf("after takeover it should point at the local proxy\n%s", twice)
	}
}

// TestRewriteRefreshesAnOlderTakeover: the section written by a
// previous version (port changed) must be replaced, not stacked.
//
// The port changes (it can be changed in the config), and if the old section is
// kept, the symptom is codex always hitting a port **nobody is listening on** --
// the user sees "connection refused" while the config file plainly says newgate.
func TestRewriteRefreshesAnOlderTakeover(t *testing.T) {
	old, _ := rewrite(sample, 8899, want{Model: "normal"})
	moved, _ := rewrite(old, 9999, want{Model: "mid"})

	if strings.Contains(moved, "127.0.0.1:8899") {
		t.Errorf("the old port should be replaced\n%s", moved)
	}
	if !strings.Contains(moved, "127.0.0.1:9999") {
		t.Errorf("the new port should be there\n%s", moved)
	}
	if !strings.Contains(moved, `model = "mid"`) {
		t.Errorf("the tier should change too\n%s", moved)
	}
}

// TestTheWindowIsOnlyWrittenWhenWeKnowIt: when the window is unknown, **do not write** that line.
//
// Writing a guessed number is worse than writing nothing: codex uses it to decide when
// to auto-compact, and "guessed too small" means long sessions get cut off early -- it
// looks like the model got dumber, and nobody suspects the extra line in the config.
func TestTheWindowIsOnlyWrittenWhenWeKnowIt(t *testing.T) {
	without, _ := rewrite(sample, 8899, want{Model: "normal"})
	if strings.Contains(without, "model_context_window") {
		t.Errorf("when the window is unknown this line should not be written\n%s", without)
	}
	with, _ := rewrite(sample, 8899, want{Model: "normal", ContextWindow: 200000})
	if !strings.Contains(with, "model_context_window = 200000") {
		t.Errorf("when the window is known it should be written\n%s", with)
	}
}

// TestApplyThenRestoreGivesBackTheOriginalBytes: the escape hatch.
//
// This is the one place in the whole takeover that must not fail: after the user runs `newgate
// off codex` (or simply uninstalls newgate), what comes back must **be** his original copy,
// not one byte different. So the assertion is on byte equality, not "looks about the same".
func TestApplyThenRestoreGivesBackTheOriginalBytes(t *testing.T) {
	testkit.Sandbox(t)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	target := filepath.Join(codexHome, "config.toml")
	if err := ioutil.WriteFile(target, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}

	tk := Takeover{}
	reps, err := tk.Apply(8899)
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 1 || reps[0].Skipped != "" {
		t.Fatalf("takeover should succeed, got %+v", reps)
	}
	if !tk.IsTakenOver(target) {
		t.Error("after takeover IsTakenOver should be true")
	}
	if b, _ := ioutil.ReadFile(target); !strings.Contains(string(b), "127.0.0.1:8899") {
		t.Errorf("after takeover it should point at the local proxy\n%s", b)
	}

	done, err := tk.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 {
		t.Fatalf("it should restore one file, got %v", done)
	}
	got, err := ioutil.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sample {
		t.Errorf("what was restored is not the original\n--- want ---\n%s\n--- got ---\n%s", sample, got)
	}
	if tk.IsTakenOver(target) {
		t.Error("after restore it should no longer count as taken over")
	}
}

// TestATaintedFileIsNotUsedAsTheOriginal: once original/ has been
// deleted, the already-taken-over file must not be stored as the original.
//
// This is a pit measured on the opencode side, and not one word of it can be
// dropped for codex: once it is stored wrong, the user's original config is gone
// **permanently** (original/ is the only copy we never touched). The criterion
// lands on "must report an error" -- storing it quietly is the real loss.
func TestATaintedFileIsNotUsedAsTheOriginal(t *testing.T) {
	testkit.Sandbox(t)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	target := filepath.Join(codexHome, "config.toml")

	taken, _ := rewrite(sample, 8899, want{Model: "normal"})
	if err := ioutil.WriteFile(target, []byte(taken), 0o600); err != nil {
		t.Fatal(err)
	}
	// original/ does not exist (the scenario where the first takeover's copy was deleted by mistake).
	if err := os.RemoveAll(filepath.Dir(agentapi.OriginalPath(target))); err != nil {
		t.Fatal(err)
	}
	tk := Takeover{}
	if _, err := tk.Apply(8899); err == nil {
		t.Fatal("an already taken-over file + no original = should error, not store it as the original")
	}
}

// TestTheSecondModelKeyIsWritten: codex has **more
// than one** model, and both must be written right.
//
// `review_model` is the one `codex review` uses (side by side with
// `model` in codex 0.155's ConfigToml, verified 2026-09-21). The
// user's words: "Why does his model have only one slot? Fix it!"
//
// The criterion lands on **each of the two keys being written on its own**: a mistake
// in one (say taking slots by index and writing the review tier into the main model)
// is completely invisible in the UI -- both boxes hold valid tier names, only review goes
// through the main model's tier, and that is only found when review is actually run once.
func TestTheSecondModelKeyIsWritten(t *testing.T) {
	out, _ := rewrite(sample, 8899, want{Model: "heavy", ReviewModel: "mid"})
	if !strings.Contains(out, `model = "heavy"`) {
		t.Errorf("the main model should go to heavy\n%s", out)
	}
	if !strings.Contains(out, `review_model = "mid"`) {
		t.Errorf("review should go to mid\n%s", out)
	}
}

// TestAnEmptyValueIsNotWrittenAtAll: a key with no
// value **writes no line at all**, not an empty value.
//
// This one was hit in measurement: `values()` gives only the keys to write, while
// the add-key loop walked "all keys that could be written", so the valueless ones were written as
// `review_model = ` (an **empty string**). In TOML that is not "not configured" -- codex sends it
// upstream as the model name. The test that went red was the idempotency one (the second takeover
// sees the empty value and rewrites again), and this one puts the criterion out in the open.
func TestAnEmptyValueIsNotWrittenAtAll(t *testing.T) {
	out, _ := rewrite(sample, 8899, want{Model: "normal"})
	for _, k := range []string{"review_model", "model_context_window", "model_auto_compact_token_limit"} {
		if strings.Contains(out, k) {
			t.Errorf("a key with no value (%s) should not appear in the file\n%s", k, out)
		}
	}
	// And an **existing** line must not be emptied by us either: write only the keys we mean to write.
	const src = "model_auto_compact_token_limit = 12345\nmodel = \"x\"\n"
	kept, _ := rewrite(src, 8899, want{Model: "normal"})
	if !strings.Contains(kept, "model_auto_compact_token_limit = 12345") {
		t.Errorf("the line the user configured themselves must not be touched\n%s", kept)
	}
}

// TestTheCompactLimitComesFromTheProfile: the auto-compact threshold is writable (when available).
//
// It is the counterpart of claude's `CLAUDE_CODE_AUTO_COMPACT_WINDOW`:
// codex estimates when to compact from what it thinks the model metadata
// is, and that metadata does not exist for tier names. Giving 0 is "do not
// declare" -- codex's own default is more right than a number we guessed.
func TestTheCompactLimitComesFromTheProfile(t *testing.T) {
	out, _ := rewrite(sample, 8899, want{Model: "normal", AutoCompact: 800000})
	if !strings.Contains(out, "model_auto_compact_token_limit = 800000") {
		t.Errorf("the threshold should be written in\n%s", out)
	}
	none, _ := rewrite(sample, 8899, want{Model: "normal"})
	if strings.Contains(none, "model_auto_compact_token_limit") {
		t.Errorf("0 means undeclared, so this line should not be written\n%s", none)
	}
}
