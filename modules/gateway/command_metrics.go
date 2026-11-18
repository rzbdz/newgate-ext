package gateway

// 本文件是 `newgate metrics`：网关计数器 + 模型健康概览。
//
// **为什么它住在这里而不是 modules/cli**（2026-09-18）：计数器是网关自己的
// 语义——哪些键、怎么分组、什么含义，全在 gateway/metrics 里定义；界面只是
// 把它排版出来。命令留在界面时，「metricOrder」这张分组顺序表和「模型健康」
// 那段渲染也一并留在界面，于是界面里躺着一堆和数据面有关的知识（用户的原话：
// 「cli 目录里面不能包含任何和 cli 展示无关的东西，什么 metrics 什么 probe」）。
//
// 搬过来之后界面不再 import gateway/metrics —— 它连这些计数器的名字都不认识。
//
// 依赖方向：gateway → cli/extension（叶子契约），不是 → modules/cli。所以这是
// 一次普通的「owner 往界面注入自己的命令」，没有回边。

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/durarg"
	"github.com/rzbdz/newgate-ext/lib/style"
	"github.com/rzbdz/newgate-ext/modules/breaker/status"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
	"github.com/rzbdz/newgate-ext/modules/gateway/metrics"
	"github.com/rzbdz/newgate-ext/modules/gateway/policy"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Rank 只在**节内**排序；节的先后由界面的槽位表决定（见 cli/extension.Section）。
// 数字与 modules/cli 里那一套同源，留空档给插队。
const (
	rankObserve     = 40
	rankMaintenance = 50
)

type metricsCommand struct{}

var (
	_ cliapi.Command    = (*metricsCommand)(nil)
	_ cliapi.Documented = (*metricsCommand)(nil)
)

func (metricsCommand) Names() []string { return []string{"metrics", "stat"} }

func (metricsCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionObserve, Rank: rankObserve,
		Usage: "metrics", Summary: i18n.T("proxy counters: interception / timeouts / failover / rerouting", nil)}
}

func (metricsCommand) Run(host cliapi.Host, _ []string) int { return runMetrics(host) }

// cmdMetrics 展示全局 binding 评分与网关计数器。可用 binding 逐个列出，
// 卡顿/不可用只汇总数量；具体失败原因由 probe 输出，避免 metrics 退化成日志。
//
// 版式：按**分组**排（请求 / 链 / 超时 / 客户端 / 插件 / 兜底），组名只在
// 该组第一行出现。原始计数器名一列不少——用户会拿它去 grep 日志。
func runMetrics(host cliapi.Host) int {
	info, ps := controlplane.State()
	if info == nil {
		return host.Die(69, i18n.T("proxy is not running (newgate start) — counters live in daemon memory", nil))
	}
	counter, uptime, ok := controlplane.Metrics(info.Port)
	if !ok {
		return host.Die(69, i18n.T("cannot reach proxy 127.0.0.1:{port} (newgate doctor)", i18n.A{"port": info.Port}))
	}
	fmt.Println(style.Title("newgate metrics",
		fmt.Sprintf("pid %d · %s", info.PID, durarg.Format(uptime))))
	if ps != nil {
		fmt.Println(style.Hint(i18n.T("{req}req/{err}err · counters reset when the daemon restarts",
			i18n.A{"req": ps.Requests, "err": ps.Failures})))
	} else {
		fmt.Println(style.Hint(i18n.T("counters reset when the daemon restarts", nil)))
	}
	printModelHealth(ps)

	if len(counter) == 0 {
		fmt.Print(style.Section(i18n.T("request counters", nil)) + "\n")
		fmt.Println(style.Dim(i18n.T("  no counters (no requests since the daemon started)", nil)))
	} else {
		t := style.NewTable(i18n.T("group", nil), i18n.T("counter", nil),
			i18n.T("count", nil), i18n.T("description", nil))
		t.AlignRight(2)
		keys := metrics.SortedKeys(counter)
		// 按**组**排，组内再按名字。不加这一步的话字母序会让「链」和「超时」
		// 交错出现，分组那一列就白设了。
		sort.SliceStable(keys, func(i, j int) bool {
			gi, gj := metricRank(keys[i]), metricRank(keys[j])
			if gi != gj {
				return gi < gj
			}
			return keys[i] < keys[j]
		})
		// 去重看**身份**、印出来看**说法**：同一个组的行只在第一行标组名。
		lastGroup := ""
		for _, k := range keys {
			id, label := metricGroup(k)
			cell := style.Dim(label)
			if id == lastGroup {
				cell = ""
			}
			lastGroup = id
			t.Row(cell, k, fmt.Sprintf("%d", counter[k]), style.Dim(metricHint(k)))
		}
		fmt.Print(style.Section(i18n.T("request counters", nil)) + "\n")
		fmt.Print(t.String())
	}
	return 0
}

// metricOrder 组的显示顺序：先「请求」，再按一次请求会依次遇到的
// 链 → 超时 → 兜底 → 插件，最后是熔断与客户端。这个顺序本身在讲请求的
// 生命周期，比字母序有用。
//
// 表里是**身份**（各贡献者自报的 ASCII 标识），不是印出来的说法——理由见
// metricRank。不认识的组排最后。
var metricOrder = []string{"requests", "chain", "timeout", "fallback", "plugin", "breaker", "client", "other"}

func printModelHealth(ps *controlplane.Doc) {
	if ps == nil {
		return
	}
	snap, err := store.Load()
	if err != nil {
		return
	}
	statuses := healthFromProxy(ps)
	bindings := map[string]bool{}
	for _, profile := range snap.Profiles {
		for _, candidates := range profile.Roles {
			for _, binding := range candidates {
				if !binding.IsRef() && binding.Provider != "" && binding.Model != "" {
					if provider, ok := snap.Providers.Providers[binding.Provider]; ok && provider.Key() != "" {
						bindings[binding.String()] = true
					}
				}
			}
		}
		if profile.Fallback != nil && !profile.Fallback.IsRef() {
			if provider, ok := snap.Providers.Providers[profile.Fallback.Provider]; ok && provider.Key() != "" {
				bindings[profile.Fallback.String()] = true
			}
		}
	}
	for _, binding := range special.Bindings(snap.State) {
		if provider, ok := snap.Providers.Providers[binding.Provider]; ok && provider.Key() != "" {
			bindings[binding.String()] = true
		}
	}
	var names []string
	for binding := range bindings {
		names = append(names, binding)
	}
	sort.SliceStable(names, func(i, j int) bool {
		a, z := statuses[names[i]], statuses[names[j]]
		if a.Open != z.Open {
			return !a.Open
		}
		ra, rz := healthDisplayRank(a), healthDisplayRank(z)
		if ra != rz {
			return ra < rz
		}
		return names[i] < names[j]
	})

	counts := map[string]int{}
	for _, name := range names {
		h := statuses[name]
		state := modelHealthState(h)
		counts[state]++
	}
	blocked := counts[healthLaggy] + counts[healthUnavailable]
	fmt.Print(style.Section(i18n.T("model health", nil)) + "\n")
	fmt.Println(style.Hint(i18n.T(
		"{usable} usable ({fast} fast · {ok} usable · {unrated} unrated) · {laggy} laggy · {down} unavailable",
		i18n.A{"usable": len(names) - blocked, "fast": counts[healthFast],
			"ok": counts[healthUsable], "unrated": counts[healthUnrated],
			"laggy": counts[healthLaggy], "down": counts[healthUnavailable]})))
	for _, name := range names {
		h := statuses[name]
		state := modelHealthState(h)
		if state == healthLaggy || state == healthUnavailable {
			continue
		}
		fmt.Println(style.Item(style.Skip, name))
		fmt.Println(style.Hint("    " + modelScoreLine(h)))
	}
	if blocked > 0 {
		fmt.Println(style.Hint(i18n.T("details for lagging and unavailable models: newgate probe", nil)))
	}
}

func healthDisplayRank(h status.Status) int {
	switch modelHealthState(h) {
	case healthFast:
		return 0
	case healthUnrated:
		return 1
	case healthUsable:
		return 2
	default:
		return 3
	}
}

// modelHealthState 把一行健康快照翻成给人看的档位名。
//
// 注意它读的是**分档明细**（Scores/Buckets），不是 daemon 算好的排序键 Rank
// ——这里要显示「哪个上下文档位多少毫秒」，而 Rank 只编码 ≤4K 那一档。
//
// 档位名与阈值来自 breaker/status（**只有一份**，2026-09-18）：这里曾经自己
// 抄了一份 3000/12000，与 breaker 的排序阈值各自演化。
// 健康档位的**身份**：比较、计数、排序都用它，显示时过 healthLabel。
//
// 为什么不用 status.Latency.String() 当判据：那是一个**说法**（它跟着语言变，
// 见 breaker/status 的说明），拿它进 switch 就等于让这一屏的走向取决于当前
// 语言。身份留在这里，说法在 healthLabel 里给。
const (
	healthFast        = "fast"        // 流畅
	healthUsable      = "usable"      // 可用
	healthLaggy       = "laggy"       // 卡顿
	healthUnavailable = "unavailable" // 不可用
	healthUnrated     = "unrated"     // 未评分
)

// healthLabel 把一个档位身份翻成给人看的说法。
func healthLabel(state string) string {
	switch state {
	case healthFast:
		return i18n.T("fast", nil)
	case healthUsable:
		return i18n.T("usable", nil)
	case healthLaggy:
		return i18n.T("laggy", nil)
	case healthUnavailable:
		return i18n.T("unavailable", nil)
	case healthUnrated:
		return i18n.T("unrated", nil)
	}
	return state
}

func modelHealthState(h status.Status) string {
	if h.Open {
		score, sampled := modelDisplayScore(h)
		if h.Grade == status.ProbeLaggy || status.Grade(score, sampled) == status.LatencySlow {
			return healthLaggy
		}
		return healthUnavailable
	}
	score, sampled := modelDisplayScore(h)
	switch status.Grade(score, sampled) {
	case status.LatencyFast:
		return healthFast
	case status.LatencyOK:
		return healthUsable
	case status.LatencySlow:
		return healthLaggy
	default:
		return healthUnrated
	}
}

func modelScoreLine(h status.Status) string {
	const labels = "≤4K,≤32K,≤128K,>128K"
	names := strings.Split(labels, ",")
	var scores []string
	for i, n := range h.Buckets {
		if n > 0 {
			latency := fmt.Sprintf("%dms", h.Scores[i])
			if h.Scores[i] == 0 {
				latency = "<1ms"
			}
			scores = append(scores, i18n.N("{bucket} {latency}/{n} sample",
				"{bucket} {latency}/{n} samples", n,
				i18n.A{"bucket": names[i], "latency": latency, "n": n}))
		}
	}
	if len(scores) == 0 && h.ScoreMs > 0 {
		samples := h.Samples
		if samples < 1 {
			samples = 1
		}
		scores = append(scores, i18n.N("≤4K {ms}ms/{n} sample", "≤4K {ms}ms/{n} samples",
			samples, i18n.A{"ms": h.ScoreMs, "n": samples}))
	}
	if len(scores) == 0 {
		return healthLabel(healthUnrated)
	}
	return healthLabel(modelHealthState(h)) + " · " + strings.Join(scores, " · ")
}

func modelDisplayScore(h status.Status) (int, bool) {
	for i, n := range h.Buckets {
		if n > 0 {
			return h.Scores[i], true
		}
	}
	return h.ScoreMs, h.ScoreMs > 0
}

// metricGroup 问一个计数器归哪一组：**先问数据面的策略账本，没人认领才落到
// 数据面自己的通用表**。
//
// 为什么是这个顺序：计数器的归属跟着「谁写下它」走。`breaker.opened` 是策略
// 写下的（判决里带回来的 Metrics），所以「熔断」这个组名与那三行说明由策略
// 自己给（见 modules/breaker/plane.go 的 MetricGroup/MetricHint）；`chain.*`
// 是数据面自己 Inc 的，归 gateway/metrics。2026-09-18 之前 breaker 那三行硬编码
// 在 metrics/hints.go 里——把「熔断器打开意味着什么」的解释权放在了一个不认识
// 熔断器的包里。
//
// 这里问的是**本进程**装出来的那本账（policy.Default()）。CLI 的进程里组件图
// 是照常装配的（命令是分派器在 Serve 期调的），所以 breaker 已经在账本上了。
func metricGroup(k string) (id, label string) {
	if id, label := policy.Default().MetricGroup(k); id != "" {
		return id, label
	}
	return metrics.Group(k)
}

// metricHint 同上，取计数器名字的人话说明。
func metricHint(k string) string {
	if h := policy.Default().MetricHint(k); h != "" {
		return h
	}
	return metrics.Hint(k)
}

// metricRank 表格里的分组顺序。**顺序**是排版决定，留在 CLI；**归属**是数据面
// 知识，归 modules/gateway/metrics（见那边的 Group）。
//
// 对的是**身份**，不是说法：这张表的顺序在讲一次请求的生命周期，换个语言顺序
// 就该原样保留；拿译文去对，两门语言里那几个词没有共同的次序，中文用户与英文
// 用户看到的就不是同一张表。身份还有个好处——它是个字面量，比对时不必翻译。
func metricRank(k string) int {
	id, _ := metricGroup(k)
	for i, name := range metricOrder {
		if name == id {
			return i
		}
	}
	return len(metricOrder)
}

// healthFromProxy 把 daemon 的熔断表按 "provider/model" 索引成一次命令内的快照。
func healthFromProxy(ps *controlplane.Doc) map[string]status.Status {
	out := map[string]status.Status{}
	if ps != nil {
		for _, s := range ps.Breakers {
			out[s.Provider+"/"+s.Model] = s
		}
	}
	return out
}
