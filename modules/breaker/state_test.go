package breaker

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// clocked 造一份带注入时钟的健康表。状态机全靠时间推进，所以测试里不 sleep：
// 老实现靠 `b.Cooldown = 5*time.Millisecond` + `time.Sleep` 摸黑走，既慢又飘。
// 返回的第二个函数把时钟往前拨。
//
// 顺带注册一条测试用的形状判据（见 testShape）。2026-09-17 起真实判据由上游
// 模块注册（modules/deepseek/shape.go），core 里不再有那条字符串——但状态机
// 的「形状账本永不摘牌」这条策略仍然要能被测到，所以这里挂一条等价的。
func clocked() (*table, func(time.Duration)) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	b := newTable()
	b.now = func() time.Time { return now }
	if _, err := b.RegisterShapeDetector(testShape{}); err != nil {
		panic(err)
	}
	return b, func(d time.Duration) { now = now.Add(d) }
}

// testShape 是 breaker 内部的测试判据：认「must be passed back + 点名了字段」
// 这个形状（真判据在 modules/deepseek）。放在这里是为了让状态机的测试不依赖
// 别的模块，同时保持判据的**形态**与真实情况一致。
type testShape struct{}

func (testShape) Name() string { return "test-shape" }

func (testShape) Match(status int, body []byte) bool {
	if status != 400 {
		return false
	}
	if !bytes.Contains(body, []byte("must be passed")) {
		return false
	}
	return bytes.Contains(body, []byte("reasoning_content")) ||
		bytes.Contains(body, []byte("content[].thinking"))
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// connFail 记一次「连接失败」，把 binding 往可用性账本上推一次。
func connFail(b *table, provider, model string) Result {
	return b.Report(provider, model, Input{Kind: KindConnError})
}

func state(t *testing.T, b *table, provider, model string) Status {
	t.Helper()
	for _, s := range b.Snapshot() {
		if s.Provider == provider && s.Model == model {
			return s
		}
	}
	t.Fatalf("快照里没有 %s/%s", provider, model)
	return Status{}
}

func TestAvailabilityNeedsConsecutiveFailures(t *testing.T) {
	b, _ := clocked()
	if r := connFail(b, "relay", "model"); r.Opened {
		t.Fatal("第一次真实流量失败不应开闸")
	}
	if !b.Available("relay", "model") {
		t.Fatal("第一次失败后 binding 就被摘了")
	}
	if r := connFail(b, "relay", "model"); !r.Opened {
		t.Fatal("第二次连续失败应开闸")
	}
	if b.Available("relay", "model") {
		t.Fatal("开闸后 binding 仍然可用")
	}
}

// TestBucketSwitchRestartsTheStreak：连续失败的定义是「同一类问题连着来」。
// 一条 binding 连吃一次连接失败和一次 429，两边各一次，还不足以说明它坏了。
func TestBucketSwitchRestartsTheStreak(t *testing.T) {
	b, _ := clocked()
	connFail(b, "relay", "model") // 可用性 1
	rate := func() Result {
		return b.Report("relay", "model", Input{Kind: KindUpstreamStatus, Status: 429})
	}
	if rate().Opened {
		t.Fatal("换成限流账本后第一次就开闸了")
	}
	if rate().Opened {
		t.Fatal("限流账本第二次就开闸了（阈值是 3）")
	}
	if !rate().Opened {
		t.Fatal("限流账本连续第三次失败应开闸")
	}
	if got := state(t, b, "relay", "model"); got.Rule != "rate limit" {
		t.Fatalf("开闸原因应是限流: %+v", got)
	}
}

// TestPerBucketPolicy：三本账的阈值/冷却互不相同，这是 2026-09-17 的核心行为
// 变化——以前一个 429 和一次连接超时用同一套「两次 60 秒」的账。
func TestPerBucketPolicy(t *testing.T) {
	tests := []struct {
		name      string
		in        Input
		threshold int
		cooldown  time.Duration
		rule      string
	}{
		{"连接失败", Input{Kind: KindConnError}, 2, 60 * time.Second, "availability"},
		{"首字节超时（客户端侧无差别）", Input{Kind: KindConnError}, 2, 60 * time.Second, "availability"},
		{"500", Input{Kind: KindUpstreamStatus, Status: 500}, 2, 60 * time.Second, "availability"},
		{"503", Input{Kind: KindUpstreamStatus, Status: 503}, 2, 60 * time.Second, "availability"},
		{"流中途断", Input{Kind: KindStreamTruncated, Written: true}, 2, 60 * time.Second, "availability"},
		{"429", Input{Kind: KindUpstreamStatus, Status: 429}, 3, 20 * time.Second, "rate limit"},
		{"401", Input{Kind: KindUpstreamStatus, Status: 401}, 1, 5 * time.Minute, "config"},
		{"403", Input{Kind: KindUpstreamStatus, Status: 403}, 1, 5 * time.Minute, "config"},
		{"404", Input{Kind: KindUpstreamStatus, Status: 404}, 1, 5 * time.Minute, "config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := clocked()
			for i := 1; i < tt.threshold; i++ {
				if r := b.Report("p", "m", tt.in); r.Opened {
					t.Fatalf("第 %d 次就开闸了，阈值应是 %d", i, tt.threshold)
				}
				if !b.Available("p", "m") {
					t.Fatalf("第 %d 次就摘牌了，阈值应是 %d", i, tt.threshold)
				}
			}
			if r := b.Report("p", "m", tt.in); !r.Opened {
				t.Fatalf("第 %d 次没有开闸", tt.threshold)
			}
			got := state(t, b, "p", "m")
			if got.Rule != tt.rule {
				t.Fatalf("原因应是 %s: %+v", tt.rule, got)
			}
			if got.CooldownMs != tt.cooldown.Milliseconds() {
				t.Fatalf("首次冷却应是 %v，实际 %dms", tt.cooldown, got.CooldownMs)
			}
			if got.State != "open" || !got.Open {
				t.Fatalf("状态应是 open: %+v", got)
			}
		})
	}
}

// TestShapeErrorsOnlyCount：形状错误永不摘牌，但要能被看见。
func TestShapeErrorsOnlyCount(t *testing.T) {
	b, _ := clocked()
	in := Input{Kind: KindUpstreamStatus, Status: 400, Body: []byte(shapeBody)}
	for i := 0; i < 50; i++ {
		if r := b.Report("p", "m", in); r.Opened {
			t.Fatalf("第 %d 次形状错误把闸打开了", i+1)
		}
	}
	if !b.Available("p", "m") {
		t.Fatal("形状错误摘掉了一个可用的 binding")
	}
	got := state(t, b, "p", "m")
	if got.ShapeSkips != 50 || got.Fails != 0 || got.State != "closed" || got.Rule != "" {
		t.Fatalf("形状错误应当只计数: %+v", got)
	}
}

// TestHalfOpenAdmitsExactlyOneTrial：半开期严格放行**一次**。
func TestHalfOpenAdmitsExactlyOneTrial(t *testing.T) {
	b, advance := clocked()
	connFail(b, "p", "m")
	connFail(b, "p", "m")

	advance(59 * time.Second)
	if b.Available("p", "m") {
		t.Fatal("冷却没到就放行了试探")
	}
	advance(time.Second) // 正好到期
	if !b.Available("p", "m") {
		t.Fatal("冷却到期后没有放行试探")
	}
	if b.Available("p", "m") {
		t.Fatal("半开期放行了第二个并发试探")
	}
	if got := state(t, b, "p", "m"); got.State != "half-open" || !got.Trial {
		t.Fatalf("状态应是 half-open 且有试探在飞: %+v", got)
	}

	// 试探发出后一直没回报（客户端中途取消、进程被换掉）：TrialTTL 到期
	// 就当它丢了，允许再放一次——否则一个没回报的试探会把 binding 永远
	// 卡在「半开、有人正在试」。
	advance(45 * time.Second)
	if !b.Available("p", "m") {
		t.Fatal("试探 TTL 过期后没有再放行")
	}
}

func TestTrialFailureReopensWithBackoff(t *testing.T) {
	b, advance := clocked()
	connFail(b, "p", "m")
	connFail(b, "p", "m") // 首次开闸：60s

	// 三次「冷却到期 → 放行试探 → 试探失败 → 回闸」，冷却应翻倍。
	for _, want := range []time.Duration{120 * time.Second, 240 * time.Second, 480 * time.Second} {
		advance(want / 2)
		if !b.Available("p", "m") {
			t.Fatalf("冷却 %v 到期后没有放行试探", want/2)
		}
		r := connFail(b, "p", "m")
		if !r.Opened {
			t.Fatalf("半开试探失败没有立刻回闸（应放行后涨到 %v）", want)
		}
		got := state(t, b, "p", "m")
		if got.CooldownMs != want.Milliseconds() {
			t.Fatalf("退避应是 %v，实际 %dms", want, got.CooldownMs)
		}
		if got.State != "open" {
			t.Fatalf("试探失败后状态应是 open: %+v", got)
		}
	}

	// 继续翻倍会撞上 10 分钟封顶。
	advance(480 * time.Second)
	if !b.Available("p", "m") {
		t.Fatal("封顶后没有放行试探")
	}
	connFail(b, "p", "m")
	if got := state(t, b, "p", "m"); got.CooldownMs != (10 * time.Minute).Milliseconds() {
		t.Fatalf("冷却应在 10 分钟封顶，实际 %dms", got.CooldownMs)
	}
}

func TestTrialSuccessClosesAndResetsBackoff(t *testing.T) {
	b, advance := clocked()
	connFail(b, "p", "m")
	connFail(b, "p", "m")

	// 先失败一次把冷却推到 120s。
	advance(60 * time.Second)
	if !b.Available("p", "m") {
		t.Fatal("冷却到期后没有放行试探")
	}
	connFail(b, "p", "m")

	advance(120 * time.Second)
	if !b.Available("p", "m") {
		t.Fatal("退避后的冷却到期没有放行试探")
	}
	b.Report("p", "m", Input{Kind: KindUpstreamSuccess})

	got := state(t, b, "p", "m")
	if got.State != "closed" || got.Open || got.Fails != 0 || got.CooldownMs != 0 {
		t.Fatalf("试探成功应合闸并让退避归位: %+v", got)
	}
	if !b.Available("p", "m") {
		t.Fatal("合闸后 binding 仍不可用")
	}

	// 合闸后再出两次失败，冷却必须从**基准** 60s 重新开始，而不是接着退避。
	connFail(b, "p", "m")
	connFail(b, "p", "m")
	if got := state(t, b, "p", "m"); got.CooldownMs != (60 * time.Second).Milliseconds() {
		t.Fatalf("合闸后冷却没有回到基准: %+v", got)
	}
}

// TestSuccessDuringIsolationDoesNotUnseal：隔离期内的成功来自更早建链的请求
// （链是每个请求开始时建的），不能当作恢复的证据——否则一个坏上游只要偶尔
// 漏一个成功就能一直赖在链上。
func TestSuccessDuringIsolationDoesNotUnseal(t *testing.T) {
	b, advance := clocked()
	connFail(b, "p", "m")
	connFail(b, "p", "m")

	advance(30 * time.Second)
	b.Report("p", "m", Input{Kind: KindUpstreamSuccess})
	if b.Available("p", "m") {
		t.Fatal("隔离期内的成功把闸解开了")
	}
	if got := state(t, b, "p", "m"); got.State != "open" {
		t.Fatalf("隔离期内的成功改了状态: %+v", got)
	}
}

// TestFailuresDuringIsolationAreNotNewEvidence：隔离期内的失败同样来自更早
// 建链的请求，不构成新证据：既不再数阈值，也不延长隔离。
func TestFailuresDuringIsolationAreNotNewEvidence(t *testing.T) {
	b, advance := clocked()
	connFail(b, "p", "m")
	connFail(b, "p", "m")
	before := state(t, b, "p", "m")

	advance(30 * time.Second)
	for i := 0; i < 10; i++ {
		if r := connFail(b, "p", "m"); r.Opened {
			t.Fatal("隔离期内的失败被当成了新的开闸事件")
		}
	}
	after := state(t, b, "p", "m")
	if after.OpenUntil != before.OpenUntil {
		t.Fatalf("隔离期内的失败延长了隔离: %v -> %v", before.OpenUntil, after.OpenUntil)
	}
	if after.Fails != before.Fails {
		t.Fatalf("隔离期内的失败被记进了账本: %d -> %d", before.Fails, after.Fails)
	}
}

// TestProbeBypassesThreshold：probe 是主动、独立的健康请求，结论是权威的
// ——差就当场摘，好就当场放（**隔离期内也一样**）。
func TestProbeBypassesThreshold(t *testing.T) {
	b, advance := clocked()
	if _, open := b.RecordProbe("p", "m", 503, 1024, time.Second, 12*time.Second, ""); !open {
		t.Fatal("一次失败的 probe 没有当场摘牌")
	}
	if b.Available("p", "m") {
		t.Fatal("probe 摘牌后 binding 仍然可用")
	}

	// 2026-11-04 现场：用户在隔离期里 probe 成功，请求却继续 404 到冷却期满
	// ——唯一能做的恢复动作变成了安慰剂。成功的主动探活必须立刻回链。
	advance(30 * time.Second)
	if grade, open := b.RecordProbe("p", "m", 200, 1024, time.Second, 12*time.Second, ""); grade != ProbeFluent || open {
		t.Fatalf("成功的 probe 应立即解封: grade=%s open=%v", grade, open)
	}
	if !b.Available("p", "m") {
		t.Fatal("probe 解封后 binding 仍不可用")
	}
	// 退避归位：下一次真失败重新从基准冷却开始，不背上一轮的账。
	if got := state(t, b, "p", "m"); got.State != "closed" || got.Open {
		t.Fatalf("解封后状态不对: %+v", got)
	}
}

// TestAvailableIsTheOnlyPathThatOpensTheTrialWindow 锁住状态机的读路径唯一性：
// 只有 Available 推进状态机。Rank/Score/Snapshot 都是纯读。
func TestAvailableIsTheOnlyPathThatOpensTheTrialWindow(t *testing.T) {
	b, advance := clocked()
	connFail(b, "p", "m")
	connFail(b, "p", "m")
	advance(time.Minute)

	b.Rank("p", "m", 1024)
	b.Score("p", "m", 1024)
	state(t, b, "p", "m")
	if got := state(t, b, "p", "m"); got.Trial {
		t.Fatal("读路径（Rank/Score/Snapshot）放行了试探名额")
	}
	if !b.Available("p", "m") {
		t.Fatal("Available 没有放行试探")
	}
}

// TestConcurrentAvailableGrantsOneTrial 用真时钟跑并发：建链期多个请求同时
// 问同一个 binding，只能有一个拿到试探名额。
func TestConcurrentAvailableGrantsOneTrial(t *testing.T) {
	b := newTable()
	b.SetPolicy(Policy{Rules: map[Bucket]Rule{
		BucketAvailability: {Threshold: 1, Cooldown: time.Millisecond},
	}})
	connFail(b, "p", "m")
	time.Sleep(5 * time.Millisecond)

	const n = 32
	var granted int32
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Available("p", "m") {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if granted != 1 {
		t.Fatalf("%d 个并发请求里有 %d 个拿到试探名额，应恰好 1 个", n, granted)
	}
}

// TestPreSealDiagnosticSparesAHealthyBinding 是 2026-09-17 现场的直接回归：
// smt-deepseek 被真实流量的**首字节超时**连续数到阈值摘掉，而每次
// `newgate probe` 都是 fluent。上闸前必须再要一次主动证据。
func TestPreSealDiagnosticSparesAHealthyBinding(t *testing.T) {
	b, _ := clocked()
	calls := 0
	b.SetVerifier(func(provider, model string) bool {
		calls++
		if provider != "p" || model != "m" {
			t.Fatalf("诊断探活问错了 binding: %s/%s", provider, model)
		}
		return true // 探活说它还通
	})

	connFail(b, "p", "m")
	r := connFail(b, "p", "m") // 数到阈值
	if r.Opened {
		t.Fatal("诊断探活说可用，却还是摘了")
	}
	if !r.Spared {
		t.Fatal("应该报 spared")
	}
	if calls != 1 {
		t.Fatalf("诊断探活跑了 %d 次，应恰好 1 次", calls)
	}
	if !b.Available("p", "m") {
		t.Fatal("被救回来的 binding 却不在链上")
	}
	got := state(t, b, "p", "m")
	if got.Fails != 0 || got.Spared != 1 || got.Open || got.State != "closed" {
		t.Fatalf("救回来之后账本不对: %+v", got)
	}
	if got.Reason == "" {
		t.Fatal("救回来的原因要留在快照里（不静默）")
	}

	// 计数清零的**意义**：下一次失败要重新从 1 开始数，而不是一上来又到阈值
	// 再探一次——否则一条间歇性抖动的 binding 会每个请求都探活一次。
	connFail(b, "p", "m")
	if calls != 1 {
		t.Fatalf("计数没有清零，又探了一次（第 %d 次）", calls)
	}

	// 诊断改口说不可用：第二次到达阈值就必须真摘。
	b.SetVerifier(func(string, string) bool { calls++; return false })
	if r := connFail(b, "p", "m"); !r.Opened || r.Spared {
		t.Fatalf("诊断说不可用却没摘: %+v", r)
	}
	if calls != 2 {
		t.Fatalf("诊断应只在到达阈值时各跑一次，实际 %d 次", calls)
	}
}

// TestPreSealDiagnosticSealsWhenProbeAlsoFails：诊断说不可用就照摘，理由里
// 要能看出「这次是双重证据」，好和「纯被动流量摘的」区分开。
func TestPreSealDiagnosticSealsWhenProbeAlsoFails(t *testing.T) {
	b, _ := clocked()
	b.SetVerifier(func(string, string) bool { return false })
	connFail(b, "p", "m")
	if r := connFail(b, "p", "m"); !r.Opened || r.Spared {
		t.Fatalf("诊断说不可用却没摘: %+v", r)
	}
	got := state(t, b, "p", "m")
	if got.State != "open" || !strings.Contains(got.Reason, "diagnostic probe also failed") {
		t.Fatalf("开闸原因应写明诊断结论: %+v", got)
	}
}

// TestPreSealDiagnosticRunsOnceForConcurrentFailures：并发失败只该探活一次。
func TestPreSealDiagnosticRunsOnceForConcurrentFailures(t *testing.T) {
	b := newTable()
	b.SetPolicy(Policy{Rules: map[Bucket]Rule{
		BucketAvailability: {Threshold: 1, Cooldown: time.Minute},
	}})
	var mu sync.Mutex
	calls := 0
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	b.SetVerifier(func(string, string) bool {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			entered <- struct{}{}
			<-release // 卡住第一个诊断，让后面的失败都撞进 verifying
		}
		return true
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); connFail(b, "p", "m") }()
	<-entered
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); connFail(b, "p", "m") }()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("并发失败触发了 %d 次诊断探活，应恰好 1 次", calls)
	}
}

// TestHalfOpenTrialFailureSkipsDiagnostic：半开试探失败是**主动**证据，不该
// 再要一次诊断——那只会拖长坏上游的隔离。
func TestHalfOpenTrialFailureSkipsDiagnostic(t *testing.T) {
	b, advance := clocked()
	calls := 0
	b.SetVerifier(func(string, string) bool { calls++; return true })

	connFail(b, "p", "m")
	connFail(b, "p", "m") // 这次会诊断（结果说健康 → 不摘）
	if calls != 1 {
		t.Fatalf("第一次到阈值应诊断一次，实际 %d", calls)
	}

	// 让这条 binding 真的被摘掉（诊断改口说不可用），再等冷却放行试探。
	b.SetVerifier(func(string, string) bool { calls++; return false })
	connFail(b, "p", "m")
	if r := connFail(b, "p", "m"); !r.Opened {
		t.Fatal("诊断说不可用却没摘")
	}
	before := calls
	advance(time.Minute)
	if !b.Available("p", "m") {
		t.Fatal("冷却到期没有放行试探")
	}
	if r := connFail(b, "p", "m"); !r.Opened {
		t.Fatal("半开试探失败没有立刻回闸")
	}
	if calls != before {
		t.Fatalf("半开试探失败不该再要诊断（多了 %d 次）", calls-before)
	}
}
