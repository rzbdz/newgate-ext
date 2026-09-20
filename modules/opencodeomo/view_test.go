package opencodeomo

import (
	"strings"
	"testing"

	"github.com/rzbdz/newgate/lib/view"
)

// TestSlotsCardExplainsAnAbsentRegistry：还没接管过 omo 时**照常出一张卡片**，
// 写明原因与怎么生成。
//
// 让它从列表里消失的话，用户会以为这个模块不在这个构建里——而实际是「还没接管过
// 一次」（与 CLI 的 omoRegistry 同一句话，同一份理由：见 lib/view 的 Concept.Broken）。
func TestSlotsCardExplainsAnAbsentRegistry(t *testing.T) {
	isolate(t)

	concepts, err := omoConcepts("")
	if err != nil {
		t.Fatalf("没有注册表不该报错（那是正常状态，不是故障）: %v", err)
	}
	if len(concepts) != 1 {
		t.Fatalf("该出一张卡片，实际 %d 张", len(concepts))
	}
	c := concepts[0]
	if c.Broken == "" {
		t.Fatal("没有注册表时必须说明原因（Broken），否则界面上是一张空卡片")
	}
	// 原因里要有两样东西：文件在哪、怎么生成。只说「读不出来」等于把人卡住。
	if !strings.Contains(c.Broken, SlotsFile()) {
		t.Errorf("原因里该点名文件（%s）: %q", SlotsFile(), c.Broken)
	}
	if !strings.Contains(c.Broken, "newgate on opencode") {
		t.Errorf("原因里该说清怎么生成一个: %q", c.Broken)
	}
}

// TestSlotsCardIsWritableAndKeepsOneWriteSemantics 是这张卡的可写棘轮。
//
// 它替代了 2026-09-20 之前那条「这张卡必须只读」的断言——那个决定改了（用户要在
// 网页上直接挪槽位），但**它守护的意图没变**：写槽位归属的语义只能有一份。所以
// 这里不只是断言「Apply 不是 nil」，还断言它**走的是与 omoUse 同一个判据**
// （validateBindingValue）：坏值被拒、好值被收，而且只动 Overrides/Mode。
func TestSlotsCardIsWritableAndKeepsOneWriteSemantics(t *testing.T) {
	isolate(t)
	if err := WriteOmoSlots(&OmoSlots{
		Version: 1, Mode: "current",
		Slots: []OmoSlot{
			{Key: "omo-sisyphus", Kind: "agent", Name: "sisyphus", Current: "normal", Suggested: "heavy"},
			{Key: "omo-librarian", Kind: "agent", Name: "librarian", Current: "light", Suggested: "light"},
		},
		Overrides: map[string]string{"omo-sisyphus": "mid"},
	}); err != nil {
		t.Fatal(err)
	}

	concepts, err := omoConcepts("")
	if err != nil {
		t.Fatal(err)
	}
	c := concepts[0]
	if c.Broken != "" {
		t.Fatalf("有注册表却说读不出来: %s", c.Broken)
	}
	if c.Apply == nil {
		t.Fatal("这张卡片该是可写的（用户要在网页上把槽位挪到别的档位）")
	}
	if c.Kind != view.KindToggles {
		t.Fatalf("Kind 该是 toggles（一行一个选择器），实际 %q", c.Kind)
	}
	data, ok := c.Data.(omoData)
	if !ok {
		t.Fatalf("Data 该是 omoData，实际 %T", c.Data)
	}

	// mode 一项 + 每个槽位一项，且**每一项的 id 都是稳定身份**（界面按 id 回传）。
	byID := map[string]omoToggle{}
	for _, it := range data.Items {
		if it.ID == "" || it.Label == "" {
			t.Errorf("项缺 id 或 label: %+v", it)
		}
		if it.Kind != "select" {
			t.Errorf("项 %q 该是 select，实际 %q", it.ID, it.Kind)
		}
		if it.Value == "" && len(it.Options) == 0 {
			t.Errorf("项 %q 既没有值也没有可选值，界面上是个死下拉", it.ID)
		}
		byID[it.ID] = it
	}
	if _, ok := byID["mode"]; !ok {
		t.Error("少了 mode 这一项：current/suggested 是这张卡上唯一影响全部槽位的开关")
	}
	if _, ok := byID["omo-sisyphus"]; !ok {
		t.Error("少了 omo-sisyphus 这一项")
	}
	// 可选值里要有空（= 跟随缺省）与四个阶梯档，否则用户没法把它挪到别处、
	// 也没法反悔（空 = 清掉覆盖）。
	opts := map[string]bool{}
	for _, o := range byID["omo-sisyphus"].Options {
		opts[o] = true
	}
	for _, want := range []string{"", "heavy", "normal", "mid", "light", "vision"} {
		if !opts[want] {
			t.Errorf("可选值里少了 %q", want)
		}
	}
	// 覆盖要**摆在值上**（而不是藏在 why 里）：用户打开这张卡第一眼要看到
	// 「这个槽位被我改过」。
	if got := byID["omo-sisyphus"].Value; got != "mid" {
		t.Errorf("被覆盖的槽位该把覆盖摆在值上，实际 %q", got)
	}
	if got := byID["omo-librarian"].Value; got != "" {
		t.Errorf("没被覆盖的该是空（= 跟随缺省），实际 %q", got)
	}

	// ---- 写的语义：与 omoUse 同一份判据 ----
	if _, err := c.Apply([]byte(`{"omo-sisyphus":"not a binding"}`), ""); err == nil {
		t.Error("坏值该被拒（validateBindingValue 与 CLI 是同一份）")
	}
	if _, err := c.Apply([]byte(`{"mode":"whatever"}`), ""); err == nil {
		t.Error("mode 只认 current/suggested，别的该被拒")
	}
	if _, err := c.Apply([]byte(`{"omo-sisyphus":"@omo-librarian","mode":"suggested"}`), ""); err != nil {
		t.Fatalf("合法的 @引用该被接受: %v", err)
	}
	after := ReadOmoSlots()
	if after.Overrides["omo-sisyphus"] != "@omo-librarian" {
		t.Errorf("覆盖没写进去: %+v", after.Overrides)
	}
	if after.Mode != "suggested" {
		t.Errorf("mode 没写进去: %q", after.Mode)
	}
	// 现场记录（接管时算出来的）不该被这张卡碰。
	if after.Slots[0].Current != "normal" || after.Slots[0].Suggested != "heavy" {
		t.Errorf("界面不该改 Was/Current/Suggested: %+v", after.Slots[0])
	}
	// 空值 = 清掉覆盖（这是「反悔」唯一自然的表达）。
	if _, err := c.Apply([]byte(`{"omo-sisyphus":""}`), ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadOmoSlots().Overrides["omo-sisyphus"]; ok {
		t.Error("空值该把覆盖删掉")
	}
}

// TestSuggestedDifferenceIsVisible 钉住「与建议不符」这条判据**在新卡上仍然看得见**。
//
// 判据与 CLI 一字不差（`Suggested != "" && Suggested != Current`）：两个界面对
// 「哪些槽位偏离了建议」必须给出同一个答案。**不能**拿 effective 去比——那是
// 另一个问题了（override 之后两者常常不等，于是整张卡都在喊，喊了就没人看）。
// toggles 的行没有颜色可用，所以这条信号落在 why 的措辞上。
func TestSuggestedDifferenceIsVisible(t *testing.T) {
	differs := OmoSlot{Key: "omo-a", Kind: "agent", Name: "a", Current: "normal", Suggested: "heavy"}
	same := OmoSlot{Key: "omo-b", Kind: "agent", Name: "b", Current: "light", Suggested: "light"}
	none := OmoSlot{Key: "omo-c", Kind: "category", Name: "c", Current: "mid"}

	if !suggestsDifferently(differs) {
		t.Error("与建议不符该被判为不符")
	}
	for _, s := range []OmoSlot{same, none} {
		if suggestsDifferently(s) {
			t.Errorf("%s 不该被判为不符（判据是与 current 比）", s.Key)
		}
	}

	isolate(t)
	if err := WriteOmoSlots(&OmoSlots{
		Version: 1, Mode: "current",
		Slots: []OmoSlot{differs, same, none},
	}); err != nil {
		t.Fatal(err)
	}
	concepts, err := omoConcepts("")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]omoToggle{}
	for _, it := range concepts[0].Data.(omoData).Items {
		byID[it.ID] = it
	}
	if !strings.Contains(byID["omo-a"].Why, "differs") {
		t.Errorf("偏离建议的槽位该在 why 里说出来，实际 %q", byID["omo-a"].Why)
	}
	for _, k := range []string{"omo-b", "omo-c"} {
		if strings.Contains(byID[k].Why, "differs") {
			t.Errorf("%s 没有偏离，不该说 differs: %q", k, byID[k].Why)
		}
	}
	// 三个现场值都要在 why 里（now/effective/suggested）——表里那三列的信息
	// 一条都不能因为换了形状就丢掉。
	for _, want := range []string{"agent/a", "normal", "heavy"} {
		if !strings.Contains(byID["omo-a"].Why, want) {
			t.Errorf("why 里少了现场值 %q: %q", want, byID["omo-a"].Why)
		}
	}
}
