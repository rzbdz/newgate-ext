// Package status 是健康表的 **wire 类型**：daemon 与 CLI 之间那份 JSON 的形状。
//
// 为什么单开一个叶子（2026-09-18）：这份类型是**跨进程契约**（`/__newgate/status`
// 的 `breakers` 字段），好几处都要用它——数据面负责序列化、CLI 负责渲染、控制面
// 客户端负责反序列化、breaker 自己的命令也要读它。它留在 modules/breaker 根包里
// 的话，「谁要用它就得 import 整个 breaker 模块」，而 breaker 的命令又要反过来用
// 控制面客户端——成环。下沉成叶子之后两边都能引。
//
// 它也是**唯一**允许被跨版本读取的东西（优雅交接期间 CLI 与 daemon 可以来自不同
// 版本），所以下面那条「只能加字段，不能改字段」是硬要求。
package status

import (
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// ProbeGrade 把探活结果归一为少量稳定档位，供路由排序和 UI 共同解释。
type ProbeGrade string

const (
	ProbeFluent      ProbeGrade = "fluent"
	ProbeUsable      ProbeGrade = "usable"
	ProbeLaggy       ProbeGrade = "laggy"
	ProbeUnavailable ProbeGrade = "unavailable"
)

// Status 是 binding 健康表的一行，也是 daemon→CLI 的 wire 契约
// （`/__newgate/status` 的 `breakers` 字段）。
//
// **只能加字段，不能改字段。** 优雅交接期间 CLI 与 daemon 可以来自不同版本：
// 新 CLI 读旧 daemon 的快照、旧 CLI 读新 daemon 的快照都必须能活。同理
// `health.json` 的文件名与这些 JSON 键也不能改——降级回滚用的 known-good
// 二进制读的就是它们。
//
// 因此 `Open` 保留原义（「现在被摘着」），半开等新状态放在新增的 `State` 里；
// 旧 CLI 看到 `Open=true` 就当它不可用，只是不知道它马上要试探回来了。
type Status struct {
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	Fails    int           `json:"fails"`
	Open     bool          `json:"open"`
	OpenFor  time.Duration `json:"open_for_ns"`
	Reason   string        `json:"reason,omitempty"`
	Grade    ProbeGrade    `json:"grade,omitempty"`
	Latency  int64         `json:"latency_ms,omitempty"`
	Checked  time.Time     `json:"checked_at,omitempty"`
	ScoreMs  int           `json:"score_ms"`
	Samples  int           `json:"samples"`
	Scores   [4]int        `json:"scores_ms,omitempty"`
	Buckets  [4]int        `json:"samples_by_bucket,omitempty"`
	OpenedAt time.Time     `json:"opened_at,omitempty"`

	// —— 2026-09-17 半开重构新增（只加不改）——
	//
	// State 是 closed / open / half-open。Open 表达不了半开：半开时 binding
	// 已经能进链了，状态机在等这一发的结果。
	State string `json:"state,omitempty"`
	// Rule 是当前这本账的名字（可用性 / 限流 / 配置 / 请求形状），
	// 让人一眼看出它是被哪一类问题摘的。
	Rule string `json:"rule,omitempty"`
	// OpenUntil 冷却到期的绝对时刻。CLI 用 OpenedAt 自算 age，不再依赖
	// 快照时刻算出来的 OpenFor（那个值一过网就陈旧了）。
	OpenUntil time.Time `json:"open_until,omitempty"`
	// Trial 这一刻有没有一个半开试探在飞。
	Trial bool `json:"trial,omitempty"`
	// ShapeSkips 请求形状错误的累计次数。这类错误永不摘牌，但这个数字要
	// 能被看见——不然「为什么老是 400」只能去翻 metrics。
	ShapeSkips int `json:"shape_skips,omitempty"`
	// CooldownMs 下次开闸会用多长（退避后的值）。
	CooldownMs int64 `json:"cooldown_ms,omitempty"`
	// Rank 是 daemon 算好的排序键（按 ≤4K 档位）。CLI 直接用它，不再自己
	// 重算 3000/12000 那套阈值——策略只有一个来源。
	Rank int `json:"rank,omitempty"`
	// Spared 上闸前那一次诊断探活把这条 binding 救回来了（差一点摘、结果
	// 探活证明它还通）。数字为累计次数，跨重启保留。
	Spared int `json:"spared,omitempty"`
}

// Latency 是延迟样本的档位。**阈值只在这里定义一次**（2026-09-18）。
//
// 排序（谁先上链）与展示（流畅 / 可用 / 卡顿）读的是同一套数。之前有三份各自的
// 3000 / 12000 拷贝：modules/breaker 的排序键、gateway 的 metrics 渲染、config 的
// tier 渲染。改一处另两处不会跟着变——这个仓库为同一件事付过一次代价（
// modules/cli/proxy.go 的注释记着 2026-09-17 那次「两边各有一份」），所以现在只
// 留一个来源：阈值在这里，档位名也在这里。
type Latency int

const (
	LatencyFast    Latency = iota // < FastMs
	LatencyOK                     // <= SlowMs
	LatencySlow                   // > SlowMs
	LatencyUnknown                // 没有样本
)

const (
	// FastMs 是「流畅」的上界。
	FastMs = 3000
	// SlowMs 是「可用」的上界；超过它就算卡顿。
	SlowMs = 12000
)

// Grade 把一个延迟样本归到档位。sampled=false（没探过）给 LatencyUnknown。
func Grade(scoreMs int, sampled bool) Latency {
	switch {
	case !sampled:
		return LatencyUnknown
	case scoreMs < FastMs:
		return LatencyFast
	case scoreMs <= SlowMs:
		return LatencyOK
	default:
		return LatencySlow
	}
}

// String 是这几个档位给人看的说法。档位本身（LatencyFast 那些常量）才是判据，
// 它不落盘、不过线，所以这里翻的是**说法**，不是判据。
func (l Latency) String() string {
	switch l {
	case LatencyFast:
		return i18n.T("fast", nil)
	case LatencyOK:
		return i18n.T("usable", nil)
	case LatencySlow:
		return i18n.T("laggy", nil)
	default:
		return i18n.T("unrated", nil)
	}
}
