package breaker

import i18n "github.com/rzbdz/newgate/lib/i18n"

// Kind 是「上游这一发到底怎么了」的归类，由调用方从实际发生的事构造。
//
// 这里只有**事实**，没有判断：数据面说「连接失败了」「上游回了 500」
// 「已经往客户端写过字节了」，不解释这意味着什么。解释在 Classify 里。
type Kind int

const (
	// KindUpstreamSuccess 上游正常回了响应（含 3xx：客户端会自己跟）。
	KindUpstreamSuccess Kind = iota
	// KindConnError dial/DNS/TLS/reset/EOF 失败，或首字节超时。
	KindConnError
	// KindClientCancel 客户端自己取消了。跟上游无关，不算任何人的账。
	KindClientCancel
	// KindUpstreamStatus 上游回了一个 >= 400 的状态码。
	KindUpstreamStatus
	// KindStreamTruncated 首字节已经拿到、字节也已经开始往客户端写，
	// 中途上游断了。换不了站（已经写出去的东西收不回），但这是实打实的
	// 可用性问题。
	KindStreamTruncated
)

// Bucket 是这笔失败该记进哪本账。不同账本有不同阈值与冷却——把它们混成一本
// 「连续失败两次就摘」是 2026-09-17 之前的核心毛病：
//
//   - 可用性：连接失败/超时/5xx/流中断，真的是「这家现在不行」；
//   - 限流：429 是秒级恢复的，用 60s×2 退避去摘它过重；
//   - 配置：401/403/404 是确定性的，一次即摘，且不必退避（重试只是等用户改
//     配置/换模型）；
//   - 形状：请求形状错误（同一份 body 换谁都一样错）跟「这家能不能用」无关，
//     **永不摘牌**，只计数。
type Bucket int

const (
	BucketNone Bucket = iota
	BucketAvailability
	BucketRateLimit
	BucketConfig
	BucketShape
)

// ruleToken 是这本账在 wire 与磁盘上的名字：`Status.Rule`、`health.json`。
//
// 它是**机器标记**，所以不翻（见 lib/i18n 的包注释与 tools/i18n/check 的 forbidden）：
// 这个值会被 persist.bucketFromName 读回来对账，一旦跟着语言变，用户在 zh 下写的
// health.json 到了 en 下就认不出来了——存的是一份数据，不是一句话。
func (b Bucket) ruleToken() string {
	switch b {
	case BucketAvailability:
		return "availability"
	case BucketRateLimit:
		return "rate limit"
	case BucketConfig:
		return "config"
	case BucketShape:
		return "request shape"
	}
	return ""
}

// ruleName 是给人看的名字：表格的「账本」列、以及摘牌原因里的那一句。
func (b Bucket) ruleName() string {
	switch b {
	case BucketAvailability:
		return i18n.T("Availability", nil)
	case BucketRateLimit:
		return i18n.T("Rate limit", nil)
	case BucketConfig:
		return i18n.T("Config", nil)
	case BucketShape:
		return i18n.T("Request shape", nil)
	}
	return ""
}

// Input 是分类需要的全部事实。**没有隐藏输入**：Shape 由 Report 事先算好塞
// 进来，所以 Classify 是纯函数，能被一张表穷举测完。
type Input struct {
	Kind   Kind
	Status int
	// Body 是上游回的错误原文（已截断）。只有 Report 读它——用来问形状检测器。
	Body []byte
	// Written 已经往客户端写过字节了吗。写过就不能换站：客户端已经收到半截
	// 响应，换个人接着写只会得到拼接的垃圾。
	Written bool
	// IsLast 链上还有没有下一站。
	IsLast bool
	// FallbackOn400 是 chain.fallback_on_400 开关。
	FallbackOn400 bool

	// Shape 命中注册的形状检测器（由 Report 填，Classify 只读）。
	Shape bool
}

// Verdict 是分类的判决。数据面据此决定「继续沿链走」还是「把这一手交给客户端」。
//
// 没有 Record 字段：`Bucket == BucketNone` 就是「不记账」。两个字段表达同一件
// 事必然漂移——留一个。
type Verdict struct {
	// Advance 换下一站。
	Advance bool
	// Bucket 归到哪本账；BucketNone 表示什么都不记（成功、客户端取消、
	// 以及「请求本身有问题，换谁都一样」的 4xx）。
	Bucket Bucket
}

// Result 是 Report 的返回：判决 + 这次记账产生的副作用（只给日志用）。
type Result struct {
	Verdict
	// Opened 这次失败把闸打开了。
	Opened bool
	// Spared 连续失败已经数到阈值，但**上闸前的诊断探活**证明这条 binding
	// 仍然可用，于是没有摘牌、计数清零。见 Breaker.SetVerifier。
	Spared bool
	// Shape 认领这次 400 的检测器名（BucketShape 时非空）。
	//
	// 数据面用它打专属日志、存证据，**同时保持对上游专有字符串的无知**：名字
	// 是检测器自己起的（"deepseek"），core 里没有任何一处写着
	// "must be passed back"。2026-09-17 之前 forward 自己认那两句文案
	// （当时那个函数叫 IsRequestShapeError，住在 core 里），等于把上游方言
	// 抄进了转发路径。
	Shape string
}

// Classify 是**唯一**的失败分类表。纯函数：同样的 Input 永远同样的 Verdict。
//
// 这张表穷举了数据面上游交互的每一种收场。逐条理由：
//
//	连接失败/首字节超时   → 走 + 记可用性：明确的「这家现在不行」
//	流中途断（已写字节）   → 不换站（写出去收不回）+ 记可用性
//	客户端取消            → 什么都不做：不是上游的错，也不该再往下撞
//	400 + 形状命中        → **走** + 只计数：同一份 body 换谁发都一样错，
//	                        所以不该记在这家头上；但换个校验更松的 provider
//	                        是有可能收下的，所以继续往下试。注意这一条**不受
//	                        fallback_on_400 约束**——那个开关是拦「拿坏请求撞遍
//	                        所有上游」的，而形状错误恰恰是「只有这家挑食」。
//	429                   → 走 + 记限流
//	401/402/403/404       → 走 + 记配置（402 insufficient balance 是确定性的
//	                        配置问题：这一家没钱了，重试它没有意义，换下一家是
//	                        唯一可能成功的方向）
//	408/409/5xx           → 走 + 记可用性
//	其它 4xx（含非形状 400）→ 不记不换：请求本身有问题，换谁都一样
//	3xx / 2xx             → 什么都不做（成功路径由 KindUpstreamSuccess 表达）
//
// 链预算耗尽不在这里：它不是上游的收场，数据面把 IsLast 置位即可——预算没了
// 就只试当前这一站，失败照样记账。
func Classify(in Input) Verdict {
	// 已经往客户端写过字节：无论如何都不能换站。
	advance := !in.Written && !in.IsLast

	switch in.Kind {
	case KindUpstreamSuccess:
		return Verdict{}
	case KindClientCancel:
		// 客户端自己走了，对面已经没人接。再沿链重试只是拿别人的钱打水漂，
		// 而且真去重试了也没人能收到结果。
		return Verdict{}
	case KindConnError:
		return Verdict{Advance: advance, Bucket: BucketAvailability}
	case KindStreamTruncated:
		return Verdict{Advance: false, Bucket: BucketAvailability}
	}

	// KindUpstreamStatus
	switch {
	case in.Status == 400 && in.Shape:
		return Verdict{Advance: advance, Bucket: BucketShape}
	case in.Status == 429:
		return Verdict{Advance: advance, Bucket: BucketRateLimit}
	case in.Status == 401, in.Status == 402, in.Status == 403, in.Status == 404:
		return Verdict{Advance: advance, Bucket: BucketConfig}
	case in.Status == 408, in.Status == 409, in.Status >= 500:
		return Verdict{Advance: advance, Bucket: BucketAvailability}
	case in.Status == 400 && in.FallbackOn400:
		// 用户明确要求 400 也往下试。仍然不记账——请求本身的问题不是
		// 上游的可用性问题。
		return Verdict{Advance: advance}
	}
	// 其它 4xx：请求本身有问题。往下走就是拿坏请求撞遍所有上游。
	return Verdict{}
}
