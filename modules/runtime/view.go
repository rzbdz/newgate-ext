package runtime

// 本文件是接管状态贡献给 web 界面的那一面：**哪些客户端在走 newgate**。
//
// 为什么是 runtime 报：接管是本模块写出来的东西——`on/off` 改哪个文件、PATH 里
// 放了什么、state.json 里记了什么意愿，全是本模块的词汇。界面自己去读那些文件的话，
// 它就得认识 shim 与 config 两种机制的区别（见 takeover 包），而那正是「谁写的东西
// 谁自己说」这条规矩要避免的（与 diagnostics.go 把两条体检留在这里同一个理由）。
//
// # 四态判据只留一份
//
// 「接管没有」不是一个布尔值，是**四种**状态——意愿与现实各有真假，两种不一致
// 各自对应一个真实故障现场：
//
//	意愿有、现实有  = 接管生效了
//	意愿有、现实没有 = 声明了却没生效——那个工具在**静默直连**（危险的那一个）
//	意愿没有、现实有 = 关掉过却没放开——用户以为直连了，其实还在走网关
//	意愿没有、现实没有 = 直连（正常，用户没想接管它）
//
// 两种不一致都是「看起来一切正常」的那类错，所以判据写在一个地方（phaseOf），
// CLI 那行汇总与这张表都从它出发——两个界面对同一份磁盘状态给出不同答案，
// 比其中一个不显示更糟。

import (
	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/runtime/takeover"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// takeoverPhase 是一个 agent 此刻处在四种状态的哪一种。
//
// 四种，不是两种：「接管没有」听起来像二选一，实际上意愿与现实各有真假，而**两种
// 不一致各自对应一个真实故障现场**（CLI 那行汇总的注释一直把它们称作「两个对称
// 故障」，但代码从来只为其中一个亮过灯——另一个是 2026-09-20 补上的）。
type takeoverPhase int

const (
	// phaseDirect 没接管，也没打算接管。正常态，不是错误。
	phaseDirect takeoverPhase = iota
	// phaseActive 接管生效了：磁盘上的现实与用户的意愿一致。
	phaseActive
	// phasePending 用户要求接管，磁盘上却没生效。
	//
	// 这是**危险的那一个**：工具会静默直连（请求不经过 newgate），而任何一处单看
	// 都「正常」。典型成因是 start/build 之后漏了一步 `newgate on`。
	phasePending
	// phaseStale 用户明确关掉过它，磁盘上却还装着。
	//
	// 成因是**释放失败**（权限坑：接管时写下的文件被 umask 削成别的用户写不动，
	// 见 CLAUDE.md §3.1——`Off` 先记意愿再动手，动手失败就留下这个状态）。
	//
	// 它不如上一个危险（流量仍然经过网关，是安全的那一侧），但它同样**看不出来**：
	// 用户以为这个工具直连了，而屏幕上一切正常。所以它是 warn 而不是 bad。
	phaseStale
	// phaseAbsent 这台机器上**没有这个工具**。
	//
	// 它比另外三种都靠前：没有工具的时候，「接管了没有」这个问题不成立——而磁盘上
	// 那点痕迹（我们改过的配置文件、我们放的 shim）**照样在**，所以不单独判它的话，
	// 一个早就卸掉的工具会一直显示 ✓。2026-09-21 实测到：`which opencode` 什么都没有，
	// 而 status 报「opencode ✓」——因为 opencode 走 config 机制，那一格判的是我们改过
	// 的 ~/.config/opencode/*.json 在不在。
	//
	// 判据是 `which` 那一条（见 confighook.Agent.Installed），**排除我们自己的 shim**
	// ——不排除的话它永远为真，正好退化成它要修的那个 bug。
	phaseAbsent
)

// phaseOf 是四种状态**唯一**的判据（CLI 的 status 行与 web 那张表都走它）。
//
// `Wanted` 的缺省是 true（没表过态就算想要，见 domain.State.TakeoverWanted），
// 所以「没表态」不会落进 phaseStale——这个状态只可能来自用户**明确**关掉过它。
// 没有这一条的话，从老版本升上来的机器会集体误报（那时的接管没记进 state.json）。
func phaseOf(s takeover.Status) takeoverPhase {
	switch {
	case !s.Installed:
		return phaseAbsent
	case s.Active && !s.Wanted:
		return phaseStale
	case s.Active:
		return phaseActive
	case s.Wanted:
		return phasePending
	default:
		return phaseDirect
	}
}

// takeoverConcepts 是接管这一刻的样子。
//
// 报**全部** agent（不只是异常的）：这张表回答的是「谁在走 newgate」，而
// 「claude 在走、opencode 没在走」是同一个答案的两半——只报异常的话，用户看不出
// 「另一个工具是不是也被接管了」。
func takeoverConcepts() ([]view.Concept, error) {
	return []view.Concept{{
		ID:    "runtime.takeover",
		Kind:  view.KindTable,
		Title: i18n.T("Who goes through newgate", nil),
		Data:  takeoverTable(takeover.List()),
	}}, nil
}

// takeoverTable 把一组接管状态摆成一张表。
//
// 它是**纯函数**（喂一组状态进去），因为要钉住的东西正好与机器无关：四态怎么翻、
// 危险的那一种有没有颜色。环境那半边（`takeover.List()` 怎么算出这些行）由
// takeover 包自己的测试守。
func takeoverTable(rows []takeover.Status) view.Table {
	table := view.Table{
		Columns: []view.Column{
			{ID: "agent", Label: i18n.T("agent", nil)},
			{ID: "state", Label: i18n.T("State", nil)},
			// 机制名（shim / config）是机器标记：它同时也是 `newgate doctor`
			// 与 takeover 包里的取值，翻它等于造一套只有界面认识的词汇。
			{ID: "mechanism", Label: i18n.T("Mechanism", nil)},
			{ID: "detail", Label: i18n.T("How", nil)},
		},
		Rows: []view.Row{},
	}
	for _, s := range rows {
		text, tone := stateCell(s)
		table.Rows = append(table.Rows, view.Row{Cells: map[string]view.Cell{
			"agent":     {Text: s.Agent},
			"state":     {Text: text, Tone: tone},
			"mechanism": dashIfEmpty(string(s.Mechanism)),
			"detail":    dashIfEmpty(s.Detail),
		}})
	}
	return table
}

// stateCell 把四种状态翻成人话 + 语义色。
func stateCell(s takeover.Status) (string, string) {
	switch phaseOf(s) {
	case phaseActive:
		return i18n.T("taken over", nil), view.ToneOK
	case phasePending:
		// 危险的是**没生效**那一半，所以文案要点明「没生效」，而不是只说
		// 「要求接管」——后者读起来像已经成了。
		return i18n.T("declared but not in effect", nil), view.ToneBad
	case phaseStale:
		// 同样要点明「没生效」，而且要说明**是哪一半没生效**：用户关过它，
		// 所以「还接着」才是那个意外。tone 是 warn 不是 bad——流量仍然经过
		// 网关，是安全的那一侧（见 phaseStale 的注释）。
		return i18n.T("turned off but still in effect", nil), view.ToneWarn
	case phaseAbsent:
		// 不画成错：没装不是什么故障，只是这台机器上没有它。但它也**不能**画成
		// 「直连」（那读起来像「它在，只是没接管」）。所以给一句自己的话、不给颜色
		// ——Tone 只有 ok/warn/bad 三个值，硬套一个都是在说「这出事了」。
		return i18n.T("not installed", nil), ""
	default:
		return i18n.T("direct", nil), ""
	}
}

// dashIfEmpty 给空值一个显式的占位符。
//
// 空格子与「这一格是空的」在界面上长得一样，而它们的意思不同：前者读起来像
// 「这里出了问题」或者「没加载出来」。横杠说清楚「这里就是没有」。
func dashIfEmpty(s string) view.Cell {
	if s == "" {
		return view.Cell{Text: "-"}
	}
	return view.Cell{Text: s}
}
