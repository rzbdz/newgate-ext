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

// itemOf 按 **id** 取那一格。
//
// 按 id 而不是按位置：位置会跟着字段增减漂移，而 id 是这个模块自己定的身份，
// 也是界面回传时用来对号的键（见 lib/view 的 Record/Field 同一条理由）。
func itemOf(t *testing.T, id string) clToggle {
	t.Helper()
	c := classifierConcept()
	if c.ID != "claudecode.classifier" || c.Kind != view.KindToggles {
		t.Fatalf("这张卡的身份变了: id=%q kind=%q", c.ID, c.Kind)
	}
	data, ok := c.Data.(clData)
	if !ok {
		t.Fatalf("Data 不是 clData，是 %T", c.Data)
	}
	for _, it := range data.Items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("这张卡上没有 %q 那一格（现在有 %d 格）", id, len(data.Items))
	return clToggle{}
}

func nakedItemOf(t *testing.T) clToggle { t.Helper(); return itemOf(t, "naked") }
func overrideItemOf(t *testing.T) clToggle {
	t.Helper()
	return itemOf(t, "classifier_override")
}

func TestTheNakedRowIsQuietWhenNothingIsConfigured(t *testing.T) {
	testkit.Sandbox(t)
	it := nakedItemOf(t)
	if it.Value != "" {
		t.Errorf("没配过的时候该是空值（= 关），实际 %q", it.Value)
	}
	if !strings.Contains(it.Why, i18n.T("off — the Bash classifier is asked as usual", nil)) {
		t.Errorf("why 里该说清它是关着的，实际 %q", it.Why)
	}
}

// TestForeverAndAWindowReadDifferently：三态里「永久」与「限时」必须**读起来不
// 一样**。它们自己会不会结束，是这张卡上最要紧的一件事——把「等一会儿就好」和
// 「它不会再关了」说成同一句话，用户就会在真正危险的那一态上掉以轻心。
func TestForeverAndAWindowReadDifferently(t *testing.T) {
	testkit.Sandbox(t)

	writeState(t, map[string]any{NakedConfigKey: NakedConfig{Mode: "forever"}})
	forever := nakedItemOf(t)
	if forever.Value != "forever" {
		t.Errorf("永久模式该把值摆成 forever，实际 %q", forever.Value)
	}
	if !strings.Contains(forever.Why, i18n.T("on (forever) — the classifier is short-circuited and every request logs [naked]", nil)) {
		t.Errorf("永久模式该说清它不会自己关，实际 %q", forever.Why)
	}

	writeState(t, map[string]any{NakedConfigKey: NakedConfig{
		Mode: "on", ExpiresAt: time.Now().Add(90 * time.Second),
	}})
	window := nakedItemOf(t)
	if window.Value != "on" {
		t.Errorf("限时窗口该把值摆成 on，实际 %q", window.Value)
	}
	if strings.Contains(window.Why, i18n.T("on (forever) — the classifier is short-circuited and every request logs [naked]", nil)) {
		t.Error("限时窗口被说成了「永久」——那会让人以为关不掉了")
	}
	if !strings.Contains(window.Why, "turns itself off in") {
		t.Errorf("限时窗口该说出还有多久自动关，实际 %q", window.Why)
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
	it := nakedItemOf(t)
	if it.Value != "" {
		t.Errorf("过期的窗口该读成空值（= 关），实际 %q", it.Value)
	}
	if strings.Contains(it.Why, "-") && strings.Contains(it.Why, "turns itself off") {
		t.Errorf("过期的窗口不该还报倒计时：%q", it.Why)
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
func TestTheLayerNoteAppearsOnlyWhenItMatters(t *testing.T) {
	testkit.Sandbox(t)
	forever := map[string]any{NakedConfigKey: NakedConfig{Mode: "forever"}}

	// 层开着：没有那条提醒。
	writeState(t, forever)
	if note := layerNote(store.LoadState()); note != "" {
		t.Errorf("层生效时不该有那条提醒，实际 %q", note)
	}

	// 整层关掉：写出「这里配的都不算数、把层开回来就都算数」。
	writeState(t, map[string]any{
		NakedConfigKey: NakedConfig{Mode: "forever"},
		"gateway":      map[string]any{"special_treatment": false},
	})
	note := layerNote(store.LoadState())
	want := i18n.T("the whole special_treatment layer is off — none of this counts until it is switched back on", nil)
	if note != want {
		t.Errorf("整层关掉时该说「都不算数、开回来就都算数」\n  实际 %q\n  想要 %q", note, want)
	}
	// 这句要真的出现在**用户看得见的那一格**上，而不是只活在一个没人调用的函数里。
	if got := nakedItemOf(t).Why; !strings.Contains(got, want) {
		t.Errorf("那条提醒没落到裸奔那一格的 why 里：%q", got)
	}
	// 配置本身不受影响：那一格照旧显示 forever（值就是配置里写着的）。
	if got := nakedItemOf(t).Value; got != "forever" {
		t.Errorf("层关掉不该改变「配置里写着什么」那一格：%q", got)
	}

	// 单独关掉一枚插件（层开着）：**只有那一格**不算数，所以那句话不能是整层的
	// 那句——它会把另一格（此刻跑得好好的）也一起否掉，读的人会去关一个没问题
	// 的补丁。
	writeState(t, map[string]any{
		NakedConfigKey: NakedConfig{Mode: "forever"},
		"gateway":      map[string]any{"special_treatment_off": []string{nakedPluginName}},
	})
	note = layerNote(store.LoadState())
	if !strings.Contains(note, nakedPluginName) {
		t.Errorf("单独关掉插件时该点名那一枚，实际 %q", note)
	}
	if strings.Contains(note, i18n.T("the whole special_treatment layer is off — none of this counts until it is switched back on", nil)) {
		t.Errorf("只关了一枚插件不该说成「整层都不算数」：%q", note)
	}
}

func TestTheOverrideItemReportsBadConfigInsteadOfHidingIt(t *testing.T) {
	testkit.Sandbox(t)

	// 没配过：那一格是空的，why 说清怎么写。
	if got := overrideItemOf(t); got.Value != "" || !strings.Contains(got.Why, "provider/model") {
		t.Errorf("没配过改道时该是空值 + 写法提示，实际 %q / %q", got.Value, got.Why)
	}

	// 坏了要**说出来**：它会让整条改道静默失效（见 st-background.go 的 Status）。
	writeState(t, map[string]any{"classifier_override": map[string]any{"provider": "only-provider"}})
	if got := overrideItemOf(t).Why; !strings.Contains(got, "not valid") {
		t.Errorf("坏配置该在 why 里说出来，实际 %q", got)
	}
}

// TestTheNakedSwitchActuallyWrites 是这张卡可写之后的**写语义**棘轮。
//
// 它与 CLI 是同一份实现（saveNakedConfig / ParseNakedConfig），所以这一条同时是
// 「两个界面不会对同一个键给出不同答案」的保险。三态各自对上一个 CLI 动作：
// "" = naked off、on = 60 秒自限窗口、forever = 永久。**不把 forever 降级成窗口**
// ——那正是这张卡以前只读的理由。
func TestTheNakedSwitchActuallyWrites(t *testing.T) {
	testkit.Sandbox(t)

	write := func(v string) {
		t.Helper()
		c := classifierConcept()
		if c.Apply == nil {
			t.Fatal("这张卡该是可写的（裸奔得有个开关）")
		}
		body, _ := json.Marshal(map[string]string{"naked": v})
		if _, err := c.Apply(body, ""); err != nil {
			t.Fatalf("写 %q 失败: %v", v, err)
		}
	}

	write("forever")
	cfg, active := ParseNakedConfig(store.LoadState().ModuleConfig[NakedConfigKey])
	if !active || cfg.Mode != "forever" {
		t.Fatalf("forever 没写进去: %+v active=%v", cfg, active)
	}

	write("on")
	cfg, active = ParseNakedConfig(store.LoadState().ModuleConfig[NakedConfigKey])
	if !active || cfg.Mode != "on" || time.Until(cfg.ExpiresAt) <= 0 {
		t.Fatalf("on 该写成一个还没到点的窗口: %+v active=%v", cfg, active)
	}

	write("")
	if _, on := ParseNakedConfig(store.LoadState().ModuleConfig[NakedConfigKey]); on {
		t.Error("空值该把裸奔关掉")
	}
	if _, ok := store.LoadState().ModuleConfig[NakedConfigKey]; ok {
		t.Error("关掉时该把那个键删掉，而不是留一个空对象")
	}

	// 认不出来的值要拦住，而不是静默当成关闭。
	if _, err := classifierConcept().Apply([]byte(`{"naked":"maybe"}`), ""); err == nil {
		t.Error("认不出来的裸奔模式该报错")
	}
}
