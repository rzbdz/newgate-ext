package breaker

// 本文件是健康表贡献给 web 界面的那一面：**一张只读的表**。
//
// 为什么是 breaker 报而不是界面自己去读 `/__newgate/status`：这张表的每一列
// 都是本模块写进去的字段（状态机的三态、记在哪本账上、冷却到期时刻、探活评级、
// 被救回的累计次数），而它们**怎么翻成人话**也是本模块的语义。界面自己去读
// 快照的话，它就得认识 `half-open` / `availability` 这些机器标记——那是判据，
// 不是文案（与 modules/gateway/view.go 那边同一条理由）。
//
// 这里与 CLI 那边同源：`newgate breaker` 的每一格文字都由下面这几个 `*Text`
// 函数产出（那边只是把结果套上颜色）。所以「跑到终端里看到的」与「网页上看到
// 的」不会各说各话——这正是拆分文字与着色两层的原因（见 stateText 的注释）。

import (
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/lib/durarg"
	"github.com/rzbdz/newgate-ext/lib/view"
	gatewayapi "github.com/rzbdz/newgate-ext/modules/gateway"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// healthConcepts 是健康表这一刻的样子。
//
// 它被调用的时机是**有人来看界面**，所以每一格都是「现在」的：冷却还剩多久、
// 探活是什么时候打的，都是按调用时刻算的（见 lib/view 的包注释）。
//
// 报**全部** binding，不只是出问题的那些：CLI 那条命令（`newgate breaker`）刻意
// 只报出事的，因为终端里一行行滚过去，没问题的行是噪音；而一张能停在屏幕上的
// 表不是——「哪些是好的」与「哪些坏了」是同一个问题的两半，而且好坏是相对的
// （一条能用的 binding 卡顿到什么程度，只有跟别的比才知道）。
func healthConcepts(table Breaker, g gatewayapi.Gateway) ([]view.Concept, error) {
	rows := table.Snapshot()
	return []view.Concept{{
		ID:    "breaker.health",
		Kind:  view.KindTable,
		Title: i18n.T("Binding health", nil),
		Data:  healthTable(rows, g),
		// Live：这张表的内容**完全由流量决定**（谁在失败、要等多久再试），
		// 而读它就是内存里一个快照。停在打开页面那一刻的表，说的是一件
		// 可能早就过去了的事——`untilText` 那几个相对时间尤其如此。
		Live: true,
	}}, nil
}

// healthTable 把快照摆成一张表。列的取舍跟着 `newgate breaker` 走（同一批字段、
// 同一套说法），少了两列：
//
//   - **Reason**（摘牌原因）是上游/网关的原文，可能很长且带引号，塞进一格会把
//     整行撑变形。它属于「点开一条看详情」，不属于总览——等界面有了详情面板再放。
//   - **Checked**（探活时刻）与「Waiting」在同一张表里问的是两件不同的事，而
//     「多久以前」这种相对时间在网页上会过期（页面开着不动，数字就不对了）。
//     延迟与评级是绝对值，留下了。
func healthTable(rows []Status, g gatewayapi.Gateway) view.Table {
	// Rows 显式初始化成空切片，不是 nil：nil 切片序列化成 `null`，而这一格的
	// 契约是「一个列表」。前端的 `?? []` 兜得住 null，但下一个消费者未必这么
	// 小心——一个空表在协议里该是 `[]`，不该是「这个字段没有」。
	t := view.Table{Rows: []view.Row{}, Columns: []view.Column{
		{ID: "binding", Label: i18n.T("binding", nil)},
		{ID: "state", Label: i18n.T("State", nil)},
		{ID: "rule", Label: i18n.T("Ledger", nil)},
		{ID: "fails", Label: i18n.T("Failures", nil), Align: "right"},
		{ID: "until", Label: i18n.T("Waiting", nil)},
		{ID: "probe", Label: i18n.T("Last probe", nil)},
		{ID: "latency", Label: i18n.T("Latency", nil), Align: "right"},
	}}
	for _, b := range rows {
		state, stateTone := stateText(b)
		until, untilTone := untilText(b)
		t.Rows = append(t.Rows, view.Row{
			// 行的身份就是 binding 键（`provider/model`）：它同时是这张表的第一列
			// 内容、也是探活要打的那个目标。
			ID: BindingKey(b.Provider, b.Model),
			Cells: map[string]view.Cell{
				"binding": {Text: BindingKey(b.Provider, b.Model)},
				"state":   {Text: state, Tone: stateTone},
				"rule":    {Text: ruleText(b)},
				"fails":   failsCell(b),
				"until":   {Text: until, Tone: untilTone},
				"probe":   {Text: probeText(b)},
				"latency": latencyCell(b),
			},
			Actions: probeActions(g, b),
		})
	}
	return t
}

// probeActions 是这一行上的按钮：**对这条 binding 真打一发最小请求**。
//
// # 为什么它值得有
//
// 这张表的每一列都是**被动流量**攒出来的：谁在失败、延迟多少、冷却到什么时候。
// 而「它现在到底通不通」这个问题，被动流量答不了——一条被摘牌的 binding 恰恰
// 没有流量，所以它在表上一直显示着几分钟前的那次失败，直到冷却期满才有新的证据。
// 终端里的答案是 `newgate probe`，而它打的是**全部**候选（一轮多花好几发）。
// 这一颗按钮把那个动作缩到一行上，也让「我刚换了个 key / 刚改了 base」这类时刻
// 有一个立刻能验的地方。
//
// # 为什么它自己不算结论
//
// 探完的结论**走与命令行同一条路**灌进策略层（见 gateway.ProbeBinding），所以
// 摘帽、延迟、冷却这几件事由同一份状态机更新——界面不会自己算一个「看起来对」
// 的结果贴在表上。表是 Live 的，刷新之后那几列就是新的。
func probeActions(g gatewayapi.Gateway, b Status) []view.Action {
	if g == nil {
		// 没装 gateway（摘除矩阵里真会摘掉它）时**不挂按钮**：一个按下去必然
		// 失败的按钮比没有按钮更糟。
		return nil
	}
	provider, model := b.Provider, b.Model
	return []view.Action{{
		ID: "probe",
		// 图标 + 一个词：这一列很窄，而「打个请求看看」这件事用图标最快认出来。
		Label: func() string { return "⚡ " + i18n.T("Test", nil) },
		Run: func() (string, error) {
			out, err := g.ProbeBinding(provider, model)
			if err != nil {
				return "", err
			}
			if !out.OK {
				// **不静默**：探不通就是探不通，把上游那句话原样带出来。这一格
				// 存在的意义正是「现在到底通不通」，而「什么都没发生」是它最坏的
				// 答案——用户会以为按钮没生效。
				msg := out.Err
				if msg == "" {
					msg = i18n.T("status {status}", i18n.A{"status": out.Status})
				}
				return "", i18n.E("{target} is not answering: {err}",
					i18n.A{"target": provider + "/" + model, "err": msg})
			}
			// 通了，但策略层可能有话说。它**只有一种**情况会说话：这一发虽然回了
			// 200，却慢过阈值，于是当场把闸打开了（见 RecordProbe 的四档）。那不是
			// 好消息，所以走错误那条路把它顶到横幅上——「通了」和「被我摘了」同时
			// 成立，只报前一半等于骗人。
			//
			// 表上那几列刷新之后也是这个结论，但两处说的是不同的话：表说「现在什么
			// 状态」，这句说「刚刚发生了什么」。
			if out.Note != "" {
				return "", i18n.E("{note}", i18n.A{"note": out.Note})
			}
			return "", nil
		},
	}}
}

// failsCell 把「连续失败」与两个只计数不算失败的数摆在一格里。
//
// 挤在一格是**刻意**的：它们回答的是同一个问题（这条 binding 最近有多不健康），
// 而分成三列会让大部分格子是空的（形状错误与被救回都是少数 binding 才有的）。
//
// 每一段都是**完整短语**，用分隔符连起来——不是「基数 + 后缀」那种拼法。拼接
// 译文的碎片是 i18n 的老坑：英文里 "2 + 3 shape" 读得通，换个语序的语言就成了
// 乱码，而翻译的人看到的只是一截没有主语的片段，无从下手。
func failsCell(b Status) view.Cell {
	parts := []string{i18n.N("{n} failure", "{n} failures", b.Fails, nil)}
	if b.ShapeSkips > 0 {
		parts = append(parts, i18n.N("{n} shape error", "{n} shape errors", b.ShapeSkips, nil))
	}
	if b.Spared > 0 {
		parts = append(parts, i18n.N("{n} spared", "{n} spared", b.Spared, nil))
	}
	tone := ""
	if b.Fails > 0 || b.ShapeSkips > 0 {
		tone = view.ToneWarn
	}
	return view.Cell{Text: strings.Join(parts, " · "), Tone: tone}
}

// latencyCell 是这一条的延迟那一格：数字 + 颜色。
//
// 着色从 2026-09-28 起（在这之前这一格是**唯一没有语气**的一格，而这恰恰是看表的人
// 唯一会去比大小的那个数）。判据与颜色都由 LatencySample 一处给出——它同时被首屏的
// 路由总览用（见 modules/breaker/latency.go 的注释）。
func latencyCell(b Status) view.Cell {
	ms, tone, ok := LatencySample(b)
	if !ok {
		return view.Cell{Text: "-"}
	}
	return view.Cell{Text: i18n.T("{ms}ms", i18n.A{"ms": ms}), Tone: tone}
}

// ---------- 文字与着色分家 ----------
//
// 下面这几个 `*Text` 是**唯一**说法来源，返回「给人看的字」+「它该是什么颜色」。
// CLI（command.go）与 web（上面那张表）各自消费它：终端把颜色套成 ANSI，网页
// 把它当语义交给 CSS。
//
// 为什么不让两边各写一份：这两处说的必须是同一件事，而它们最容易分家的地方
// 恰恰是**判据**（什么算「摘了」、账本名怎么翻）。分家之后症状是「终端说健康、
// 网页说半开」——同一个进程的同一份数据，两个界面各说各话，看的人只会怀疑数据
// 本身。颜色则必须分层：ANSI 转义在网页上是一串乱码，CSS 类名在终端里是一串
// 乱码，两边共用一个字符串是行不通的。

// stateText 是状态机三态的人话。返回的 tone 取 view.Tone*（空串 = 不着色）。
//
// daemon 可能是旧的（优雅交接期间 CLI 与 daemon 版本可以不同），老快照没有
// state 字段，就从 Open 推——旧语义里只有「摘了」和「没摘」两种。
//
// `b.State` 的取值（closed / open / half-open）是机器标记，只用来做判据；翻的
// 是给人看的那一侧。
func stateText(b Status) (string, string) {
	switch b.State {
	case "half-open":
		if b.Trial {
			return i18n.T("half-open · trial in flight", nil), view.ToneWarn
		}
		return i18n.T("half-open · awaiting trial", nil), view.ToneWarn
	case "open":
		return i18n.T("tripped", nil), view.ToneBad
	case "closed":
		return i18n.T("healthy", nil), view.ToneOK
	}
	if b.Open {
		return i18n.T("tripped", nil), view.ToneBad
	}
	return i18n.T("healthy", nil), view.ToneOK
}

// ruleText 把快照里的账本名（机器标记）翻回人话。认不出来的原样显示：优雅
// 交接期间旧 daemon 给的是**旧值**（当时的显示名），一个陌生值不该被吞掉。
func ruleText(b Status) string {
	if b.Rule == "" {
		return "-"
	}
	if name := bucketFromName(b.Rule).ruleName(); name != "" {
		return name
	}
	return b.Rule
}

// untilText 说清「还要等多久」，以及在冷却是干什么用的：退避之后冷却会越来越长
// （60s → 120s → … → 10 分钟），用户看到的数字对不上基准是正常的，所以把试探
// 状态也放进来。
//
// 「多久」是相对时间。这里仍然按**调用时刻**算：这张表每次请求现生成（见
// healthConcepts），所以数字与快照同岁。而快照本身不会停在原地——这张卡声明了
// `Live`（见 healthConcepts），界面每隔几秒重问一次，数字跟着走；后端因此**不必**
// 给绝对时刻，前端也不必自己走秒。（2026-09-20 更正：这条注释原来把「页面不动」
// 当成前提，那个前提在 `Concept.Live` 之后不成立了。）
func untilText(b Status) (string, string) {
	if b.Trial {
		return i18n.T("trial in flight", nil), view.ToneWarn
	}
	if b.OpenUntil.IsZero() {
		return "-", ""
	}
	if d := time.Until(b.OpenUntil); d > 0 {
		return durarg.Format(int(d.Seconds())), ""
	}
	return i18n.T("expired", nil), view.ToneOK
}

// probeText 是上一次主动探活的评级。
//
// ProbeGrade 是 wire 契约里的档位名（fluent / usable / laggy / unavailable），
// 机器标记，原样显示——它同时也是 `newgate probe` 与 tier 链排序用的那套词。
func probeText(b Status) string {
	if b.Checked.IsZero() || b.Grade == "" {
		return i18n.T("never probed", nil)
	}
	return string(b.Grade)
}
