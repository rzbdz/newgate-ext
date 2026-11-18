package pluginmanager

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/lib/durarg"
	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// command 是 `newgate plugin`。
//
// **它住在这里而不是 modules/cli**：这是「everything is module」的直接推论——
// 命令是这个模块的用户界面，模块自己提供它，通过 cli.RegisterCommand 注入。
// 2026-09-18 修过一次真实的违规：那一版把它写在 modules/cli 里，理由是「注册
// 命令要拿到 cli 的端口，而 cli 要渲染列表又得依赖 plugin-manager，成环」——
// 那个环是**自己造的**，因为它同时让 cli 去依赖本模块。正确的拆法是反过来：
// 让 cli 不必认识本模块（状态行走 cli.RegisterStatus 由本模块自报）。
//
// **注入用 modules.Optional，不是 Need**（照抄「注册命令就 Need(cli)」会被
// app/graph_test.go 的 TestUIStaysOutOfTheDependencyGraph 当场拦下）：弱依赖的
// 意思是「界面在就注册进去，不在就跳过」——本模块的功能一个都不少，只是没有入口。
// 它仍然是一条**排序边**，所以本模块的 Start 一定在界面之后就绪（见
// component.Optional；那边记着这条边从 Optional → Inject → Optional 的来回）。
type command struct{ manager Manager }

var (
	_ cliapi.Command        = (*command)(nil)
	_ cliapi.Documented     = (*command)(nil)
	_ cliapi.StatusProvider = (*command)(nil)
)

func (c *command) Names() []string { return []string{"plugin", "plugins"} }

func (c *command) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionModules,
		Usage:   i18n.T("plugin [module[.path]] [on|off] [duration]", nil),
		Summary: i18n.T("list all modules by category; toggle a module or one of its switch points", nil),
	}
}

// Run 的用法（**args 里没有 "plugin" 这个动词**，分派器已经剥掉了，见
// cliapi.Command 的契约说明）：
//
//	newgate plugin                          按分类列出全部模块
//	newgate plugin <模块>                    展开一个模块：它的开关点与当前状态
//	newgate plugin <模块> on|off [时长]       开/关这个模块的**全部**开关点
//	newgate plugin <模块>.<路径> on|off [时长] 只开/关一个开关点
//
// 列表的枚举源是**组件图**（经本模块合并），不是「谁上报过」。这样没参与开关
// 体系的模块也列得出来，标成「无法 runtime 开关（v1）」——「这个模块没有开关」
// 和「这个模块忘了注册」绝不能长得一样。
func (c *command) Run(host cliapi.Host, args []string) int {
	sub := cliapi.Positional(args, 0)
	switch sub {
	case "", "ls", "list":
		return c.list()
	}
	action := cliapi.Positional(args, 1)
	if action == "" {
		return c.explain(host, sub)
	}
	if action != "on" && action != "off" {
		return host.Die(64, i18n.T(
			"plugin: unknown verb \"{verb}\" (usage: newgate plugin <module>[.<path>] on|off [duration])",
			i18n.A{"verb": action}))
	}
	return c.toggle(host, sub, action == "on", cliapi.Positional(args, 2))
}

// list 按分类分组列出全部模块。
func (c *command) list() int {
	modules := c.manager.Modules()
	st := store.LoadState()

	total, switchPoints := 0, 0
	nameW := 14
	for _, m := range modules {
		total++
		switchPoints += len(m.Switches)
		// 列宽跟着最长的名字走：模块名里有 19 字符的（claudecode-deepseek），
		// 写死宽度会让它把右边的字挤在一起。
		if w := style.VisibleWidth(m.Name); w > nameW {
			nameW = w
		}
	}
	fmt.Println(style.Title("newgate plugin",
		i18n.T("{modules} modules · {switches} runtime switch points", i18n.A{
			"modules": total, "switches": switchPoints})))

	for _, typ := range DisplayOrder() {
		var group []Module
		for _, m := range modules {
			if groupOf(m.Type) == typ {
				group = append(group, m)
			}
		}
		if len(group) == 0 {
			continue
		}
		// 组内按名字排序：分组顺序（DisplayOrder）是有意义的，组内顺序不是——
		// 拿拓扑启动顺序排会让人每次都要重新找一遍。
		sort.Slice(group, func(i, j int) bool { return group[i].Name < group[j].Name })
		// Section 自带前导换行、没有结尾换行：Println 正好补上后者，
		// 于是每组之前恰好一个空行（用 Print 会让标题和第一行粘在一起）。
		fmt.Println(style.Section(string(typ)))
		for _, m := range group {
			printModuleLine(st, m, nameW)
		}
	}

	fmt.Println()
	fmt.Println(style.Hint(i18n.T("expand one module:  newgate plugin <module>", nil)))
	fmt.Println(style.Hint(i18n.T("toggle one point:   newgate plugin <module>.<path> on|off [5m|forever]", nil)))
	return 0
}

// printModuleLine 渲染列表里的一行：模块名 + 它的开关点概览。
func printModuleLine(st *domain.State, m Module, nameW int) {
	name := style.Pad(m.Name, nameW) + " "
	// 描述单独一行，跟在模块名后面。
	//
	// 为什么不接在模块名那一行的尾巴上：那一行还要放第一个开关点，而描述是**成句
	// 的话**（十几到几十个字），挤在一起两边都读不成——折行边界会随终端宽度跳，
	// 开关点那半截会跑到描述后面去。描述独占一行之后，两个宽度各自独立。
	if m.Desc != "" {
		fmt.Println("  " + name + style.Dim(m.Desc))
		name = style.Pad("", nameW) + " "
	}
	if len(m.Switches) == 0 {
		fmt.Println("  " + name + style.Dim(i18n.T("cannot be toggled at runtime (v1)", nil)))
		return
	}
	var parts []string
	for _, sw := range m.Switches {
		parts = append(parts, style.Dim(shortPath(sw.Path))+" "+switchState(st, sw))
	}
	// 第一段跟在模块名后面，其余折到下一行的缩进里——避免一行超过 MaxColumns。
	fmt.Println("  " + name + parts[0])
	for _, p := range parts[1:] {
		fmt.Println("  " + style.Pad("", nameW) + " " + p)
	}
}

// explain 展开一个模块或一个开关点。
func (c *command) explain(host cliapi.Host, target string) int {
	st := store.LoadState()

	if sw, ok := c.manager.Lookup(target); ok {
		return explainSwitch(host, st, sw)
	}

	m, ok := findModule(c.manager, target)
	if !ok {
		return host.Die(65, i18n.T(
			"no module or switch point named \"{target}\" (see the list with newgate plugin)",
			i18n.A{"target": target}))
	}
	if len(m.Switches) == 0 {
		fmt.Println(style.Title("newgate plugin "+m.Name, string(m.Type)))
		fmt.Println(style.Rule(72))
		fmt.Println(style.Item(style.Skip,
			i18n.T("cannot be toggled at runtime: this module reports no switch points", nil)))
		fmt.Println(style.Hint(i18n.T("v1 only covers switches a module reports itself; "+
			"a module that reports none cannot be toggled at runtime", nil)))
		return 0
	}
	fmt.Println(style.Title("newgate plugin "+m.Name, string(m.Type)))
	fmt.Println(style.Rule(72))
	for _, sw := range m.Switches {
		fmt.Println(style.Item(style.OK, style.Bold(sw.Path)+"  "+switchState(st, sw)))
		fmt.Println("    " + style.Dim(sw.Title))
		fmt.Println("    " + style.Dim(i18n.T("what turning it off does: {why}", i18n.A{"why": sw.Why})))
		if sw.Danger != DangerSafe {
			fmt.Println("    " + dangerColor(sw.Danger)(
				i18n.T("danger level {level}", i18n.A{"level": sw.Danger})))
		}
	}
	fmt.Println()
	fmt.Println(style.Hint(i18n.T("toggle the whole module: newgate plugin {name} off",
		i18n.A{"name": m.Name})))
	return 0
}

func explainSwitch(host cliapi.Host, st *domain.State, sw Switch) int {
	fmt.Println(style.Title("newgate plugin "+sw.Path, string(sw.Danger)))
	fmt.Println(style.Rule(72))
	fmt.Println(style.Item(style.OK, sw.Title))
	fmt.Println(style.Item(style.Skip, i18n.T("now: {state}", i18n.A{"state": switchState(st, sw)})))
	fmt.Println(style.Item(style.Skip, i18n.T("what turning it off does: {why}", i18n.A{"why": sw.Why})))
	if sw.Danger == DangerFootgun {
		fmt.Println(style.Item(style.Bad,
			i18n.T("this is a footgun: turning it on/off needs a time limit, forever is not accepted", nil)))
	}
	verb := "off"
	if !sw.Default {
		verb = "on"
	}
	fmt.Println()
	fmt.Println(style.Hint(i18n.T("change it: newgate plugin {path} {verb} [duration]",
		i18n.A{"path": sw.Path, "verb": verb})))
	return 0
}

// toggle 改一个或一组开关点。
func (c *command) toggle(host cliapi.Host, target string, on bool, dur string) int {
	switches, err := c.targetsOf(target)
	if err != nil {
		return host.Die(65, err.Error())
	}

	ttl, forever, derr := switchTTL(dur)
	if derr != nil {
		return host.Die(64, derr.Error())
	}

	st := store.LoadState()

	var changed []string
	for _, sw := range switches {
		// **写语义只有一份**（SetSwitch）：出厂态决定走哪张表、动作等于出厂态就是
		// 撤销这条设定、footgun 不接受永久——这几条错了，命令行与浏览器就会对同一
		// 份配置给出不同答案。所以这里不重写一遍，只负责落盘与通知。
		//
		// 它返回的 error 目前只有两种：footgun 说了「永久」（用法错误，64），以及
		// 配置序列化失败（理论上到不了——里面只有字符串与时间）。
		if err := SetSwitch(st, sw, on, ttl, forever); err != nil {
			return host.Die(64, err.Error())
		}
		changed = append(changed, sw.Path)
	}

	if serr := store.SaveState(st); serr != nil {
		return host.Die(70, serr.Error())
	}
	host.NotifyProxy()

	// 这里的「已打开/已关闭」与 switchState 那组（已开/开/已关/关）是**两句话**：
	// 键 = 原文，两个位置的中文不同就必须写两句英文，否则译文只能二选一。
	mark, word := style.OK, i18n.T("switched on", nil)
	if !on {
		mark, word = style.Warn, i18n.T("switched off", nil)
	}
	fmt.Println(style.Item(mark, fmt.Sprintf("%s %s", word, strings.Join(changed, ", "))))
	after := store.LoadState()
	for _, sw := range switches {
		if until := Remaining(after, sw.Path); !until.IsZero() {
			fmt.Println(style.Hint(i18n.T("  {path} reverts automatically in {left}", i18n.A{
				"path": sw.Path,
				"left": durarg.Format(int(time.Until(until).Seconds()))})))
		}
	}
	if !on {
		fmt.Println(style.Hint(i18n.T("revert it: newgate plugin {target} on",
			i18n.A{"target": target})))
	}
	return 0
}

// targetsOf 把用户给的目标解析成一组开关点。带点的是单个路径，不带点的是模块名
// （模块名里没有点——组件名用连字符，所以这条判据不会歧义）。
func (c *command) targetsOf(target string) ([]Switch, error) {
	if strings.Contains(target, ".") {
		sw, ok := c.manager.Lookup(target)
		if !ok {
			return nil, i18n.E(
				"no switch point named \"{target}\" (see the list with newgate plugin)",
				i18n.A{"target": target})
		}
		return []Switch{sw}, nil
	}
	m, ok := findModule(c.manager, target)
	if !ok {
		return nil, i18n.E(
			"no module named \"{target}\" (see the list with newgate plugin)",
			i18n.A{"target": target})
	}
	if len(m.Switches) == 0 {
		return nil, i18n.E(
			"module \"{target}\" reports no switch points, cannot be toggled at runtime (a v1 limitation)",
			i18n.A{"target": target})
	}
	return m.Switches, nil
}

func findModule(manager Manager, name string) (Module, bool) {
	for _, m := range manager.Modules() {
		if m.Name == name {
			return m, true
		}
	}
	return Module{}, false
}

// switchTTL 解析时长参数。空串 = 用开关点自己的默认 TTL；"forever" = 不限时。
func switchTTL(s string) (time.Duration, bool, error) {
	switch s {
	case "":
		return 0, false, nil
	case "forever":
		return 0, true, nil
	}
	d, err := durarg.Parse(s)
	if err != nil {
		return 0, false, i18n.E(
			"plugin: unknown duration \"{value}\" (supported: 30s / 2m / 2min / 1h / forever)",
			i18n.A{"value": s})
	}
	return d, false, nil
}

func setEntry(table map[string]Entry, path string, until time.Time) map[string]Entry {
	if table == nil {
		table = map[string]Entry{}
	}
	table[path] = Entry{Until: until}
	return table
}

func dropEntry(table map[string]Entry, path string) map[string]Entry {
	delete(table, path)
	return table
}

// rawConfig 取 state.json 里 plugin_manager 那一段原始字节。
func rawConfig(st *domain.State) []byte {
	if st == nil {
		return nil
	}
	return st.ModuleConfig[StateKey]
}

// groupOf 把未知分类归到 others。**不报错**：分类是产品概念，会随版本长出新成员，
// 硬拒绝会让一个新模块因为用了个新分类词就把整个列表打崩，而它其实只是想被分到
// 「其它」里。约定俗成 + 留余量。
func groupOf(t Type) Type {
	for _, known := range DisplayOrder() {
		if t == known {
			return t
		}
	}
	return TypeOthers
}

// shortPath 列表里只显示模块名之后的那一段（模块名已经在左边一列了）。
func shortPath(path string) string {
	if i := strings.Index(path, "."); i >= 0 {
		return path[i+1:]
	}
	return path
}

// switchState 渲染一条开关点现在的状态。
func switchState(st *domain.State, sw Switch) string {
	until := Remaining(st, sw.Path)
	suffix := ""
	if !until.IsZero() {
		suffix = style.Dim(i18n.T("({left} left)",
			i18n.A{"left": durarg.Format(int(time.Until(until).Seconds()))}))
	}
	// 「已关/开」与「已开/关」：出厂开着的是 kill switch（写 Off 表），出厂关着
	// 的是 mode（写 On 表）。英文用 turned off/on 与 on/off 保住这层区别。
	if sw.Default {
		if Off(st, sw.Path) {
			return dangerColor(sw.Danger)(i18n.T("turned off", nil)) + suffix
		}
		return style.Dim(i18n.T("on", nil)) + suffix
	}
	if On(st, sw.Path) {
		return dangerColor(sw.Danger)(i18n.T("turned on", nil)) + suffix
	}
	return style.Dim(i18n.T("off", nil)) + suffix
}

func dangerColor(d Danger) func(string) string {
	switch d {
	case DangerFootgun:
		return style.Red
	case DangerQuirk:
		return style.Yellow
	default:
		return style.Green
	}
}
