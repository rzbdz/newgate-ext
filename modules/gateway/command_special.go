package gateway

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/style"
	"github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate/lib/i18n"

	"github.com/rzbdz/newgate-ext/modules/gateway/special"
)

// specialCommand 是 `newgate st`：special_treatment 插件层的清单与开关。
//
// **它住在这里而不是 modules/cli**：这一层的插件是网关数据面的一部分，插件名、
// Why、开关语义全是 gateway 自己的知识。2026-09-18 之前它写在 cli 里，于是 cli
// 要 import gateway/special 并**直接读那个包的级注册表**（`special.Plugins()`）——
// 绕过 capability 去读别人的 owner 状态，正是 everything is module 要消灭的那种
// 依赖（同一条也适用于 `probe` / `metrics` / `schema-repair`）。搬回来之后 cli
// 不必认识 special 这个包，gateway 也能自己决定这些命令长什么样。
//
// 用法：
//
//	newgate st                 插件清单：一行一个，为什么存在
//	newgate st <插件>          单个插件的完整说明
//	newgate st on|off          整层开关
//	newgate st on|off <插件>   单独开关一个
//
// 为什么值得有这个命令：这一层会**改用户的请求**。用户排查「上游报错是不是
// newgate 改坏的」时，必须能一眼看到有哪些补丁在生效、并且能逐个关掉验证。
type specialCommand struct{}

var (
	_ cliapi.Command    = (*specialCommand)(nil)
	_ cliapi.Documented = (*specialCommand)(nil)
)

func (specialCommand) Names() []string {
	return []string{"st", "special", "special-treatment", "special_treatment"}
}

func (specialCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionObserve, Rank: rankObserve,
		Usage:   i18n.T("st [on|off] [plugin]", nil),
		Summary: i18n.T("special_treatment switches and descriptions", nil),
	}
}

// Run 收到的 args **不含 "st" 这个动词**（见 cliapi.Command 的契约）。
func (specialCommand) Run(host cliapi.Host, args []string) int {
	st := store.LoadState()
	sub := cliapi.Positional(args, 0)
	name := cliapi.Positional(args, 1)

	if sub == "" || sub == "status" || sub == "ls" {
		return specialList(host, st)
	}
	// 直接给插件名 = 看它的完整说明（Why 常常是好几行，铺在清单里会淹掉）
	if p := findPlugin(sub); p != nil {
		return specialExplain(host, st, sub)
	}

	if sub != "on" && sub != "off" {
		return host.Die(64, i18n.T("No plugin named {name} (run newgate st for the list); switch the whole layer with newgate st on|off",
			i18n.A{"name": strconv.Quote(sub)}))
	}
	on := sub == "on"

	if name == "" {
		if err := gatewaystate.SetSpecialTreatment(on); err != nil {
			return host.Die(70, err.Error())
		}
		if on {
			fmt.Println(style.Item(style.OK, i18n.T("special_treatment layer enabled", nil)))
		} else {
			fmt.Println(style.Item(style.Skip, i18n.T("special_treatment layer disabled", nil)) +
				style.Dim(i18n.T("   Requests forwarded unchanged, upstream quirks no longer patched", nil)))
		}
		host.NotifyProxy()
		return 0
	}

	if findPlugin(name) == nil {
		return host.Die(65, i18n.T("No plugin named {name} (run newgate st for the list)",
			i18n.A{"name": strconv.Quote(name)}))
	}
	if err := gatewaystate.SetSpecialPlugin(name, on); err != nil {
		return host.Die(70, err.Error())
	}
	if on {
		fmt.Println(style.Item(style.OK, i18n.T("Plugin {name} enabled", i18n.A{"name": name})))
	} else {
		fmt.Println(style.Item(style.Skip, i18n.T("Plugin {name} disabled", i18n.A{"name": name})))
	}
	if !gatewaystate.SpecialEnabled(st) {
		fmt.Println(style.Hint(i18n.T("The whole layer is still off; run newgate st on first", nil)))
	}
	host.NotifyProxy()
	return 0
}

func findPlugin(name string) special.Plugin {
	for _, p := range special.Plugins() {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

// specialState 单个插件此刻的状态：层开关 + 单插件开关。
//
// 两个界面共用它：CLI 印那个 mark，web 卡片把同一个 mark 翻成颜色（specialTone）。
// 判据只有这一处——「什么算生效」不该在两张界面上各写一遍，那迟早会各说各话。
func specialState(st *domain.State, name string) (mark, word string) {
	switch {
	case !gatewaystate.SpecialEnabled(st):
		return style.Skip, i18n.T("layer off", nil)
	case gatewaystate.PluginOff(st, name):
		return style.Bad, i18n.T("disabled individually", nil)
	}
	return style.OK, i18n.T("active", nil)
}

// specialTone 把 CLI 那个 mark 翻成 web 卡片的颜色。
//
// **一对一，不重新判断严重度**：生效 → ok，整层关掉 → warn，单独关掉一个 → bad，
// 与终端上那个 mark 说的是同一件事。这条不是审美：`runtime/view.go` 写着「两个
// 界面对同一份磁盘状态给出不同答案，比其中一个不显示更糟」——把 CLI 的 ✗ 在网页上
// 画成黄色，就是那个「不同答案」，而且是**最坏的一种**：两边的文字一样，只有严重度
// 不一样，看的人先信了颜色，再看文字时已经晚了。
//
// tone 的词表只有三个（ok/warn/bad，见 lib/view），而 mark 有四个：style.Skip
// （整层关掉的 `·`）没有对应的 tone，落到 warn 上——「这些补丁现在都不在」该被看见，
// 但它不是 bad：那一档已经被「单独关掉」占着了。
func specialTone(mark string) string {
	switch mark {
	case style.OK:
		return view.ToneOK
	case style.Bad:
		return view.ToneBad
	default:
		return view.ToneWarn
	}
}

func specialList(host cliapi.Host, st *domain.State) int {
	ps := special.Plugins()
	layer := style.Green(i18n.T("on", nil))
	if !gatewaystate.SpecialEnabled(st) {
		layer = style.Yellow(i18n.T("off (whole layer)", nil))
	}
	fmt.Println(style.Title("newgate st", i18n.N("special_treatment {layer} · {n} plugin",
		"special_treatment {layer} · {n} plugins", len(ps), i18n.A{"layer": layer})))
	fmt.Println(style.Rule(72))
	if len(ps) == 0 {
		fmt.Println(style.Hint(i18n.T("No plugins registered", nil)))
		return 0
	}
	t := style.NewTable(i18n.T("State", nil), i18n.T("Plugin", nil), i18n.T("Why it exists", nil))
	for _, p := range ps {
		mark, _ := specialState(st, p.Name())
		why := strings.SplitN(p.Why(), "\n", 2)[0]
		t.Row(style.Mark(mark), p.Name(), style.Dim(why))
	}
	fmt.Print(t.String())
	fmt.Println(style.Hint(i18n.T("Upstream quirk patches: they apply only to the upstream that claimed the request, and every change is logged", nil)))
	fmt.Println(style.Hint(i18n.T("Full description: newgate st <plugin> · disable one: newgate st off <plugin> · disable the layer: newgate st off", nil)))
	// 推理缓存的计数也归本模块报（见 thinkcache_status.go）：以前它经
	// cliapi.Host 的口子绕一圈回界面渲染，那条口子的名字（PrintThinkCache）
	// 本身就是「界面认识了一个模块概念」的证据。
	printThinkCache()
	return 0
}

func specialExplain(host cliapi.Host, st *domain.State, name string) int {
	p := findPlugin(name)
	mark, word := specialState(st, name)
	fmt.Println(style.Title("newgate st "+name, word))
	fmt.Println(style.Rule(72))
	fmt.Println(style.Item(mark, p.Why()))
	fmt.Println()
	if gatewaystate.PluginOff(st, name) {
		fmt.Println(style.Hint(i18n.T("Enable: newgate st on {name}", i18n.A{"name": name})))
	} else {
		fmt.Println(style.Hint(i18n.T("Disable: newgate st off {name}", i18n.A{"name": name})))
	}
	return 0
}
