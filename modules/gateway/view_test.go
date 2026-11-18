package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// 日志那条概念的取材部分（tailLines / redactLine）是纯函数，所以它在叶子里就能
// 测：起一个真文件，读它的尾巴。这条路径的错误都不吵——读多了、读少了、把凭据
// 发出去了，界面上看起来都只是「日志」。所以它值得几条断言。

func writeLog(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "newgate.log")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTailLinesReturnsTheLastOnesOldestFirst(t *testing.T) {
	p := writeLog(t, "a\nb\nc\nd\ne\n")
	// 从旧到新：最新的一行在最下面（见 logData.Lines 的说明）。
	lines, truncated, err := tailLines(p, 10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lines, "|"); got != "a|b|c|d|e" {
		t.Errorf("the whole file fits, so it should be given as is: %q", got)
	}
	if truncated {
		t.Error("nothing was dropped, so it must not say truncated")
	}

	// 只要最后 3 行：上面那两行**确实**没给出去，所以 truncated 该是 true——
	// 「你看的不是全部」这件事不说是会骗人的（读的人会以为日志从这里开始）。
	lines, truncated, err = tailLines(p, 3, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lines, "|"); got != "c|d|e" {
		t.Errorf("wrong tail: %q", got)
	}
	if !truncated {
		t.Error("two more lines above were not given, so it should say truncated")
	}
}

// TestTailLinesStopsAtTheByteLimit：日志按 16MB×4 轮转，而浏览器每隔几秒就刷新
// 一次这个视图。所以读取必须是**有上界的**——按行回溯会把整个文件读一遍，那个
// 成本随日志增长，而界面看起来只是「有点慢」。
func TestTailLinesStopsAtTheByteLimit(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString(strings.Repeat("x", 100))
		b.WriteString("\n")
	}
	b.WriteString("last line\n")
	p := writeLog(t, b.String())

	lines, truncated, err := tailLines(p, 400, 512)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Error("cut by the byte limit, so it should say truncated - otherwise the user thinks the log is only this long")
	}
	if len(lines) == 0 || lines[len(lines)-1] != "last line" {
		t.Errorf("the last line of the tail should be the newest one, got %q", lines[len(lines)-1:])
	}
	// 第一行多半是半截的（被 limit 切开），必须丢掉——留下它，读的人会拿一段
	// 没有开头的输出当证据。
	if strings.Contains(lines[0], "x") && len(lines[0]) != 100 {
		t.Errorf("the first line is cut in half: %q", lines[0])
	}
}

func TestTailLinesOnMissingFile(t *testing.T) {
	_, _, err := tailLines(filepath.Join(t.TempDir(), "nope.log"), 10, 1024)
	if err == nil {
		t.Error("a missing file should error - the caller uses that to say 'no log yet' rather than 'the log is empty'")
	}
}

// TestRedactLine：日志本来不该出现凭据，但它是**别人写进来的**（provider 的 URL、
// 某个模块打出来的请求头、将来某个插件记的 body），而这个视图会把日志发到浏览器。
// 兜底判据宁可误杀。
func TestRedactLine(t *testing.T) {
	cases := map[string]string{
		"using key sk-abcdefghijklmnopqrst for demo": "using key *** for demo",
		`provider api_key=abcdefghijklmnop sent`:     `provider api_key=*** sent`,
		`Authorization: Bearer abcdefghijklmnop.qrs`: `Authorization: ***`,
		"-> deepseek/model-heavy (200)":              "-> deepseek/model-heavy (200)",
	}
	for in, want := range cases {
		if got := redactLine(in); got != want {
			t.Errorf("redactLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakePlugin 是这条断言用的假插件：真的插件住在发行版里（deepseek / glm /
// claudecode_*），内核自带的装配一个都没有——不装假的，这条棘轮在 CI 里就只会
// 走 t.Skip，等于没写。
type fakePlugin struct{ name, why string }

func (f fakePlugin) Name() string                { return f.name }
func (f fakePlugin) Why() string                 { return f.why }
func (f fakePlugin) Match(*special.Request) bool { return false }
func (f fakePlugin) Apply(b []byte, _ *special.Request) ([]byte, []string, error) {
	return b, nil, nil
}

// installFakePlugins 换上一本只有这几枚插件的注册表，跑完还原。
//
// 注册表是**进程级**的（插件在模块 Start 里注册进来），所以这里用带所有权的
// InstallDefault——与 modules/gateway/module.go 里那条同一个口子，用完必须还回去，
// 否则别的测试会看见这几枚假插件。
func installFakePlugins(t *testing.T, ps ...special.Plugin) {
	t.Helper()
	reg := special.NewRegistry()
	for _, p := range ps {
		if _, err := reg.Register(p); err != nil {
			t.Fatalf("installing fake plugin %q: %v", p.Name(), err)
		}
	}
	t.Cleanup(special.InstallDefault(reg))
}

// TestEverythingGatewayShowsDeclaresItselfLive：这三张卡的数据都会自己变
// （计数器在涨、日志在写、补丁开关可能在终端被拨），所以它们都声明了 Live。
//
// 为什么值得一条断言：Live 掉掉之后**什么都不会报错**——界面只是不再每几秒问
// 一次，卡片安静地停在打开页面那一刻。那正是这张卡最不该有的样子（一张说着
// 「现在哪些补丁在动你的请求」的表，答的却是十分钟前的）。
func TestEverythingGatewayShowsDeclaresItselfLive(t *testing.T) {
	cs, err := gatewayConcepts()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) == 0 {
		t.Fatal("the gateway has no cards at all - this assertion is a no-op")
	}
	for _, c := range cs {
		if !c.Live {
			t.Errorf("%s does not declare Live: it reads a snapshot in memory, and the content changes on its own", c.ID)
		}
	}
}

// specialTableOf 取出那张卡的数据。断言放在**数据**上而不是渲染出来的 HTML 上
// （与 archmap 那边同一条）：数据是结论，渲染只是它的一种画法。
func specialTableOf(t *testing.T) view.Table {
	t.Helper()
	c := specialConcept()
	if c.ID != "gateway.special" || c.Kind != view.KindTable {
		t.Fatalf("the identity of this card changed: id=%q kind=%q", c.ID, c.Kind)
	}
	tb, ok := c.Data.(view.Table)
	if !ok {
		t.Fatalf("Data is not a view.Table, it is %T", c.Data)
	}
	return tb
}

// TestTheSpecialTableListsEveryPluginAndItsState 是这张卡的棘轮。
//
// 它存在的理由：special_treatment 会**改用户的请求**，而 `newgate st` 是回答
// 「是不是 newgate 把我的请求改坏了」的地方。只装 dashboard 的装配里那条命令
// 不存在（dist-dashboard 关掉了 cli），这张卡就是唯一的答案——所以它必须真的
// 列出**每一个**插件、并且把层开关与单插件开关都反映在状态上。
//
// 状态的说法与 CLI 同源（specialState 一处判据），这里钉的是「它确实被用上了」：
// 漏掉层开关的话，界面会把整层关掉画成「全部生效」——比不显示更坏。
func TestTheSpecialTableListsEveryPluginAndItsState(t *testing.T) {
	testkit.Sandbox(t)
	installFakePlugins(t,
		fakePlugin{name: "shape-400", why: "the upstream rejects a tail it cannot read\n(second line, which the table must not carry)"},
		fakePlugin{name: "reasoning-backfill", why: "clients drop the reasoning content"},
	)

	ps := special.Plugins()
	if len(ps) != 2 {
		t.Fatalf("this registry should hold two plugins, got %d", len(ps))
	}
	stateOf := func(tb view.Table, name string) view.Cell {
		t.Helper()
		for _, r := range tb.Rows {
			if r.Cells["plugin"].Text == name {
				return r.Cells["state"]
			}
		}
		t.Fatalf("the table has no plugin %q", name)
		return view.Cell{}
	}

	// —— 默认：层开着、没有单独关掉的 → 每一行都生效 ——
	tb := specialTableOf(t)
	if len(tb.Rows) != len(ps) {
		t.Errorf("the table has %d rows for %d registered plugins - the missing ones are invisible in the UI",
			len(tb.Rows), len(ps))
	}
	for _, p := range ps {
		if got := stateOf(tb, p.Name()); got.Text != i18n.T("active", nil) || got.Tone != view.ToneOK {
			t.Errorf("in the default state %q should be in effect (ok), got %q/%q", p.Name(), got.Text, got.Tone)
		}
		for _, r := range tb.Rows {
			if r.Cells["plugin"].Text != p.Name() {
				continue
			}
			// Why 只给第一行：完整说明有好几行，铺进表里会把表淹掉。
			if w := r.Cells["why"].Text; w == "" || strings.Contains(w, "\n") {
				t.Errorf("the why of %q should be a non-empty single line, got %q", p.Name(), w)
			}
		}
	}

	// —— 单独关掉一个：只有它那一行变，而且要说清是**哪一种**关 ——
	//
	// 断言钉的是**具体那句话与那个颜色**，不是「不是 active 就行」：两种关掉各有
	// 各的说法（「整层关了」/「单独关掉」），把两个标签写反——界面对着一个被关掉的
	// 插件说「整层都关了」——只断言「变了」的测试照样绿。
	off := ps[0].Name()
	if err := gatewaystate.SetSpecialPlugin(off, false); err != nil {
		t.Fatal(err)
	}
	tb = specialTableOf(t)
	if got := stateOf(tb, off); got.Text != i18n.T("disabled individually", nil) || got.Tone != view.ToneBad {
		t.Errorf("with %q disabled individually it should say 'disabled individually' (bad), got %q/%q", off, got.Text, got.Tone)
	}
	for _, p := range ps[1:] {
		if got := stateOf(tb, p.Name()); got.Text != i18n.T("active", nil) {
			t.Errorf("only %q was disabled, yet %q also became %q", off, p.Name(), got.Text)
		}
	}

	// —— 整层关掉：每一行都要说这件事，而且说的是「整层」 ——
	if err := gatewaystate.SetSpecialTreatment(false); err != nil {
		t.Fatal(err)
	}
	tb = specialTableOf(t)
	for _, p := range ps {
		if got := stateOf(tb, p.Name()); got.Text != i18n.T("layer off", nil) || got.Tone != view.ToneWarn {
			t.Errorf("with the whole layer off %q should say 'layer off' (warn), got %q/%q - "+
				"showing it as in effect is the lie of 'all patches are on'", p.Name(), got.Text, got.Tone)
		}
	}
}
