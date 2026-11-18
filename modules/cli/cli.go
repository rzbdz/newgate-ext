// Package cli 是命令行前端。
//
// 分层规则见 docs/03-architecture.md 与 docs/08-operations.md：
// CLI / TUI / Web 三个壳都**不允许**自己实现业务逻辑，只能调用下层。
// 一旦允许某个壳「就这一个功能自己写一下」，三端行为漂移就开始了，
// 而且不可逆——用户会发现「Web 上能删的东西 CLI 删不掉」，然后不再
// 信任任何一端。
package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/buildinfo"
	"github.com/rzbdz/newgate-ext/lib/style"
	"github.com/rzbdz/newgate/lib/i18n"
)

// usageText 组装 `newgate --help`。
//
// **界面自己不认识任何一条命令、任何一个模块**：所有命令行都由拥有那项能力的
// 模块经 HelpLine 声明，这里只做组装与排版。上一版这里写死了一张 cmd(...) 清单，
// 结果是命令搬回模块之后界面还在硬编码它们——"搬了"等于白搬，而且每加一个模块
// 都得回来改界面（那正是本次重构要拆掉的东西）。
//
// 位置由命令自己声明的 Rank 决定；节的先后由槽位规则决定（见 extension.Section
// 与 PlanSections），节内再按 Rank、Usage 排。同 Rank 时靠 Usage 兜底，保证每次
// 跑出来顺序一致。
func usageText(service *service) string {
	// 收集：谁注入的命令，就由谁声明它在 help 里长什么样、放哪个**槽位**。
	//
	// 界面在这里**不认识任何一条命令、任何一个模块**：它只认识一组通用槽位键 +
	// 一条「自定义节先到先得、抢不到进 others」的规则（extension.PlanSections）。
	// 装一个新模块从不要求改这里一行。
	type entry struct {
		declared Section
		line     HelpLine
	}
	var entries []entry
	if service != nil {
		for _, c := range service.commands.All() {
			doc, ok := c.(Documented)
			if !ok {
				continue // 可选接口：没声明就不占一行，但仍然能用
			}
			line := doc.Help()
			if line.Usage == "" {
				continue
			}
			entries = append(entries, entry{line.Section, line})
		}
	}
	var glossary []GlossaryLine
	if service != nil {
		glossary = service.glossaryLines()
	}

	// 左列的宽度**从数据算**，不再写死（2026-09-20）。
	//
	// 上一版是 `commandWidth = 38` / `termWidth = 12`——那两个数字是**英文那一版
	// 的实测值**（装了什么模块、术语行有多长），换一门语言或装一个新模块就不再
	// 成立：写窄了右列被挤成一条竖线，写宽了说明被推出版面。所以按实际内容的最宽
	// 显示宽度取（CJK 双宽，用 style.VisibleWidth），再夹进一个合理区间——区间
	// 的两端就是上面那两种坏情况，style.MaxColumns 仍是不可越过的硬上限。
	usages := make([]string, 0, len(entries))
	for _, e := range entries {
		usages = append(usages, e.line.Usage)
	}
	terms := make([]string, 0, len(glossary))
	for _, line := range glossary {
		terms = append(terms, line.Term)
	}
	commandWidth := clamp(widest(usages), 24, 40)
	termWidth := clamp(widest(terms), 8, 18)

	var b strings.Builder
	b.WriteString(style.Bold("newgate"))
	b.WriteString(" ")
	b.WriteString(i18n.T("— the semantic model layer proxy for AI CLIs", nil))
	b.WriteString("\n")

	// 左列按显示宽度补齐（CJK 双宽），右列一律暗色——扫读时先看命令名，
	// 需要时再看说明。
	cmd := func(left, right string) {
		// Usage 比左栏宽时**单独占一行**。上一版把它和 summary 硬拼在同一行，
		// verify 那条长 flags 直接变成 `...--json]给每条…`——两个机器可读/人读的
		// 东西粘在一起（2026-10-28 截图）。usage 是命令的写法，不该为了版面被截断，
		// 也不该把右栏挤没。
		if style.VisibleWidth(left) > commandWidth {
			for _, line := range wrapWords(left, style.MaxColumns-2) {
				b.WriteString("  " + style.Cyan(line) + "\n")
			}
			if right != "" {
				for _, line := range wrapWords(right, style.MaxColumns-4) {
					b.WriteString("    " + style.Dim(line) + "\n")
				}
			}
			return
		}
		// 左栏补到固定宽度后**总是**再留一格：usage 恰好占满 commandWidth 时，
		// 没有这一格，right 会紧贴着最后一个字符。
		prefix := "  " + style.Cyan(style.Pad(left, commandWidth)) + " "
		if right == "" {
			b.WriteString(prefix)
			b.WriteString("\n")
			return
		}
		lines := wrapWords(right, style.MaxColumns-2-commandWidth-1)
		for i, line := range lines {
			if i == 0 {
				b.WriteString(prefix)
				b.WriteString(style.Dim(line))
				b.WriteString("\n")
			} else {
				b.WriteString(strings.Repeat(" ", 2+commandWidth+1))
				b.WriteString(style.Dim(line))
				b.WriteString("\n")
			}
		}
	}

	// 规划按**声明顺序**做（= 命令注册顺序），因为自定义节的名额是先到先得的。
	declared := make([]Section, 0, len(entries))
	for _, e := range entries {
		declared = append(declared, e.declared)
	}
	plan := PlanSections(declared)

	// 同一个显示节里的行按 Rank 再按 Usage 排；节的先后由 plan 决定，不参与
	// Rank 竞争——否则掰小自己的 Rank 就能把整节搬到最前面，槽位表就白设了。
	grouped := map[Section][]HelpLine{}
	for _, e := range entries {
		slot := plan.Slot(e.declared)
		grouped[slot] = append(grouped[slot], e.line)
	}
	for _, slot := range plan.Order() {
		lines := grouped[slot]
		if len(lines) == 0 {
			continue
		}
		sort.Slice(lines, func(i, j int) bool {
			if lines[i].Rank != lines[j].Rank {
				return lines[i].Rank < lines[j].Rank
			}
			return lines[i].Usage < lines[j].Usage
		})
		// 标题是槽位的**显示名**，不是它的键：键是身份（ASCII 稳定标识），
		// 随语言变的是这一层（见 extension.Section.Display）。
		b.WriteString("\n")
		b.WriteString(style.Bold(slot.Display()))
		b.WriteString("\n")
		for _, line := range lines {
			cmd(line.Usage, line.Summary)
		}
	}

	// 术语表是**界面自己的**东西：用户不知道某个词是什么意思时看的字典。它不是
	// 命令行清单（那是模块的），所以留在这里。但**表里的每一行都归模块自己**——
	// 界面上一版还在写死 `profile` 与那张配置文件清单（providers.json /
	// mappings/*.kv / state.json），那两样都是 config 的词汇，界面凭什么知道。
	// 现在全部经 cliapi.Glossarist 交上来（见 modules/config/diagnostics.go）。
	//
	// 右列跟着左列一起折行：定义是模块写的，长度不受界面控制，写死一行的版式
	// 迟早会被某一条长定义顶破 75 列上限（style.MaxColumns）。
	b.WriteString("\n")
	b.WriteString(style.Bold(i18n.T("Glossary", nil)))
	b.WriteString("\n")
	term := func(left, right string) {
		// 与命令那栏同一个理由：词条恰好占满 termWidth 时（中文四字 = 8 列，
		// 正好是下限）没有这一格，定义会紧贴着词条（`配置文件providers.json`）。
		prefix := "  " + style.Pad(style.Cyan(left), termWidth) + " "
		lines := wrapWords(right, style.MaxColumns-2-termWidth-1)
		for i, line := range lines {
			if i == 0 {
				b.WriteString(prefix)
				b.WriteString(style.Dim(line))
				b.WriteString("\n")
			} else {
				b.WriteString(strings.Repeat(" ", 2+termWidth+1))
				b.WriteString(style.Dim(line))
				b.WriteString("\n")
			}
		}
	}
	for _, line := range glossary {
		term(line.Term, line.Definition)
	}
	return b.String()
}

// wrapWords 按**词边界**折行；一个词比整行还长时才退回按显示宽度切字符。
//
// 为什么不用 style.Wrap：那个是字符级的（CJK 没有空格，字符级是唯一可行的
// 通用做法），但把它用在 help 的右栏会把 `<tag>` 断成 `<ta` / `g>`、
// `--provider` 断成两截——那些是**机器标记**，断了就既 grep 不到也拷不走。
// 这里先按空格分词：英文/flag 保持完整，中文整句作为一个词，超宽时才交给
// style.Wrap 按宽度切。两边的取舍各自成立。
func wrapWords(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	var lines []string
	var cur strings.Builder
	curW := 0
	add := func(word string) {
		ww := style.VisibleWidth(word)
		if curW > 0 && curW+1+ww <= width {
			cur.WriteByte(' ')
			cur.WriteString(word)
			curW += 1 + ww
			return
		}
		if curW > 0 {
			lines = append(lines, cur.String())
			cur.Reset()
			curW = 0
		}
		if ww <= width {
			cur.WriteString(word)
			curW = ww
			return
		}
		parts := style.Wrap(word, width)
		for i, part := range parts {
			if i == len(parts)-1 {
				cur.WriteString(part)
				curW = style.VisibleWidth(part)
				continue
			}
			lines = append(lines, part)
		}
	}
	for _, word := range strings.Fields(s) {
		add(word)
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func runCLI(service *service, args []string) int {
	args = splitAtVerb(service, args)
	if shouldAuditLayout(service, args) {
		return auditLayout(args, func() int { return run(service, args) })
	}
	return run(service, args)
}

func run(service *service, args []string) int {
	if len(args) == 0 {
		fmt.Print(usageText(service))
		return 0
	}

	// 动作型选项，可出现在任意位置
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--set-profile" || strings.HasPrefix(args[i], "--set-profile="):
			name := optValue(args, i, "--set-profile")
			if name == "" {
				return die(64, i18n.T("--set-profile needs a profile name", nil))
			}
			// 认得出这个选项的形状（argv 解析是界面的活），但**语义归 config**：
			// 归一成 config 那条命令认识的形状再交给它。
			command, ok := service.moduleCommand("--set-profile")
			if !ok {
				return die(64, i18n.T("--set-profile is not implemented (this assembly has no config module)", nil))
			}
			rest := []string{name}
			if agent := findFlag(args, "--agent", "--tool", "--target"); agent != "" {
				rest = append(rest, "--agent", agent)
			}
			return command.Run(moduleCLIHost{verb: name}, rest)
		}
	}

	// 账本查表：模块注入的命令与界面自己的命令走同一条路。
	//
	// **界面在这里不认识任何一条命令**——它只知道"有人往这个账本里挂过东西"。
	// 上一版这里是一个几百行的 switch（后来是 detectLaunch + cmdLaunch），每一条
	// 都是一次「界面认识某个模块」，那批命令因此永远搬不回自己的模块。
	//
	// 前置选项也要容忍：`newgate --profile ds claude` 里第一个 token 是 newgate
	// 自己的选项，真正要执行的命令在后面。**找出那一条是 argv 解析**，所以归界面；
	// 它叫什么、怎么解析剩下的参数，归命令自己。
	if name := lookupName(args); name != "" {
		if command, ok := service.moduleCommand(name); ok {
			return command.Run(moduleCLIHost{verb: name}, dropName(args, name))
		}
	}
	return die(64, i18n.T("unknown command {cmd} (see newgate --help)", i18n.A{"cmd": args[0]}))
}

// VersionLine 是 `newgate version` 的输出（版式与取值都在 lib/buildinfo）。
func VersionLine() string { return buildinfo.VersionLine() }

// dropName 去掉 argv 里**那个动词**（第一个与 name 相同的 token），返回它的参数。
//
// 为什么不是 `args[1:]`：前置选项也要容忍（`newgate --profile ds claude`），那时
// 动词不在第 0 位上。契约说「args 里没有命令名」，那就得按名字而不是按位置剥。
func dropName(args []string, name string) []string {
	for i, a := range args {
		if a == name {
			return append(append([]string{}, args[:i]...), args[i+1:]...)
		}
	}
	return args
}

// splitAtVerb 把 `<动词>@<值>` 拆成 `<动词> @<值>` 两个 token。
//
// 为什么界面要认这个形状：`newgate upgrade@v0.9.0` 是用户会直接敲的写法（与
// `pip install x==1`、`go install x@v1` 同一种肌肉记忆），而分派键是动词本身——
// 不拆的话它是一条「未知命令 upgrade@v0.9.0」。拆开之后 `@v0.9.0` 作为**普通的
// 位置参数**交给命令，它是什么意思（版本、tag、分支）归命令自己解释；界面不认识
// 任何一个值，也不知道哪条命令收它——不认得 `@…` 的命令会按自己的规矩报错。
//
// 只在「整串不是一条命令、而 @ 前面那段是」时才拆：一条名字里本来就带 @ 的
// 命令（今天没有）照常按原样分派，不会被这条语法糖抢走。
func splitAtVerb(service *service, args []string) []string {
	if service == nil || len(args) == 0 {
		return args
	}
	name := lookupName(args)
	verb, value, ok := strings.Cut(name, "@")
	if !ok || verb == "" || value == "" {
		return args
	}
	if _, exact := service.moduleCommand(name); exact {
		return args
	}
	if _, known := service.moduleCommand(verb); !known {
		return args
	}
	out := make([]string, 0, len(args)+1)
	for i, a := range args {
		if a == name {
			out = append(out, verb, "@"+value)
			return append(out, args[i+1:]...)
		}
		out = append(out, a)
	}
	return args
}

// lookupCommand 按同一套名字解析规则查账本。
func lookupCommand(service *service, args []string) (Command, bool) {
	if service == nil || len(args) == 0 {
		return nil, false
	}
	return service.moduleCommand(lookupName(args))
}

// lookupName 找出这串 argv 里**该由哪条命令处理**：跳过 newgate 自己的前置选项
// 与它们的值，取第一个位置参数；都没有就退回 args[0]（让未知命令的报错带上它）。
//
// 只认 `--profile` / `--preset` / `--agent` 这类「后面跟一个值」的选项形状。
// 这个名单很短而且稳定：它是 newgate 自己的全局选项，不是命令的参数。
func lookupName(args []string) string {
	skipNext := false
	for _, a := range args {
		switch {
		case skipNext:
			skipNext = false
		case a == "--profile" || a == "--preset" || a == "--agent":
			skipNext = true
		case strings.HasPrefix(a, "-"):
			// 其它选项（--force / -v / --json …）由命令自己解析，跳过。
		default:
			return a
		}
	}
	return args[0]
}

// ---------- 小工具 ----------

// die 是界面自己的报错出口（版式在 lib/style，各模块共用同一份）。
func die(code int, msg string) int { return style.Die(code, msg) }

// clamp 把一个宽度夹进 [lo, hi]——`--help` 两列的宽度都从数据算，算出来的值
// 需要有个合理区间兜着（见 usageText）。
func clamp(w, lo, hi int) int {
	if w < lo {
		return lo
	}
	if w > hi {
		return hi
	}
	return w
}

// widest 一组字符串里最宽的**显示宽度**（CJK 双宽，判据在 lib/style）。
//
// 为什么不是 len()：`接管` 的字节数是 6、列宽是 4；按字节算出来的列宽会把版面
// 撑歪，而那正是「换了语言看起来就不对」的一半来源。
func widest(ss []string) int {
	w := 0
	for _, s := range ss {
		if v := style.VisibleWidth(s); v > w {
			w = v
		}
	}
	return w
}

func optValue(args []string, i int, name string) string {
	if strings.Contains(args[i], "=") {
		return strings.SplitN(args[i], "=", 2)[1]
	}
	if i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func findFlag(args []string, names ...string) string {
	for i, a := range args {
		for _, n := range names {
			if a == n && i+1 < len(args) {
				return args[i+1]
			}
			if strings.HasPrefix(a, n+"=") {
				return strings.SplitN(a, "=", 2)[1]
			}
		}
	}
	return ""
}
