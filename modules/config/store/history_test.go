package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
)

// Keep a copy of "the previous version" before writing.
//
// These pin **being able to go back after breaking something once**: the
// scene on 2026-09-20 was a front-end bug writing away the tiers of one of the user's
// profiles, when nowhere could retrieve the copy from before the write (`backups/` is
// for takeover, and the config directory is not in git). The criterion is asymmetric
// cost -- a backup is a few KB, losing one is the config in the user's hands.

// TestAWriteKeepsThePreviousVersion: after one write,
// **the previous version** can be read back from history.
func TestAWriteKeepsThePreviousVersion(t *testing.T) {
	dir := sandbox(t)
	path := filepath.Join(dir, "mappings", "x.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("before"), 0o660); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteIfUnchanged(path, Revision(path), []byte("after")); err != nil {
		t.Fatal(err)
	}

	// On disk is the new one.
	b, _ := os.ReadFile(path)
	if string(b) != "after" {
		t.Fatalf("the write did not take effect: %q", b)
	}
	// In history is the old one.
	hist := HistoryEntries(path)
	if len(hist) != 1 {
		t.Fatalf("one version of history should be kept, actual %d", len(hist))
	}
	old, err := os.ReadFile(hist[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(old) != "before" {
		t.Errorf("the history should hold the version from before the write, actual %q", old)
	}
}

// TestTheHistoryIsARing: keep only the most recent 20 versions, it cannot grow forever.
//
// The config directory is written over and over (every save click in the UI is
// one), and keeping everything amounts to inflating it into a log that only grows.
func TestTheHistoryIsARing(t *testing.T) {
	dir := sandbox(t)
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("v0"), 0o660); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= historyKeep+5; i++ {
		if _, err := WriteIfUnchanged(path, Revision(path), []byte(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	hist := HistoryEntries(path)
	if len(hist) != historyKeep {
		t.Fatalf("only %d versions should be kept, actual %d", historyKeep, len(hist))
	}
	// What is kept must be **the most recent** 20 versions: the oldest one has v5 (v0..v4 were
	// squeezed out), and the newest has v24 (= the content before the second-to-last write).
	oldest, err := os.ReadFile(hist[len(hist)-1])
	if err != nil {
		t.Fatal(err)
	}
	if string(oldest) != fmt.Sprintf("v%d", 5) {
		t.Errorf("the evicted ones should be the oldest, actual oldest is %q", oldest)
	}
	newest, err := os.ReadFile(hist[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(newest) != fmt.Sprintf("v%d", historyKeep+4) {
		t.Errorf("the newest version should be the content before the last write, actual %q", newest)
	}
}

// TestCreatingAFileLeavesNoHistory: creating (nothing was on disk) has nothing to keep -- keeping
// an empty version would only make whoever browses history think the file was empty at that moment.
func TestCreatingAFileLeavesNoHistory(t *testing.T) {
	dir := sandbox(t)
	path := filepath.Join(dir, "mappings", "fresh.kv")
	if _, err := WriteIfUnchanged(path, "", []byte("desc=new\n")); err != nil {
		t.Fatal(err)
	}
	if hist := HistoryEntries(path); len(hist) != 0 {
		t.Errorf("creating a new file should not leave history, actual %d", len(hist))
	}
}

// TestHistoryIsPerFile: history is separated per file. Sharing one pot, rolling back
// one would show someone else's content, which is more dangerous than no backup at all.
func TestHistoryIsPerFile(t *testing.T) {
	dir := sandbox(t)
	a := filepath.Join(dir, "mappings", "a.kv")
	b := filepath.Join(dir, "mappings", "b.kv")
	if err := os.MkdirAll(filepath.Dir(a), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("old-"+filepath.Base(p)), 0o660); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := WriteIfUnchanged(a, Revision(a), []byte("new-a")); err != nil {
		t.Fatal(err)
	}
	if hist := HistoryEntries(b); len(hist) != 0 {
		t.Errorf("a file never written should have no history, actual %d", len(hist))
	}
	hist := HistoryEntries(a)
	if len(hist) != 1 {
		t.Fatalf("the one that was written should have one version, actual %d", len(hist))
	}
	got, _ := os.ReadFile(hist[0])
	if string(got) != "old-a.kv" {
		t.Errorf("the history got someone else's content: %q", got)
	}
}

// TestEverythingThatWritesGoesThroughTheSamePath: **every** disk
// write goes through the same path (atomic replace + backup ring).
//
// Why this deserves a test: this path has had two real leaks -- `writeJSON` assembling
// `path + ".tmp"` itself (**a fixed name**: two concurrent writers collide on the same
// inode, and one rename carries the half-written content of the other), and `newgate profile
// kv --write` calling os.WriteFile directly (the process dying halfway leaves a truncated
// config, and that version has not even a backup). Two implementations coexisting only makes
// one of them rot slowly -- so here the criterion is pinned by **behavior**: whichever entry
// point, after writing the previous version is there, and no temporary file is left behind.
func TestEverythingThatWritesGoesThroughTheSamePath(t *testing.T) {
	dir := sandbox(t)
	if err := os.MkdirAll(filepath.Join(dir, "mappings"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 1. store.Write: the one that does not go through CAS (`newgate profile kv --write` uses it).
	kv := filepath.Join(dir, "mappings", "x.kv")
	if err := Write(kv, []byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if err := Write(kv, []byte("second\n")); err != nil {
		t.Fatal(err)
	}
	ents := HistoryEntries(kv)
	if len(ents) != 1 {
		t.Fatalf("writing twice should keep only one version of history (the first was a create, there is no previous one), actual %d", len(ents))
	}
	if b, _ := os.ReadFile(ents[0]); string(b) != "first\n" {
		t.Errorf("the one in history is not the previous version: %q", b)
	}

	// 2. SaveProfile: the JSON one (the one that used to assemble a fixed temporary name itself).
	empty := map[string]domain.Candidates{}
	for _, body := range []string{"one", "two"} {
		if err := SaveProfile(&domain.Profile{Name: "y", Description: body, Roles: empty}); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(HistoryEntries(filepath.Join(dir, "mappings", "y.json"))); got != 1 {
		t.Errorf("the JSON write path left no history (it is the one that built its own temp name back then): %d", got)
	}

	// 3. No temporary files left: `mappings/` should only have the two we recognize.
	names, err := os.ReadDir(filepath.Join(dir, "mappings"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range names {
		if e.Name() != "x.kv" && e.Name() != "y.json" {
			t.Errorf("after the write there is an extra file %q -- the temp file should disappear on rename", e.Name())
		}
	}
}

// TestHistoryIsNewestFirst: `HistoryEntries` must be **newest to oldest**.
//
// The numbering at the command layer (`#1` = the most recent
// version, and no version number takes that one) is built directly on this, and it is implemented
// with `sort.Reverse(sort.StringSlice(...))` -- **lexicographic** order. Today names are 19-digit
// UnixNano, and for equal-length numbers lexicographic order happens to equal numeric order, so
// it is coincidentally right; the day the name format changes (zero padding, a prefix, a different
// monotonic quantity), this **silently** flips, and the symptom is "default retrieves the oldest
// version" -- a direction error only discovered when someone is at his most panicked. So pin it.
func TestHistoryIsNewestFirst(t *testing.T) {
	dir := sandbox(t)
	path := filepath.Join(dir, "mappings", "x.kv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"v1", "v2", "v3"} {
		if err := Write(path, []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	ents := HistoryEntries(path)
	if len(ents) != 2 {
		t.Fatalf("writing three times should keep two versions (the first is a create, no previous one), actual %d", len(ents))
	}
	// ents[0] should be the **newer** one, i.e. v2 (v3 is still on disk and did not enter the ring).
	if b, _ := os.ReadFile(ents[0]); string(b) != "v2" {
		t.Errorf("the first entry should be the newer version v2, actual %q", b)
	}
	if b, _ := os.ReadFile(ents[1]); string(b) != "v1" {
		t.Errorf("the second entry should be the older version v1, actual %q", b)
	}
}

// TestRestoringIsItselfUndoable: after retrieving a
// version, **the one pushed out is in the ring too**.
//
// This is not a flourish: every use case of restore is "I am panicking", and an irreversible
// "undo" is one nobody dares press while panicking. `cmdProfileRestore` goes through
// store.Write (the same path as other writes), so this property **comes from the write path**,
// not from something that command does extra -- and the test watches exactly that fact.
func TestRestoringIsItselfUndoable(t *testing.T) {
	dir := sandbox(t)
	path := filepath.Join(dir, "mappings", "x.kv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("v2")); err != nil {
		t.Fatal(err)
	}

	// "Retrieve v1" = write that version in the ring back (that is what the command does).
	ents := HistoryEntries(path)
	if len(ents) != 1 {
		t.Fatalf("precondition: writing twice should give one version of history, actual %d", len(ents))
	}
	old, _ := os.ReadFile(ents[0])
	if err := Write(path, old); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "v1" {
		t.Errorf("after restoring, the disk should hold v1, actual %q", got)
	}

	// The pushed-out v2 is retrievable too now -- restore is an ordinary write.
	ents = HistoryEntries(path)
	if len(ents) != 2 {
		t.Fatalf("restoring is itself a write, so the ring should hold two versions, actual %d", len(ents))
	}
	if b, _ := os.ReadFile(ents[0]); string(b) != "v2" {
		t.Errorf("the most recent version should be the displaced v2, actual %q", b)
	}
}
