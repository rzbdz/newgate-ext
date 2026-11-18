package logx

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// mkGroup 造一组 dump 文件：base + 四个后缀，mtime 一律钉到 at。
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

// touch 造一个单文件，mtime 钉到 at。
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

// groupAlive：这组的四个文件是不是一个不差地都还在。
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

// TestPruneDirByKeepsNewestGroup：keep=1 时按 mtime 留最新那组，其余整组删掉。
func TestPruneDirByKeepsNewestGroup(t *testing.T) {
	dir := t.TempDir()
	mkGroup(t, dir, "err-402-req000001", epoch.Add(1*time.Minute))
	mkGroup(t, dir, "err-422-req000002", epoch.Add(2*time.Minute))
	mkGroup(t, dir, "err-502-req000003", epoch.Add(3*time.Minute))

	PruneDirBy(dir, "err-", 1)

	if !groupAlive(t, dir, "err-502-req000003") {
		t.Errorf("最新的 err-502-req000003 该留下，实际剩 %v", listFiles(t, dir))
	}
	for _, base := range []string{"err-402-req000001", "err-422-req000002"} {
		if groupAlive(t, dir, base) {
			t.Errorf("%s 比留下的那组旧，该被删掉", base)
		}
	}
}

// TestPruneDirByKeepsNewestEvenIfNameSortsFirst：回归点。名字字典序最小
// （err-400 < 其余状态码，也 < 同组 req 号更大的）但 mtime 最新的一组必须
// 活下来——老实现留「名字最大」的，正好把它第一个删掉。
func TestPruneDirByKeepsNewestEvenIfNameSortsFirst(t *testing.T) {
	dir := t.TempDir()
	mkGroup(t, dir, "err-402-req000001", epoch.Add(1*time.Minute)) // 最旧
	mkGroup(t, dir, "err-422-req000002", epoch.Add(2*time.Minute))
	mkGroup(t, dir, "err-429-req000003", epoch.Add(3*time.Minute))
	mkGroup(t, dir, "err-400-req001840", epoch.Add(4*time.Minute)) // 名字最小、最新

	PruneDirBy(dir, "err-", 2)

	if !groupAlive(t, dir, "err-400-req001840") {
		t.Fatalf("字典序最小但最新的 err-400-req001840 被删了，剩 %v", listFiles(t, dir))
	}
	if !groupAlive(t, dir, "err-429-req000003") {
		t.Errorf("第二新的 err-429-req000003 该留下，剩 %v", listFiles(t, dir))
	}
	for _, base := range []string{"err-402-req000001", "err-422-req000002"} {
		if groupAlive(t, dir, base) {
			t.Errorf("%s 是最旧两组，该被删掉", base)
		}
	}
}

// TestPruneDirByGroupAgeIsNewestFileInGroup：组的年龄取组内最新那份文件的
// mtime——只补写其中一份，这组就该被当成「新」保住。
func TestPruneDirByGroupAgeIsNewestFileInGroup(t *testing.T) {
	dir := t.TempDir()
	mkGroup(t, dir, "err-402-req000001", epoch.Add(1*time.Minute))
	mkGroup(t, dir, "err-422-req000002", epoch.Add(2*time.Minute))
	// 这组整体最旧，但 meta 是被刚刚补写的：组年龄 = 最新那份 = 现在。
	mkGroup(t, dir, "err-502-req000003", epoch.Add(1*time.Minute))
	late := epoch.Add(9 * time.Minute)
	if err := os.Chtimes(filepath.Join(dir, "err-502-req000003.meta.txt"), late, late); err != nil {
		t.Fatal(err)
	}

	PruneDirBy(dir, "err-", 1)

	if !groupAlive(t, dir, "err-502-req000003") {
		t.Errorf("err-502-req000003 的组内最新文件是刚写的，该留下，剩 %v", listFiles(t, dir))
	}
}

// TestPruneDirBySameMtimeTiebreakByName：mtime 相同时按名字升序留——结果确定，
// 不靠 ReadDir 顺序。
func TestPruneDirBySameMtimeTiebreakByName(t *testing.T) {
	dir := t.TempDir()
	same := epoch.Add(5 * time.Minute)
	mkGroup(t, dir, "err-400-req000002", same)
	mkGroup(t, dir, "err-400-req000001", same)

	PruneDirBy(dir, "err-", 1)

	if !groupAlive(t, dir, "err-400-req000001") {
		t.Errorf("同 mtime 该按名字升序留 err-400-req000001，剩 %v", listFiles(t, dir))
	}
	if groupAlive(t, dir, "err-400-req000002") {
		t.Error("同 mtime 时名字更大的 err-400-req000002 该被删掉")
	}
}

// TestPruneDirByIgnoresOtherPrefixes：只动 prefix 命中的组，req-* 等其他前缀
// 的文件一律不碰（dump 目录里 req-* 与 err-* 共用）。
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
			t.Errorf("非 err- 前缀的 %s 不该被删: %v", want, err)
		}
	}
}

// TestPruneDirByNoopWhenAtOrBelowKeep：组数 ≤ keep 时一个文件都不删。
func TestPruneDirByNoopWhenAtOrBelowKeep(t *testing.T) {
	dir := t.TempDir()
	mkGroup(t, dir, "err-402-req000001", epoch.Add(1*time.Minute))
	mkGroup(t, dir, "err-422-req000002", epoch.Add(2*time.Minute))

	before := listFiles(t, dir)
	PruneDirBy(dir, "err-", 2)
	duringLen := len(listFiles(t, dir))
	if duringLen != len(before) {
		t.Fatalf("keep=2、只有 2 组时不该删，文件数 %d → %d", len(before), duringLen)
	}

	PruneDirBy(dir, "err-", 5)
	after := listFiles(t, dir)
	if len(after) != len(before) {
		t.Fatalf("keep=5 时不该删，文件数 %d → %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("文件清单变了：%v → %v", before, after)
		}
	}
}

// TestPruneDirByIgnoresSubdirectories：子目录既不参与分组计数，也不被删——
// 哪怕它的名字恰好落在某个被清理的 base 前缀下。
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

	// keep=2：err-402-req000001 这组会被清，同名的子目录必须原样在。
	PruneDirBy(dir, "err-", 2)

	if fi, err := os.Stat(sub); err != nil || !fi.IsDir() {
		t.Fatalf("子目录 %s 被动了: %v", sub, err)
	}
	if _, err := os.Stat(inside); err != nil {
		t.Errorf("子目录里的文件不该被删: %v", err)
	}
	if groupAlive(t, dir, "err-402-req000001") {
		t.Error("err-402-req000001 的普通文件该被删（被删的只是文件，不含同名子目录）")
	}
}
