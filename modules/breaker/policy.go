package breaker

import "time"

// Rule 是一本账的策略。
type Rule struct {
	// Threshold 连续失败几次开闸。**0 = 永不摘牌**（只计数），形状账本用。
	Threshold int
	// Cooldown 首次隔离时长。
	Cooldown time.Duration
	// Backoff 半开试探又失败时，下次隔离时长乘以它。1 = 不退避。
	Backoff float64
	// MaxCooldown 退避上限。
	MaxCooldown time.Duration
}

// Policy 是全部熔断策略。整份可替换（SetPolicy），方便以后挂到 state.json
// 的热更新上；现在留在 Go 里，因为这一轮要先把机制改对，旋钮后面再说。
type Policy struct {
	Rules map[Bucket]Rule
	// TrialTTL 是半开试探的存活期。试探发出后这么久还没有结论（客户端中途
	// 取消、进程被换掉），就当这次试探丢了，允许再放一次——否则一个没回报的
	// 试探会把 binding 永远卡在「半开、有人正在试」的状态里。
	TrialTTL time.Duration
}

// DefaultPolicy 是出厂策略。
//
// 三本账的数字不是随手取的：
//
//   - 可用性 2 次 / 60s：连续两次真失败基本能排除偶发。60s 是「上游抖一下」
//     的量级，退避到 10 分钟封顶，避免坏上游被反复放回来撞用户请求。
//   - 限流 3 次 / 20s：429 是秒级恢复的，用可用性那套 60s×2 去摘它过重——
//     2026-09-17 之前就是这么干的，一个限流中的 provider 会被摘到 2 分钟。
//   - 配置 1 次 / 5m / 不退避：401/403 是 key 坏了，404 是这家没这个模型。
//     两者都是确定性的，试第二次纯属浪费；退避也没意义——重试只是在等用户
//     去改配置，改成什么样不由我们决定。
//   - 形状 Threshold 0：永不摘牌。
func DefaultPolicy() Policy {
	return Policy{
		Rules: map[Bucket]Rule{
			BucketAvailability: {
				Threshold: 2, Cooldown: 60 * time.Second,
				Backoff: 2, MaxCooldown: 10 * time.Minute,
			},
			BucketRateLimit: {
				Threshold: 3, Cooldown: 20 * time.Second,
				Backoff: 2, MaxCooldown: 2 * time.Minute,
			},
			BucketConfig: {
				Threshold: 1, Cooldown: 5 * time.Minute,
				Backoff: 1, MaxCooldown: 5 * time.Minute,
			},
			BucketShape: {Threshold: 0},
		},
		TrialTTL: 45 * time.Second,
	}
}

// rule 取某本账的策略；表里没写就当「不记」。
func (p Policy) rule(b Bucket) Rule {
	if r, ok := p.Rules[b]; ok {
		return r
	}
	return Rule{}
}

// normalized 补齐零值，让 SetPolicy 的调用方只需要写想改的那几项。
func (p Policy) normalized() Policy {
	out := DefaultPolicy()
	if p.TrialTTL > 0 {
		out.TrialTTL = p.TrialTTL
	}
	for _, b := range []Bucket{BucketAvailability, BucketRateLimit, BucketConfig, BucketShape} {
		if r, ok := p.Rules[b]; ok {
			out.Rules[b] = r
		}
	}
	return out
}
