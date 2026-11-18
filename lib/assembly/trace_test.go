package assembly

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/testing/testkit"
	modules "github.com/rzbdz/newgate/component"
)

// staticLoader installs nothing: the record's content is irrelevant to this test,
// only that a real assembly happened and produced trace lines.
type staticLoader struct{}

func (staticLoader) Load() ([]modules.Component, error) {
	return []modules.Component{{Name: "only", Type: "test"}}, nil
}

// This family tests the 2026-09-20 change: the assembly
// record is written whole **only when it changed**.
//
// Why it is worth pinning: the test is "is the content the same with timestamps
// and durations removed", and those two things are carried by every line. A looser
// test (say, hashing the raw text) would not error or crash, it would just **never
// match** -- so the block is written every time, the change accomplishes nothing, and
// the log looks perfectly normal. This kind of "the ratchet loosened itself" failure
// is only visible if you really compare two records that differ only in timestamps.

// record drives one assembly record and returns what it wrote out.
func record(t *testing.T, prev string, lines ...string) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	r := newAssemblyRecord(&buf, prev)
	for _, l := range lines {
		// Pass %s, not line as format: records contain literal braces like {name}.
		r.add("%s", l)
	}
	r.flush()
	return buf.String(), assemblyFingerprint(r.plain)
}

// A typical assembly record. Three lines cover the three variable
// pieces: per-component duration, final duration, timestamp.
var (
	assemblyA = []string{
		"assembly: loader 1 handed over 2 components",
		"start 1/2 cli done (took 321µs)",
		"start 2/2 gateway done (took 1ms)",
		"assembly complete: 2 components, took 12ms",
	}
	// Differs from A only in durations: the µs/ms digits changed, still the same two components.
	assemblyA2 = []string{
		"assembly: loader 1 handed over 2 components",
		"start 1/2 cli done (took 999µs)",
		"start 2/2 gateway done (took 8ms)",
		"assembly complete: 2 components, took 55ms",
	}
	// Differs from A in **what was installed**: one more module, and the order changed.
	assemblyB = []string{
		"assembly: loader 1 handed over 3 components",
		"start 1/2 cli done (took 321µs)",
		"start 2/3 gateway done (took 1ms)",
		"assembly complete: 3 components, took 12ms",
	}
)

// The same record **in Chinese** -- copied verbatim from the production log (2026-09-20 15:18),
// not made up: full-width parentheses, the localized "took", and Chinese `，` separators.
//
// Why it is a must: this normalization used to recognize only the English `(took
// …)`, so every translated line carried its duration into the fingerprint, **every
// run was a new fingerprint**, and the whole block was rewritten every time (measured in
// production; the same binary wrote four different fingerprints in a row). And the tests
// then were all in English -- the test picked a language itself, and it happened to be
// the only one that passed. This is the same class of mistake as "the test opens the
// file with O_RDWR itself": the input fed to the code under test was not the real one.
var (
	assemblyZH = []string{
		"assembly: loader 1 handed over 2 components",
		"start 1/2 cli done (took 321µs)",
		"start 2/2 gateway done (took 1ms)",
		"assembly done: 2 components, took 12ms",
	}
	assemblyZH2 = []string{
		"assembly: loader 1 handed over 2 components",
		"start 1/2 cli done (took 999µs)",
		"start 2/2 gateway done (took 8ms)",
		"assembly done: 2 components, took 55ms",
	}
)

func TestFingerprintIgnoresTimeAndDuration(t *testing.T) {
	_, fp1 := record(t, "", assemblyA...)
	_, fp2 := record(t, "", assemblyA2...)
	if fp1 != fp2 {
		t.Errorf("two records differing only in duration must have the same fingerprint -- otherwise the whole record is rewritten every time and this change accomplished nothing\n  %s\n  %s", fp1, fp2)
	}
	_, fp3 := record(t, "", assemblyB...)
	if fp1 == fp3 {
		t.Error("one extra module started but the fingerprint did not change -- then the whole record on the next upgrade will not be written")
	}

	// The same block translated: durations changed, the
	// fingerprint must not (this is what broke in production).
	_, zh1 := record(t, "", assemblyZH...)
	_, zh2 := record(t, "", assemblyZH2...)
	if zh1 != zh2 {
		t.Errorf("in Chinese, two records differing only in duration must have the same fingerprint -- otherwise on a Chinese machine the whole record is rewritten every time\n  %s\n  %s", zh1, zh2)
	}
	// And normalization must really strip the durations, not happen to be equal.
	if strings.Contains(normalizeTraceLine(assemblyZH[1]), "321") ||
		strings.Contains(normalizeTraceLine(assemblyZH[3]), "12ms") {
		t.Errorf("duration was not stripped: %q / %q",
			normalizeTraceLine(assemblyZH[1]), normalizeTraceLine(assemblyZH[3]))
	}
}

func TestFullRecordIsWrittenOnlyWhenItChanges(t *testing.T) {
	// First run: no record in the log yet (prev is empty) -> whole block + marker line.
	out, fp := record(t, "", assemblyA...)
	for _, l := range assemblyA {
		if !strings.Contains(out, l) {
			t.Errorf("the first run should write the whole record, this line is missing: %s", l)
		}
	}
	if !strings.Contains(out, "assembly: fingerprint "+fp[:12]) {
		t.Errorf("the whole record must end with the fingerprint marker, got:\n%s", out)
	}

	// Second run: same content as the first (only durations differ) -> **nothing written**.
	//
	// This used to leave one line, "same as the existing record". That is not a transition,
	// it is echo: one `newgate status` adds a "nothing changed" line to the daemon log
	// (on 2026-09-21 the user's words were "I do not want to see this in the log").
	again, _ := record(t, fp[:12], assemblyA2...)
	if strings.TrimSpace(again) != "" {
		t.Errorf("the second assembly of the same thing should write nothing, got:\n%s", again)
	}
	// And "not writing" must not sabotage the next run: the fingerprint marker is still in
	// the log (the one the first run left), so the third run still recognizes "unchanged".
	// This is the premise of the rule above -- without it, not writing would turn into
	// "rewrite the whole block every run", exactly what this mechanism fixes. (The run below
	// verifies just that: prev is still fp, assembly unchanged -> still nothing written.)
	if third, _ := record(t, fp[:12], assemblyA2...); strings.TrimSpace(third) != "" {
		t.Errorf("after \"unchanged means no write\", the next run should also write nothing (the marker line is still in the log), got:\n%s", third)
	}

	// Third run: assembly changed -> the whole block again.
	changed, _ := record(t, fp[:12], assemblyB...)
	if !strings.Contains(changed, "assembly complete: 3 components, took 12ms") {
		t.Errorf("when the assembly changes the whole record is rewritten, got:\n%s", changed)
	}
}

func TestLastFingerprintReadsTheTailOfTheLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newgate.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o660)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if got := lastFingerprint(f); got != "" {
		t.Errorf("must not find a fingerprint in an empty log, got %q", got)
	}

	// The previous record, then more than 8KB of ordinary log, then a new one. The
	// window only covers the tail: what is found must be the **most recent** one.
	if _, err := f.WriteString("assembly: fingerprint aaaaaaaaaaaa — old\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Repeat("2026/09/20 18:00:00 [proxy] some traffic\n", 300)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("assembly: fingerprint bbbbbbbbbbbb — new\n"); err != nil {
		t.Fatal(err)
	}
	if got := lastFingerprint(f); got != "bbbbbbbbbbbb" {
		t.Errorf("should recover the latest fingerprint bbbbbbbbbbbb, got %q", got)
	}
}

// The window is cut in the middle of the file, so the first segment is
// necessarily half a line -- it holds no marker, but that must not void the whole
// block (real log lines are long; cutting mid-line is the norm, not an edge case).
func TestFingerprintSurvivesAWindowCutMidLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newgate.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o660)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if _, err := f.WriteString(strings.Repeat("x", tailWindow-5)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\nassembly: fingerprint cccccccccccc — after the cut\n"); err != nil {
		t.Fatal(err)
	}
	if got := lastFingerprint(f); got != "cccccccccccc" {
		t.Errorf("a truncated half-line must not invalidate the whole lookback, got %q", got)
	}
}

// TestARepeatedInvocationWritesOneLine is the **real path** test for this
// change: it does not rebuild a record, it **really calls TraceToLogFile
// twice, each assembling the whole graph**, just like the binary does.
//
// Why it is essential: the tests above drive assemblyRecord directly and the test opens the
// file itself -- so nothing asserts which mode the production path opens the log with. That
// is how it slipped through on 2026-09-20: TraceToLogFile opened the file with O_WRONLY, the
// look-back always hit EBADF, every run counted as the first and wrote the whole block (105
// lines each time, exactly as before the change), while all four tests above were green.
func TestARepeatedInvocationWritesNothing(t *testing.T) {
	env := testkit.Sandbox(t)

	// One "invocation" = really assembling the whole graph and finishing, as cmd/newgate does.
	logPath := filepath.Join(env.Home, "newgate.log")
	invoke := func() {
		stop := TraceToLogFile(logPath)
		built, err := modules.New(staticLoader{})
		if err != nil {
			t.Fatalf("assembly failed: %v", err)
		}
		_ = built.Stop(context.Background())
		stop()
	}
	lines := func() int {
		raw, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("reading the log: %v", err)
		}
		return strings.Count(string(raw), "\n")
	}

	invoke()
	first := lines()
	if first < 3 {
		t.Fatalf("the first invocation must write the whole assembly record, got %d lines", first)
	}
	// Second run: **not one extra line**.
	//
	// It used to be "keep just 1 line" (a line saying "same as the existing record"). That line was
	// still echo: one `newgate status` adds a "nothing changed" line to the daemon log, while every
	// line in this log should be **a transition**. On 2026-09-21 the user's words were "I do not want
	// to see this thing show up in the log". (Here it is 0 lines, not 1 -- the first version of the
	// change still asserted 1 line, so the test went red, and it went red exactly where it should.)
	invoke()
	if grew := lines() - first; grew != 0 {
		t.Errorf("the second call for the same assembly must write nothing, but %d extra lines appeared"+
			"lookback did not find the previous record (the log file open mode? how the fingerprint is written?)", grew)
	}
}
