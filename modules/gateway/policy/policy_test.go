package policy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// --- 桩 -------------------------------------------------------------------

type named struct {
	name string
	why  string
}

func (n named) Name() string { return n.name }
func (n named) Why() string  { return n.why }

type admitter struct {
	named
	asked   int
	ok      bool
	reason  string
	rank    int
	onAdmit func()
}

func (a *admitter) Admit(provider, model string) (bool, string) {
	a.asked++
	if a.onAdmit != nil {
		a.onAdmit()
	}
	return a.ok, a.reason
}

func (a *admitter) Rank(string, string, int) int { return a.rank }

type adjudicator struct {
	named
	outcomes  []Outcome // Judge 收到的（要判的）
	observeds []Outcome // Observe 收到的（成功观测量）
	verdict   func(Outcome) Verdict
}

func (a *adjudicator) Judge(o Outcome) Verdict {
	a.outcomes = append(a.outcomes, o)
	if a.verdict == nil {
		return Verdict{}
	}
	return a.verdict(o)
}

func (a *adjudicator) Observe(o Outcome) { a.observeds = append(a.observeds, o) }

type controller struct {
	named
	doc  map[string]json.RawMessage
	acks []ProbeAck
	got  []ProbeObservation
}

func (c *controller) Doc() map[string]json.RawMessage { return c.doc }
func (c *controller) ObserveProbes(obs []ProbeObservation) []ProbeAck {
	c.got = append(c.got, obs...)
	return c.acks
}

type flusher struct {
	named
	n     int
	flush func() error
}

func (f *flusher) Flush() error {
	f.n++
	if f.flush == nil {
		return nil
	}
	return f.flush()
}

// namer 只认自己那个键——真实实现（breaker）就是这么写的：认领别人的计数器
// 会把别人的说明顶掉。
type namer struct {
	named
	prefix string
	group  string // 身份：ASCII 稳定标识（排序/去重用）
	label  string // 说法：给人看的锚点
	hint   string
}

func (n namer) MetricGroup(key string) (string, string) {
	if strings.HasPrefix(key, n.prefix) {
		return n.group, n.label
	}
	return "", ""
}

func (n namer) MetricHint(key string) string {
	if strings.HasPrefix(key, n.prefix) {
		return n.hint
	}
	return ""
}

type envrec struct {
	logs   []string
	probes []string
}

func (e *envrec) Logf(format string, args ...any) { e.logs = append(e.logs, format) }
func (e *envrec) Probe(provider, model string) bool {
	e.probes = append(e.probes, provider+"/"+model)
	return true
}

type binder struct {
	named
	got Env
}

func (b *binder) BindEnv(env Env) { b.got = env }

// --- 最小系统 --------------------------------------------------------------

// TestEmptyRegistryIsTheMinimalSystem 锁住**没有贡献者时**四个口的默认值。
//
// 这是整套倒置的兜底方向：gateway 必须能在「一个策略都没装」的情况下独立工作。
// 任何一处默认写反了（比如准入默认 false），网关就退化成「必须装熔断器才转发
// 得出去」，而那是比熔断器坏掉严重得多的故障。
func TestEmptyRegistryIsTheMinimalSystem(t *testing.T) {
	r := New()
	if !r.Empty() {
		t.Fatal("新账本应该是空的")
	}
	if ok, why := r.Admit("p", "m"); !ok || why != "" {
		t.Errorf("最小系统：全部候选可用, got (%v, %q)", ok, why)
	}
	if n := r.Rank("p", "m", 1024); n != NeutralRank {
		t.Errorf("最小系统：不重排（中性键）, got %d", n)
	}
	if v := r.Judge(Outcome{Kind: ConnectionFailed}); v.Stop || v.Attribute ||
		len(v.Metrics) != 0 || v.Note != "" || v.Evidence != nil {
		t.Errorf("最小系统：结局期没有意见, got %+v", v)
	}
	if doc := r.Doc(); doc != nil {
		t.Errorf("最小系统：控制面没有额外一节, got %v", doc)
	}
	if acks := r.ObserveProbes([]ProbeObservation{{Provider: "p"}}); acks != nil {
		t.Errorf("最小系统：探活结论收下但没有回应, got %v", acks)
	}
	if id, label := r.MetricGroup("anything"); id != "" || label != "" {
		t.Errorf("最小系统：不认领任何计数器, got (%q, %q)", id, label)
	}
	if h := r.MetricHint("anything"); h != "" {
		t.Errorf("最小系统：不给任何说明, got %q", h)
	}
	r.Flush(nil) // 没有 Flusher，不该 panic
	r.BindEnv(&envrec{})
	r.Observe(Outcome{Kind: Succeeded})
}

// --- 注册期的拒绝 ----------------------------------------------------------

// TestRegisterRejectsAnonymousAndDuplicate 锁住注册期的两类拒绝。
//
// 「先到先得」是这个功能最不该有的行为：那样「谁占了这个名字」在清单里看不出来，
// 而查重一旦推迟到读侧（分派/上报的时候），错误就变成了运行期的静默覆盖。
func TestRegisterRejectsAnonymousAndDuplicate(t *testing.T) {
	r := New()
	if _, err := r.Register(named{}); err == nil {
		t.Error("没有名字的贡献者应该被拒")
	}
	if _, err := r.Register(nil); err == nil {
		t.Error("nil 贡献者应该被拒")
	}
	if _, err := r.Register(named{name: "a"}); err != nil {
		t.Fatal(err)
	}
	_, err := r.Register(named{name: "a"})
	if err == nil || i18n.ID(err) != "policy: contributor {name} is already registered" {
		t.Errorf("同名重复注册应该被拒并说清原因, got %v", err)
	}
}

// TestRegisterRejectsSecondAdmitter 锁住「准入只能有一个贡献者」。
//
// 理由不是保守，是 Admit **不是查询**：健康表在冷却期满时发放「半开试探名额」，
// 一个名额只放行一次真实请求，而 resolve 对每个候选恰好问一次。多个 Admitter
// 求 AND 会让「名额发不发」取决于**另一个不相关贡献者**的返回值：前者发了名额、
// 后者否决，那条 binding 就在 TTL 内既进不了链也没人在试。
func TestRegisterRejectsSecondAdmitter(t *testing.T) {
	r := New()
	if _, err := r.Register(&admitter{named: named{name: "first"}}); err != nil {
		t.Fatal(err)
	}
	_, err := r.Register(&admitter{named: named{name: "second"}})
	if err == nil || i18n.ID(err) != "policy: only one contributor may be an admitter; {name} is already taken by {holder} (it carries side effects, see the Admitter docs)" {
		t.Fatalf("第二个准入者应该被拒, got %v", err)
	}
	// 拒绝之后账本得干净：不能被半个注册留下痕迹。
	if len(r.Filters()) != 1 {
		t.Errorf("拒绝之后贡献者数 = %d, want 1", len(r.Filters()))
	}
}

// TestRegisterRejectsDocKeyCollision 锁住状态文档的字段归属。
//
// 两个贡献者报同一个顶层字段，唯一可能的结果是其中一个被静默覆盖——而字段名是
// 跨版本 wire 契约（老 CLI 就按这些键读），静默覆盖等于让升级窗口里的表现取决于
// 注册顺序。所以撞车必须在**注册期**当场报错。
func TestRegisterRejectsDocKeyCollision(t *testing.T) {
	r := New()
	first := &controller{named: named{name: "a"}, doc: map[string]json.RawMessage{
		"breakers": json.RawMessage(`[]`)}}
	if _, err := r.Register(first); err != nil {
		t.Fatal(err)
	}
	second := &controller{named: named{name: "b"}, doc: map[string]json.RawMessage{
		"breakers": json.RawMessage(`[]`)}}
	_, err := r.Register(second)
	if err == nil || i18n.ID(err) != "policy: state document field {key} is already taken by {owner}" {
		t.Fatalf("同一个状态字段被两家占用应该被拒, got %v", err)
	}
	// 不撞车的能进来。
	third := &controller{named: named{name: "c"}, doc: map[string]json.RawMessage{
		"quotas": json.RawMessage(`{}`)}}
	if _, err := r.Register(third); err != nil {
		t.Fatalf("字段不撞车却进不来: %v", err)
	}
}

// TestReleaseIsScopedAndIdempotent 锁住注销的语义：拿到的句柄只撤自己那一次注册，
// 且重复调用无害（Stop 必须幂等）。
func TestReleaseIsScopedAndIdempotent(t *testing.T) {
	r := New()
	first := &admitter{named: named{name: "a"}, ok: true}
	release, err := r.Register(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if !r.Empty() {
		t.Error("注销之后账本应该空了")
	}
	if err := release(); err != nil {
		t.Errorf("重复注销必须无害（Stop 要幂等），得到 %v", err)
	}

	// 注销 → 再注册同名：那个旧句柄不该把新注册删掉。
	// （后半段用普通贡献者而不是准入者——准入只能有一个，那是另一条规矩。）
	if _, err := r.Register(named{name: "a"}); err != nil {
		t.Fatal(err)
	}
	stale, err := r.Register(named{name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if len(r.Filters()) != 2 {
		t.Errorf("陈旧句柄把别人的注册删掉了：现在剩 %d 个", len(r.Filters()))
	}
	_ = stale
}

// --- 各阶段的合成 ----------------------------------------------------------

// TestAdmitAsksExactlyOncePerCandidate 锁住「每个候选恰好问一次」。
//
// 这个次数不是风格问题：它是半开试探名额能成立的前提（发一次名额 = 放行一次
// 真实请求）。集成侧由 resolve 保证（它对每个候选调一次），这里锁的是账本自己
// 不会额外多问——比如为了「合成多个来源」而重复调用。
func TestAdmitAsksExactlyOncePerCandidate(t *testing.T) {
	r := New()
	a := &admitter{named: named{name: "a"}, ok: true}
	if _, err := r.Register(a); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{"p1/m1", "p2/m2", "p3/m3"} {
		parts := strings.SplitN(candidate, "/", 2)
		if ok, _ := r.Admit(parts[0], parts[1]); !ok {
			t.Fatalf("%s 应该可用", candidate)
		}
	}
	if a.asked != 3 {
		t.Errorf("三个候选问了 %d 次, want 3", a.asked)
	}
}

// TestJudgeMergesVerdictsAndToleratesPanic 锁住 B 阶段的合成规则与 fail-open。
//
// 合成写死在一处（merge）而不是留给调用方，是因为「谁的判决赢」这种规则一旦
// 各写一遍必然漂移。fail-open 的方向与内核默认一致：一个坏插件绝不能把整条链
// 的判断夺走。
func TestJudgeMergesVerdictsAndToleratesPanic(t *testing.T) {
	r := New()
	stops := &adjudicator{named: named{name: "stops"}, verdict: func(Outcome) Verdict {
		return Verdict{Stop: true, Metrics: []string{"b.metric"}, Note: "  [我停]"}
	}}
	attrs := &adjudicator{named: named{name: "attrs"}, verdict: func(Outcome) Verdict {
		return Verdict{Attribute: true, Metrics: []string{"a.metric", "b.metric"},
			Evidence: &Evidence{Tag: "t", Subject: "s"}}
	}}
	boom := &adjudicator{named: named{name: "boom"}, verdict: func(Outcome) Verdict {
		panic("策略崩了")
	}}
	for _, f := range []Filter{stops, attrs, boom} {
		if _, err := r.Register(f); err != nil {
			t.Fatal(err)
		}
	}
	v := r.Judge(Outcome{Provider: "p", Model: "m", Kind: RejectedStatus, Status: 400})
	if !v.Stop || !v.Attribute {
		t.Errorf("Stop/Attribute 是 OR：任何一位说停就停, got %+v", v)
	}
	if strings.Join(v.Metrics, ",") != "a.metric,b.metric" {
		t.Errorf("Metrics 是去重并按字典序（输出要稳定），got %v", v.Metrics)
	}
	if v.Note != "  [我停]" {
		t.Errorf("Note 按注册顺序拼接, got %q", v.Note)
	}
	if v.Evidence == nil || v.Evidence.Tag != "t" {
		t.Errorf("Evidence 取第一个非 nil, got %+v", v.Evidence)
	}
	if len(attrs.outcomes) != 1 {
		t.Errorf("每一位裁决者都该被问到, got %d", len(attrs.outcomes))
	}
}

// TestObserveDeliversToEveryAdjudicator 锁住成功观测的分发（延迟样本按它选桶）。
func TestObserveDeliversToEveryAdjudicator(t *testing.T) {
	r := New()
	a, b := &adjudicator{named: named{name: "a"}}, &adjudicator{named: named{name: "b"}}
	for _, f := range []Filter{a, b} {
		if _, err := r.Register(f); err != nil {
			t.Fatal(err)
		}
	}
	r.Observe(Outcome{Kind: Succeeded, RequestBytes: 1234, TTFT: 20 * time.Millisecond})
	if len(a.observeds) != 1 || a.observeds[0].RequestBytes != 1234 || len(b.observeds) != 1 {
		t.Errorf("成功观测量没送到每一位: a=%v b=%v", a.observeds, b.observeds)
	}
	if len(a.outcomes) != 0 {
		t.Errorf("Observe 不该顺带走一遍判决（那会让策略把成功当失败记一次）: %v", a.outcomes)
	}
}

// TestDocMergesTopLevelKeys 锁住 C 阶段的合并：每个人的字段并进**顶层**，
// 而不是塞进一个 extra 对象（老 CLI 读的就是顶层那些键）。
func TestDocMergesTopLevelKeys(t *testing.T) {
	r := New()
	for _, c := range []*controller{
		{named: named{name: "a"}, doc: map[string]json.RawMessage{"breakers": json.RawMessage(`[1]`)}},
		{named: named{name: "b"}, doc: map[string]json.RawMessage{"quotas": json.RawMessage(`{}`)}},
	} {
		if _, err := r.Register(c); err != nil {
			t.Fatal(err)
		}
	}
	doc := r.Doc()
	if len(doc) != 2 {
		t.Fatalf("文档合并结果 = %v", doc)
	}
	if string(doc["breakers"]) != "[1]" {
		t.Errorf("贡献者的值被改动了: %s", doc["breakers"])
	}
}

// TestObserveProbesFansOut 锁住探活结论的分发与 ack 收集。
func TestObserveProbesFansOut(t *testing.T) {
	r := New()
	a := &controller{named: named{name: "a"}, acks: []ProbeAck{{Opened: true, Note: "开了"}}}
	b := &controller{named: named{name: "b"}}
	for _, c := range []*controller{a, b} {
		if _, err := r.Register(c); err != nil {
			t.Fatal(err)
		}
	}
	obs := []ProbeObservation{{Provider: "p", Model: "m"}}
	acks := r.ObserveProbes(obs)
	if len(a.got) != 1 || len(b.got) != 1 {
		t.Errorf("结论没送到每一位控制面贡献者: a=%d b=%d", len(a.got), len(b.got))
	}
	if len(acks) != 1 || !acks[0].Opened || acks[0].Note != "开了" {
		t.Errorf("ack 没被收回来: %+v", acks)
	}
}

// TestMetricNamingTakesTheFirstClaim 锁住观测面命名的「认领」语义：第一位说认识
// 的赢，没人认识给空串（由调用方兜底到内核默认）。
func TestMetricNamingTakesTheFirstClaim(t *testing.T) {
	r := New()
	for _, n := range []namer{
		{named: named{name: "a"}, prefix: "breaker.", group: "breaker", label: "熔断", hint: "熔断器打开"},
		{named: named{name: "b"}, prefix: "breaker.", group: "other", label: "别的", hint: "别的说明"},
	} {
		if _, err := r.Register(n); err != nil {
			t.Fatal(err)
		}
	}
	// 断言**身份**：它是语言无关的那一半，也是排序用的那一半。说法跟着语言走，
	// 在这里钉住它只会让翻译一改测试就红。
	if id, label := r.MetricGroup("breaker.opened"); id != "breaker" || label != "熔断" {
		t.Errorf("分组取第一位认领的, got (%q, %q)", id, label)
	}
	if h := r.MetricHint("breaker.opened"); h != "熔断器打开" {
		t.Errorf("说明取第一位认领的, got %q", h)
	}
	if id, label := r.MetricGroup("chain.failover"); id != "" || label != "" {
		t.Errorf("没人认领的该给空串（调用方兜底）, got (%q, %q)", id, label)
	}
	if h := r.MetricHint("chain.failover"); h != "" {
		t.Errorf("没人认领的说明该给空串（调用方兜底）, got %q", h)
	}
}

// TestFlushReportsFailuresWithoutBlocking 锁住 D 阶段：落盘失败要说出来（不静默），
// 但绝不阻断停机。
func TestFlushReportsFailuresWithoutBlocking(t *testing.T) {
	r := New()
	boom := errors.New("磁盘满")
	bad := &flusher{named: named{name: "bad"}, flush: func() error { return boom }}
	good := &flusher{named: named{name: "good"}}
	for _, f := range []Filter{bad, good} {
		if _, err := r.Register(f); err != nil {
			t.Fatal(err)
		}
	}
	var logged []string
	r.Flush(func(format string, args ...any) {
		logged = append(logged, format+"|"+strings.Join(toStrings(args), ","))
	})
	if bad.n != 1 || good.n != 1 {
		t.Errorf("每个人都要落一次盘: bad=%d good=%d", bad.n, good.n)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "bad") {
		t.Errorf("落盘失败必须说出来并点名: %v", logged)
	}
}

// TestBindEnvReachesOnlyBinders 锁住运行期能力的交付面：只给实现了 EnvBinder 的人，
// 而且能力是**数据面在 Serve 期**递过来的（见包注释里的三段法则）。
func TestBindEnvReachesOnlyBinders(t *testing.T) {
	r := New()
	b := &binder{named: named{name: "binder"}}
	plain := &adjudicator{named: named{name: "plain"}}
	for _, f := range []Filter{b, plain} {
		if _, err := r.Register(f); err != nil {
			t.Fatal(err)
		}
	}
	env := &envrec{}
	r.BindEnv(env)
	if b.got != Env(env) {
		t.Error("实现了 EnvBinder 的人没拿到运行期能力")
	}
}

func toStrings(in []any) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
