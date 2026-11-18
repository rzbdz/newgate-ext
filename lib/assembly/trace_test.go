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

// 这一族测的是 2026-09-20 那条改动：装配记录**只在它变了的时候**整段写出去。
//
// 为什么值得钉：判据是「去掉时间戳与耗时之后内容是否相同」，而这两样东西恰好是
// 每一行都带的。判据写松一点（比如直接哈希原文）不会报错、不会崩，只是**永远
// 都不同**——于是整段每次都写，改了个寂寞，而日志看起来一切正常。这类「棘轮
// 自己松掉」的失败，只有真的拿两份只差时间戳的记录去比才看得见。

// record 驱动一段装配记录，返回它写出来的东西。
func record(t *testing.T, prev string, lines ...string) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	r := newAssemblyRecord(&buf, prev)
	for _, l := range lines {
		// 传 %s 而不是把 line 当格式串：记录里本来就有 {name} 这类字面花括号。
		r.add("%s", l)
	}
	r.flush()
	return buf.String(), assemblyFingerprint(r.plain)
}

// 一份典型的装配记录。三行分别覆盖三种会变的东西：逐组件耗时、收尾耗时、时间戳。
var (
	assemblyA = []string{
		"assembly: loader 1 handed over 2 components",
		"start 1/2 cli done (took 321µs)",
		"start 2/2 gateway done (took 1ms)",
		"assembly complete: 2 components, took 12ms",
	}
	// 与 A 只差耗时：µs/ms 那几位变了，装的还是那两个组件。
	assemblyA2 = []string{
		"assembly: loader 1 handed over 2 components",
		"start 1/2 cli done (took 999µs)",
		"start 2/2 gateway done (took 8ms)",
		"assembly complete: 2 components, took 55ms",
	}
	// 与 A 差在**装了什么**：多了一个模块，顺序也变了。
	assemblyB = []string{
		"assembly: loader 1 handed over 3 components",
		"start 1/2 cli done (took 321µs)",
		"start 2/3 gateway done (took 1ms)",
		"assembly complete: 3 components, took 12ms",
	}
)

// 同一段记录在**中文**下的样子——逐字抄自线上日志（2026-09-20 15:18），不是
// 编出来的：全角括号、`用时`、以及中文的 `，` 分隔。
//
// 为什么必须有：这套归一化当初只认英文的 `(took …)`，于是译文的每一行都把耗时
// 带进了指纹，**每次运行都是新指纹**，整段每次都重写（线上实测，同一份二进制连
// 写四条不同指纹）。而那时的测试全用英文——测试自己挑了一门语言，恰好是唯一能
// 通过的那门。这与「测试自己用 O_RDWR 打开文件」是同一类错：喂给被测代码的
// 输入不是真实的那一份。
var (
	assemblyZH = []string{
		"装配：第 1 个 loader 交出 2 个组件",
		"启动 1/2 cli 完成（用时 321µs）",
		"启动 2/2 gateway 完成（用时 1ms）",
		"装配完成：2 个组件，用时 12ms",
	}
	assemblyZH2 = []string{
		"装配：第 1 个 loader 交出 2 个组件",
		"启动 1/2 cli 完成（用时 999µs）",
		"启动 2/2 gateway 完成（用时 8ms）",
		"装配完成：2 个组件，用时 55ms",
	}
)

func TestFingerprintIgnoresTimeAndDuration(t *testing.T) {
	_, fp1 := record(t, "", assemblyA...)
	_, fp2 := record(t, "", assemblyA2...)
	if fp1 != fp2 {
		t.Errorf("两份只差耗时的记录应当同指纹——否则整段每次都重写，这条改动等于没做\n  %s\n  %s", fp1, fp2)
	}
	_, fp3 := record(t, "", assemblyB...)
	if fp1 == fp3 {
		t.Error("多起了一个模块，指纹却没变——那升级那一发的整段记录就不会写出来了")
	}

	// 译文的同一段：耗时变了，指纹必须不变（线上就是栽在这里）。
	_, zh1 := record(t, "", assemblyZH...)
	_, zh2 := record(t, "", assemblyZH2...)
	if zh1 != zh2 {
		t.Errorf("中文下两份只差耗时的记录应当同指纹——否则中文机器上整段每次都重写\n  %s\n  %s", zh1, zh2)
	}
	// 而且归一化要真的把耗时剥掉了，不是碰巧相等。
	if strings.Contains(normalizeTraceLine(assemblyZH[1]), "321") ||
		strings.Contains(normalizeTraceLine(assemblyZH[3]), "12ms") {
		t.Errorf("耗时没被剥掉：%q / %q",
			normalizeTraceLine(assemblyZH[1]), normalizeTraceLine(assemblyZH[3]))
	}
}

func TestFullRecordIsWrittenOnlyWhenItChanges(t *testing.T) {
	// 第一发：日志里还没有记录（prev 为空）→ 整段 + 标记行。
	out, fp := record(t, "", assemblyA...)
	for _, l := range assemblyA {
		if !strings.Contains(out, l) {
			t.Errorf("第一发应当写整段，缺了这一行: %s", l)
		}
	}
	if !strings.Contains(out, "assembly: fingerprint "+fp[:12]) {
		t.Errorf("整段末尾应当留下指纹标记，实际:\n%s", out)
	}

	// 第二发：与第一发同内容（耗时不同而已）→ **一个字都不写**。
	//
	// 原来这里留一行「与已有的记录相同」。那不是跃迁，是回音：敲一条
	// `newgate status` 就往守护进程日志里加一行「什么都没变」（2026-09-21
	// 用户的原话是「我不想要看到日志里出现这东西」）。
	again, _ := record(t, fp[:12], assemblyA2...)
	if strings.TrimSpace(again) != "" {
		t.Errorf("同一份装配第二次不应该写任何东西，实际:\n%s", again)
	}
	// 而且「没写」不能把下一发坑了：指纹标记还在日志里（第一发留下的那一条），
	// 所以第三发照样认得出「没变」。这一条是上面那条的前提——少了它，不写就
	// 变成了「每发都重写整段」，正是这个机制要修的东西。
	// （下面那一发验的就是这件事：prev 仍然是 fp，装配没变 → 还是不写。）
	if third, _ := record(t, fp[:12], assemblyA2...); strings.TrimSpace(third) != "" {
		t.Errorf("「没变就不写」之后，下一发应当同样不写（标记行还在日志里），实际:\n%s", third)
	}

	// 第三发：装配变了 → 又整段写。
	changed, _ := record(t, fp[:12], assemblyB...)
	if !strings.Contains(changed, "assembly complete: 3 components, took 12ms") {
		t.Errorf("装配变了就要整段重写，实际:\n%s", changed)
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
		t.Errorf("空日志里不该找出指纹，得到 %q", got)
	}

	// 上一次的记录，然后隔开 >8KB 的普通日志，再一条新的。
	// 窗口只盖得住尾部：找回来的必须是**最近**那一条。
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
		t.Errorf("应当找回最近那条指纹 bbbbbbbbbbbb，得到 %q", got)
	}
}

// 窗口是从文件中间切的，第一段必然是半行——那半行里没有标记，但不能因此
// 整段作废（真实日志的每一行都长，切在半行上是常态，不是边角情况）。
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
		t.Errorf("被切断的半行不该让整次回看作废，得到 %q", got)
	}
}

// TestARepeatedInvocationWritesOneLine 是这条改动的**真路径**测试：不重建装配
// 记录，而是像二进制那样**真的调用两次 TraceToLogFile，各自装配一遍整图**。
//
// 为什么非要有它：上面那几条都是拿着 assemblyRecord 直接驱动，文件由测试自己
// 打开——于是「生产路径用什么模式打开日志」这件事没有任何断言盖着。2026-09-20
// 就是这么漏的：TraceToLogFile 用 O_WRONLY 开文件，回看必然 EBADF，每一发都被
// 当成第一次，整段照写（实测每次 105 行，与改动之前一模一样），而上面四条全绿。
func TestARepeatedInvocationWritesNothing(t *testing.T) {
	env := testkit.Sandbox(t)

	// 一次「调用」= 这个进程真的装配一遍整图并收尾，与 cmd/newgate 那条路同形。
	logPath := filepath.Join(env.Home, "newgate.log")
	invoke := func() {
		stop := TraceToLogFile(logPath)
		built, err := modules.New(staticLoader{})
		if err != nil {
			t.Fatalf("装配失败: %v", err)
		}
		_ = built.Stop(context.Background())
		stop()
	}
	lines := func() int {
		raw, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("读日志: %v", err)
		}
		return strings.Count(string(raw), "\n")
	}

	invoke()
	first := lines()
	if first < 3 {
		t.Fatalf("the first invocation must write the whole assembly record, got %d lines", first)
	}
	// 第二发：**一行都不该多**。
	//
	// 原来是「只留 1 行」（一行「与已有的记录相同」）。那一行仍然是回音：敲一条
	// `newgate status` 就往守护进程的日志里加一行「什么都没变」，而这份日志上
	// 每一行都该是**一次跃迁**。用户 2026-09-21 的原话是「我不想要看到日志里出现
	// 这东西」。（这里是 0 行而不是 1 行——第一版改完这个测试还在断言 1 行，
	// 于是它红了，红的正是它该红的地方。）
	invoke()
	if grew := lines() - first; grew != 0 {
		t.Errorf("同一份装配的第二次调用不该写任何东西，实际多了 %d 行——"+
			"回看没找到上一次的记录（日志文件的打开模式？指纹的写法？）", grew)
	}
}
