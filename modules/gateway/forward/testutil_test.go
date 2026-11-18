package forward

import (
	"bytes"
	"encoding/json"
	"log"
	"sync"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/policy"
	"github.com/rzbdz/newgate-ext/modules/gateway/thinkcache"
)

// isolate 把配置目录指到临时目录，免得测试读到（或写坏）真实的 ~/.config/newgate。
//
// 2026-09-20 从 thinkcache_test.go 搬到这里：那三个用例断的是**发行版模块**
// （DeepSeek 的逐字回填 / 跨 agent 迁移）的策略，跟着拥有者搬去发行版仓库了，
// 而这个 helper 留下来的用例（compact_test.go）还要用。
func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	t.Setenv("NEWGATE_TARGET_DIR", dir)
	t.Setenv("HOME", dir)
	if _, err := store.Init(false); err != nil {
		t.Fatalf("初始化临时配置失败: %v", err)
	}
	thinkcache.Default = thinkcache.New(32<<20, 0) // 每个测试从空缓存开始
}

// newTestServer 造一个**最小系统**的测试用 Server：策略账本上一张纸条都没有。
//
// 这个默认是刻意的。数据面自己该做的事——转发、字节保真、换站、日志、证据
// 落盘——在这个面上全能测完，而**策略语义一条都不在**：形状 400 算不算这家
// 的账、阈值是几、指标叫什么名字，全是 modules/breaker 的事，它自己测
// （见 breaker/plane_test.go）。
//
// 2026-09-18 之前这里注入的是一张真健康表（`breaker.NewTable()`），于是
// forward 的单测里躺着「什么算形状错误」「阈值 2」这类判据——转发路径并不知道
// 这些，它只是恰好拿到了唯一一位策略。倒置之后策略是**账本上的一格**，默认
// 那格是空的。
//
// 注意它**不带 Watch**，所以 handler 会退化成 `store.Load()` 直接读盘——
// 配置从哪来由包级 TestMain（modules_test.go）铺的沙箱决定，见那里的注释。
//
// 走 forward.New（而不是拼一个字面量）是必须的：它把 stopCh/drainCh 这两个
// 停机信号通道建起来，而控制停机那条测试正是要断言「令牌验过之后通道被关掉」。
// 曾经这里写成 `&Server{Port: 0, Health: ...}`，靠那会儿恰好没有这类断言才没炸。
func newTestServer() *Server {
	return New("", 0, nil, nil, policy.New())
}

// newLoggingTestServer 同 newTestServer，但把代理日志收进返回的 buffer——
// 「不静默是硬要求」那一类断言（改写了什么、判据是谁认的、证据存哪了）只能
// 在日志上验，不能只看响应和状态。
//
// 返回的是**带锁的** buffer，不是 bytes.Buffer：日志由 HTTP handler 那个
// goroutine 写，断言由测试 goroutine 读，而「响应体读完」不等于 handler 已经
// 退出——它后面还要打收尾那几行（stream ended normally 之类）。裸 bytes.Buffer
// 在这里是一场必然会撞上的数据竞争（2026-09-23 `go test -race ./...` 实测：
// `bytes.(*Buffer).String()` 撞 `log.(*Logger).output()`），而它红得还很有迷惑性
// ——报的是**某一个**用到了新日志行的测试，看着像那行日志有问题。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newLoggingTestServer() (*Server, *syncBuffer) {
	buf := &syncBuffer{}
	s := newTestServer()
	s.Logger = log.New(buf, "", 0)
	return s, buf
}

// scriptedFilter 是 forward 侧的测试策略：它**记下数据面交给它的每一个结局**
// 并按脚本回答。
//
// 为什么测试里自己写一条而不是 import modules/breaker 用它那条：import 会成环
// （breaker → gateway → forward），而且更重要的——forward 的单测要锁的是
// **数据面报的事实对不对、判决执行得对不对**，不是 breaker 的账本语义。判据
// 的真值表在 breaker 自己的测试里，接线在 testing/system。这与「判据别硬编码
// 上游字符串」是同一条分层规矩（见 forward_test.go 里 shapeTestDetector 的说明）。
type scriptedFilter struct {
	mu       sync.Mutex
	outcomes []policy.Outcome
	docs     map[string]string
	verdict  func(policy.Outcome) policy.Verdict
}

func newScriptedFilter() *scriptedFilter { return &scriptedFilter{} }

func (f *scriptedFilter) Name() string { return "scripted" }
func (f *scriptedFilter) Why() string {
	return "forward 的单测策略：记录事实 + 按脚本回答"
}

func (f *scriptedFilter) Judge(o policy.Outcome) policy.Verdict {
	f.mu.Lock()
	f.outcomes = append(f.outcomes, o)
	f.mu.Unlock()
	if f.verdict == nil {
		return policy.Verdict{}
	}
	return f.verdict(o)
}

// Observe 收下成功那一发；数据面走的是同一条 Judge 路径，这里不另记。
func (f *scriptedFilter) Observe(policy.Outcome) {}

// Doc 让状态文档里多一节，用来锁「贡献者的字段并进顶层」这件事。
func (f *scriptedFilter) Doc() map[string]json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.docs) == 0 {
		return nil
	}
	out := map[string]json.RawMessage{}
	for key, value := range f.docs {
		out[key] = json.RawMessage(value)
	}
	return out
}

// seen 返回数据面交给策略的全部结局（副本）。
func (f *scriptedFilter) seen() []policy.Outcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]policy.Outcome(nil), f.outcomes...)
}

// seenOf 只挑某个 provider 的结局。
func (f *scriptedFilter) seenOf(provider string) []policy.Outcome {
	var out []policy.Outcome
	for _, o := range f.seen() {
		if o.Provider == provider {
			out = append(out, o)
		}
	}
	return out
}

// probeFilter 只实现 C 阶段：把探活结论收下，用来锁「数据面只负责收发与翻译」。
type probeFilter struct {
	mu   sync.Mutex
	got  []policy.ProbeObservation
	acks []policy.ProbeAck
}

func (f *probeFilter) Name() string { return "probe-recorder" }
func (f *probeFilter) Why() string  { return "forward 的单测策略：记录探活观察值" }

func (f *probeFilter) Doc() map[string]json.RawMessage { return nil }

func (f *probeFilter) ObserveProbes(obs []policy.ProbeObservation) []policy.ProbeAck {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, obs...)
	return f.acks
}

func (f *probeFilter) observed() []policy.ProbeObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]policy.ProbeObservation(nil), f.got...)
}
