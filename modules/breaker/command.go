package breaker

// 本文件是 `newgate breaker`：**现在哪些 binding 出问题了、为什么**。
// 2026-09-18 从 modules/cli/breaker.go 整体搬来。
//
// 为什么搬：这张表就是本模块的健康账本本身——状态、账本、冷却到期时刻、原因、
// 试探在不在飞，全是本模块写进去的字段。它留在界面时，界面得认识 Status 的每个
// 字段才画得出这张表。
//
// 命令由本模块在 Start 里注册进界面：本模块对 ui 声明一条**弱依赖**
// （Optional(cli)，见 component.Optional），于是它排在界面之后——Start 跑到这里
// 时界面的账本已经就绪。界面**不依赖本模块**（它只读控制面文档），所以这条边
// 不可能成环。

import (
	"fmt"
	"time"

	"github.com/rzbdz/newgate-ext/lib/style"
	"github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// 观测那一节的位置（与原来写在界面里时一致）。
const rankBreaker = 40

type breakerCommand struct{}

var (
	_ cliapi.Command    = (*breakerCommand)(nil)
	_ cliapi.Documented = (*breakerCommand)(nil)
)

func (breakerCommand) Names() []string { return []string{"breaker", "breakers"} }

func (breakerCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionObserve, Rank: rankBreaker,
		Usage:   "breaker",
		Summary: i18n.T("which bindings are tripped, why, and for how long", nil)}
}

func (breakerCommand) Run(host cliapi.Host, _ []string) int { return runBreaker(host) }

// cmdBreaker 只回答一件事：**现在哪些 binding 出问题了、为什么**。
//
// 为什么单独立一条命令，不并进 `metrics`：`metrics` 的「模型健康」段落
// （printModelHealth）故意把卡顿/不可用的详情藏起来只说个数量，理由写在
// 那段注释里——避免体检退化成日志墙。但排查「deepseek 怎么又被摘了」这
// 类问题时，恰恰要看那几条被藏起来的详情：什么时候摘的、还要等多久、
// 连续失败几次、记在哪本账上、上一次 probe 给的评级与延迟。
//
// 2026-09-17 半开重构后这条命令分两段：
//
//   - 被摘牌的：状态、账本、冷却到期时刻、原因、试探在不在飞；
//   - 只计数没摘牌的：请求形状错误（永不摘牌）和还没到阈值的失败。
//     第二段是这轮新加的可见面——以前「失败 1 次、闸还没开」在快照里
//     根本不存在，而形状错误只躺在 metrics 计数器里，用户看到「明明
//     能用却被摘了」查不到原因。
func runBreaker(host cliapi.Host) int {
	info, ps := controlplane.State()
	if info == nil {
		// Die 的散文由**调用点**过 i18n：style 只管版式（"newgate: " 前缀是程序名，
		// 属于机器标记，不翻）。
		return host.Die(69, i18n.T("the proxy is not running (newgate start) — "+
			"the breaker table lives in the daemon's memory", nil))
	}
	if ps == nil {
		return host.Die(69, i18n.T("cannot reach the proxy at 127.0.0.1:{port} (newgate doctor)",
			i18n.A{"port": info.Port}))
	}

	var open, counted []Status
	for _, b := range ps.Breakers {
		switch {
		case b.Open:
			open = append(open, b)
		case b.Fails > 0 || b.ShapeSkips > 0 || b.Spared > 0:
			counted = append(counted, b)
		}
	}

	if len(open) == 0 && len(counted) == 0 {
		fmt.Println(style.Title("newgate breaker", fmt.Sprintf("pid %d", info.PID)))
		fmt.Println(style.Hint(i18n.T("no bindings are in trouble", nil)))
		return 0
	}

	fmt.Println(style.Title("newgate breaker",
		i18n.T("pid {pid} · {tripped} tripped · {counted} counted only", i18n.A{
			"pid": info.PID, "tripped": len(open), "counted": len(counted)})))

	if len(open) > 0 {
		t := style.NewTable("binding", i18n.T("State", nil), i18n.T("Ledger", nil),
			i18n.T("Tripped ago", nil), i18n.T("Waiting", nil),
			i18n.T("Failures", nil), i18n.T("Reason", nil))
		for _, b := range open {
			t.Row(b.Provider+"/"+b.Model, stateLabel(b), ruleLabel(b),
				agoLabel(b.OpenedAt), untilLabel(b),
				fmt.Sprintf("%d", b.Fails), b.Reason)
		}
		fmt.Print(t.String())
		fmt.Println()
		fmt.Print(probeTable(open))
	}

	if len(counted) > 0 {
		fmt.Println()
		fmt.Println(style.Section(i18n.T("counted only, not tripped", nil)) +
			style.Dim(i18n.T("   these bindings are still in the chain", nil)))
		t := style.NewTable("binding", i18n.T("Ledger", nil), i18n.T("Failures", nil),
			i18n.T("Shape errors", nil), i18n.T("Spared", nil), i18n.T("Last probe", nil))
		for _, b := range counted {
			t.Row(b.Provider+"/"+b.Model, ruleLabel(b),
				fmt.Sprintf("%d", b.Fails), shapeLabel(b), sparedLabel(b), probeLabel(b))
		}
		fmt.Print(t.String())
	}

	fmt.Println()
	if spared := totalSpared(ps.Breakers); spared > 0 {
		// 「救回」是这轮新增的可见面：上闸前那次诊断探活把 binding 从摘牌边缘
		// 拉了回来。数字不回零（跨重启累计），所以它回答的是「这条链一共被
		// 误判过几次」，而不是「现在还有几次没清」。
		fmt.Println(style.Hint(i18n.T(
			"diagnostic probes spared {n} bindings in total: real traffic reached the threshold, "+
				"but an active probe proved it still works, so the counter was cleared and it was not tripped",
			i18n.A{"n": spared})))
	}
	// 半开之后解封不再只有 probe 一条路：真实流量在冷却期满后会自动被放行
	// 一次做试探，成了就合闸。probe 仍然是**立刻**改结论的手段。
	fmt.Println(style.Hint(i18n.T("once the cooldown expires, one real request is let through "+
		"as a trial: success closes the breaker, failure re-trips it and doubles the cooldown "+
		"(capped at 10 minutes)", nil)))
	fmt.Println(style.Hint(i18n.T("  newgate probe        # change the verdict right now with "+
		"an active probe, without waiting for the cooldown", nil)))
	fmt.Println(style.Hint(i18n.T("  newgate tier <tier>   # see where this binding ranks "+
		"in the chain, and whether it is skipped", nil)))
	return 0
}

// probeTable 单独排一段：probe 结论是**另一个来源**（主动探活 vs 真实流量），
// 混在摘牌原因里会让人以为是同一件事。
func probeTable(rows []Status) string {
	t := style.NewTable("binding", i18n.T("Last probe", nil),
		i18n.T("Latency", nil), i18n.T("Checked", nil))
	for _, b := range rows {
		t.Row(b.Provider+"/"+b.Model, probeLabel(b), latencyLabel(b), checkedLabel(b))
	}
	return t.String()
}

func probeLabel(b Status) string {
	if b.Checked.IsZero() || b.Grade == "" {
		return style.Dim(i18n.T("never probed", nil))
	}
	// 文字与判据同源（见 view.go 的「文字与着色分家」）：这里只决定怎么上色。
	return probeText(b)
}

func latencyLabel(b Status) string {
	if b.Checked.IsZero() {
		return style.Dim("-")
	}
	return fmt.Sprintf("%dms", b.Latency)
}

func checkedLabel(b Status) string {
	if b.Checked.IsZero() {
		return style.Dim("-")
	}
	return i18n.T("{ago} ago",
		i18n.A{"ago": time.Since(b.Checked).Round(time.Second)})
}

// stateLabel 把状态机的三态翻成人看的标签。daemon 可能是旧的（优雅交接期间 CLI
// 与 daemon 版本可以不同），老快照没有 state 字段，就从 Open 推——旧语义里
// 只有「摘了」和「没摘」两种。
//
// `b.State` 的取值（closed / open / half-open）是机器标记，只用来做判据；翻的
// 是给人看的那一侧。
func stateLabel(b Status) string {
	// 文字与判据都在 view.go（两个界面共用一份），这里只管上色。
	text, tone := stateText(b)
	return paint(text, tone)
}

// paint 把语义色套成 ANSI。**颜色只在渲染层出现**：同一份 stateText 的结果，
// 网页那边套的是 CSS 类名，终端这边套的是转义序列（见 view.go 的分家说明）。
func paint(text, tone string) string {
	switch tone {
	case view.ToneOK:
		return style.Green(text)
	case view.ToneWarn:
		return style.Yellow(text)
	case view.ToneBad:
		return style.Red(text)
	}
	return text
}

// ruleLabel 把快照里的账本名（机器标记）翻回人话。认不出来的原样显示：优雅
// 交接期间旧 daemon 给的是**旧值**（当时的显示名），一个陌生值不该被吞掉。
func ruleLabel(b Status) string {
	if b.Rule == "" {
		return style.Dim("-")
	}
	return ruleText(b)
}

func shapeLabel(b Status) string {
	if b.ShapeSkips == 0 {
		return style.Dim("-")
	}
	return i18n.N("{n} time", "{n} times", b.ShapeSkips, nil)
}

// sparedLabel 显示「差点被摘、被诊断探活救回来」的累计次数。
func sparedLabel(b Status) string {
	if b.Spared == 0 {
		return style.Dim("-")
	}
	return style.Green(i18n.N("{n} time", "{n} times", b.Spared, nil))
}

// totalSpared 汇总整张表的救回次数，供页脚那句话用。
func totalSpared(rows []Status) int {
	n := 0
	for _, b := range rows {
		n += b.Spared
	}
	return n
}

func agoLabel(at time.Time) string {
	if at.IsZero() {
		return style.Dim("-")
	}
	return i18n.T("{ago} ago", i18n.A{"ago": time.Since(at).Round(time.Second)})
}

// untilLabel 说清「还要等多久」，以及在冷却是干什么用的：退避之后冷却会
// 越来越长（60s → 120s → … → 10 分钟），用户看到的数字对不上基准是正常的，
// 所以把试探状态也放进来。
func untilLabel(b Status) string {
	text, tone := untilText(b)
	if text == "-" {
		return style.Dim(text)
	}
	return paint(text, tone)
}
