package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
)

// 写盘之前留一份「上一版」。
//
// 这几条锁的是**一次改坏之后还能翻回去**：2026-09-20 现场是一个前端 bug 把用户
// 某个 profile 的档位写没了，而当时没有任何地方能找回写之前那一份（`backups/`
// 是接管用的、配置目录不在 git 里）。判据是代价不对称——留备份是几 KB，丢一次
// 是用户手上的配置。

// TestAWriteKeepsThePreviousVersion：写一次之后，**上一版**能从历史里读回来。
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

	// 盘上是新的。
	b, _ := os.ReadFile(path)
	if string(b) != "after" {
		t.Fatalf("写入没生效: %q", b)
	}
	// 历史里是旧的。
	hist := HistoryEntries(path)
	if len(hist) != 1 {
		t.Fatalf("该留一版历史，实际 %d 版", len(hist))
	}
	old, err := os.ReadFile(hist[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(old) != "before" {
		t.Errorf("历史里该是写之前那份，实际 %q", old)
	}
}

// TestTheHistoryIsARing：只留最近 20 版，不能无限长。
//
// 配置目录会被反复写（界面上每点一次保存就是一次），留全量等于把配置目录撑成
// 一个只涨不消的日志。
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
		t.Fatalf("该只留 %d 版，实际 %d 版", historyKeep, len(hist))
	}
	// 留下的必须是**最近**的那 20 版：最旧的那一版里是 v5（v0..v4 已被挤掉），
	// 最新那一版里是 v24（= 倒数第二次写之前的内容）。
	oldest, err := os.ReadFile(hist[len(hist)-1])
	if err != nil {
		t.Fatal(err)
	}
	if string(oldest) != fmt.Sprintf("v%d", 5) {
		t.Errorf("挤掉的该是最旧的几版，实际最旧一版是 %q", oldest)
	}
	newest, err := os.ReadFile(hist[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(newest) != fmt.Sprintf("v%d", historyKeep+4) {
		t.Errorf("最新一版该是最后一次写之前的内容，实际 %q", newest)
	}
}

// TestCreatingAFileLeavesNoHistory：新建（盘上本来没有）没什么可留的——留一个空
// 版本只会让「翻历史」的人以为那一刻文件是空的。
func TestCreatingAFileLeavesNoHistory(t *testing.T) {
	dir := sandbox(t)
	path := filepath.Join(dir, "mappings", "fresh.kv")
	if _, err := WriteIfUnchanged(path, "", []byte("desc=new\n")); err != nil {
		t.Fatal(err)
	}
	if hist := HistoryEntries(path); len(hist) != 0 {
		t.Errorf("新建一份文件不该留下历史，实际 %d 版", len(hist))
	}
}

// TestHistoryIsPerFile：历史按文件分家。共用一锅的话，回滚某一份时会看到别人的
// 内容，而那比没有备份更危险。
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
		t.Errorf("没写过的文件不该有历史，实际 %d 版", len(hist))
	}
	hist := HistoryEntries(a)
	if len(hist) != 1 {
		t.Fatalf("写过的那个该有一版，实际 %d", len(hist))
	}
	got, _ := os.ReadFile(hist[0])
	if string(got) != "old-a.kv" {
		t.Errorf("历史串了别人的内容: %q", got)
	}
}

// TestEverythingThatWritesGoesThroughTheSamePath：**所有**落盘都走同一条路
// （原子替换 + 备份环）。
//
// 为什么值得一条测试：这条路上有两个真实存在过的漏口——`writeJSON` 自己拼
// `path + ".tmp"`（**固定名**：两个并发写者撞在同一个 inode 上，一个 rename 走的
// 是对方写了一半的内容），以及 `newgate profile kv --write` 直接 os.WriteFile
// （进程死在半路留下一份被截断的配置，而那一版连备份都没有）。两份实现并存只会让
// 其中一份慢慢腐坏——所以这里按**行为**钉：不管是哪个入口，写完之后上一版都在，
// 而且不留临时文件。
func TestEverythingThatWritesGoesThroughTheSamePath(t *testing.T) {
	dir := sandbox(t)
	if err := os.MkdirAll(filepath.Join(dir, "mappings"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 1. store.Write：不走 CAS 的那种（`newgate profile kv --write` 用的就是它）。
	kv := filepath.Join(dir, "mappings", "x.kv")
	if err := Write(kv, []byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if err := Write(kv, []byte("second\n")); err != nil {
		t.Fatal(err)
	}
	ents := HistoryEntries(kv)
	if len(ents) != 1 {
		t.Fatalf("写两次该只留一版历史（第一次是新建，没有上一版），实际 %d 版", len(ents))
	}
	if b, _ := os.ReadFile(ents[0]); string(b) != "first\n" {
		t.Errorf("历史里那份不是上一版: %q", b)
	}

	// 2. SaveProfile：JSON 那条（曾经自己拼固定临时名的那条）。
	empty := map[string]domain.Candidates{}
	for _, body := range []string{"一", "二"} {
		if err := SaveProfile(&domain.Profile{Name: "y", Description: body, Roles: empty}); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(HistoryEntries(filepath.Join(dir, "mappings", "y.json"))); got != 1 {
		t.Errorf("JSON 那条写路径没留历史（它就是当时自己拼临时名的那条）: %d 版", got)
	}

	// 3. 不留临时文件：`mappings/` 里只该有我们认识的那两个。
	names, err := os.ReadDir(filepath.Join(dir, "mappings"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range names {
		if e.Name() != "x.kv" && e.Name() != "y.json" {
			t.Errorf("写完之后多了一个文件 %q——临时文件该在 rename 时消失", e.Name())
		}
	}
}

// TestHistoryIsNewestFirst：`HistoryEntries` 必须**从新到旧**。
//
// 命令那一层的编号（`#1` = 最近那一版、不带版本号就取它）直接建立在这条上，而它
// 是靠 `sort.Reverse(sort.StringSlice(...))` 实现的——**字典序**。今天名字是 19 位
// 的 UnixNano，等长数字的字典序恰好等于数值序，所以它碰巧是对的；哪天名字格式一改
// （补零、加前缀、换成别的单调量），这条会**静默**反过来，而症状是「default 取回
// 的是最老的那一版」——一个只在人最慌的时候才被发现的方向错。所以钉住它。
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
		t.Fatalf("写三次该留两版（第一次是新建，没有上一版），实际 %d", len(ents))
	}
	// ents[0] 该是**较新**的那一版，也就是 v2（v3 还在盘上，没进环）。
	if b, _ := os.ReadFile(ents[0]); string(b) != "v2" {
		t.Errorf("第一项该是较新那版 v2，实际 %q", b)
	}
	if b, _ := os.ReadFile(ents[1]); string(b) != "v1" {
		t.Errorf("第二项该是较老那版 v1，实际 %q", b)
	}
}

// TestRestoringIsItselfUndoable：取回一版之后，**被顶掉的那一版也在环里**。
//
// 这条不是锦上添花：恢复的全部使用场景都是「我正在慌」，而一个不可撤销的「撤销」
// 没人在慌的时候敢按。`cmdProfileRestore` 走的是 store.Write（与别的写同一条路），
// 所以这条性质是**从写路径来的**，不是那条命令额外做了什么——测试盯的正是这个事实。
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

	// 「取回 v1」= 把环里那一版写回去（命令做的就是这一件事）。
	ents := HistoryEntries(path)
	if len(ents) != 1 {
		t.Fatalf("前提：写两次该有一版历史，实际 %d", len(ents))
	}
	old, _ := os.ReadFile(ents[0])
	if err := Write(path, old); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "v1" {
		t.Errorf("取回之后盘上该是 v1，实际 %q", got)
	}

	// 被顶掉的 v2 现在也取得到——恢复是一次普通的写。
	ents = HistoryEntries(path)
	if len(ents) != 2 {
		t.Fatalf("取回本身也是一次写，环里该有两版，实际 %d", len(ents))
	}
	if b, _ := os.ReadFile(ents[0]); string(b) != "v2" {
		t.Errorf("最近那一版该是被顶掉的 v2，实际 %q", b)
	}
}
