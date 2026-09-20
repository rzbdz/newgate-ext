package claudecode

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
	"github.com/rzbdz/newgate/modules/config/store"
	"github.com/rzbdz/newgate/testing/testkit"
)

// 这一族测的是那张分类器卡片。断言打在**数据**上（view.Table），不打在 HTML 上。
//
// 为什么它值几条断言：`naked forever` 是永久短路安全分类器的开关，而这张卡在
// 关掉 cli 的那份发行版里是**唯一**能看见它的地方。所以「开着的时候画面是坏的
// 颜色」「配置还在、但这一层此刻不生效」这两件事都必须真的成立——它们错了不会
// 报错，只会让用户以为分类器还开着（或者以为它关了）。

// writeState 把几段模块原文写进沙箱的 state.json——卡片读的就是它（store.LoadState）。
func writeState(t *testing.T, modules map[string]any) {
	t.Helper()
	st := store.LoadState()
	st.ModuleConfig = map[string][]byte{}
	for key, v := range modules {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		st.ModuleConfig[key] = raw
	}
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
}

// rowsOf 取那张卡的行。**按位置**取：行的身份是「第几行」，不是它印出来的那句
// 话——拿文字去查表，等于把译文也一起断言进去了。
//
// 第三行（这一层算不算数）只在层不生效时出现，所以它由 layerRowOf 单独取，
// 而 rowsOf 只返回前两行。
func rowsOf(t *testing.T) []map[string]view.Cell {
	t.Helper()
	c := classifierConcept()
	if c.ID != "claudecode.classifier" || c.Kind != view.KindTable {
		t.Fatalf("这张卡的身份变了: id=%q kind=%q", c.ID, c.Kind)
	}
	tb, ok := c.Data.(view.Table)
	if !ok {
		t.Fatalf("Data 不是 view.Table，是 %T", c.Data)
	}
	if len(tb.Rows) < 2 {
		t.Fatalf("该有裸奔与改道两行，实际 %d 行", len(tb.Rows))
	}
	return tb.Rows
}

func nakedRowOf(t *testing.T) map[string]view.Cell { t.Helper(); return rowsOf(t)[0] }
func overrideRowOf(t *testing.T) map[string]view.Cell {
	t.Helper()
	return rowsOf(t)[1]
}

// layerRowOf 取第三行；层生效时它**不该存在**——那一行说的全是「此刻不算数」，
// 没这回事的时候摆一行「一切正常」只是噪音。
func layerRowOf(t *testing.T) map[string]view.Cell {
	t.Helper()
	rows := rowsOf(t)
	if len(rows) < 3 {
		t.Fatalf("层不生效时该多一行说明，实际只有 %d 行", len(rows))
	}
	if len(rows) > 3 {
		t.Fatalf("只该多一行，实际 %d 行", len(rows))
	}
	return rows[2]
}

func noLayerRow(t *testing.T) {
	t.Helper()
	if rows := rowsOf(t); len(rows) != 2 {
		t.Errorf("层生效时不该有第三行，实际 %d 行", len(rows))
	}
}

func TestTheNakedRowIsQuietWhenNothingIsConfigured(t *testing.T) {
	testkit.Sandbox(t)
	row := nakedRowOf(t)
	if got := row["state"]; got.Text != i18n.T("off", nil) || got.Tone != view.ToneOK {
		t.Errorf("没配过的时候该是 off/ok，实际 %q/%q", got.Text, got.Tone)
	}
}

// TestTheNakedRowIsRedOnlyForForever：三态里只有「永久」画成 bad。另外两种自己
// 会结束，把它们也画成红，等于把「等一会儿就好」和「它不会再关了」说成同一件事。
func TestTheNakedRowIsRedOnlyForForever(t *testing.T) {
	testkit.Sandbox(t)

	writeState(t, map[string]any{NakedConfigKey: NakedConfig{Mode: "forever"}})
	if got := nakedRowOf(t)["state"]; got.Text != i18n.T("on (forever)", nil) || got.Tone != view.ToneBad {
		t.Errorf("forever 该是 bad，实际 %q/%q", got.Text, got.Tone)
	}

	writeState(t, map[string]any{NakedConfigKey: NakedConfig{
		Mode: "on", ExpiresAt: time.Now().Add(90 * time.Second),
	}})
	got := nakedRowOf(t)["state"]
	if got.Tone != view.ToneWarn {
		t.Errorf("限时窗口该是 warn（它会自己结束），实际 %q/%q", got.Text, got.Tone)
	}
	if got.Text == i18n.T("on (forever)", nil) {
		t.Error("限时窗口被说成了「永久」——那会让人以为关不掉了")
	}
}

// TestAnExpiredWindowReadsAsOff：窗口是**懒过期**的（没有 timer 去关它，见
// ParseNakedConfig 的注释）。一个已经到点的窗口在**任何**读点都该表现为「没开」，
// 这张卡也不例外——显示「还有 -6m3s 自动关」正是 2026-09-17 实跑踩过的那个 bug。
func TestAnExpiredWindowReadsAsOff(t *testing.T) {
	testkit.Sandbox(t)
	writeState(t, map[string]any{NakedConfigKey: NakedConfig{
		Mode: "on", ExpiresAt: time.Now().Add(-time.Minute),
	}})
	if got := nakedRowOf(t)["state"]; got.Text != i18n.T("off", nil) || got.Tone != view.ToneOK {
		t.Errorf("过期的窗口该读成 off/ok，实际 %q/%q", got.Text, got.Tone)
	}
}

// TestTheLayerRowAppearsOnlyWhenItMatters 是这张卡最值钱的一条。
//
// 「层关着、`forever` 还留在 state.json 里」是最该被看见的局面：门现在关着，
// 但把 special_treatment 一开回来它就又开了。那张表上面前两行说的是「配置里
// 写着什么」，光看它们会以为分类器此刻就是短路的——所以必须有第三行说清
// 「那些配置此刻不算数」。反过来，层生效时那一行不该出现（摆一句「一切正常」
// 只是噪音）。
//
// 这一条也是「两行各说各话」的棘轮：改之前只有裸奔那一行解释层状态，改道那行
// 照样说「优先级最高」——同一张表里两种说法。
func TestTheLayerRowAppearsOnlyWhenItMatters(t *testing.T) {
	testkit.Sandbox(t)
	forever := map[string]any{NakedConfigKey: NakedConfig{Mode: "forever"}}

	// 层开着：没有第三行。
	writeState(t, forever)
	noLayerRow(t)

	// 整层关掉：第三行出现，说的是「这里配的都不算数、把层开回来就都算数」。
	writeState(t, map[string]any{
		NakedConfigKey: NakedConfig{Mode: "forever"},
		"gateway":      map[string]any{"special_treatment": false},
	})
	row := layerRowOf(t)
	if row["state"].Tone == view.ToneOK {
		t.Errorf("层关着却画成 ok：%q/%q", row["state"].Text, row["state"].Tone)
	}
	want := i18n.T("the whole layer is off, so nothing configured here counts right now; it counts again the moment the layer is switched back on", nil)
	if row["detail"].Text != want {
		t.Errorf("整层关掉时该说「这里配的都不算数、开回来就都算数」\n  实际 %q\n  想要 %q", row["detail"].Text, want)
	}
	// 配置本身不受影响：裸奔那行照旧说 forever（红），只是「此刻不算数」。
	if got := nakedRowOf(t)["state"]; got.Text != i18n.T("on (forever)", nil) || got.Tone != view.ToneBad {
		t.Errorf("层关掉不该改变「配置里写着什么」那一行：%q/%q", got.Text, got.Tone)
	}

	// 单独关掉一枚插件（层开着）：**只有那一行**不算数，所以那句话不能是整层的
	// 那句——它会把另一行（此刻跑得好好的）也一起否掉，读的人会去关一个没问题
	// 的补丁。
	writeState(t, map[string]any{
		NakedConfigKey: NakedConfig{Mode: "forever"},
		"gateway":      map[string]any{"special_treatment_off": []string{nakedPluginName}},
	})
	row = layerRowOf(t)
	if !strings.Contains(row["state"].Text, nakedPluginName) {
		t.Errorf("单独关掉插件时第三行该点名那一枚，实际 %q", row["state"].Text)
	}
	want = i18n.T("{names}: switched off individually, so that row counts for nothing right now; it counts again the moment it is switched back on",
		i18n.A{"names": nakedPluginName})
	if row["detail"].Text != want {
		t.Errorf("只关了一枚插件时不该说成「整层都不算数」\n  实际 %q\n  想要 %q", row["detail"].Text, want)
	}
}

func TestTheOverrideRowReportsBadConfigInsteadOfHidingIt(t *testing.T) {
	testkit.Sandbox(t)

	if got := overrideRowOf(t)["state"]; got.Text != i18n.T("none", nil) || got.Tone != view.ToneOK {
		t.Errorf("没配过改道时该是 none/ok，实际 %q/%q", got.Text, got.Tone)
	}

	// 坏了要**说出来**：它会让整条改道静默失效（见 st-background.go 的 Status）。
	writeState(t, map[string]any{"classifier_override": map[string]any{"provider": "only-provider"}})
	if got := overrideRowOf(t)["state"]; got.Tone != view.ToneBad {
		t.Errorf("坏配置该画成 bad，实际 %q/%q", got.Text, got.Tone)
	}
}
