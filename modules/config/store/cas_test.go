package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 带基线的写：**读的时候是哪一份，写的时候就还得是哪一份**。
//
// 这几条锁的是「两个写者（命令行 + 浏览器）撞在同一份配置上」那件事的正确性。
// 破了它的症状是最难查的一类：用户的改动**静默消失**，没有报错、没有日志，
// 只有「我明明改过啊」。

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
		t.Fatalf("基线对得上却写失败了: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "second" {
		t.Errorf("内容没写进去: %q", got)
	}
	if rev != Revision(path) {
		t.Errorf("返回的新基线 %q 与磁盘上的对不上（%q）", rev, Revision(path))
	}
}

// TestWriteIfUnchangedRefusesStaleBaseline 是这套东西存在的理由：**发现冲突并
// 把两边都交出来**。只回一句「过期了」等于让用户去猜自己丢了什么。
func TestWriteIfUnchangedRefusesStaleBaseline(t *testing.T) {
	dir := sandbox(t)
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("theirs"), 0o660); err != nil {
		t.Fatal(err)
	}

	_, err := WriteIfUnchanged(path, "sha256:0000000000000000000000000000000000000000000000000000000000000000", []byte("mine"))
	if err == nil {
		t.Fatal("基线对不上却写成功了——那正是静默覆盖")
	}
	var stale *StaleError
	if !errors.As(err, &stale) {
		t.Fatalf("该回 *StaleError，实际 %T: %v", err, err)
	}
	if string(stale.Disk) != "theirs" {
		t.Errorf("冲突时该带上磁盘现状原文，实际 %q", stale.Disk)
	}
	if stale.Current != Revision(path) || stale.Base == stale.Current {
		t.Errorf("两边的身份该分别给出: base=%q current=%q", stale.Base, stale.Current)
	}
	if got, _ := os.ReadFile(path); string(got) != "theirs" {
		t.Errorf("冲突时**什么都不该写**，实际变成了 %q", got)
	}
	if !errors.Is(err, ErrStale) {
		t.Error("该能用 errors.Is(err, ErrStale) 判，不然调用方只能靠字符串")
	}
}

// TestWriteIfUnchangedCreatesWithEmptyBaseline：空基线 = 「我加载时它还不存在」。
// 文件真不存在时该放行（建新文件），存在时该拦下（有人抢先建了）。
func TestWriteIfUnchangedCreatesWithEmptyBaseline(t *testing.T) {
	dir := sandbox(t)
	path := filepath.Join(dir, "mappings", "new.json")
	if _, err := WriteIfUnchanged(path, "", []byte("{}")); err != nil {
		t.Fatalf("空基线上不存在的文件该能建: %v", err)
	}
	if _, err := WriteIfUnchanged(path, "", []byte("{}")); err == nil {
		t.Error("文件已经存在了，空基线该算冲突（有人抢先建了它）")
	}
}

// TestConcurrentWritersCannotBothWin 是「两个前端同时跑」那条 race 保护点。
//
// 没有锁的话：两个写者各自读到旧内容、各自通过基线比对、后一个 rename 把前一个
// 的改动盖掉——而**两边都收到了成功**。跑 -race 才有意义。
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
		t.Fatalf("%d 个写者同时用同一条基线提交，成功了 %d 个——必须恰好一个（其余的该拿到冲突）", writers, wins)
	}
}

// TestWriteLeavesNoTempFiles：临时文件是随机名（不是固定的 .tmp），所以并发写者
// 不会撞在同一个 inode 上；而且写完必须清干净——留下的垃圾会让下一次 `git status`
// 或配置目录审计看起来像有东西坏了。
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
			t.Errorf("写完留下了临时文件 %s", e.Name())
		}
	}
}

// TestRevisionOfEmptyAndMissing；文件不存在与空文件是**两种不同的身份**——
// 前者是「还没有」，后者是「有一个空文件」。混为一谈的话，「删掉它」与「清空它」
// 在冲突检测里就分不出来了。
func TestRevisionDistinguishesMissingFromEmpty(t *testing.T) {
	dir := sandbox(t)
	missing := filepath.Join(dir, "nope.json")
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, nil, 0o660); err != nil {
		t.Fatal(err)
	}
	if Revision(missing) == Revision(empty) {
		t.Error("不存在的文件与空文件的基线不该相同")
	}
	if Revision(missing) != "" {
		t.Errorf("不存在的文件基线该是空串，实际 %q", Revision(missing))
	}
}
