package breaker

import (
	"testing"
	"time"
)

// shapeBody 是一条会被 testShape（见 state_test.go）认领的 400 原文。
//
// 真实的判据文案不再住在这个包里——2026-09-17 起它由上游模块注册
// （modules/deepseek/shape.go），那边的测试用的是真上游响应快照。这里只需要
// 一条**形状对得上 testShape** 的 body，用来验证状态机对「形状账本」的处理
// （永不摘牌、只计数）。
const shapeBody = `{"type":"error","message":"The ` + "`reasoning_content`" +
	` in the thinking mode must be passed back to the API."}`

// TestClassify 是决策表的真值表：每一行对应 docs/05-gateway.md 里那张表的一行。
//
// 这张表是「沿不沿链走 / 记不记账 / 记进哪本账」的唯一裁判，所以它必须能被
// 穷举读完。Shape 由 Report 事先算好塞进来（见 Report），这里直接给。
func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		in   Input
		want Verdict
	}{
		// —— 成功 ——
		{"上游成功", Input{Kind: KindUpstreamSuccess}, Verdict{}},

		// —— 客户端取消：不是上游的错，也不该再往下撞 ——
		{"客户端取消（链中间）", Input{Kind: KindClientCancel}, Verdict{}},
		{"客户端取消（链尾）", Input{Kind: KindClientCancel, IsLast: true}, Verdict{}},
		{"客户端取消（已写字节）",
			Input{Kind: KindClientCancel, Written: true}, Verdict{}},

		// —— 连接类失败：换站 + 记可用性 ——
		{"连接失败（链中间）",
			Input{Kind: KindConnError}, Verdict{Advance: true, Bucket: BucketAvailability}},
		{"连接失败（链尾）",
			Input{Kind: KindConnError, IsLast: true}, Verdict{Bucket: BucketAvailability}},
		{"首字节超时（已写字节）",
			Input{Kind: KindConnError, Written: true}, Verdict{Bucket: BucketAvailability}},

		// —— 流中途断：换不了站（写出去收不回），但这是实打实的可用性问题 ——
		{"流中途断", Input{Kind: KindStreamTruncated, Written: true},
			Verdict{Bucket: BucketAvailability}},
		{"流中途断（链尾）",
			Input{Kind: KindStreamTruncated, Written: true, IsLast: true},
			Verdict{Bucket: BucketAvailability}},

		// —— 400 + 形状命中：往下试，但绝不记在这家头上 ——
		{"形状 400（链中间，fallback 关）",
			Input{Kind: KindUpstreamStatus, Status: 400, Shape: true},
			Verdict{Advance: true, Bucket: BucketShape}},
		{"形状 400（链尾）",
			Input{Kind: KindUpstreamStatus, Status: 400, Shape: true, IsLast: true},
			Verdict{Bucket: BucketShape}},
		{"形状 400（已写字节）",
			Input{Kind: KindUpstreamStatus, Status: 400, Shape: true, Written: true},
			Verdict{Bucket: BucketShape}},

		// —— 400 其它：请求本身有问题，不记不换 ——
		{"非形状 400（fallback 关）",
			Input{Kind: KindUpstreamStatus, Status: 400}, Verdict{}},
		{"非形状 400（fallback 开，链中间）",
			Input{Kind: KindUpstreamStatus, Status: 400, FallbackOn400: true}, Verdict{Advance: true}},
		{"非形状 400（fallback 开，链尾）",
			Input{Kind: KindUpstreamStatus, Status: 400, FallbackOn400: true, IsLast: true}, Verdict{}},

		// —— 限流 ——
		{"429（链中间）",
			Input{Kind: KindUpstreamStatus, Status: 429},
			Verdict{Advance: true, Bucket: BucketRateLimit}},
		{"429（链尾）",
			Input{Kind: KindUpstreamStatus, Status: 429, IsLast: true},
			Verdict{Bucket: BucketRateLimit}},

		// —— 配置：key 坏了 / 这家没这个模型 / 这家没钱了 ——
		{"401",
			Input{Kind: KindUpstreamStatus, Status: 401},
			Verdict{Advance: true, Bucket: BucketConfig}},
		{"402",
			Input{Kind: KindUpstreamStatus, Status: 402},
			Verdict{Advance: true, Bucket: BucketConfig}},
		{"402（链尾）",
			Input{Kind: KindUpstreamStatus, Status: 402, IsLast: true},
			Verdict{Bucket: BucketConfig}},
		{"403",
			Input{Kind: KindUpstreamStatus, Status: 403},
			Verdict{Advance: true, Bucket: BucketConfig}},
		{"404",
			Input{Kind: KindUpstreamStatus, Status: 404},
			Verdict{Advance: true, Bucket: BucketConfig}},
		{"404（链尾）",
			Input{Kind: KindUpstreamStatus, Status: 404, IsLast: true},
			Verdict{Bucket: BucketConfig}},

		// —— 可用性：上游挂着 / 排队 ——
		{"408", Input{Kind: KindUpstreamStatus, Status: 408},
			Verdict{Advance: true, Bucket: BucketAvailability}},
		{"409", Input{Kind: KindUpstreamStatus, Status: 409},
			Verdict{Advance: true, Bucket: BucketAvailability}},
		{"500", Input{Kind: KindUpstreamStatus, Status: 500},
			Verdict{Advance: true, Bucket: BucketAvailability}},
		{"502", Input{Kind: KindUpstreamStatus, Status: 502},
			Verdict{Advance: true, Bucket: BucketAvailability}},
		{"503", Input{Kind: KindUpstreamStatus, Status: 503},
			Verdict{Advance: true, Bucket: BucketAvailability}},
		{"599（>=500 一律算可用性）", Input{Kind: KindUpstreamStatus, Status: 599},
			Verdict{Advance: true, Bucket: BucketAvailability}},

		// —— 其它 4xx / 3xx：请求本身有问题，撞遍所有上游没有意义 ——
		{"405", Input{Kind: KindUpstreamStatus, Status: 405}, Verdict{}},
		{"418", Input{Kind: KindUpstreamStatus, Status: 418}, Verdict{}},
		{"3xx（不再转移）", Input{Kind: KindUpstreamStatus, Status: 302}, Verdict{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.in); got != tt.want {
				t.Errorf("Classify(%+v) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

// TestClassifyShapeIsExemptFromFallbackOn400：形状错误**不受**
// chain.fallback_on_400 约束。那个开关拦的是「拿坏请求撞遍所有上游」，而形状
// 错误恰恰相反——同一份 body 只有这家挑食，换一家是唯一可能成功的路。
func TestClassifyShapeIsExemptFromFallbackOn400(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		got := Classify(Input{
			Kind: KindUpstreamStatus, Status: 400, Shape: true, FallbackOn400: fallback,
		})
		if !got.Advance || got.Bucket != BucketShape {
			t.Errorf("fallback_on_400=%v 时形状 400 = %+v，应继续沿链且只计数", fallback, got)
		}
	}
}

// TestClassifyNeverAdvancesAfterWritingBytesOrAtChainEnd 是那两条不变式：
// 已经写出去的字节收不回，链上没有下一站也没得换。任何 Kind、任何状态码、
// 任何形状判定下都必须成立。
func TestClassifyNeverAdvancesAfterWritingBytesOrAtChainEnd(t *testing.T) {
	kinds := []Kind{KindUpstreamSuccess, KindConnError, KindClientCancel,
		KindUpstreamStatus, KindStreamTruncated}
	statuses := []int{0, 200, 302, 400, 401, 402, 403, 404, 408, 409, 429, 500, 502, 503}
	for _, kind := range kinds {
		for _, status := range statuses {
			for _, shape := range []bool{false, true} {
				for _, fallback := range []bool{false, true} {
					for _, written := range []bool{false, true} {
						for _, last := range []bool{false, true} {
							in := Input{
								Kind: kind, Status: status, Shape: shape,
								FallbackOn400: fallback, Written: written, IsLast: last,
							}
							if got := Classify(in); got.Advance && (written || last) {
								t.Fatalf("Classify(%+v) 在 written=%v isLast=%v 时仍然换站",
									in, written, last)
							}
						}
					}
				}
			}
		}
	}
}

// TestClassifyBucketOnlyForKnownCauses：Bucket 是「记进哪本账」，只可能来自
// 已知的那几类原因。这条挡住以后加分类时手滑给出一个没人处理的新桶。
func TestClassifyBucketOnlyForKnownCauses(t *testing.T) {
	known := map[Bucket]bool{
		BucketNone: true, BucketAvailability: true, BucketRateLimit: true,
		BucketConfig: true, BucketShape: true,
	}
	for kind := KindUpstreamSuccess; kind <= KindStreamTruncated; kind++ {
		for status := 0; status <= 600; status++ {
			for _, shape := range []bool{false, true} {
				got := Classify(Input{Kind: kind, Status: status, Shape: shape})
				if !known[got.Bucket] {
					t.Fatalf("Classify(%v, %d, shape=%v) 给出了未知的账本 %d",
						kind, status, shape, got.Bucket)
				}
				if got.Bucket == BucketShape && status != 400 {
					t.Fatalf("非 400（%d）被判成了请求形状错误", status)
				}
				// 形状账本只在 KindUpstreamStatus 下出现。
				if got.Bucket == BucketShape && kind != KindUpstreamStatus {
					t.Fatalf("%v 被判成了请求形状错误", kind)
				}
			}
		}
	}
}

// TestBucketRuleNames：账本有**两个**名字，各有各的用处，都不能随手改：
//
//   - ruleToken 落盘（health.json 与 /__newgate/status 的 `rule`），是**机器标记**，
//     与语言无关——known-good 二进制读同一份文件，换个语言重启也得认得回来；
//   - ruleName 是给人看的（表格的「账本」列、摘牌原因里那一句），走 i18n。
func TestBucketRuleNames(t *testing.T) {
	tokens := map[Bucket]string{
		BucketNone:         "",
		BucketAvailability: "availability",
		BucketRateLimit:    "rate limit",
		BucketConfig:       "config",
		BucketShape:        "request shape",
	}
	for b, token := range tokens {
		if got := b.ruleToken(); got != token {
			t.Errorf("Bucket(%d).ruleToken() = %q, want %q", b, got, token)
		}
		if token == "" {
			continue
		}
		if got := bucketFromName(token); got != b {
			t.Errorf("bucketFromName(%q) = %d, want %d", token, got, b)
		}
	}
	if got := bucketFromName("没有这本账"); got != BucketNone {
		t.Errorf("未知账本名应折成 BucketNone，得到 %d", got)
	}
	// 没装译文表时 i18n.T 走恒等路径，所以这里读到的就是英文原文。
	names := map[Bucket]string{
		BucketAvailability: "Availability",
		BucketRateLimit:    "Rate limit",
		BucketConfig:       "Config",
		BucketShape:        "Request shape",
	}
	for b, name := range names {
		if got := b.ruleName(); got != name {
			t.Errorf("Bucket(%d).ruleName() = %q, want %q", b, got, name)
		}
	}
}

// TestDefaultPolicyIsComplete：策略表必须覆盖每一个会用到的账本，
// 否则 rule() 会返回零值 Rule（阈值为 0 = 永不摘牌）——一个笔误就能让
// 「500 不熔断」这种事故静默上线。
func TestDefaultPolicyIsComplete(t *testing.T) {
	p := DefaultPolicy()
	for _, b := range []Bucket{BucketAvailability, BucketRateLimit, BucketConfig, BucketShape} {
		if _, ok := p.Rules[b]; !ok {
			t.Errorf("默认策略缺了 %s 这本账", b.ruleName())
		}
	}
	for b, r := range p.Rules {
		if b == BucketShape {
			if r.Threshold != 0 {
				t.Errorf("请求形状账本必须永不摘牌，阈值 = %d", r.Threshold)
			}
			continue
		}
		if r.Threshold < 1 {
			t.Errorf("%s 账本的阈值 = %d，会变成永不摘牌", b.ruleName(), r.Threshold)
		}
		if r.Cooldown <= 0 {
			t.Errorf("%s 账本的冷却 = %v", b.ruleName(), r.Cooldown)
		}
		if r.Backoff < 1 {
			t.Errorf("%s 账本的退避 = %v（<1 会让冷却越试越短）", b.ruleName(), r.Backoff)
		}
		if r.MaxCooldown > 0 && r.MaxCooldown < r.Cooldown {
			t.Errorf("%s 账本的封顶 %v 小于首次冷却 %v", b.ruleName(), r.MaxCooldown, r.Cooldown)
		}
	}
	if p.TrialTTL <= 0 {
		t.Errorf("试探 TTL = %v：没有它，一个没回报的试探会把 binding 永久卡在半开", p.TrialTTL)
	}
}

// TestPolicyNormalizationFillsDefaults：SetPolicy 只写想改的几项，其余按默认
// 补齐——不然调用方漏写一项就等于把那一类失败设成「永不摘牌」。
func TestPolicyNormalizationFillsDefaults(t *testing.T) {
	got := Policy{Rules: map[Bucket]Rule{
		BucketRateLimit: {Threshold: 7, Cooldown: time.Second},
	}}.normalized()

	if got.Rules[BucketRateLimit].Threshold != 7 || got.Rules[BucketRateLimit].Cooldown != time.Second {
		t.Fatalf("显式给出的项被覆盖了: %+v", got.Rules[BucketRateLimit])
	}
	def := DefaultPolicy()
	if got.Rules[BucketAvailability] != def.Rules[BucketAvailability] {
		t.Fatalf("未给出的账本没有补默认值: %+v", got.Rules[BucketAvailability])
	}
	if got.TrialTTL != def.TrialTTL {
		t.Fatalf("未给出的 TrialTTL 没有补默认值: %v", got.TrialTTL)
	}
	// 退避 1 是「不退避」的**显式**写法，不能被当成零值丢掉。
	p := Policy{Rules: map[Bucket]Rule{
		BucketAvailability: {Threshold: 1, Cooldown: time.Second, Backoff: 1},
	}}.normalized()
	if p.Rules[BucketAvailability].Backoff != 1 {
		t.Fatalf("显式的 Backoff=1 被覆盖了: %+v", p.Rules[BucketAvailability])
	}
}

func TestSplitBindingKey(t *testing.T) {
	provider, model := splitBindingKey(bindingKey("relay", "deepseek-chat"))
	if provider != "relay" || model != "deepseek-chat" {
		t.Fatalf("splitBindingKey 往返失败: %q / %q", provider, model)
	}
	// 模型名里带 \x00 是不可能的事，但拼接键的解析不能因此吃进错误的分割。
	if p, m := splitBindingKey("nokey"); p != "nokey" || m != "" {
		t.Fatalf("没有分隔符时不该猜: %q / %q", p, m)
	}
}

func BenchmarkClassify(b *testing.B) {
	in := Input{Kind: KindUpstreamStatus, Status: 500, Body: []byte(shapeBody)}
	for i := 0; i < b.N; i++ {
		if v := Classify(in); !v.Advance {
			b.Fatalf("Classify = %+v", v)
		}
	}
}
