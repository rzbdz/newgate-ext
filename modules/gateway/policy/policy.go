// Package policy 是数据面**自己留出来的决策点**。
//
// # 为什么需要它
//
// 一次转发在流程上有四处「必须做个决定」的地方：
//
//	A 建链期  这条 binding 现在能不能进候选？排第几？
//	B 结局期  沿不沿链走？这一发算不算这条 binding 的失败？留什么痕？
//	C 控制面  我自己那份状态怎么报出去？探活结论怎么收？
//	D 停机    关机前还有谁的账没落盘？
//
// 2026-09-18 之前这四个问题都由 modules/breaker 回答，而 gateway 是**直接调用
// 它**的：`forward.Server` 上有一个 `Health breaker.Breaker` 字段，热路径里写着
// `breaker.Input{Kind: breaker.KindConnError}`、`res.Verdict.Bucket`、
// `case breaker.BucketShape:`。也就是说 gateway 的源码里出现了**策略的名字**——
// 它把 breaker 当成了一个 lib，而不是「往我这儿插东西的一个组件」。
//
// 这一层把能力方向掉头：gateway 从自己的状态机里读出上面四个决策点，各留一个
// 可注册的口；谁有意见谁注册进来。判据是可机械核对的——
//
//	command grep -rn "breaker" modules/gateway/forward/ modules/gateway/module.go
//
// 必须是 0 行（`modules/breaker/status` 是 wire 叶子，按
// docs/03-architecture.md §2 的注允许直接 import，不算）。
//
// # 内核给的是机制，不是政策
//
// **没有贡献者时 = 最小系统**，四个口都有明确的内核默认：
//
//	A 全部候选可用、不重排（与 resolve.Opts 的 nil 语义一致）
//	B 停 = 响应已开始 || 链上没有下一站；不记任何账、不打任何痕
//	C 控制面少一节；探活结论收下、无话可说
//	D 无事可做
//
// 有一条边界值得写死：**策略只能叫停，不能强行前进**。核对过旧分类表的全表，
// 除「其它 4xx」（本可重试却停下）之外，换站与否恒等于「没写过响应 + 还有下一站」，
// 与记进哪本账无关——所以「叫停」是唯一的干涉方向，不存在反过来的情形。
// 零值 Verdict 表示「没有意见」，于是它落在内核默认上；这与仓库的 fail-open
// 硬要求一致（见 CLAUDE.md §4）：一个报错/没认领的贡献者，绝不该让整条链断掉。
package policy

import (
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"time"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Filter 是一个贡献者的身份。
//
// 与 special.Plugin 的 Name()/Why() 同构：名字进日志与查重，Why 回答「这东西
// 为什么在」。少了它，一个贡献者出问题时就只剩「有个东西返回了 Stop」。
type Filter interface {
	Name() string
	Why() string
}

// OutcomeKind 是「这一次尝试到底怎么了」——**只有事实，没有判断**。
//
// 数据面说「连接失败了」「上游回了 500」「响应已经开始写了」，不解释这意味着
// 什么；解释在贡献者的 Judge 里。这条分界与旧 modules/breaker 的 Kind 一致，
// 只是名字换成了中性的：内核不该出现任何一家策略的词汇。
type OutcomeKind string

const (
	// Succeeded 拿到了 2xx 响应头。
	Succeeded OutcomeKind = "succeeded"
	// ConnectionFailed 连接阶段失败（dial/DNS/TLS/reset/EOF，或首字节超时）。
	// 客户端自己取消**不在这里**：数据面不把它交给策略（见 Outcome 的说明）。
	ConnectionFailed OutcomeKind = "connection_failed"
	// RejectedStatus 上游回了一个 >= 400 的状态码。
	RejectedStatus OutcomeKind = "rejected_status"
	// StreamCut 响应已经开始往客户端写，中途上游断流。
	StreamCut OutcomeKind = "stream_cut"
)

// Outcome 是内核交给策略的全部事实。**没有隐藏输入**：同一个 Outcome 永远得到
// 同一个 Verdict，所以每一条策略都能被一张表穷举测完。
type Outcome struct {
	Provider string
	Model    string
	// Binding 是 provider/model 的展示形式，只给日志用。
	Binding string
	Kind    OutcomeKind
	// Status 上游状态码，只有 RejectedStatus 有。
	Status int
	// Body 上游错误原文（已截断）。只有 RejectedStatus 有——数据面为了这段
	// 证据本来就读了它，读它的原因从「喂给 breaker」变成「喂给策略」。
	Body []byte
	// ResponseStarted 已经往客户端发过响应头了吗。开始写之后就不能换站：
	// 客户端已经收到半截响应，换个人接着写只会得到拼接的垃圾。
	ResponseStarted bool
	// IsLast 链上没有下一站了（最后一个候选，或总预算已用尽）。
	IsLast bool
	// FallbackOn400 是 chain.fallback_on_400 开关。
	FallbackOn400 bool
	// RequestBytes 我们发出去的 body 字节数；延迟样本按它选上下文桶。
	RequestBytes int
	// TTFT 从发出请求到拿到响应头的时间。
	TTFT time.Duration
}

// Verdict 是策略的回答。
//
// 内核只搬运、不解释：Metrics 是计数器名、Note 是一句人话、Evidence.Tag 是日志
// 标记与证据目录前缀——全是**不透明字符串**。gateway 里因此不出现任何一家上游
// 或策略的专有词汇（`[shape-400]` 这个字面量由贡献者给，见 Evidence）。
type Verdict struct {
	// Stop 这一次不要再换站了。
	//
	// 零值（false）= 「我没意见」，于是落在内核默认上：只要响应还没开始写、
	// 链上还有下一站，就继续沿链走。fail-open 的方向（CLAUDE.md §4）。
	Stop bool
	// Attribute 这一发算在这条 binding 头上吗。
	//
	// 它驱动 `newgate status` 里那个 failures 计数（今天只数「上游的账」：
	// 形状类 400 与「请求本身有问题」的 4xx 都不算）。它是内核的问题
	// （「记在谁头上」），不是策略的桶名——所以内核只当布尔搬运。
	Attribute bool
	// Metrics 要 +1 的计数器名，内核原样 Inc。
	Metrics []string
	// Note 日志后缀，原样打在那一行末尾（含前导空格由贡献者自己给）。
	Note string
	// Evidence 非 nil = 这一发要留证据（形状类错误的专属日志与存档）。
	Evidence *Evidence
}

// Verdict 的合成规则（多个贡献者时）。
//
// 写死在这里而不是留给调用方，是因为「谁的判决赢」这种事一旦各写一遍必然漂移：
//
//	Stop / Attribute  OR      —— 任何一个说停就停，说记账就记账
//	Metrics           并集    —— 去重后按字典序（输出稳定）
//	Note              拼接    —— 按贡献者的注册顺序
//	Evidence          第一个非 nil（注册顺序）
//
// 注意 OR 的方向与「内核默认」是一致的：判决只往「更保守」的方向叠。
func merge(a, b Verdict) Verdict {
	a.Stop = a.Stop || b.Stop
	a.Attribute = a.Attribute || b.Attribute
	a.Metrics = append(a.Metrics, b.Metrics...)
	if b.Note != "" {
		a.Note += b.Note
	}
	if a.Evidence == nil {
		a.Evidence = b.Evidence
	}
	return a
}

// Evidence 是一发「要留痕」的结局的证据元数据。
//
// 为什么整块由贡献者给：`[shape-400]` 与 `dump/shape-400-deepseek/` 里那两个词
// 都是**上游方言**（哪家的请求形状校验、判据叫什么名字），而转发路径按
// docs/05-gateway.md 的规矩不许出现上游专有字符串。贡献者给标记，内核只负责
// 打日志、落盘、以及保持无知。
type Evidence struct {
	// Tag 日志里的方括号标记与证据目录前缀，如 "shape-400"。
	Tag string
	// Subject 是谁认领的，进日志与目录名，如 "deepseek"。
	Subject string
	// Archive 另外存一份不参与滚动清理的现场。
	//
	// 为什么要有这一位：这类 400 偶发又致命，dump 目录的滚动清理会把它挤掉，
	// 所以现场要单独留档。
	Archive bool
}

// --- 可选接口：按阶段实现，缺哪个就跳过哪个 -------------------------------
//
// 复用仓库已有的「必选身份 + 可选阶段」形状（gateway/special 那套）：贡献者
// 只实现自己关心的阶段，内核 type-assert 发现。

// Admitter 是 A 阶段（建链期）的贡献者。
//
// **只允许一个**（注册第二个当场报错）。理由不是保守，是 Admit 不是查询：
// 健康表在冷却期满时要发放「半开试探名额」，一个名额只放行一次真实请求，而
// resolve 对每个候选问一次、覆盖链的路径上还会再问一次——「恰好一次」这件事
// 今天靠的是名额自己有 TTL 兜底（见 modules/breaker 的 state.go）。多个
// Admitter 求 AND 会让「名额发不发」取决于**另一个不相关贡献者**的返回值：
// 前者发了名额、后者否决，那条 binding 就在 TTL 内既进不了链也没人在试。
//
// 所以这一位留出的是「谁来回答准入」而不是「多方共同回答」。真要多个来源，
// 做法是它们自己合成一个 Admitter 再注册。
type Admitter interface {
	// Admit 这条 binding 现在能不能进候选。false 时 reason 是给用户看的
	// 跳过原因（`newgate tier` 会原样打出来）。
	Admit(provider, model string) (ok bool, reason string)
	// Rank 排序键，越小越优先。只重排链头之后的 fallback。
	Rank(provider, model string, contextBytes int) int
}

// Adjudicator 是 B 阶段（结局期）的贡献者。
//
// 可以多个，判决按 merge 的规则合成（见 Verdict 的说明）。
type Adjudicator interface {
	// Judge 对这一次结合作出判决。**必须是纯函数**：同一个 Outcome 永远
	// 同样的 Verdict，不加锁、不写状态。记账在 Observe 里。
	Judge(Outcome) Verdict
	// Observe 只有成功那一发会走到：记一次真实观测量（延迟样本）。
	Observe(Outcome)
}

// Controller 是 C 阶段（控制面）的贡献者。
type Controller interface {
	// Doc 是贡献给 `/__newgate/status` 的字段，键就是它在文档顶层的样子。
	//
	// gateway 把它并进状态文档的顶层——**刻意不是塞进一个 extra 对象**：
	// 老版本 CLI（优雅交接期间新旧二进制会混跑）读的是顶层 `breakers`，
	// 挪窝会让它在升级窗口里看见空表。键撞车在**注册期**就报错（与
	// `confighook.RegisterStateField` 抢同一个字段的处置一致），不静默覆盖。
	//
	// 契约：便宜、无副作用（注册时问一次，之后每个 status 请求问一次）。
	Doc() map[string]json.RawMessage

	// ObserveProbes 收下探活的主动结论。
	ObserveProbes(obs []ProbeObservation) []ProbeAck
}

// ProbeObservation 是一条探活结论（`newgate probe` 经控制端点回传）。
type ProbeObservation struct {
	Provider     string
	Model        string
	Status       int
	Latency      time.Duration
	ContextBytes int
	Error        string
	// SlowAfter 是分类器首字节阈值：probe 是 4-token 极小请求，超过这个阈值
	// 仍未完成，就不具备进入交互 fallback 链的资格，即使它最终回了 200。
	SlowAfter time.Duration
}

// ProbeAck 是贡献者对一条探活结论的回应（只用来打日志与计数）。
type ProbeAck struct {
	// Opened 这条结论把闸打开了。
	Opened bool
	// Note 要打出来的一行人话；空 = 没什么可说。
	Note string
}

// Flusher 是 D 阶段（停机）的贡献者：把还没落盘的状态同步写出去。
type Flusher interface {
	Flush() error
}

// Env 是数据面在**运行期**提供给贡献者的能力。
//
// 它不是「配置」，是此刻活着的事实：日志出口、以及拿当前配置快照发一次最小
// 探活。它存在的原因是「上闸前诊断」——某个 binding 连续失败到阈值时，先主动
// 探一次，通就不摘牌（见 modules/breaker 的 SetVerifier）。
//
// 为什么是 gateway 给而不是 breaker 自己发：探活要读当前配置快照（provider 的
// base、key、方言）并复用数据面的探活实现。能力归 gateway，借给贡献者。
type Env interface {
	Logf(format string, args ...any)
	// Probe 发一次最小探活。**fail-closed**：拿不到 provider 配置、key 为空、
	// 出错，一律 false——诊断不能成为坏 binding 的免死金牌，它只该拦住误判。
	Probe(provider, model string) bool
}

// EnvBinder 是可选阶段：要运行期能力的贡献者实现它。
type EnvBinder interface {
	BindEnv(Env)
}

// MetricNamer 是可选阶段：给自己的计数器声明分组与说明。
//
// 为什么不放在 Controller 里：观测面的命名与状态文档的字段是两件事，而且
// 将来可能有只产生计数器、不产生状态节的贡献者。
type MetricNamer interface {
	// MetricGroup 这个计数器归哪一组：身份（ASCII 稳定标识，排序/去重用）与
	// 说法（给人看的锚点）；不认识给两个空串。身份与说法分开的理由见
	// modules/gateway/metrics.Group——排序必须与语言无关。
	MetricGroup(key string) (id, label string)
	// MetricHint 这个计数器的人话说明；不认识给空串。
	MetricHint(key string) string
}

// --- 账本 -----------------------------------------------------------------

// Registry 是贡献者账本。
//
// 它按注册顺序执行（filter 之间的**相对顺序**由注册顺序决定，同
// `component.Registry[T]`）：A 阶段只允许一个，B/C/D 阶段的合成规则本身与顺序
// 无关（OR / 并集），只有 Note 的拼接顺序依赖它——而 Note 的先后是给人看的，
// 稳定即可。
type Registry struct {
	mu      sync.RWMutex
	filters []Filter
	tokens  map[string]uint64
	next    uint64
}

// New 建一本空账本。空账本 = 最小系统，四个口都走内核默认。
func New() *Registry { return &Registry{tokens: map[string]uint64{}} }

// 进程级默认账本。**形状照抄 gateway/special 的 InstallDefault/SetDefault**
// （见那边的注释）：装配出来的 gateway 在 Start 里把自己的账本装成默认，
// 而「启动数据面的人」可以拿到它。
//
// 为什么需要这一层：起数据面的地方有两处——守护进程主循环（serve.go，包内
// 直接拿 port.filters）和系统级测试（testing/system 从真组件图里起一份真
// 数据面）。后者若自己 New() 一本，就会绕过所有贡献者的注册，把「接线对了
// 吗」这类回归变成空转（2026-09-17 那条形状判据的测试正是这么空转掉的）。
//
// 它**不是**给业务代码用的后门：写入口永远只有 RegisterFilter/Register，
// 读侧只有数据面。生产进程里只会装配一个 gateway，所以「哪一本」没有歧义；
// 测试里同进程起多张图时靠 InstallDefault 返回的 restore 互相隔离。
var (
	defaultMu       sync.RWMutex
	defaultRegistry = New()
)

// Default 当前装配出来的那本账（没装配过任何 gateway 时是一本空账）。
func Default() *Registry {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultRegistry
}

// SetDefault 直接设置默认账本。装配路径请优先用 InstallDefault。
func SetDefault(r *Registry) {
	if r == nil {
		panic("policy: nil default registry")
	}
	defaultMu.Lock()
	defaultRegistry = r
	defaultMu.Unlock()
}

// InstallDefault 装上带所有权的默认账本，返回恢复函数。恢复时核对当前实例，
// 免得一个先启动的组件在自己的 Stop 里把后启动者的账本顶掉。
func InstallDefault(r *Registry) func() {
	if r == nil {
		panic("policy: nil default registry")
	}
	defaultMu.Lock()
	previous := defaultRegistry
	defaultRegistry = r
	defaultMu.Unlock()
	return func() {
		defaultMu.Lock()
		if defaultRegistry == r {
			defaultRegistry = previous
		}
		defaultMu.Unlock()
	}
}

// Register 挂一个贡献者进来。
//
// 写锁内查重：名字撞车、以及「第二个 Admitter」当场报错。查重与插入对并发注册
// 是原子的——先到先得是这个功能最不该有的行为（那样「谁占了这个名字」在清单里
// 看不出来）。
func (r *Registry) Register(f Filter) (modules.Release, error) {
	if f == nil || f.Name() == "" {
		return nil, i18n.E("policy: contributor must have a name", nil)
	}
	if err := r.validate(f); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.filters {
		if existing.Name() == f.Name() {
			return nil, i18n.E("policy: contributor {name} is already registered",
				i18n.A{"name": strconv.Quote(f.Name())})
		}
	}
	if _, is := f.(Admitter); is {
		for _, existing := range r.filters {
			if _, taken := existing.(Admitter); taken {
				return nil, i18n.E(
					"policy: only one contributor may be an admitter; {name} is already taken by {holder} (it carries side effects, see the Admitter docs)",
					i18n.A{"name": strconv.Quote(f.Name()), "holder": strconv.Quote(existing.Name())})
			}
		}
	}
	r.next++
	token := r.next
	r.tokens[f.Name()] = token
	r.filters = append(r.filters, f)
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.tokens[f.Name()] != token {
			return nil // 陈旧 Release：这一条已经被撤过了，Stop 要幂等
		}
		delete(r.tokens, f.Name())
		for i, existing := range r.filters {
			if existing.Name() == f.Name() {
				r.filters = append(r.filters[:i], r.filters[i+1:]...)
				break
			}
		}
		return nil
	}, nil
}

// validate 在拿到写锁之前做「一眼能看出来的错」的检查：状态文档的键撞车。
//
// 放在锁外是因为它要调贡献者的 Doc()，而那是个可能加锁的调用；把别人的代码
// 放在我们的写锁里是自找的锁序问题。
func (r *Registry) validate(f Filter) error {
	controller, ok := f.(Controller)
	if !ok {
		return nil
	}
	mine := controller.Doc()
	if len(mine) == 0 {
		return nil
	}
	type other struct {
		name string
		keys []string
	}
	r.mu.RLock()
	var others []other
	for _, existing := range r.filters {
		c, is := existing.(Controller)
		if !is {
			continue
		}
		var keys []string
		for key := range c.Doc() {
			keys = append(keys, key)
		}
		others = append(others, other{name: existing.Name(), keys: keys})
	}
	r.mu.RUnlock()
	for _, o := range others {
		for _, key := range o.keys {
			if _, clash := mine[key]; clash {
				return i18n.E("policy: state document field {key} is already taken by {owner}",
					i18n.A{"key": strconv.Quote(key), "owner": strconv.Quote(o.name)})
			}
		}
	}
	return nil
}

// Filters 当前贡献者（副本，按注册顺序）。
func (r *Registry) Filters() []Filter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Filter(nil), r.filters...)
}

// Empty 一个贡献者都没有——即「最小系统」。
func (r *Registry) Empty() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.filters) == 0
}

// --- A 阶段：准入与排序 ----------------------------------------------------

// Admit 问准入。
//
// **必须对每个候选恰好问一次**（resolve 就是这么调的），因为健康表在这条路径
// 上有副作用（发放半开试探名额）。所以这里**不短路**：即便将来有多个来源，
// 也是全部问完再合成——短路会让后面的贡献者少调一次，把名额漏发。
func (r *Registry) Admit(provider, model string) (bool, string) {
	r.mu.RLock()
	var admitter Admitter
	for _, f := range r.filters {
		if a, ok := f.(Admitter); ok {
			admitter = a
			break
		}
	}
	r.mu.RUnlock()
	if admitter == nil {
		return true, ""
	}
	return admitter.Admit(provider, model)
}

// Rank 问排序键；没有贡献者时给中性值（等价于「不重排」）。
func (r *Registry) Rank(provider, model string, contextBytes int) int {
	r.mu.RLock()
	var admitter Admitter
	for _, f := range r.filters {
		if a, ok := f.(Admitter); ok {
			admitter = a
			break
		}
	}
	r.mu.RUnlock()
	if admitter == nil {
		return NeutralRank
	}
	return admitter.Rank(provider, model, contextBytes)
}

// NeutralRank 是「没有意见」的排序键，也是 resolve 里中性值的同一档。
const NeutralRank = 1_000_000

// --- B 阶段：结局裁决 -----------------------------------------------------

// Judge 收集所有裁决并合成。
//
// 贡献者 panic 按「没有意见」处置（fail-open）：一个坏插件绝不能把整条链的
// 判断夺走——这与 special.Respond 的处置同一条规矩。
func (r *Registry) Judge(o Outcome) Verdict {
	var out Verdict
	for _, f := range r.Filters() {
		adj, ok := f.(Adjudicator)
		if !ok {
			continue
		}
		out = merge(out, judgeSafely(adj, o))
	}
	sort.Strings(out.Metrics)
	out.Metrics = dedupe(out.Metrics)
	return out
}

func judgeSafely(adj Adjudicator, o Outcome) (v Verdict) {
	defer func() {
		if recover() != nil {
			v = Verdict{}
		}
	}()
	return adj.Judge(o)
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// Observe 把成功观测量交给所有裁决者。
func (r *Registry) Observe(o Outcome) {
	for _, f := range r.Filters() {
		if adj, ok := f.(Adjudicator); ok {
			safely(func() { adj.Observe(o) })
		}
	}
}

func safely(fn func()) {
	defer func() { _ = recover() }()
	fn()
}

// --- C 阶段：控制面 -------------------------------------------------------

// Doc 合并所有贡献者的状态字段。
//
// 键撞车在注册期已经拦掉，所以这里的合并是纯拼接。
func (r *Registry) Doc() map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, f := range r.Filters() {
		controller, ok := f.(Controller)
		if !ok {
			continue
		}
		for key, value := range controller.Doc() {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ObserveProbes 把探活结论分发给所有控制面贡献者。
func (r *Registry) ObserveProbes(obs []ProbeObservation) []ProbeAck {
	var acks []ProbeAck
	for _, f := range r.Filters() {
		controller, ok := f.(Controller)
		if !ok {
			continue
		}
		safely(func() { acks = append(acks, controller.ObserveProbes(obs)...) })
	}
	return acks
}

// MetricGroup / MetricHint 问观测面的命名；没人认识就给空串，由调用方兜底。
func (r *Registry) MetricGroup(key string) (id, label string) {
	for _, f := range r.Filters() {
		if namer, ok := f.(MetricNamer); ok {
			if id, label := namer.MetricGroup(key); id != "" {
				return id, label
			}
		}
	}
	return "", ""
}

func (r *Registry) MetricHint(key string) string {
	for _, f := range r.Filters() {
		if namer, ok := f.(MetricNamer); ok {
			if hint := namer.MetricHint(key); hint != "" {
				return hint
			}
		}
	}
	return ""
}

// --- D 阶段：停机 ---------------------------------------------------------

// Flush 让所有有账的贡献者落盘。错误按「不静默」报出去，但不阻断停机。
func (r *Registry) Flush(logf func(string, ...any)) {
	for _, f := range r.Filters() {
		flusher, ok := f.(Flusher)
		if !ok {
			continue
		}
		if err := flusher.Flush(); err != nil && logf != nil {
			logf("[policy] %s", i18n.T("flush failed for {name}: {err}", i18n.A{"name": f.Name(), "err": err}))
		}
	}
}

// --- 运行期能力 -----------------------------------------------------------

// BindEnv 把运行期能力交给要它的贡献者。
//
// 时刻是**数据面自己 Start 的时候**（Serve 期），不是装配期：Env 里那两件事
// （日志出口、拿当前配置快照探活）只有在服务真的起来了才存在。见
// docs/02-component-framework.md 的三段法则。
func (r *Registry) BindEnv(env Env) {
	for _, f := range r.Filters() {
		if binder, ok := f.(EnvBinder); ok {
			safely(func() { binder.BindEnv(env) })
		}
	}
}
