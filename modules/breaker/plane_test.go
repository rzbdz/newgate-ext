package breaker

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	gwpolicy "github.com/rzbdz/newgate-ext/modules/gateway/policy"
)

// TestPlaneJudgeTruthTable 是**判决表**的真值表：数据面报一件事实，策略回答
// 「停不停 / 算不算它的账 / 记哪些数 / 留什么痕」。
//
// 这张表与 modules/breaker/classify_test.go 的 TestClassify 是同一份语义的两面：
// 那一张锁的是「这件事归到哪本账」（Classify 的纯函数判决），这一张锁的是
// 「这本账在数据面上表现成什么」（Attribute / Stop / Metrics / Evidence）。
// 2026-09-18 倒置之前，后一半散在 modules/gateway/forward 的三个分支里
// （一个 switch 决定 failures++，另一处 if 决定要不要留证据）——那正是「策略
// 词汇漏进内核」的现场。现在它们全在这里，逐行可读。
//
// 每行一个全新的表（Judge 会写状态：未到阈值的失败也要落盘）。
func TestPlaneJudgeTruthTable(t *testing.T) {
	shapeErr := []byte(shapeBody)

	tests := []struct {
		name string
		out  gwpolicy.Outcome
		want gwpolicy.Verdict
	}{
		// —— 成功：什么都不做（延迟样本由 Observe 走另一条路）——
		{"上游成功",
			gwpolicy.Outcome{Kind: gwpolicy.Succeeded},
			gwpolicy.Verdict{}},

		// —— 连接类失败：算它的账；链上还有下一站就继续 ——
		{"连接失败（链中间）",
			gwpolicy.Outcome{Kind: gwpolicy.ConnectionFailed},
			gwpolicy.Verdict{Attribute: true}},
		{"连接失败（链尾）",
			gwpolicy.Outcome{Kind: gwpolicy.ConnectionFailed, IsLast: true},
			gwpolicy.Verdict{Attribute: true, Stop: true}},
		{"连接失败（已开始写响应）",
			gwpolicy.Outcome{Kind: gwpolicy.ConnectionFailed, ResponseStarted: true},
			gwpolicy.Verdict{Attribute: true, Stop: true}},

		// —— 流中途断：写出去的东西收不回，一定不换站，但这是实打实的
		// 可用性问题（旧代码只记进健康表、没进 failures 计数，见 Judge 的说明）——
		{"流中途断",
			gwpolicy.Outcome{Kind: gwpolicy.StreamCut, ResponseStarted: true},
			gwpolicy.Verdict{Attribute: true, Stop: true}},

		// —— 400 + 形状命中：不记在这家头上，但留痕（证据由 Tag/Subject 描述）——
		{"形状 400（链中间）",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 400, Body: shapeErr},
			gwpolicy.Verdict{
				Metrics:  []string{"breaker.skipped.shape_error"},
				Evidence: &gwpolicy.Evidence{Tag: "shape-400", Subject: "test-shape", Archive: true},
			}},
		{"形状 400（链尾）",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 400, Body: shapeErr, IsLast: true},
			gwpolicy.Verdict{
				Stop:     true,
				Metrics:  []string{"breaker.skipped.shape_error"},
				Evidence: &gwpolicy.Evidence{Tag: "shape-400", Subject: "test-shape", Archive: true},
			}},

		// —— 400（非形状）：请求本身有问题。fallback_on_400 只表达「要不要
		// 换个上游试试」，不代表这是这家的账 ——
		{"非形状 400（fallback 关）",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 400, Body: []byte(`{"error":"schema"}`)},
			gwpolicy.Verdict{Stop: true}},
		{"非形状 400（fallback 开，链中间）",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 400,
				Body: []byte(`{"error":"schema"}`), FallbackOn400: true},
			gwpolicy.Verdict{}},
		{"非形状 400（fallback 开，链尾）",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 400,
				Body: []byte(`{"error":"schema"}`), FallbackOn400: true, IsLast: true},
			gwpolicy.Verdict{Stop: true}},

		// —— 限流 / 配置 / 可用性：三本账，都要计在它头上 ——
		{"429（链中间）",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 429},
			gwpolicy.Verdict{Attribute: true}},
		{"429（链尾）",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 429, IsLast: true},
			gwpolicy.Verdict{Attribute: true, Stop: true}},
		// 配置账本阈值 1（确定性错误，试第二次纯属浪费），所以第一发就摘牌，
		// 于是这一行同时带出 breaker.opened。
		{"401（配置账本一次即摘）",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 401},
			gwpolicy.Verdict{Attribute: true, Metrics: []string{"breaker.opened"}}},
		{"403（配置账本一次即摘）",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 403},
			gwpolicy.Verdict{Attribute: true, Metrics: []string{"breaker.opened"}}},
		{"404（配置账本一次即摘）",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 404},
			gwpolicy.Verdict{Attribute: true, Metrics: []string{"breaker.opened"}}},
		{"402（余额不足：确定性的配置问题，与 401/403/404 同账本一次即摘）",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 402},
			gwpolicy.Verdict{Attribute: true, Metrics: []string{"breaker.opened"}}},
		{"408",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 408},
			gwpolicy.Verdict{Attribute: true}},
		{"409",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 409},
			gwpolicy.Verdict{Attribute: true}},
		{"500",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 500},
			gwpolicy.Verdict{Attribute: true}},
		{"599（>=500 一律算可用性）",
			gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 599},
			gwpolicy.Verdict{Attribute: true}},

		// —— 其它 4xx：请求本身有问题，撞遍所有上游没有意义 ——
		{"405", gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 405},
			gwpolicy.Verdict{Stop: true}},
		{"418", gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 418},
			gwpolicy.Verdict{Stop: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table, _ := clocked()
			tt.out.Provider, tt.out.Model, tt.out.Binding = "p", "m", "p/m"
			got := plane{t: table}.Judge(tt.out)
			if got.Stop != tt.want.Stop || got.Attribute != tt.want.Attribute {
				t.Errorf("Judge(%+v) = {Stop:%v Attribute:%v}, want {Stop:%v Attribute:%v}",
					tt.out, got.Stop, got.Attribute, tt.want.Stop, tt.want.Attribute)
			}
			if strings.Join(got.Metrics, ",") != strings.Join(tt.want.Metrics, ",") {
				t.Errorf("Judge(%+v) 的计数器 = %v, want %v", tt.out, got.Metrics, tt.want.Metrics)
			}
			switch {
			case tt.want.Evidence == nil && got.Evidence != nil:
				t.Errorf("Judge(%+v) 留了不该留的证据: %+v", tt.out, got.Evidence)
			case tt.want.Evidence != nil && got.Evidence == nil:
				t.Errorf("Judge(%+v) 该留证据却没留", tt.out)
			case tt.want.Evidence != nil && *got.Evidence != *tt.want.Evidence:
				t.Errorf("Judge(%+v) 的证据 = %+v, want %+v", tt.out, *got.Evidence, *tt.want.Evidence)
			}
		})
	}
}

// TestPlaneJudgeCountsFailuresIntoTheLedger 锁住判决背后的**记账**：算在它账上
// 的结局真的进了健康表，不算的真的没进。
//
// 与真值表分两份：那份验「回答了什么」，这份验「回答之后状态动没动」——
// 两者漂移过一次（2026-09-17 之前链中间摘牌、链尾不摘，因为两处判据各写了一遍）。
func TestPlaneJudgeCountsFailuresIntoTheLedger(t *testing.T) {
	counting := []struct {
		name string
		out  gwpolicy.Outcome
	}{
		{"连接失败", gwpolicy.Outcome{Kind: gwpolicy.ConnectionFailed}},
		{"429", gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 429}},
		{"404", gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 404}},
		{"500", gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 500}},
	}
	for _, tt := range counting {
		t.Run("记账/"+tt.name, func(t *testing.T) {
			table, _ := clocked()
			tt.out.Provider, tt.out.Model = "p", "m"
			plane{t: table}.Judge(tt.out)
			if got := rowOf(table, "p", "m"); got.Fails == 0 {
				t.Errorf("%s 没进账本: %+v", tt.name, got)
			}
		})
	}

	notCounting := []struct {
		name string
		out  gwpolicy.Outcome
	}{
		{"形状 400", gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 400, Body: []byte(shapeBody)}},
		{"非形状 400", gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 400, Body: []byte(`{"error":"x"}`)}},
		{"405", gwpolicy.Outcome{Kind: gwpolicy.RejectedStatus, Status: 405}},
	}
	for _, tt := range notCounting {
		t.Run("不记账/"+tt.name, func(t *testing.T) {
			table, _ := clocked()
			tt.out.Provider, tt.out.Model = "p", "m"
			res := plane{t: table}.Judge(tt.out)
			if res.Attribute {
				t.Errorf("%s 被判成要记账", tt.name)
			}
			if got := rowOf(table, "p", "m"); got.Fails != 0 || got.Open {
				t.Errorf("%s 进了可用性账本: %+v", tt.name, got)
			}
		})
	}
}

// TestPlaneJudgeReportsOpenAndSpared 锁住「摘牌那一刻的两句人话与两个计数器」。
//
// 它们以前长在数据面的 `s.report()` 与 `breakerNote()` 里（一个 switch 决定
// 计数，一个函数拼日志后缀）。倒置之后这两件事一起回家：**摘牌意味着什么**
// 只有健康表知道。
func TestPlaneJudgeReportsOpenAndSpared(t *testing.T) {
	t.Run("连到阈值就摘牌", func(t *testing.T) {
		table, _ := clocked()
		p := plane{t: table}
		out := gwpolicy.Outcome{Provider: "p", Model: "m", Binding: "p/m",
			Kind: gwpolicy.ConnectionFailed}

		first := p.Judge(out)
		if strings.Join(first.Metrics, ",") != "" || first.Note != "" {
			t.Errorf("第一发不该摘牌（阈值 2）：metrics=%v note=%q", first.Metrics, first.Note)
		}
		second := p.Judge(out)
		if strings.Join(second.Metrics, ",") != "breaker.opened" {
			t.Errorf("第二发该报 breaker.opened，得到 %v", second.Metrics)
		}
		if !strings.Contains(second.Note, "breaker opened") {
			t.Errorf("摘牌那一发必须说清楚（不然日志里只有失败、没有结论）：%q", second.Note)
		}
		if !second.Attribute {
			t.Error("摘牌的那一发当然算它的账")
		}
	})

	t.Run("诊断探活拦下来就不摘", func(t *testing.T) {
		table, _ := clocked()
		// 装上「上闸前诊断」：说它仍然可用。
		table.SetVerifier(func(provider, model string) bool { return true })
		p := plane{t: table}
		out := gwpolicy.Outcome{Provider: "p", Model: "m", Binding: "p/m",
			Kind: gwpolicy.ConnectionFailed}

		p.Judge(out)
		second := p.Judge(out)
		if strings.Join(second.Metrics, ",") != "breaker.spared" {
			t.Errorf("该报 breaker.spared，得到 %v", second.Metrics)
		}
		// 「日志里有失败、newgate breaker 里却没有它」是会被查错方向的一种组合，
		// 所以这行必须自己解释清楚。
		if !strings.Contains(second.Note, "still healthy") {
			t.Errorf("被救回来的那一发必须说清楚：%q", second.Note)
		}
		if !table.Available("p", "m") {
			t.Error("诊断说可用，binding 却不在链上")
		}
	})
}

// TestPlaneAdmitIsAvailablePlusAReason 锁住 A 阶段：准入的判据与那句给人看的
// 理由。
//
// 理由文案以前写死在 modules/config/resolve/chain.go 里——准入的判据属于提供
// 它的人，resolve 只认「返回一个理由」这个形状。它现在回家了，而 `newgate tier`
// 里那句「为什么不是我想的那个」一个字都没变。
//
// 断言的是理由的**源语言原文**（i18n 的键就是那句话）：换一门语言、改一处措辞，
// 不该让这条测试跟着红。
func TestPlaneAdmitIsAvailablePlusAReason(t *testing.T) {
	table, advance := clocked()
	p := plane{t: table}

	if ok, why := p.Admit("p", "m"); !ok || why != "" {
		t.Errorf("没什么事的时候应该可用且没有理由，得到 (%v, %q)", ok, why)
	}

	// 摘牌：配置账本一次即摘。
	p.Judge(gwpolicy.Outcome{Provider: "p", Model: "m", Kind: gwpolicy.RejectedStatus, Status: 401})
	if ok, why := p.Admit("p", "m"); ok || why != "circuit open" {
		t.Errorf("摘牌后应该不可用且理由是「circuit open」，得到 (%v, %q)", ok, why)
	}

	// 冷却期满：放一次半开试探。**这就是为什么 Admitter 只允许一个**——
	// 这条路径有副作用，而 resolve 对每个候选恰好问一次。
	advance(6 * time.Minute)
	if ok, _ := p.Admit("p", "m"); !ok {
		t.Fatal("冷却期满后应该放行一次半开试探")
	}
	if ok, _ := p.Admit("p", "m"); ok {
		t.Error("半开名额只放一次：第二个并发请求不该也进链")
	}
}

// TestPlaneDocKeepsTheWireKey 锁住 C 阶段的**跨版本契约**：健康表报出去的字段
// 名仍然是顶层 `breakers`，形状仍然是 status.Status 的数组。
//
// 优雅交接期间新旧二进制会混跑：老 CLI 读的就是这个键、这些 JSON 键。策略账本
// 把贡献者的字段并进状态文档顶层（而不是塞进一个 extra 对象）正是为了它。
func TestPlaneDocKeepsTheWireKey(t *testing.T) {
	table, _ := clocked()
	p := plane{t: table}
	p.Judge(gwpolicy.Outcome{Provider: "p", Model: "m", Kind: gwpolicy.RejectedStatus, Status: 401})

	doc := p.Doc()
	raw, ok := doc["breakers"]
	if !ok {
		t.Fatalf("状态文档里没有 breakers 键：%v", doc)
	}
	var got []Status
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("breakers 不是 Status 数组: %v", err)
	}
	if len(got) != 1 || got[0].Provider != "p" || !got[0].Open {
		t.Errorf("快照不对: %+v", got)
	}
	// 老 CLI 读的字段一个不少。
	//
	// `rule` 的值是**机器标记**（与语言无关的稳定标记，见 classify.go 的
	// ruleToken）：老 CLI 原样显示它，不解析。换成译文才会真的打断交接窗口。
	if !strings.Contains(string(raw), `"open":true`) ||
		!strings.Contains(string(raw), `"rule":"config"`) {
		t.Errorf("wire 键变了（会打断优雅交接窗口里的老 CLI）: %s", raw)
	}
}

// TestPlaneObserveProbesIsAuthoritative 锁住「探活是权威证据」：它不受连续失败
// 阈值约束——结论差就当场摘，结论好就当场放（隔离期内也一样，2026-11-04）。
func TestPlaneObserveProbesIsAuthoritative(t *testing.T) {
	table, _ := clocked()
	p := plane{t: table}

	// 阈值是 2，但一次失败 probe 就够。
	acks := p.ObserveProbes([]gwpolicy.ProbeObservation{{
		Provider: "p", Model: "m", Status: 500, Error: "connect refused",
		SlowAfter: time.Second,
	}})
	if len(acks) != 1 || !acks[0].Opened {
		t.Fatalf("失败 probe 应该当场摘牌: %+v", acks)
	}
	if !strings.Contains(acks[0].Note, "at least 60s") {
		t.Errorf("摘牌那句话讲的是健康表的退避语义，只有这里知道: %q", acks[0].Note)
	}
	if ok, _ := p.Admit("p", "m"); ok {
		t.Error("probe 说不可用，binding 却还在链上")
	}

	// 好 probe → 当场放回来，**不等冷却期满**（用户刚 probe 过就该能用）。
	acks = p.ObserveProbes([]gwpolicy.ProbeObservation{{
		Provider: "p", Model: "m", Status: 200, Latency: 100 * time.Millisecond,
		SlowAfter: time.Second,
	}})
	if len(acks) != 1 || acks[0].Opened {
		t.Errorf("好 probe 不该报 opened: %+v", acks)
	}
	if ok, why := p.Admit("p", "m"); !ok {
		t.Errorf("probe 说通了，binding 却没回链: %q", why)
	}
}

// TestPlaneBindEnvWiresTheTwoCapabilities 锁住运行期能力的交付：日志出口接在
// 持久化错误上（不静默），探活接在「上闸前诊断」上。
//
// 这两条以前都是数据面**反向**注入给健康表的（SetErrorHandler / SetVerifier 由
// forward 在 Start 里调）——方向是反的，那正是「能力归谁」没澄清的铁证。
func TestPlaneBindEnvWiresTheTwoCapabilities(t *testing.T) {
	table, _ := clocked()
	var logged []string
	probed := 0
	plane{t: table}.BindEnv(envStub{
		// 把这一行**渲染出来**再收：BindEnv 现在传的是 `Logf("%s", 消息)`
		// （格式化由 i18n 做，译文里出现 `%` 不该让日志变形），只收 format
		// 会看到一个字面的 `%s`。
		logf:  func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
		probe: func(provider, model string) bool { probed++; return false },
	})

	// 落盘失败必须说出来。直接调那条出口（写盘本身在这里不方便触发，
	// 而这一条测的是「BindEnv 有没有把它接上」）。
	if table.onError == nil {
		t.Fatal("持久化错误没有出口——健康状态不能因为写盘失败而静默丢失")
	}
	table.onError(errForTest{})
	if len(logged) != 1 || !strings.Contains(logged[0], "[breaker]") {
		t.Errorf("错误没有走到数据面的日志出口（而且那句话是健康表的知识，"+
			"该由这里给）: %v", logged)
	}

	// 上闸前诊断接的是数据面的探活能力。
	if table.verify == nil {
		t.Fatal("上闸前诊断没接上")
	}
	table.verify("p", "m")
	if probed != 1 {
		t.Errorf("诊断调的不是数据面借给我们的 Probe（调了 %d 次）", probed)
	}
}

type envStub struct {
	logf  func(string, ...any)
	probe func(string, string) bool
}

func (e envStub) Logf(format string, args ...any) { e.logf(format, args...) }
func (e envStub) Probe(provider, model string) bool {
	return e.probe(provider, model)
}

// rowOf 从快照里挑出某条 binding 的那一行；没有这一行时给零值（等同于「账本上
// 什么都没发生」——正是「不记账」那几条断言要看的东西）。
func rowOf(b *table, provider, model string) Status {
	for _, s := range b.Snapshot() {
		if s.Provider == provider && s.Model == model {
			return s
		}
	}
	return Status{}
}

type errForTest struct{}

func (errForTest) Error() string { return "写盘失败（测试）" }

// TestPlaneMetricNamingLivesHere 锁住观测面的命名归属：`newgate metrics` 里
// `breaker.*` 那三行的说明由本模块自己给。
//
// 2026-09-18 之前它们硬编码在 modules/gateway/metrics/hints.go 里——把「熔断器
// 打开意味着什么」的解释权放在了不认识熔断器的包里。
func TestPlaneMetricNamingLivesHere(t *testing.T) {
	p := plane{t: newTable()}
	for _, key := range []string{"breaker.opened", "breaker.spared", "breaker.skipped.shape_error"} {
		// 断言**身份**：它语言无关，也是表格排序用的那一半。说法（"熔断"）跟着
		// 语言走，钉住它只会让翻译一改测试就红。
		if id, label := p.MetricGroup(key); id != "breaker" || label == "" {
			t.Errorf("MetricGroup(%q) = (%q, %q), want identity %q with a non-empty label",
				key, id, label, "breaker")
		}
		if h := p.MetricHint(key); h == "" {
			t.Errorf("MetricHint(%q) 是空的——这三行以前是有说明的", key)
		}
	}
	// 别人的计数器不认领：认领了就会把别人的说明顶掉。
	if id, label := p.MetricGroup("chain.failover"); id != "" || label != "" {
		t.Errorf("MetricGroup 认领了不属于它的计数器: (%q, %q)", id, label)
	}
	if h := p.MetricHint("chain.failover"); h != "" {
		t.Errorf("MetricHint 认领了不属于它的计数器: %q", h)
	}
}
