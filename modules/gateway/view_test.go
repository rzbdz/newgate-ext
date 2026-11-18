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
		t.Errorf("整个文件都读得下，该原样给出: %q", got)
	}
	if truncated {
		t.Error("一条都没丢，不该说 truncated")
	}

	// 只要最后 3 行：上面那两行**确实**没给出去，所以 truncated 该是 true——
	// 「你看的不是全部」这件事不说是会骗人的（读的人会以为日志从这里开始）。
	lines, truncated, err = tailLines(p, 3, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lines, "|"); got != "c|d|e" {
		t.Errorf("尾巴取错了: %q", got)
	}
	if !truncated {
		t.Error("上面还有两行没给出来，该说 truncated")
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
		t.Error("被字节上限切过，该说 truncated——不说的话用户以为日志只有这么长")
	}
	if len(lines) == 0 || lines[len(lines)-1] != "last line" {
		t.Errorf("尾巴的最后一行该是最新的那条，实际 %q", lines[len(lines)-1:])
	}
	// 第一行多半是半截的（被 limit 切开），必须丢掉——留下它，读的人会拿一段
	// 没有开头的输出当证据。
	if strings.Contains(lines[0], "x") && len(lines[0]) != 100 {
		t.Errorf("第一行是半截的: %q", lines[0])
	}
}

func TestTailLinesOnMissingFile(t *testing.T) {
	_, _, err := tailLines(filepath.Join(t.TempDir(), "nope.log"), 10, 1024)
	if err == nil {
		t.Error("文件不存在该报错——调用方据此说「还没有日志」而不是「日志是空的」")
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
			t.Errorf("redactLine(%q) = %q，想要 %q", in, got, want)
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
			t.Fatalf("装假插件 %q: %v", p.Name(), err)
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
		t.Fatal("网关一张卡都没有——这条断言等于没跑")
	}
	for _, c := range cs {
		if !c.Live {
			t.Errorf("%s 没有声明 Live：它读的是内存里的一把，而内容会自己变", c.ID)
		}
	}
}

// specialTableOf 取出那张卡的数据。断言放在**数据**上而不是渲染出来的 HTML 上
// （与 archmap 那边同一条）：数据是结论，渲染只是它的一种画法。
func specialTableOf(t *testing.T) view.Table {
	t.Helper()
	c := specialConcept()
	if c.ID != "gateway.special" || c.Kind != view.KindTable {
		t.Fatalf("这张卡的身份变了: id=%q kind=%q", c.ID, c.Kind)
	}
	tb, ok := c.Data.(view.Table)
	if !ok {
		t.Fatalf("Data 不是 view.Table，是 %T", c.Data)
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
		t.Fatalf("这本注册表该有两枚插件，实际 %d", len(ps))
	}
	stateOf := func(tb view.Table, name string) view.Cell {
		t.Helper()
		for _, r := range tb.Rows {
			if r.Cells["plugin"].Text == name {
				return r.Cells["state"]
			}
		}
		t.Fatalf("表里没有插件 %q", name)
		return view.Cell{}
	}

	// —— 默认：层开着、没有单独关掉的 → 每一行都生效 ——
	tb := specialTableOf(t)
	if len(tb.Rows) != len(ps) {
		t.Errorf("表里有 %d 行，注册了 %d 个插件——少的那几个用户在界面上看不见",
			len(tb.Rows), len(ps))
	}
	for _, p := range ps {
		if got := stateOf(tb, p.Name()); got.Text != i18n.T("active", nil) || got.Tone != view.ToneOK {
			t.Errorf("默认状态下 %q 该是生效(ok)，实际 %q/%q", p.Name(), got.Text, got.Tone)
		}
		for _, r := range tb.Rows {
			if r.Cells["plugin"].Text != p.Name() {
				continue
			}
			// Why 只给第一行：完整说明有好几行，铺进表里会把表淹掉。
			if w := r.Cells["why"].Text; w == "" || strings.Contains(w, "\n") {
				t.Errorf("%q 的 why 该是单行非空，实际 %q", p.Name(), w)
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
		t.Errorf("单独关掉 %q 之后该说「disabled individually」(bad)，实际 %q/%q", off, got.Text, got.Tone)
	}
	for _, p := range ps[1:] {
		if got := stateOf(tb, p.Name()); got.Text != i18n.T("active", nil) {
			t.Errorf("只关了 %q，%q 却也变成了 %q", off, p.Name(), got.Text)
		}
	}

	// —— 整层关掉：每一行都要说这件事，而且说的是「整层」 ——
	if err := gatewaystate.SetSpecialTreatment(false); err != nil {
		t.Fatal(err)
	}
	tb = specialTableOf(t)
	for _, p := range ps {
		if got := stateOf(tb, p.Name()); got.Text != i18n.T("layer off", nil) || got.Tone != view.ToneWarn {
			t.Errorf("整层关掉之后 %q 该说「layer off」(warn)，实际 %q/%q——"+
				"画成生效是「全部补丁都在」的谎", p.Name(), got.Text, got.Tone)
		}
	}
}
