package confighook

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/store"
)

// 这一族测的是那张「槽位 → 档位」覆盖表**落盘时的形状**。
//
// 为什么值得单独测：它的每一条规则都是「写进去之后看起来也对」的那种错。留下一条
// 与缺省相同的记录，界面上一切正常，只有以后改缺省的人会发现自己对一部分用户不
// 生效——而那些用户从没配过任何东西。这类故障没有现场可复现，只能靠这里钉住。
//
// 沙箱自己搭（不用 testing/testkit）：**那个包 import 了本包**（它要装配真组件图），
// 本包再用它就成了循环。所以这里只做 Sandbox 里与本测试有关的那一件事——把
// NEWGATE_HOME 指到一个临时目录。
func sandbox(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEWGATE_HOME", home)
}

// testSlots 是一张两槽位的表，第二个槽位声明了自己的例外（`inherit` 那类）。
func testSlots() []Slot {
	return []Slot{
		{Name: "main", Tier: "normal"},
		{Name: "sub", Tier: "mid", Also: []string{"inherit"}},
	}
}

func testOverrides() SlotOverrides {
	return SlotOverrides{Key: "test_slots", AgentID: "test", Slots: testSlots()}
}

func stored(t *testing.T, key string) (string, bool) {
	t.Helper()
	raw, ok := store.LoadState().ModuleConfig[key]
	return string(raw), ok
}

// TestWritingAllDefaultsRemovesTheKey：全部等于缺省 = 那个键**消失**。
//
// 这条是实测踩出来的：只判「传进来是空表」是不够的，因为界面一次交**全部**槽位
// ——用户把最后一行改回缺省时，传进来的是「每一行都等于缺省」，而不是空表。少了
// 后半条判据，state.json 里会留下一个 `{}`：读回来等价于没配过，所以它不会让任何
// 东西坏掉，只是一条谁也看不懂、谁也不敢删的噪音。
//
// codex 那边一路都是这个形状（它只有一个槽位），所以是它在浏览器验收里先红的。
func TestWritingAllDefaultsRemovesTheKey(t *testing.T) {
	sandbox(t)
	o := testOverrides()

	// 先真的写一条进去，证明这个键本来会出现——否则下面那条断言可能是「本来就没写」。
	if err := o.Write(map[string]string{"main": "heavy"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := stored(t, o.Key); !ok {
		t.Fatal("改过的槽位该落盘")
	}

	// 再全部改回缺省：键该消失，而不是留一个空对象。
	if err := o.Write(map[string]string{"main": "normal", "sub": "mid"}); err != nil {
		t.Fatal(err)
	}
	if raw, ok := stored(t, o.Key); ok {
		t.Errorf("全部等于缺省之后那个键该消失，实际留下 %q", raw)
	}
	// 空表也一样（这是原来就有的那条）。
	if err := o.Write(map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if raw, ok := stored(t, o.Key); ok {
		t.Errorf("空表该删键，实际留下 %q", raw)
	}
}

// TestOnlyTheChangedEntriesAreWritten：只写改过的那几行，值原样。
func TestOnlyTheChangedEntriesAreWritten(t *testing.T) {
	sandbox(t)
	o := testOverrides()
	if err := o.Write(map[string]string{"main": "normal", "sub": "heavy"}); err != nil {
		t.Fatal(err)
	}
	raw, ok := stored(t, o.Key)
	if !ok {
		t.Fatal("改过的槽位该落盘")
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got["sub"] != "heavy" {
		t.Errorf("改过的那行该在，实际 %v", got)
	}
	if _, there := got["main"]; there {
		t.Errorf("与缺省相同的行不该写进去，实际 %v", got)
	}
}

// TestABadValueIsRefusedAndLeavesNothingBehind：打错的档位名写不进去。
//
// 它会被原样注入成模型名（claude）或写进 config.toml（codex），在上游那边表现为
// 「不存在的模型」——症状是某些请求突然 400，而它跟配置之间隔着好几层。
func TestABadValueIsRefusedAndLeavesNothingBehind(t *testing.T) {
	sandbox(t)
	o := testOverrides()
	if err := o.Write(map[string]string{"main": "hevy"}); err == nil {
		t.Fatal("不存在的档位该被拒绝")
	}
	if raw, ok := stored(t, o.Key); ok {
		t.Errorf("被拒的写不该留下任何痕迹，实际 %q", raw)
	}
	// 客户端自己声明的例外收得下（`inherit` 是 Claude Code 的语义，我们只存不解释）。
	if err := o.Write(map[string]string{"sub": "inherit"}); err != nil {
		t.Errorf("槽位声明过的例外该收得下: %v", err)
	}
	// 但它只属于声明了它的那个槽位。
	if err := o.Write(map[string]string{"main": "inherit"}); err == nil {
		t.Error("main 没声明 inherit，该被拒绝")
	}
}

// TestABadValueOnDiskDoesNotBreakAnything：手改坏的文件要**读得回来**（只是不生效）。
//
// state.json 是给人手改的，而这一格改坏的后果不该是「客户端起不来」：读的时候滤掉
// 坏值、回落到缺省，并且把坏的那条**报出来**（界面上会说明），而不是安静地把它当
// 成没配过——那样用户会以为自己的改动生效了。
func TestABadValueOnDiskDoesNotBreakAnything(t *testing.T) {
	sandbox(t)
	o := testOverrides()

	// 直接往磁盘上写一份带坏值的（绕过 Write 的校验，模拟手改）。
	st := store.LoadState()
	if st.ModuleConfig == nil {
		st.ModuleConfig = map[string][]byte{}
	}
	st.ModuleConfig[o.Key] = []byte(`{"main":"hevy","sub":"light"}`)
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}

	ok, bad := o.Read()
	if _, there := ok["main"]; there {
		t.Error("坏值不该出现在可用的那一份里")
	}
	if bad["main"] != "hevy" {
		t.Errorf("坏值要单独报出来，实际 %v", bad)
	}
	if ok["sub"] != "light" {
		t.Errorf("同一个文件里好的那一行该照常生效，实际 %v", ok)
	}
	// 「没改过就用缺省」只有一份实现（TierOf），这里验的是那个回落真的到位。
	if got := TierOf(nil, Slot{Name: "main", Tier: "normal"}); got != "normal" {
		t.Errorf("坏值该回落到描述符里的缺省，实际 %q", got)
	}
}

// TestTheKeyIsPerClient：两家客户端各写各的键，互不干扰。
//
// 这条听着像废话，但它正是「codex 也要一张同样的表」这件事的前提：共用的如果是一
// 个键，改 codex 的档位会把 claude 的一起改掉。
func TestTheKeyIsPerClient(t *testing.T) {
	sandbox(t)
	a := SlotOverrides{Key: "a_slots", AgentID: "a", Slots: testSlots()}
	b := SlotOverrides{Key: "b_slots", AgentID: "b", Slots: testSlots()}
	if err := a.Write(map[string]string{"main": "heavy"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Write(map[string]string{"main": "light"}); err != nil {
		t.Fatal(err)
	}
	okA, _ := a.Read()
	okB, _ := b.Read()
	if okA["main"] != "heavy" || okB["main"] != "light" {
		t.Errorf("两家的表串了：a=%v b=%v", okA, okB)
	}
	// 而且它们真的落在两个键上。
	if _, ok := store.LoadState().ModuleConfig["a_slots"]; !ok {
		t.Error("a 的键不在")
	}
	if _, ok := store.LoadState().ModuleConfig["b_slots"]; !ok {
		t.Error("b 的键不在")
	}
}
