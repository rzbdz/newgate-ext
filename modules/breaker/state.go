package breaker

import (
	"sync"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

type probeResult struct {
	Grade     ProbeGrade
	LatencyMs int64
	CheckedAt time.Time
}

// record 是一个 binding 的全部健康状态。
//
// 状态的四个形态：
//
//	closed      openedAt 为零。正常参与建链。
//	open        openedAt 非零、now < openUntil。摘牌中，不进候选链。
//	half-open   open 且 now >= openUntil。Available 会放行**一次**真实请求
//	            做试探（halfOpenAt 是这次试探的失效时刻）；其它并发请求仍然
//	            被挡在外面。
//	closed(回)  试探成功 → openedAt 清零、退避归位。
//
// 为什么要有 half-open：「摘了只能靠手动 probe 放回来」是 2026-09-17 之前的
// 硬伤——ark 被真实连接超时摘掉后卡了 16 分钟，直到用户手敲 `newgate probe`。
// 半开让真实流量自己证明恢复。安全性由三点保住：只在冷却期满放行、半开期严格
// 一次、试探失败立刻回闸并把冷却翻倍（60s → 120s → 240s …，10 分钟封顶），
// 所以坏上游不会每分钟回来撞一次。
type record struct {
	fails      int       // 当前这本账的连续失败数
	bucket     Bucket    // fails 记的是哪本账（换账本要清零重数）
	openedAt   time.Time // 非零 = 已摘牌
	openUntil  time.Time // 冷却何时到期
	halfOpenAt time.Time // 非零 = 半开试探在飞，值 = 试探失效时刻
	cooldown   time.Duration
	reason     string
	shapeSkips int // 请求形状错误的次数（永不摘牌，只计数）
	spared     int // 上闸前诊断探活把它救回来的次数
	// verifying 这一刻有一个「上闸前诊断」在飞。它挡的是并发重复诊断：
	// 同一个 binding 同时来两条失败，只该探活一次。
	verifying bool
}

func (r *record) state(now time.Time) string {
	switch {
	case r.openedAt.IsZero():
		return "closed"
	case now.Before(r.openUntil):
		return "open"
	default:
		return "half-open"
	}
}

// table 是健康表本体。**一个 binding 一行**（provider + model），不是一
// provider 一行——同一家上游的不同模型经常一个通一个不通。
type table struct {
	mu      sync.Mutex
	records map[string]*record
	probes  map[string]probeResult
	ranker  *ranker
	shapes  shapeRegistry
	policy  Policy
	now     func() time.Time // 测试注入时钟；生产恒为 time.Now

	file         string
	persistedAt  time.Time
	persistTimer *time.Timer
	onError      func(error)
	loadErr      error // 装载期失败，等 SetErrorHandler 装上后补报
	verify       func(provider, model string) bool
}

func newTable() *table {
	return &table{
		records: map[string]*record{},
		probes:  map[string]probeResult{},
		ranker:  newRanker(),
		policy:  DefaultPolicy(),
		now:     time.Now,
	}
}

// SetPolicy 整份替换策略；零值项按 DefaultPolicy 补齐。
//
// 今天**没有生产调用者**（零值策略就是 DefaultPolicy），也**不在公开的 Breaker
// 接口上**——理由见 api.go 那段收窄说明。
func (b *table) SetPolicy(p Policy) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.policy = p.normalized()
}

// SetErrorHandler 注入持久化错误出口；健康状态不能因后台写盘失败而静默丢失。
//
// 与 SetVerifier 一样，它不在公开的 Breaker 接口上：唯一的调用者是 BindEnv
// （见 api.go 的收窄说明）。
//
// 装载期的错误（health.json 坏了）也走这里：那一刻还没有出口，所以先存着，
// 出口一装上就立刻补报——「不静默」是这个仓库的硬要求。
func (b *table) SetErrorHandler(fn func(error)) {
	b.mu.Lock()
	b.onError = fn
	pending := b.loadErr
	b.loadErr = nil
	b.mu.Unlock()
	if pending != nil && fn != nil {
		fn(pending)
	}
}

// SetVerifier 注入「上闸前的最后一次诊断」。
//
// 连续失败数到达阈值时先别摘：调这个函数做几次主动探活，探活说它还通就不摘、
// 计数清零。理由是真实现场（2026-09-17）：smt-deepseek 被摘了很多次，每次
// `newgate probe` 都是 fluent——因为真实流量失败的是**第一字节超时**（分类器
// 那条链把 126KB 的 system 塞进 12s 的紧预算），而探活发的是最小请求，永远探
// 不到这个边界。两者的结论不一致时，谁的证据更硬？探活是**主动、可控、可重复**
// 的，被动流量则是单点、受上下文尺寸和排队影响的。摘牌会让用户被悄悄换给别的
// 模型，代价不对称，所以上闸前必须再要一次主动证据。
//
// 返回 true = 这条 binding 仍然可用（不摘）；false = 确认不可用（照摘）。
// nil（默认）＝不做这一步，行为与以前完全一致。
//
// **它不在公开的 Breaker 接口上**（2026-09-21 收窄）：全仓库唯一的调用者是下面
// BindEnv 那一处，而把它摆在接口上意味着任何拿到健康表 capability 的模块都能
// 一句 `SetVerifier(nil)` 把这道安全诊断**静默**卸掉——不报错、依赖图上没有边、
// 改动时也看不出来。一个能力只该有一扇门，门开在拥有它的模块上。
func (b *table) SetVerifier(fn func(provider, model string) bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.verify = fn
}

func bindingKey(provider, model string) string { return provider + "\x00" + model }

func (b *table) recordLocked(provider, model string) *record {
	key := bindingKey(provider, model)
	r := b.records[key]
	if r == nil {
		r = &record{}
		b.records[key] = r
	}
	return r
}

// Available 回答这个 binding 现在能不能进候选链。
//
// 它是**唯一**会推进状态机的读路径：冷却期满时顺手把 binding 推进半开，并发放
// 这一轮的试探名额。建链期每个候选只问一次，所以「放行一次」在这里天然成立。
func (b *table) Available(provider, model string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.records[bindingKey(provider, model)]
	if r == nil || r.openedAt.IsZero() {
		return true
	}
	now := b.now()
	if now.Before(r.openUntil) {
		return false
	}
	if !r.halfOpenAt.IsZero() && now.Before(r.halfOpenAt) {
		return false // 已经有一个试探在飞，不并发放第二个
	}
	r.halfOpenAt = now.Add(b.policy.TrialTTL)
	return true
}

// Report 回报一次上游交互的结局，并返回判决。
//
// 这是数据面唯一需要的记账入口：分类（Classify）与状态迁移都在这里，调用方
// 只负责把「实际发生了什么」如实描述出来。2026-09-17 之前数据面自己在两处
// 分支里各判断一次 4xx 的语义，两处判据不一致，形状错误在链中间会摘牌、在
// 链尾不会。
func (b *table) Report(provider, model string, in Input) Result {
	detector, shape := b.shapeOf(in.Status, in.Body)
	in.Shape = shape
	v := Classify(in)
	if in.Kind != KindUpstreamSuccess && v.Bucket == BucketNone {
		// 客户端取消、"换谁都一样"的 4xx：什么都不改，也**不要**在账本里
		// 造一行空记录——不然每次取消都会往 `newgate breaker` 里塞一行
		// 全零的 binding。
		return Result{Verdict: v}
	}

	b.mu.Lock()
	now := b.now()

	switch {
	case in.Kind == KindUpstreamSuccess:
		// 只碰已经存在的记录：成功的 binding 不需要为它凭空造一行，它的
		// 延迟样本另有落处（ranker），而状态迁移只可能发生在已有记录上。
		if r := b.records[bindingKey(provider, model)]; r != nil {
			b.succeedLocked(r, now)
		}
		b.mu.Unlock()
		return Result{Verdict: v}
	case v.Bucket == BucketShape:
		r := b.recordLocked(provider, model)
		r.shapeSkips++
		b.persistLocked()
		b.mu.Unlock()
		// Shape 带上认领的判据名：数据面据此打专属日志并存证据，而它不需要
		// 认识任何上游专有字符串——名字是检测器自己起的。
		return Result{Verdict: v, Shape: detector}
	}

	r := b.recordLocked(provider, model)
	opened, needsVerify := b.failLocked(r, v.Bucket, now)
	verify := b.verify
	if !needsVerify {
		b.mu.Unlock()
		return Result{Verdict: v, Opened: opened}
	}
	if verify == nil {
		// 没装诊断：照旧直接开闸。行为与 2026-09-17 之前完全一致，
		// 装配里没接探活能力时不会因此少摘一条坏 binding。
		b.openLocked(r, b.policy.rule(v.Bucket), now,
			i18n.T("consecutive real-traffic failures ({bucket})",
				i18n.A{"bucket": v.Bucket.ruleName()}))
		b.mu.Unlock()
		return Result{Verdict: v, Opened: true}
	}
	// 到阈值了，但**先别摘**：到锁外去做一次主动诊断（探活要发网络请求，
	// 不能拿着状态机的锁做）。只让一个 goroutine 诊断，其余失败照常计数。
	r.verifying = true
	b.mu.Unlock()

	healthy := verify(provider, model)

	b.mu.Lock()
	defer b.mu.Unlock()
	r = b.records[bindingKey(provider, model)]
	if r == nil {
		return Result{Verdict: v}
	}
	r.verifying = false
	if healthy {
		// 诊断说它还通：这次连续失败是单点/上下文相关的（首字节超时、排队、
		// 大 prefill），不是这家不行。计数清零，不摘牌。
		r.fails = 0
		r.bucket = BucketNone
		r.spared++
		r.reason = i18n.T("pre-trip diagnostic probe proved it healthy ({bucket} counter cleared)",
			i18n.A{"bucket": v.Bucket.ruleName()})
		b.persistLocked()
		return Result{Verdict: v, Spared: true}
	}
	b.openLocked(r, b.policy.rule(v.Bucket), b.now(),
		i18n.T("consecutive real-traffic failures and the diagnostic probe also failed ({bucket})",
			i18n.A{"bucket": v.Bucket.ruleName()}))
	return Result{Verdict: v, Opened: true}
}

// succeedLocked 记一次真实成功。
//
// 「成功」并不总是能解封：冷却期内的成功可能来自更早建链的请求（链是每个请求
// 开始时建的），它不能当作恢复的证据——否则一个坏上游只要偶尔漏一个成功就能
// 一直赖在链上。冷却期满之后的成功才是半开试探的结论，那才解封。
func (b *table) succeedLocked(r *record, now time.Time) {
	r.fails = 0
	if r.openedAt.IsZero() || now.Before(r.openUntil) {
		return
	}
	b.unsealLocked(r)
}

// unsealLocked 立刻合闸并把退避归位。给**主动探活**的权威结论用：
// 它不受隔离期约束（见 RecordProbe）。
func (b *table) unsealLocked(r *record) {
	r.openedAt = time.Time{}
	r.openUntil = time.Time{}
	r.halfOpenAt = time.Time{}
	r.cooldown = 0 // 退避归位：下次再从基准冷却开始
	r.reason = ""
	r.bucket = BucketNone
	b.persistLocked()
}

// failLocked 记一次失败，返回 (这次把闸打开了吗, 要不要先做上闸前诊断)。
//
// 半开试探的失败是**当场定案**的，不走诊断：那一次试探本身就是主动证据，
// 再要一次只是拖长坏上游的隔离。诊断只服务于「被动流量把一条可能还活着的
// binding 数到阈值」这一种情形。
func (b *table) failLocked(r *record, bucket Bucket, now time.Time) (bool, bool) {
	rule := b.policy.rule(bucket)
	if rule.Threshold <= 0 {
		return false, false // 这本账永不摘牌
	}
	if !r.openedAt.IsZero() {
		if now.Before(r.openUntil) {
			// 隔离期内的失败来自更早建链的请求，不构成新证据：既不再数
			// 阈值，也不延长隔离。
			return false, false
		}
		// 冷却已过（半开）：这一次失败就是试探的结论，立刻回闸并退避。
		// 不重新数阈值——试探本身就是那一票。
		r.halfOpenAt = time.Time{}
		b.openLocked(r, rule, now,
			i18n.T("half-open trial failed ({bucket})", i18n.A{"bucket": bucket.ruleName()}))
		return true, false
	}
	if r.bucket != bucket {
		// 换账本了：连续失败的定义是「同一类问题连着来」，不是「各种问题
		// 凑够两次」。一条 binding 连吃 429 和连接超时，两边各一次，还不
		// 足以说明它坏了。
		r.bucket, r.fails = bucket, 0
	}
	r.fails++
	if r.fails < rule.Threshold {
		b.persistLocked() // 未开闸也要落盘：不然重启等于白送一次免死金牌
		return false, false
	}
	if r.verifying {
		// 已经有一个诊断在飞：这一条只计数，等它的结论。两个并发失败同时
		// 去探活是白花两倍的钱买同一个答案。
		b.persistLocked()
		return false, false
	}
	return false, true
}

func (b *table) openLocked(r *record, rule Rule, now time.Time, reason string) {
	switch {
	case r.cooldown <= 0:
		r.cooldown = rule.Cooldown
	case rule.Backoff > 1:
		r.cooldown = time.Duration(float64(r.cooldown) * rule.Backoff)
	}
	if rule.MaxCooldown > 0 && r.cooldown > rule.MaxCooldown {
		r.cooldown = rule.MaxCooldown
	}
	if r.cooldown <= 0 {
		r.cooldown = rule.Cooldown
	}
	r.openedAt = now
	r.openUntil = now.Add(r.cooldown)
	r.halfOpenAt = time.Time{}
	r.reason = reason
	b.persistLocked()
}

// RecordProbe 记录主动探活的四档结论，并返回评级和是否仍然熔断。
//
// probe 是主动、独立的健康请求，所以它**不受阈值约束**：结论差就当场摘，
// 结论好就当场放（隔离期内也一样，见下面那段）。这与真实流量那条路不同——
// 那条路上单次成功不足以证明什么，单次失败也不足以定案。
func (b *table) RecordProbe(provider, model string, status, contextBytes int,
	latency, slowAfter time.Duration, probeErr string) (ProbeGrade, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := bindingKey(provider, model)
	now := b.now()
	grade := ProbeFluent
	reason := ""
	switch {
	case status == 200 && latency > slowAfter:
		grade, reason = ProbeLaggy, i18n.T("probe {latency} exceeded {limit}",
			i18n.A{"latency": latency.Round(time.Millisecond).String(), "limit": slowAfter.String()})
	case probeErr != "":
		grade, reason = ProbeUnavailable,
			i18n.T("probe failed: {err}", i18n.A{"err": probeErr})
	case status != 200:
		grade, reason = ProbeUnavailable,
			i18n.T("probe HTTP {status}", i18n.A{"status": status})
	case latency >= 3*time.Second:
		grade = ProbeUsable
	}
	b.probes[key] = probeResult{
		Grade: grade, LatencyMs: latency.Milliseconds(), CheckedAt: now,
	}
	if latency > 0 {
		b.ranker.observe(key, contextBytes, latency)
	}
	r := b.recordLocked(provider, model)

	if reason != "" {
		rule := b.policy.rule(BucketAvailability)
		r.cooldown = 0 // probe 的结论是权威的：退避重新从基准开始
		b.openLocked(r, rule, now, reason)
		r.fails = rule.Threshold
		return grade, true
	}
	// 成功的主动探活是**权威证据**：它刚刚真的把这条 binding 打通了，所以
	// 当场合闸——**不等隔离期满**。
	//
	// 2026-11-04 现场：用户在熔断期间手敲 probe，结论是 fluent，请求却继续
	// 404 到 60s 隔离期结束。最短隔离那层硬下限（2026-09-17 加的）防的是
	// 「上游抖一下就被 probe 立刻放回来」，但今天 probe **只有人发起**
	// （`newgate probe` / 界面上的测试按钮），不存在自动 probe 抖动的问题；
	// 真实流量那条路仍然走半开试探，短时间的抖动照样不会突破隔离。
	// 把用户唯一能做的恢复动作变成安慰剂，代价远大于那点抖动风险。
	r.fails = 0
	b.unsealLocked(r)
	return grade, false
}

// ObserveSuccess 把真实请求的首响应延迟写进对应上下文桶。EWMA 让近期表现
// 权重大，同时避免单次抖动把顺序永久改变。
func (b *table) ObserveSuccess(provider, model string, contextBytes int, ttft time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ranker.observe(bindingKey(provider, model), contextBytes, ttft)
	// 延迟样本来自热路径，不能每发请求都落盘；最多每 5 秒写一次。
	// breaker/probe 状态变化仍会立即调用 persistLocked。
	const interval = 5 * time.Second
	if time.Since(b.persistedAt) >= interval {
		b.persistLocked()
	} else if b.file != "" && b.persistTimer == nil {
		wait := interval - time.Since(b.persistedAt)
		b.persistTimer = time.AfterFunc(wait, func() {
			b.mu.Lock()
			b.persistTimer = nil
			b.persistLocked()
			b.mu.Unlock()
		})
	}
}

// Flush 把节流窗口内尚未落盘的延迟样本同步写出，供 daemon 优雅退出使用。
func (b *table) Flush() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.persistTimer != nil {
		b.persistTimer.Stop()
		b.persistTimer = nil
	}
	b.persistLocked()
}

// Score 返回当前上下文桶的预测 TTFT（毫秒）。完全未观测用 6s 中性值，
// 排在流畅之后、可用档中部。
func (b *table) Score(provider, model string, contextBytes int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if score, ok := b.ranker.score(bindingKey(provider, model), contextBytes); ok {
		return score
	}
	return 6000
}

// Rank 把延迟档位编码进排序键：流畅最前，未观测居中，可用最后。
func (b *table) Rank(provider, model string, contextBytes int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ranker.rank(bindingKey(provider, model), contextBytes)
}

// Snapshot 冻结一份现状，按 provider/model 排序，便于人读和 diff。
func (b *table) Snapshot() []Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	out := make([]Status, 0, len(b.records))
	for key := range b.entryKeysLocked() {
		provider, model := splitBindingKey(key)
		r := b.records[key]
		s := Status{Provider: provider, Model: model}
		if r != nil {
			s.Fails = r.fails
			s.Rule = r.bucket.ruleToken()
			s.ShapeSkips = r.shapeSkips
			s.Spared = r.spared
			s.CooldownMs = r.cooldown.Milliseconds()
			s.Reason = r.reason
			if !r.openedAt.IsZero() {
				s.Open = true
				s.OpenFor = now.Sub(r.openedAt)
				s.OpenedAt = r.openedAt
				s.OpenUntil = r.openUntil
			}
			s.State = r.state(now)
			s.Trial = !r.halfOpenAt.IsZero() && now.Before(r.halfOpenAt)
		}
		if s.State == "" {
			s.State = "closed"
		}
		if p, ok := b.probes[key]; ok {
			s.Grade, s.Latency, s.Checked = p.Grade, p.LatencyMs, p.CheckedAt
		}
		b.ranker.fill(&s, key)
		s.Rank = b.ranker.rank(key, 0)
		out = append(out, s)
	}
	sortStatuses(out)
	return out
}

// entryKeysLocked 是「哪些 binding 值得出现在快照/落盘里」的唯一判据。
//
// 曾经是 openedAt ∪ scores ∪ probes——漏掉了「失败过但还没被摘」的 binding，
// 于是 `newgate breaker` 看不见「失败 1 次、闸还没开」，重启也把这个计数丢了。
// 现在整张 records 都在：只要发生过任何一件事，这一行就存在。
func (b *table) entryKeysLocked() map[string]bool {
	keys := map[string]bool{}
	for key := range b.records {
		keys[key] = true
	}
	for key := range b.probes {
		keys[key] = true
	}
	for _, key := range b.ranker.keys() {
		keys[key] = true
	}
	return keys
}

func (b *table) reportLocked(err error) {
	if b.onError != nil {
		b.onError(err)
	}
}

func splitBindingKey(key string) (string, string) {
	for i := range key {
		if key[i] == 0 {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}
