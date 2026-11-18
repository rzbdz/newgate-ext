package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Baseline-carrying writes: **whichever copy you
// read, that is the copy you must still be writing**.
//
// These pin the correctness of "two writers (the command line + the browser) colliding
// on one config". The symptom of breaking it is the hardest kind to trace: the
// user's change **vanishes silently**, no error, no log, only "but I did change it".

func sandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	return dir
}

func TestWriteIfUnchangedAcceptsMatchingBaseline(t *testing.T) {
	dir := sandbox(t)
	path := filepath.Join(dir, "mappings", "x.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("first"), 0o660); err != nil {
		t.Fatal(err)
	}
	base := Revision(path)

	rev, err := WriteIfUnchanged(path, base, []byte("second"))
	if err != nil {
		t.Fatalf("the baseline matched yet the write failed: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "second" {
		t.Errorf("the content was not written: %q", got)
	}
	if rev != Revision(path) {
		t.Errorf("the returned new baseline %q does not match the one on disk (%q)", rev, Revision(path))
	}
}

// TestWriteIfUnchangedRefusesStaleBaseline is the reason this whole
// thing exists: **find the conflict and hand both sides out**. Replying
// with just "it is stale" leaves the user guessing what he lost.
func TestWriteIfUnchangedRefusesStaleBaseline(t *testing.T) {
	dir := sandbox(t)
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("theirs"), 0o660); err != nil {
		t.Fatal(err)
	}

	_, err := WriteIfUnchanged(path, "sha256:0000000000000000000000000000000000000000000000000000000000000000", []byte("mine"))
	if err == nil {
		t.Fatal("the baseline did not match yet the write succeeded -- that is exactly a silent overwrite")
	}
	var stale *StaleError
	if !errors.As(err, &stale) {
		t.Fatalf("should return *StaleError, actual %T: %v", err, err)
	}
	if string(stale.Disk) != "theirs" {
		t.Errorf("on conflict it should carry the current raw text on disk, actual %q", stale.Disk)
	}
	if stale.Current != Revision(path) || stale.Base == stale.Current {
		t.Errorf("both identities should be given separately: base=%q current=%q", stale.Base, stale.Current)
	}
	if got, _ := os.ReadFile(path); string(got) != "theirs" {
		t.Errorf("on conflict **nothing should be written**, actual became %q", got)
	}
	if !errors.Is(err, ErrStale) {
		t.Error("it should be testable with errors.Is(err, ErrStale), otherwise callers can only match on strings")
	}
}

// TestWriteIfUnchangedCreatesWithEmptyBaseline: an empty baseline = "it did not
// exist when I loaded". When the file really does not exist it should pass (creating
// a new file); when it exists it should be blocked (someone got there first).
func TestWriteIfUnchangedCreatesWithEmptyBaseline(t *testing.T) {
	dir := sandbox(t)
	path := filepath.Join(dir, "mappings", "new.json")
	if _, err := WriteIfUnchanged(path, "", []byte("{}")); err != nil {
		t.Fatalf("a nonexistent file should be creatable with an empty baseline: %v", err)
	}
	if _, err := WriteIfUnchanged(path, "", []byte("{}")); err == nil {
		t.Error("the file already exists, so an empty baseline should count as a conflict (someone created it first)")
	}
}

// TestConcurrentWritersCannotBothWin is the race-protection
// point for "two front ends running at once".
//
// Without the lock: two writers each read the old content, each pass
// the baseline comparison, and the later rename overwrites the earlier
// change -- while **both received success**. Only meaningful under -race.
func TestConcurrentWritersCannotBothWin(t *testing.T) {
	dir := sandbox(t)
	path := filepath.Join(dir, "mappings", "x.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("v0"), 0o660); err != nil {
		t.Fatal(err)
	}
	base := Revision(path)

	const writers = 8
	var wg sync.WaitGroup
	ok := make([]bool, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := WriteIfUnchanged(path, base, []byte("v-"+string(rune('a'+i))))
			ok[i] = err == nil
		}(i)
	}
	wg.Wait()

	wins := 0
	for _, w := range ok {
		if w {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d writers committed with the same baseline at once, %d succeeded -- exactly one must (the rest should get a conflict)", writers, wins)
	}
}

// TestWriteLeavesNoTempFiles: temporary files are randomly named (not a fixed .tmp), so concurrent
// writers do not collide on the same inode; and after writing it must be cleaned up -- leftover
// garbage would make the next `git status` or config directory audit look like something is broken.
func TestWriteLeavesNoTempFiles(t *testing.T) {
	dir := sandbox(t)
	path := filepath.Join(dir, "x.json")
	if _, err := WriteIfUnchanged(path, "", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("the write left a temp file behind: %s", e.Name())
		}
	}
}

// TestRevisionOfEmptyAndMissing: a missing file and an empty file are **two different
// identities** -- the former is "not there yet", the latter is "there is an empty file".
// Conflate them and "delete it" and "empty it" become indistinguishable in conflict detection.
func TestRevisionDistinguishesMissingFromEmpty(t *testing.T) {
	dir := sandbox(t)
	missing := filepath.Join(dir, "nope.json")
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, nil, 0o660); err != nil {
		t.Fatal(err)
	}
	if Revision(missing) == Revision(empty) {
		t.Error("a nonexistent file and an empty file must not have the same baseline")
	}
	if Revision(missing) != "" {
		t.Errorf("the baseline of a nonexistent file should be an empty string, actual %q", Revision(missing))
	}
}
