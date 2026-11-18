package forward

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/gateway/metrics"
)

// 这一组测试锁的是 forward.go 里那段短路（2026-09-28）：**两条互不相干的路由
// 到这里合成同一个结果**——只发链头、不换人。
//
//   - `--profile=xx`：URL 路径里那一段 `/p/<name>`（launch 只在点名时才写它），
//     此前它只把 profile 钉成链头，链尾照样按 profile 优先级接上别人。用户点名
//     的那个挂了就**静默换到另一个 profile**——配置显示的、界面显示的、实际跑
//     的是三个不同的东西。
//   - `newgate fallback off`：全局关掉所有 fallback 链。
//
// 判据选「第二站被没被敲过」（命中计数）而不是「返回了什么」：返回码在这两种
// 情况下都可能一样，而「有没有换人」只有上游自己知道。
//
// **这条测试证明的是这次截断，不是「上游挂了也不换人」那件现场事**：截断发生在
// `s.Filters.Admit` 之前（那段代码在 resolve.ResolveRequest 里面），所以一个
// 被策略判成不可用的候选在这里根本轮不到。真现场（某个 provider 真挂了、熔断器
// 真的摘了它的牌）由 mock/e2e_claude.sh 那边锁——那里上游是真的坏的，断言不可能
// 因为「恰好没人可换」而对上。

const fallbackShapeBody = `{"type":"error","message":"The ` + "`reasoning_content`" +
	` in the thinking mode must be passed back to the API."}`

// fallbackRig 是这组测试的现场：两站假上游 + 一台前端。
type fallbackRig struct {
	srv   *Server
	logs  *syncBuffer
	front *httptest.Server
	hits1 *int64
	hits2 *int64
}

// newFallbackRig 起一条两站链：第一站用「思考模式要求逐字回传」那句形状错误回
// 400，第二站正常回 200。
//
// 两站都必须是**真起的 httptest**，不能拿假函数糊过去：这条测试唯一有分量的
// 判据就是「第二站有没有被敲门」，而那个数只有真服务器数得出来。
//
// 注册 shapeTestFilter{advance: true} 是为了让链上第一发的判决与现场一致（形状
// 400 能不能换人由策略说，不由数据面猜）；它对第二站的 200 是惰性的。注意它
// **左右不了**被截断那两种情况：`isLast` 在循环体顶部就按 len(steps) 算好了，
// 只剩一步时恒为真，`advance` 于是与判决无关。
func newFallbackRig(t *testing.T) *fallbackRig {
	t.Helper()

	var h1, h2 int64
	up1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&h1, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, fallbackShapeBody)
	}))
	t.Cleanup(up1.Close)

	up2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&h2, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(up2.Close)

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{
			{Profile: "ds",
				Binding:  domain.Binding{Provider: "ds-shape", Model: "deepseek-flash"},
				Provider: testProvider(up1.URL)},
			{Profile: "other",
				Binding:  domain.Binding{Provider: "other-ok", Model: "real-model-1"},
				Provider: testProvider(up2.URL)},
		}
	}
	// testChain 是个**包级变量**，而 store.Load() 每个请求都重新读盘。漏掉这一步
	// 重置，后面任何一个用例都会拿到这条两站假链——它那两个 provider 在别人那份
	// 沙箱里并不存在，症状是「另一个测试莫名其妙地换人了」。
	// （TestClientCancelAbortsUpstream 就是这么漏的；这里顺手补上它缺的那一句。）
	//
	// 这里必须是 t.Cleanup，**不能是 defer**：defer 在本 helper 返回时就跑了，
	// 也就是在 rig.post 之前——那样 testChain 恒为 nil，每个用例都退到真实的
	// resolve.ResolveRequest 上，而那份沙箱里没有 profiles.json，症状是
	// 「tier heavy has no usable candidate under profile …」的 404。
	t.Cleanup(func() { testChain = nil; metrics.Default.Reset() })

	srv, logs := newLoggingTestServer()
	if _, err := srv.Filters.Register(shapeTestFilter{advance: true}); err != nil {
		t.Fatalf("registering the policy: %v", err)
	}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	t.Cleanup(front.Close)
	return &fallbackRig{srv: srv, logs: logs, front: front, hits1: &h1, hits2: &h2}
}

// post 发一发普通 OpenAI 方言请求，返回响应与原文。
func (r *fallbackRig) post(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(r.front.URL+path, "application/json",
		strings.NewReader(`{"model":"newgate/heavy","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(body)
}

// TestFallbackOnByDefaultStillSwaps：不装开关时**照旧换人**。
//
// 这条是另外两条的对照：没有它，「第二站一次没被敲」也可能只是这条假链根本没接
// 上（比如 testChain 没生效、模型名没落到档位上），而那两种情况下的绿是空转的。
// 所以默认这一发必须证明链是**活的**。
func TestFallbackOnByDefaultStillSwaps(t *testing.T) {
	sandboxState(t, `{"port": 0, "chain": {"fallback_on_400": true}}`)
	metrics.Default.Reset()
	rig := newFallbackRig(t)

	resp, body := rig.post(t, "/v1/chat/completions")
	if resp.StatusCode != 200 || body != `{"ok":true}` {
		t.Fatalf("by default it should fail over to the second stop and return its 200 as is; got %d %q\n%s", resp.StatusCode, body, rig.logs.String())
	}
	if n := atomic.LoadInt64(rig.hits1); n != 1 {
		t.Errorf("the first stop was hit %d times, want 1", n)
	}
	// 上游挂了重试会把同一站再敲一次，所以这里只断言「被敲过」。
	if n := atomic.LoadInt64(rig.hits2); n == 0 {
		t.Error("the second stop was never hit - the chain is not wired, so the rest of this test passes vacuously")
	}
	if resp.Header.Get("X-Newgate-Failover") == "" {
		t.Error("it failed over but there is no X-Newgate-Failover line - the scene cannot be seen from the response")
	}
	logs := rig.logs.String()
	if !strings.Contains(logs, "advancing to next chain step") {
		t.Errorf("it failed over but left no trace:\n%s", logs)
	}
	// 留痕的落点：这一发在日志里该自报「换了人」（成功路径按 i>0 打这句）。
	if !strings.Contains(logs, "(failover)") {
		t.Errorf("the success log line is not marked failover:\n%s", logs)
	}
	if got := metrics.Default.Snapshot()["chain.failover"]; got != 1 {
		t.Errorf("chain.failover = %d, want 1", got)
	}
	if got := metrics.Default.Snapshot()["chain.step_failed"]; got != 1 {
		t.Errorf("chain.step_failed = %d, want 1", got)
	}
}

// TestPinnedProfileStopsAtTheHead：`--profile=xx` 只走那一个 profile。
//
// 判据是 URL 路径里那一段 `/p/<name>`——launch 只在用户点了名时才把它写进 base
// URL，parseTarget 读出来就是 Target.Profile。所以这条测试走的是**和 `newgate
// claude --profile=x` 完全同一条路径**，不是另造一个开关。
func TestPinnedProfileStopsAtTheHead(t *testing.T) {
	sandboxState(t, `{"port": 0, "chain": {"fallback_on_400": true}}`)
	metrics.Default.Reset()
	rig := newFallbackRig(t)

	// /a/claude/ 那一段只是让这次调用带上 agent 身份（现场就是 `newgate claude`
	// 起的进程），钉住 profile 的是 /p/ 那一段。
	resp, body := rig.post(t, "/a/claude/p/pinned-test/v1/chat/completions")

	if n := atomic.LoadInt64(rig.hits2); n != 0 {
		t.Fatalf("a profile was named yet it failed over: the second stop was hit %d times", n)
	}
	if n := atomic.LoadInt64(rig.hits1); n != 1 {
		t.Errorf("the first stop was hit %d times, want 1", n)
	}
	// 这一发必须**撞在上游上**：用户要的就是上游的原文，不是被谁接住之后的 200。
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400 (the named one is down, so report it as is)", resp.StatusCode)
	}
	if body != fallbackShapeBody {
		t.Errorf("the upstream text was changed\n want: %q\n got:  %q", fallbackShapeBody, body)
	}
	if got := resp.Header.Get("X-Newgate-Failover"); got != "" {
		t.Errorf("no failover happened yet X-Newgate-Failover was written: %q", got)
	}
	logs := rig.logs.String()
	// 被砍掉的那几步连同原因都要在日志里——不静默。格式是 formatSteps 的
	// `profile:provider/model`，所以这里能精确到那一步是谁。
	if !strings.Contains(logs, "profile pinned-test is pinned, dropping 1 fallback step: other:other-ok/real-model-1") {
		t.Errorf("nothing recorded 'who was dropped and why':\n%s", logs)
	}
	if strings.Contains(logs, "advancing to next chain step") {
		t.Errorf("it moved on even though the profile was pinned:\n%s", logs)
	}
	if got := metrics.Default.Snapshot()["chain.failover"]; got != 0 {
		t.Errorf("chain.failover = %d, want 0", got)
	}
}

// TestFallbackOffStopsAtTheHead：`newgate fallback off` 关的是**其余所有路径**。
//
// 与上一条同一个截断点、同一个结果，区别只在于判据来自全局开关而不是这次调用的
// 路径——所以这里走的是最普通的 URL（不带 /p/ 段）。
func TestFallbackOffStopsAtTheHead(t *testing.T) {
	sandboxState(t, `{"port": 0, "gateway": {"fallback": false}, "chain": {"fallback_on_400": true}}`)
	metrics.Default.Reset()
	rig := newFallbackRig(t)

	resp, body := rig.post(t, "/v1/chat/completions")

	if n := atomic.LoadInt64(rig.hits2); n != 0 {
		t.Fatalf("fallback is off yet it failed over: the second stop was hit %d times", n)
	}
	if n := atomic.LoadInt64(rig.hits1); n != 1 {
		t.Errorf("the first stop was hit %d times, want 1", n)
	}
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400 (turning fallback off means the request hits the upstream directly)", resp.StatusCode)
	}
	if body != fallbackShapeBody {
		t.Errorf("the upstream text was changed\n want: %q\n got:  %q", fallbackShapeBody, body)
	}
	if got := resp.Header.Get("X-Newgate-Failover"); got != "" {
		t.Errorf("no failover happened yet X-Newgate-Failover was written: %q", got)
	}
	logs := rig.logs.String()
	if !strings.Contains(logs, "fallback is off, dropping 1 chain step: other:other-ok/real-model-1") {
		t.Errorf("nothing recorded 'who was dropped and why':\n%s", logs)
	}
	if got := metrics.Default.Snapshot()["chain.failover"]; got != 0 {
		t.Errorf("chain.failover = %d, want 0", got)
	}
}

// TestFallbackOffBeatsTheProfilePin... 不需要：两者要的结果完全一样（只走链头），
// 先后只在日志措辞上有差别，而那种先后顺序是 switch 的两个 case，读一眼就知道。
// 这里改成锁另一件真正会分家的事——**超时预算**。

// TestFallbackOffIgnoresTheTimeoutBudget：「无视所有 timeout」不是另写的一条分支，
// 而是截断**推出来的**：budget 那道闸带 `i > 0`，链只剩一步时 i 恒为 0。
//
// 预算用 1ms：`start` 是这一发请求刚进 handler 时取的，链上任何一次上游往返都不
// 可能在一毫秒内回来，所以只要那道闸够得着（链上还有第二站），`i > 0` 那一发必然
// 判超时、必然打 `budget-exhausted` 进 X-Newgate-Chain。这就是「1ms」买到的东西：
// 截断若被谁拆了，这条测试当场看得见；写成 `total_budget_ms: 0` 则**什么都测不到**
// ——`ChainLimits.Budget()` 把 `<= 0` 当没配，退回默认 120000ms（Domain 的语义，
// 不是这里的），那样这条测试对预算是否生效就完全瞎了。
//
// 断言停在「上游原文 + 只敲了第一站 + 这个计数器是 0」：预算若真的生效，第二站
// 会被敲到（第 1022 行只改 isLast 与 trail，**不跳出**这一发），响应变成第二站的
// 200——那是另一条测试的现场，这条只关心第一站有没有被如实报出来。
func TestFallbackOffIgnoresTheTimeoutBudget(t *testing.T) {
	sandboxState(t, `{"port": 0, "gateway": {"fallback": false}, "chain": {"fallback_on_400": true, "total_budget_ms": 1}}`)
	metrics.Default.Reset()
	rig := newFallbackRig(t)

	resp, body := rig.post(t, "/v1/chat/completions")
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400 (the budget should only affect whether to wait for the next stop)", resp.StatusCode)
	}
	if body != fallbackShapeBody {
		t.Errorf("the upstream text was changed\n got: %q", body)
	}
	if n := atomic.LoadInt64(rig.hits1); n != 1 {
		t.Errorf("the first stop was hit %d times, want 1 (the budget must not block the first request)", n)
	}
	if n := atomic.LoadInt64(rig.hits2); n != 0 {
		t.Errorf("the second stop was hit %d times - with one step left in the chain the budget gate never applies", n)
	}
	if got := metrics.Default.Snapshot()["chain.budget_exhausted"]; got != 0 {
		t.Errorf("chain.budget_exhausted = %d, want 0 (with one step left this check never applies)", got)
	}
	if trail := resp.Header.Get("X-Newgate-Chain"); strings.Contains(trail, "budget-exhausted") {
		t.Errorf("budget-exhausted appears in X-Newgate-Chain: %q", trail)
	}
}

// 最后一条说明，留给下一个改这段的人：这里的 400 原文会被原样转给客户端，那是
// 4xx 定案分支的行为（`w.Write(eb)`）。**不要断言 X-Newgate-Evidence**：它的值是
// filepath.Base(base)，而 saveErrEvidence 在写不出文件时给的是空 base，这个头
// 就退化成 "."——那是一条看着像有、其实没有内容的断言。
