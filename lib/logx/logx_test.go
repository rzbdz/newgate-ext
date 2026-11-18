package logx

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// mkGroup builds a set of dump files: base + four suffixes, all pinned to mtime at.
func mkGroup(t *testing.T, dir, base string, at time.Time) {
	t.Helper()
	for _, suffix := range []string{".client-sent.json", ".we-sent.json", ".upstream-said.json", ".meta.txt"} {
		p := filepath.Join(dir, base+suffix)
		if err := os.WriteFile(p, []byte(base), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
}

// touch creates a single file with its mtime pinned to at.
func touch(t *testing.T, dir, name string, at time.Time) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(name), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// groupAlive: whether all four files of the group are still there, not one missing.
func groupAlive(t *testing.T, dir, base string) bool {
	t.Helper()
	for _, suffix := range []string{".client-sent.json", ".we-sent.json", ".upstream-said.json", ".meta.txt"} {
		if _, err := os.Stat(filepath.Join(dir, base+suffix)); err != nil {
			return false
		}
	}
	return true
}

var epoch = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

// TestPruneDirByKeepsNewestGroup: with keep=1, keep the newest group by mtime, delete the rest.
func TestPruneDirByKeepsNewestGroup(t *testing.T) {
	dir := t.TempDir()
	mkGroup(t, dir, "err-402-req000001", epoch.Add(1*time.Minute))
	mkGroup(t, dir, "err-422-req000002", epoch.Add(2*time.Minute))
	mkGroup(t, dir, "err-502-req000003", epoch.Add(3*time.Minute))

	PruneDirBy(dir, "err-", 1)

	if !groupAlive(t, dir, "err-502-req000003") {
		t.Errorf("the newest err-502-req000003 should be kept, but %v are left", listFiles(t, dir))
	}
	for _, base := range []string{"err-402-req000001", "err-422-req000002"} {
		if groupAlive(t, dir, base) {
			t.Errorf("expected %s to be deleted: older than the group that was kept", base)
		}
	}
}

// TestPruneDirByKeepsNewestEvenIfNameSortsFirst: the regression point. The
// group with the lexicographically smallest name (err-400 < the other status
// codes, and < a larger req number) but the newest mtime must survive -- the
// old implementation kept the "largest name", which deleted exactly it first.
func TestPruneDirByKeepsNewestEvenIfNameSortsFirst(t *testing.T) {
	dir := t.TempDir()
	mkGroup(t, dir, "err-402-req000001", epoch.Add(1*time.Minute)) // oldest
	mkGroup(t, dir, "err-422-req000002", epoch.Add(2*time.Minute))
	mkGroup(t, dir, "err-429-req000003", epoch.Add(3*time.Minute))
	mkGroup(t, dir, "err-400-req001840", epoch.Add(4*time.Minute)) // smallest name, newest

	PruneDirBy(dir, "err-", 2)

	if !groupAlive(t, dir, "err-400-req001840") {
		t.Fatalf("err-400-req001840 sorts first but is the newest, yet it was deleted; left %v", listFiles(t, dir))
	}
	if !groupAlive(t, dir, "err-429-req000003") {
		t.Errorf("the second newest err-429-req000003 should be kept, left %v", listFiles(t, dir))
	}
	for _, base := range []string{"err-402-req000001", "err-422-req000002"} {
		if groupAlive(t, dir, base) {
			t.Errorf("%s is among the two oldest groups, expected to be deleted", base)
		}
	}
}

// TestPruneDirByGroupAgeIsNewestFileInGroup: a group's age is the mtime of its
// newest file -- touch just one of them and the group must be kept as "new".
func TestPruneDirByGroupAgeIsNewestFileInGroup(t *testing.T) {
	dir := t.TempDir()
	mkGroup(t, dir, "err-402-req000001", epoch.Add(1*time.Minute))
	mkGroup(t, dir, "err-422-req000002", epoch.Add(2*time.Minute))
	// This group is the oldest overall, but meta was just rewritten: group age = newest file = now.
	mkGroup(t, dir, "err-502-req000003", epoch.Add(1*time.Minute))
	late := epoch.Add(9 * time.Minute)
	if err := os.Chtimes(filepath.Join(dir, "err-502-req000003.meta.txt"), late, late); err != nil {
		t.Fatal(err)
	}

	PruneDirBy(dir, "err-", 1)

	if !groupAlive(t, dir, "err-502-req000003") {
		t.Errorf("the newest file in the err-502-req000003 group was just written, so it should be kept; left %v", listFiles(t, dir))
	}
}

// TestPruneDirBySameMtimeTiebreakByName: with equal mtime, keep by name
// ascending -- a deterministic result that does not depend on ReadDir order.
func TestPruneDirBySameMtimeTiebreakByName(t *testing.T) {
	dir := t.TempDir()
	same := epoch.Add(5 * time.Minute)
	mkGroup(t, dir, "err-400-req000002", same)
	mkGroup(t, dir, "err-400-req000001", same)

	PruneDirBy(dir, "err-", 1)

	if !groupAlive(t, dir, "err-400-req000001") {
		t.Errorf("with equal mtime keep err-400-req000001 by ascending name, left %v", listFiles(t, dir))
	}
	if groupAlive(t, dir, "err-400-req000002") {
		t.Error("with equal mtime the larger name err-400-req000002 should be deleted")
	}
}

// TestPruneDirByIgnoresOtherPrefixes: only groups matching prefix are touched; files with
// other prefixes such as req-* are never touched (req-* and err-* share the dump dir).
func TestPruneDirByIgnoresOtherPrefixes(t *testing.T) {
	dir := t.TempDir()
	mkGroup(t, dir, "err-402-req000001", epoch.Add(1*time.Minute))
	mkGroup(t, dir, "err-422-req000002", epoch.Add(2*time.Minute))
	mkGroup(t, dir, "req-000009", epoch.Add(1*time.Minute))
	touch(t, dir, "notes.txt", epoch.Add(1*time.Minute))

	PruneDirBy(dir, "err-", 1)

	for _, want := range []string{
		"req-000009.client-sent.json", "req-000009.we-sent.json",
		"req-000009.upstream-said.json", "req-000009.meta.txt", "notes.txt",
	} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("a file without the err- prefix, %s, must not be deleted: %v", want, err)
		}
	}
}

// TestPruneDirByNoopWhenAtOrBelowKeep: when the group count is <= keep, not one file is deleted.
func TestPruneDirByNoopWhenAtOrBelowKeep(t *testing.T) {
	dir := t.TempDir()
	mkGroup(t, dir, "err-402-req000001", epoch.Add(1*time.Minute))
	mkGroup(t, dir, "err-422-req000002", epoch.Add(2*time.Minute))

	before := listFiles(t, dir)
	PruneDirBy(dir, "err-", 2)
	duringLen := len(listFiles(t, dir))
	if duringLen != len(before) {
		t.Fatalf("keep=2 with only 2 groups must not delete; file count %d -> %d", len(before), duringLen)
	}

	PruneDirBy(dir, "err-", 5)
	after := listFiles(t, dir)
	if len(after) != len(before) {
		t.Fatalf("keep=5 must not delete; file count %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("the file list changed: %v -> %v", before, after)
		}
	}
}

// TestPruneDirByIgnoresSubdirectories: subdirectories neither count toward grouping
// nor get deleted -- even if their name happens to fall under a pruned base prefix.
func TestPruneDirByIgnoresSubdirectories(t *testing.T) {
	dir := t.TempDir()
	mkGroup(t, dir, "err-402-req000001", epoch.Add(1*time.Minute))
	mkGroup(t, dir, "err-422-req000002", epoch.Add(2*time.Minute))
	mkGroup(t, dir, "err-502-req000003", epoch.Add(3*time.Minute))
	sub := filepath.Join(dir, "err-402-req000001.notes")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(sub, "scratch.txt")
	if err := os.WriteFile(inside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// keep=2: the err-402-req000001 group is pruned, and the same-named subdirectory must remain.
	PruneDirBy(dir, "err-", 2)

	if fi, err := os.Stat(sub); err != nil || !fi.IsDir() {
		t.Fatalf("subdirectory %s was touched: %v", sub, err)
	}
	if _, err := os.Stat(inside); err != nil {
		t.Errorf("files inside the subdirectory must not be deleted: %v", err)
	}
	if groupAlive(t, dir, "err-402-req000001") {
		t.Error("the regular file err-402-req000001 should be deleted (only files are deleted, not the same-named subdirectory)")
	}
}
