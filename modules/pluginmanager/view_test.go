package pluginmanager

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
)

// 界面那一份开关点（view.go）的断言分两半：**画出来的东西对不对**，以及
// **写下去的东西对不对**。后者最要紧——那是用户按一下就把运行期行为改掉的路径。

func register(t *testing.T, m Manager, module string, sws ...Switch) {
	t.Helper()
	if _, err := m.RegisterSelf(module, sws); err != nil {
		t.Fatal(err)
	}
}

func sw(path, danger string, def bool) Switch {
	s := Switch{Path: path, Title: "title of " + path, Why: "why of " + path, Default: def}
	s.Danger = Danger(danger)
	if s.Danger == DangerFootgun {
		s.TTL = 5 * time.Minute
	}
	return s
}

// TestViewSeparatesFootgunsFromTheRest：footgun 不能出现在那张**可写**卡片里。
//
// 它的写语义要求带时限，而这一版没有让用户选时限的界面；混进去的结果是界面上多了
// 一个按下去必定失败的开关——比不给更糟。所以它单列一张只读卡片。
func TestViewSeparatesFootgunsFromTheRest(t *testing.T) {
	m := start(t)
	register(t, m, "demo",
		sw("demo.kill", "safe", true),
		sw("demo.mode", "quirk", false),
		sw("demo.gun", "footgun", true),
	)

	concepts, err := viewConcepts(m)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]view.Concept{}
	for _, c := range concepts {
		byID[c.ID] = c
	}
	writable, ok := byID["plugin-manager.switches"]
	if !ok {
		t.Fatalf("可写的那张卡片不见了: %+v", concepts)
	}
	if writable.Apply == nil {
		t.Error("开关点卡片该是可写的")
	}
	gun, ok := byID["plugin-manager.footguns"]
	if !ok {
		t.Fatal("footgun 该单列一张卡片")
	}
	if gun.Apply != nil {
		t.Error("footgun 卡片不该可写（这里的写要么失败、要么绕过时限）")
	}
	for _, item := range gun.Data.(switchesData).Items {
		if item.ID != "demo.gun" {
			t.Errorf("footgun 卡片里混进了别的东西: %s", item.ID)
		}
		// 只读就得说清楚**去哪儿改**，不然用户只知道这里点不动。
		if !strings.Contains(item.Why, "newgate plugin") {
			t.Errorf("只读的 footgun 该写明用哪条命令改: %q", item.Why)
		}
	}
	if n := len(writable.Data.(switchesData).Items); n != 2 {
		t.Errorf("可写那张该有两个开关点，实际 %d", n)
	}
}

// TestViewReportsTheEffectiveState：画出来的 `value` 是**当前生效**的状态，而
// 它由出厂态与用户的设定共同决定（kill switch 看 Off 表、mode 看 On 表）。
// 这里画错，用户就会看到一个与 `newgate plugin` 相反的开关。
func TestViewReportsTheEffectiveState(t *testing.T) {
	m := start(t)
	register(t, m, "demo", sw("demo.kill", "safe", true), sw("demo.mode", "quirk", false))

	values := func() map[string]bool {
		t.Helper()
		concepts, err := viewConcepts(m)
		if err != nil {
			t.Fatal(err)
		}
		writable := switchConcept(t, concepts)
		out := map[string]bool{}
		for _, item := range writable.Data.(switchesData).Items {
			out[item.ID] = item.Value
		}
		return out
	}

	// 出厂态：kill switch 开着、mode 关着。
	got := values()
	if !got["demo.kill"] || got["demo.mode"] {
		t.Fatalf("出厂态画错了: %v", got)
	}

	// 用户关掉 kill switch：state 里写一条，界面就该跟着变。
	st := store.LoadState()
	if err := SetSwitch(st, sw("demo.kill", "safe", true), false, 0, false); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
	if got := values(); got["demo.kill"] {
		t.Errorf("关掉之后界面还说它开着: %v", got)
	}
}

// TestViewApplyWritesWithABaseline：保存走的是「比对基线 → 原子写」那条路。
// 基线过期要能报出**冲突**（而不是把人家的改动盖掉），这正是 web 与 CLI 同时改
// 同一份 state.json 时的现场。
func TestViewApplyWritesWithABaseline(t *testing.T) {
	m := start(t)
	register(t, m, "demo", sw("demo.kill", "safe", true))
	concepts, err := viewConcepts(m)
	if err != nil {
		t.Fatal(err)
	}
	c := switchConcept(t, concepts)
	base := c.Data.(switchesData).Base

	rev, err := c.Apply(json.RawMessage(`{"demo.kill":false}`), base)
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if rev == base {
		t.Error("写完该给出新基线（前端续着改不用刷新页面）")
	}
	if !Off(store.LoadState(), "demo.kill") {
		t.Error("state 里没有写进「关掉」这条设定")
	}

	// 拿**旧的**基线再写一次：这就是「命令行在你看页面的时候改了同一个文件」。
	// 必须报冲突并把两边原文都带上，绝不覆盖。
	_, err = c.Apply(json.RawMessage(`{"demo.kill":true}`), base)
	var conflict *view.Conflict
	if !errors.As(err, &conflict) {
		t.Fatalf("基线过期该报冲突，实际 %v", err)
	}
	if conflict.Yours == "" || conflict.Theirs == "" {
		t.Error("冲突要把两边原文都带上（用户才知道自己丢的是什么）")
	}
	if conflict.Path == "" || !strings.HasSuffix(conflict.Path, "state.json") {
		t.Errorf("冲突要点名是哪份文件: %q", conflict.Path)
	}
}

// TestViewApplyRejectsAnUnknownSwitch：界面手里的开关点已经不在了（换了产物、
// 模块被关掉）。**报错**而不是跳过：静默跳过的那天，用户以为按下的开关生效了。
func TestViewApplyRejectsAnUnknownSwitch(t *testing.T) {
	m := start(t)
	register(t, m, "demo", sw("demo.kill", "safe", true))
	concepts, _ := viewConcepts(m)
	_, err := switchConcept(t, concepts).Apply(
		json.RawMessage(`{"demo.gone":true}`), store.Revision(paths.StateFile()))
	if err == nil {
		t.Fatal("不认识的开关点该报错")
	}
	if !strings.Contains(err.Error(), "demo.gone") {
		t.Errorf("报错要点名是哪一个: %v", err)
	}
}

// TestViewReportsModulesWithNoSwitchesAtAll 是**骨架发行版**那条：一个开关点
// 都没上报时，开关那两张卡片刻意不报（空卡片看起来像「没加载出来」），但模块
// 清单必须照报——它回答的是另一个问题（这个构建由哪些模块组成），而那种构建
// 恰恰最需要它：升级完先确认的就是「装的模块对不对」（见 docs/08-operations.md）。
//
// 这条也是那个早退分支的棘轮：清单是后来插进去的，很容易被放在 `return` 之后。
func TestViewReportsModulesWithNoSwitchesAtAll(t *testing.T) {
	m := start(t)

	concepts, err := viewConcepts(m)
	if err != nil {
		t.Fatal(err)
	}
	var table view.Concept
	for _, c := range concepts {
		if c.ID == "plugin-manager.modules" {
			table = c
		}
		if c.ID == "plugin-manager.switches" || c.ID == "plugin-manager.footguns" {
			t.Errorf("一个开关点都没有，不该报 %s（空卡片像「没加载出来」）", c.ID)
		}
	}
	if table.ID == "" {
		t.Fatal("没有开关点时，模块清单也不报了——骨架发行版就什么都看不见了")
	}
	if table.Kind != view.KindTable {
		t.Errorf("模块清单该是 table，实际 %q", table.Kind)
	}
	if table.Apply != nil {
		t.Error("模块清单是可写的？装什么模块由产物决定，改不了")
	}

	data := table.Data.(view.Table)
	if len(data.Columns) == 0 {
		t.Fatal("模块清单没有列")
	}
	columns := map[string]bool{}
	for _, col := range data.Columns {
		columns[col.ID] = true
	}
	if len(data.Rows) != len(m.Modules()) {
		t.Errorf("清单该有 %d 行（每个模块一行），实际 %d", len(m.Modules()), len(data.Rows))
	}
	for _, row := range data.Rows {
		if row.Cells["module"].Text == "" || row.Cells["type"].Text == "" {
			t.Errorf("每一行都该有模块名与分类: %+v", row.Cells)
		}
		// 没有开关点的模块显示横杠，不是 0：「一个都没有」与「0 个」不是一回事，
		// 后者不是可能的状态（登记了就是至少一个）。
		if row.Cells["switches"].Text == "" {
			t.Errorf("开关点那格是空的: %+v", row.Cells)
		}
		for id := range row.Cells {
			if !columns[id] {
				t.Errorf("格子 %q 没有对应的列——前端取不到，这一格是空白", id)
			}
		}
	}
}

// switchConcept 按 **ID** 取可写的那张开关卡片，**不按下标**。
//
// 下标曾经能用，因为那张卡片恰好是第一个产出的。加了模块清单之后它就不是了，
// 于是按下标的写法取到一张 table，以一个看不懂的类型断言失败告终。ID 才是概念
// 的稳定身份（见 lib/view 的 Concept.ID）——产出顺序是账本的实现细节，测试不该
// 依赖它。
func switchConcept(t *testing.T, concepts []view.Concept) view.Concept {
	t.Helper()
	for _, c := range concepts {
		if c.ID == "plugin-manager.switches" {
			return c
		}
	}
	t.Fatal("可写的那张开关卡片不见了")
	return view.Concept{}
}

// TestViewDeclaresWhichCardsChangeOnTheirOwn 是这几张卡 `Live` 的清单棘轮。
//
// 两张开关卡声明：它们的值**会被终端改**（`newgate plugin … on/off`），而带时限的
// 那些**自己会变回去**（TTL 在读取时判定，见 query.go 的 Remaining——没有任何人
// 去写 state.json）。页面停在打开那一刻，界面就在说一个已经不对的当前值。
// 模块清单不声明：那是**这个构建**的事实，编译期就定了。
//
// 为什么值得一条断言：`Live` 掉了不会报错，只是那几张卡不再刷新——静默。
func TestViewDeclaresWhichCardsChangeOnTheirOwn(t *testing.T) {
	m := start(t)
	register(t, m, "demo",
		sw("demo.mode", "safe", false),
		sw("demo.kill", "footgun", true),
	)
	concepts, err := viewConcepts(m)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"plugin-manager.modules":  false, // 这个构建由哪些模块组成：不会自己变
		"plugin-manager.switches": true,
		"plugin-manager.footguns": true,
	}
	seen := map[string]bool{}
	for _, c := range concepts {
		w, expected := want[c.ID]
		if !expected {
			t.Fatalf("多了一张没预期的卡 %s", c.ID)
		}
		seen[c.ID] = true
		if c.Live != w {
			t.Errorf("%s 的 Live 该是 %v，实际 %v", c.ID, w, c.Live)
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("少了 %s——它本该被产出", id)
		}
	}
}
