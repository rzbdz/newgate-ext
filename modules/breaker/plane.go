package breaker

// 本文件是**健康表把自己挂进数据面**的那一层薄适配（2026-09-18）。
//
// 在此之前方向是反的：`modules/gateway` 声明 `Need(breaker.Capability)`，把一个
// `breaker.Breaker` 塞进 `forward.Server.Health` 字段，热路径里直接写着
// `breaker.Input{Kind: breaker.KindConnError}` / `res.Verdict.Bucket` /
// `case breaker.BucketShape:`。那是 **lib 用法**——gateway 的源码里出现了策略的
// 名字，而按 docs/03-architecture.md §2 的判据（「需要被启动、被停止、被撤销
// 吗？」）健康表是有生命周期、有所有者的服务，必须走 capability 的反方向。
//
// 现在：gateway 从自己的状态机里读出四个决策点（建链准入 / 结局裁决 / 控制面
// 自报 / 停机落盘），`plane` 实现其中对应的接口，在 breaker 的 `Start` 里经
// `gateway.RegisterFilter` 注册进去。健康表从此是**消费者**，不是被 import 的库。
//
// 判据（可机械核对）：`modules/gateway` 的源码里不再出现 "breaker"。
//
// # 为什么适配层在这里而不是在 gateway
//
// 因为「什么算失败、记进哪本账、什么算形状错误、留下来的痕叫什么名字」全都是
// **策略**，不是机制。gateway 只提供四个口和一本账（policy.Registry），它对
// 这四件事一个字都不该知道。这一层的存在正是那条分界的物证。

import (
	"encoding/json"
	"strings"

	gwpolicy "github.com/rzbdz/newgate-ext/modules/gateway/policy"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// plane 是健康表在数据面决策点上的化身。
//
// 它**没有自己的状态**：所有状态都在 `*table` 里，plane 只做翻译。这样
// `newgate breaker`（直接读 health.json / Snapshot）与热路径看到的是同一份事实。
type plane struct{ t *table }

// 七个可选阶段全实现。写全 var 断言是为了让「哪一天删掉一个接口」在这里当场
// 编译失败，而不是静默退化成「那个决策点没人回答」。
var (
	_ gwpolicy.Filter      = plane{}
	_ gwpolicy.Admitter    = plane{}
	_ gwpolicy.Adjudicator = plane{}
	_ gwpolicy.Controller  = plane{}
	_ gwpolicy.Flusher     = plane{}
	_ gwpolicy.EnvBinder   = plane{}
	_ gwpolicy.MetricNamer = plane{}
)

func (p plane) Name() string { return "breaker" }

func (p plane) Why() string {
	return i18n.T("binding health table: trips after consecutive failures, "+
		"releases one half-open trial when the cooldown expires", nil)
}

// --- A 阶段：建链准入 -----------------------------------------------------

// Admit 就是 Available 加一句人话。
//
// 那句理由（"熔断中"）以前写死在 modules/config/resolve/chain.go 里——准入的
// 判据属于提供它的人，resolve 只认「返回一个理由」这个**形状**，不认任何一家
// 的词汇（见 resolve.Opts.Available 的注释）。它现在回家了。
//
// **副作用照旧**：冷却期满时 Available 会发放这一轮的「半开试探名额」，一个
// 名额只放行一次真实请求（见 state.go 的 Available）。resolve 对每个候选恰好
// 问一次，所以「放行一次」在那里天然成立——这也是 policy.Admitter 只允许一个
// 贡献者的原因。
func (p plane) Admit(provider, model string) (bool, string) {
	if p.t.Available(provider, model) {
		return true, ""
	}
	// 这句话会被 resolve 原样带进 Skip.Reason，于是它同时是**给人看的理由**与
	// `newgate tier` 那一栏的内容。所以它可以翻——但**只有它可以翻**：那一栏的
	// 归类（modules/config 的 skipKind）认的是 resolve 打的机器类目 Kind，不是
	// 这句话的措辞。2026-09-20 之前它认的是措辞（strings.Contains "熔断"），
	// 那让「换个语言就少一栏」成了一个随时会发生的故障。
	return false, i18n.T("circuit open", nil)
}

func (p plane) Rank(provider, model string, contextBytes int) int {
	return p.t.Rank(provider, model, contextBytes)
}

// --- B 阶段：结局裁决 -----------------------------------------------------

// Judge 把一次结局翻成 (不换站吗 / 算它的账吗 / 记哪些数 / 留什么痕)。
//
// 它**会写状态**——与 gateway 那侧接口注释里「纯函数」那句的差别在这里说清楚：
// 分类（Classify）是纯的，但记账与分类在旧代码里是同一个调用（`table.Report`），
// 拆开就会让「认领形状错误」和「只计数不摘牌」这两件事各判一次、各漂各的
// （2026-09-17 之前的现场：同一发 400 在链中间摘牌、在链尾不摘）。所以这里保持
// 一次调用判完加记完，代价是它会在**上闸前诊断**时发网络请求（最长 16s）——
// 数据面本来就是这么调它的，见 verifyProbeAttempts。
func (p plane) Judge(in gwpolicy.Outcome) gwpolicy.Verdict {
	res := p.t.Report(in.Provider, in.Model, input(in))

	if in.Kind == gwpolicy.Succeeded {
		// 成功那一发**也要**过 Report：冷却期满之后的成功是半开试探的结论，
		// 它会解封（见 succeedLocked）。除此之外一个字都不用说——不叫停、不
		// 记账、不留痕，延迟样本走 Observe 那条路。
		return gwpolicy.Verdict{}
	}

	v := gwpolicy.Verdict{Stop: !res.Advance, Note: note(res, in.Provider)}
	switch {
	case in.Kind == gwpolicy.RejectedStatus && res.Bucket == BucketShape:
		// 请求形状错误：不是这家的账（同一份 body 换谁都一样错），但**要留痕**。
		// 只计数、永不摘牌。
	case in.Kind == gwpolicy.RejectedStatus && res.Bucket == BucketNone:
		// 「请求本身有问题」的 4xx：也不记在它头上。
	default:
		// 连接失败 / 流中断 / 429 / 401,403,404 / 408,409,5xx：算这条 binding 的账。
		//
		// 流中断今天也进来了（2026-09-18）：旧代码只给它打一行日志，一个每次流到
		// 一半就断的上游在 `newgate status` 的 failures 上完全隐形——它在健康表里
		// 记了账，在网关计数里却没有。这一处是这一轮**唯一**刻意的行为变化。
		v.Attribute = true
	}
	switch {
	case res.Opened:
		v.Metrics = append(v.Metrics, "breaker.opened")
	case res.Spared:
		v.Metrics = append(v.Metrics, "breaker.spared")
	}
	if res.Bucket == BucketShape {
		v.Metrics = append(v.Metrics, "breaker.skipped.shape_error")
	}
	if res.Shape != "" {
		// `[shape-400]` 这个字面量与 `dump/shape-400-<判据>/` 的目录前缀由**认领它的
		// 策略给**：那是上游方言（哪家挑食、判据叫什么），转发路径按
		// docs/05-gateway.md 的规矩不许出现上游专有字符串。
		v.Evidence = &gwpolicy.Evidence{Tag: "shape-400", Subject: res.Shape, Archive: true}
	}
	return v
}

// input 把数据面的事实翻成分类表的输入。**一一对应，不加任何判断**。
func input(o gwpolicy.Outcome) Input {
	in := Input{
		Written:       o.ResponseStarted,
		IsLast:        o.IsLast,
		FallbackOn400: o.FallbackOn400,
	}
	switch o.Kind {
	case gwpolicy.Succeeded:
		in.Kind = KindUpstreamSuccess
	case gwpolicy.ConnectionFailed:
		in.Kind = KindConnError
	case gwpolicy.StreamCut:
		// 断流**按定义**已经写过字节（首字节拿到了才谈得上中途断），所以这里
		// 不看 ResponseStarted：这一位要表达的是「收不回来了」，不是「写到哪了」。
		in.Kind = KindStreamTruncated
		in.Written = true
	case gwpolicy.RejectedStatus:
		in.Kind = KindUpstreamStatus
		in.Status = o.Status
		in.Body = o.Body
	}
	return in
}

// note 是旧 `breakerNote` 的正身（那两句话是健康表的知识，以前长在转发路径里）。
func note(res Result, provider string) string {
	switch {
	case res.Opened:
		return i18n.T("  [breaker opened: {provider} removed for now]",
			i18n.A{"provider": provider})
	case res.Spared:
		// 差一点摘、被诊断探活拦下来。必须打出来：这解释了「日志里有失败、
		// newgate breaker 里却没有它」这个会让人查错方向的组合。
		return i18n.T("  [diagnostic probe proved {provider} still healthy, not tripped]",
			i18n.A{"provider": provider})
	}
	return ""
}

// Observe 只有成功那一发会走到：记一次真实首字节延迟样本。可用性不在这里动
// ——首字节已经拿到之后再摘牌毫无意义（见 state.go 的 succeedLocked）。
func (p plane) Observe(in gwpolicy.Outcome) {
	if in.Kind != gwpolicy.Succeeded {
		return
	}
	p.t.ObserveSuccess(in.Provider, in.Model, in.RequestBytes, in.TTFT)
}

// --- C 阶段：控制面 -------------------------------------------------------

// Doc 把健康表并进 `/__newgate/status` 的**顶层** `breakers` 字段。
//
// 键名与 JSON 形状一字不动：它是跨版本 wire 契约（`modules/breaker/status` 的
// Status，优雅交接期间新旧二进制会混跑，老 CLI 读的就是这个键）。策略账本把
// 各家的字段并进状态文档顶层而不是塞进一个 extra 对象，正是为了这个。
//
// 落在标量上的老字段也在：Requests / Failures / Breakers 三个名字今天与昨天
// 完全一致，只是产出它们的地方从「转发路径调 Health」变成了「转发路径合并
// 策略自报」。
func (p plane) Doc() map[string]json.RawMessage {
	raw, err := json.Marshal(p.t.Snapshot())
	if err != nil {
		// Snapshot 返回的全是标量与定长数组，Marshal 不可能失败；真失败了也
		// 只是少一节状态，不能让 status 端点整个 500。
		return nil
	}
	return map[string]json.RawMessage{"breakers": raw}
}

// ObserveProbes 收下主动探活的结论。它是**权威证据**（主动、可控、可重复），
// 所以不受连续失败阈值约束：差就当场摘，好就当场放（隔离期内也一样）。
//
// 返回的 Note 由数据面原样打日志：那句「至少 60s，或一次成功的 probe」讲的是
// 健康表的退避语义，只有这里知道。
func (p plane) ObserveProbes(obs []gwpolicy.ProbeObservation) []gwpolicy.ProbeAck {
	out := make([]gwpolicy.ProbeAck, 0, len(obs))
	for _, o := range obs {
		grade, opened := p.t.RecordProbe(o.Provider, o.Model, o.Status, o.ContextBytes,
			o.Latency, o.SlowAfter, o.Error)
		ack := gwpolicy.ProbeAck{Opened: opened}
		if opened {
			ack.Note = i18n.T(
				"[probe] tripped {provider}/{model}: {grade} (at least 60s, "+
					"or a successful probe brings it back immediately)",
				i18n.A{"provider": o.Provider, "model": o.Model, "grade": grade})
		}
		out = append(out, ack)
	}
	return out
}

// --- D 阶段：停机 ---------------------------------------------------------

// Flush 把节流窗口内还没落盘的延迟样本同步写出去（优雅退出用）。
//
// 今天的实现没有错误可报（persistLocked 把失败交给 onError），所以恒返回 nil；
// 接口留着 error 是因为「落盘失败了」这件事将来可能要影响退出码，而那时改接口
// 就晚了。
func (p plane) Flush() error {
	p.t.Flush()
	return nil
}

// --- 运行期能力 -----------------------------------------------------------

// BindEnv 接住数据面递过来的运行期能力，把两件事接回健康表：
//
//   - SetErrorHandler：健康状态的读写失败必须说出来。出口是数据面的日志，
//     但**这句话是健康表的知识**（以前长在 forwarding 路径里）；
//   - SetVerifier：上闸前的最后一次主动诊断（见 api.go 的 SetVerifier）。
//     以前这是数据面反过来注入给健康表的一个回调——那条反向边正是「能力归谁」
//     没澄清的铁证，现在它归数据面所有、借给策略（policy.Env.Probe）。
func (p plane) BindEnv(env gwpolicy.Env) {
	p.t.SetErrorHandler(func(err error) {
		// 格式化由 i18n.T 做，所以这里用 `%s`：译文里出现一个 `%` 不该让这行日志变形。
		env.Logf("%s", i18n.T("[breaker] health table read/write failed "+
			"(continuing with in-memory state): {err}", i18n.A{"err": err}))
	})
	p.t.SetVerifier(env.Probe)
}

// --- 观测面的命名 ---------------------------------------------------------

// MetricGroup / MetricHint 让 `newgate metrics` 那张表里属于本模块的三行由本
// 模块自己描述。
//
// 以前它们硬编码在 modules/gateway/metrics/hints.go 里（`breaker.opened` /
// `breaker.spared` / `breaker.skipped.shape_error` 三个字面量）——那是把「熔断
// 器打开」这件事的解释权放在了不认识熔断器的包里。
//
// 身份（"breaker"）与说法（`Breaker` 的译文）分开给：那张表按组排序，而排序
// 必须与语言无关——理由写在 modules/gateway/metrics.Group 的注释里。
func (p plane) MetricGroup(key string) (id, label string) {
	if strings.HasPrefix(key, "breaker.") {
		return "breaker", i18n.T("Breaker", nil)
	}
	return "", ""
}

func (p plane) MetricHint(key string) string {
	switch key {
	case "breaker.opened":
		return i18n.T("breaker opened, provider removed for now", nil)
	case "breaker.spared":
		return i18n.T("consecutive failures reached the threshold, but the pre-trip "+
			"diagnostic probe proved it healthy, so it was not tripped", nil)
	case "breaker.skipped.shape_error":
		return i18n.T("request shape error (a 400 claimed by a shape detector, "+
			"e.g. deepseek's reasoning pass-back check), skipped from breaker accounting", nil)
	}
	return ""
}
