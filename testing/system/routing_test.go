package system_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/testing/system"
)

// anthropicBody 造一发 anthropic 方言的最小请求。model 填档位名（heavy/normal/
// …）——这正是客户端被接管后实际发出来的东西：注入的是档位名，怎么解析由代理
// 按请求时的当前配置决定。
func anthropicBody(model string) string {
	return `{"model":"` + model + `","max_tokens":16,` +
		`"messages":[{"role":"user","content":"hi"}]}`
}

// lastUpstreamModel 取假上游收到的最新一发请求里的 model 字段。
func lastUpstreamModel(t *testing.T, h *system.Harness) string {
	t.Helper()
	records := h.Upstream.Requests()
	if len(records) == 0 {
		t.Fatal("假上游一发请求都没收到")
	}
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(records[len(records)-1].Body, &payload); err != nil {
		t.Fatalf("解析上游收到的请求体: %v（原文 %s）", err, records[len(records)-1].Body)
	}
	return payload.Model
}

// TestRoutesTierToProfileChainHead 是这一层最核心的一条：档位名进来，真实模型名
// 出去，而且响应头把「这次走了谁」告诉调用方。
//
// 期望值来自 app 里铺的默认配置（seed.go）：默认 profile 是 cheap，
// cheap.normal 没定义，由 CandidatesFor 用 mid 顶上 → upstream-a/model-medium。
// 这条链把三件事串起来了：档位回退、profile 解析、绑定选provider。
func TestRoutesTierToProfileChainHead(t *testing.T) {
	h := system.Start(t)

	resp := h.Post("/a/claude/v1/messages", anthropicBody("normal"))
	body := system.ReadBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态 = %d, want 200；body=%s", resp.StatusCode, body)
	}
	if got := lastUpstreamModel(t, h); got != "model-medium" {
		t.Errorf("上游收到的 model = %q, want model-medium（cheap.normal 回退到 mid）", got)
	}
	route := resp.Header.Get("X-Newgate-Route")
	if !strings.Contains(route, "upstream-a/model-medium") {
		t.Errorf("X-Newgate-Route = %q, 期望含 upstream-a/model-medium", route)
	}
	if got := resp.Header.Get("X-Newgate-Profile"); got != "cheap" {
		t.Errorf("X-Newgate-Profile = %q, want cheap（默认 profile）", got)
	}
}

// TestPathProfileOverrideIsScopedToThisCall 锁住「单次 profile 覆盖不改全局状态」。
//
// 注入时把 profile 编码进 base URL 路径（/p/<profile>），代理从路径读出来做
// 本次调用的链头，完全不碰 state。所以先打一发覆盖的、再打一发不覆盖的，第二
// 发必须回到默认——如果实现是「改了全局再转发」，第二发就会跟着变，而用户根本
// 没下过这个命令。（这条路径目前**没有写进 docs**，行为只在这里和
// TestUnknownProfileDoesNotLieAboutWhatServedIt 里有描述。）
func TestPathProfileOverrideIsScopedToThisCall(t *testing.T) {
	h := system.Start(t)

	overridden := h.Post("/a/claude/p/expensive/v1/messages", anthropicBody("normal"))
	system.ReadBody(t, overridden)

	if overridden.StatusCode != http.StatusOK {
		t.Fatalf("覆盖 profile 的发请求状态 = %d, want 200", overridden.StatusCode)
	}
	if got := overridden.Header.Get("X-Newgate-Profile"); got != "expensive" {
		t.Errorf("覆盖发 X-Newgate-Profile = %q, want expensive", got)
	}
	// expensive.mid = upstream-b/model-large。跟默认的 upstream-a/model-medium
	// 不同，所以能区分出覆盖真的生效了。
	if got := lastUpstreamModel(t, h); got != "model-large" {
		t.Errorf("覆盖发上游 model = %q, want model-large（expensive.mid）", got)
	}

	h.Upstream.Reset()
	plain := h.Post("/a/claude/v1/messages", anthropicBody("normal"))
	system.ReadBody(t, plain)

	if got := plain.Header.Get("X-Newgate-Profile"); got != "cheap" {
		t.Errorf("紧接着的普通发 X-Newgate-Profile = %q, want cheap（覆盖不该泄漏成全局）", got)
	}
	if got := lastUpstreamModel(t, h); got != "model-medium" {
		t.Errorf("紧接着的普通发上游 model = %q, want model-medium", got)
	}
}

// TestUnknownProfileDoesNotLieAboutWhatServedIt 锁住「响应头绝不回显一个
// 不存在的 profile」。
//
// 为什么不是断言 404：路径覆盖 `/p/<profile>` 目前**没有**校验 profile 存不
// 存在——未知名字会被当成链头传下去，而 orderProfiles 找不到它，链就按
// priority 顺序解析（top-only → cheap → …），最后一发 200，`X-Newgate-Profile`
// 如实报出真正服务的那个。这一层能锁的是其中无可争议的一半：**它必须如实
// 报告**，不能把客户端点名的 "no-such-profile" 原样回显——那才是真正会误导人
// 的静默（用户以为钉住了某个模型，实际走的是别的，而计费和输出都对不上）。
//
// 已知不一致（2026-09-17 记，**未修**）：CLI 侧 `newgate claude --profile=nope`
// 是拒绝的（退出码 65，见 mock/e2e_claude.sh 第 5 节），HTTP 侧却静默降级。
// 同一个概念两条路两种态度，与「不猜」的原则不符。修它要给 handleProxy 加一段
// 校验并改线上行为，不在本次重构范围内；这里先把现状钉住，免得它悄悄变成别
// 的样子。
func TestUnknownProfileDoesNotLieAboutWhatServedIt(t *testing.T) {
	h := system.Start(t)

	resp := h.Post("/a/claude/p/no-such-profile/v1/messages", anthropicBody("normal"))
	body := system.ReadBody(t, resp)

	if got := resp.Header.Get("X-Newgate-Profile"); got == "no-such-profile" {
		t.Fatalf("X-Newgate-Profile 回显了不存在的 profile %q；body=%s", got, body)
	}
	if resp.StatusCode == http.StatusOK {
		if got := resp.Header.Get("X-Newgate-Profile"); got == "" {
			t.Errorf("200 但没报 X-Newgate-Profile，调用方无从知道实际用了谁；headers=%v",
				resp.Header)
		}
	}
}

// TestFailoverMovesToNextCandidate 验 fallback 链真的会走。
//
// 假上游武装下一发返回 429（真实世界里上游限流就是这样），代理应该换到链上
// 下一个候选而不是把 429 直接甩给客户端——客户端看到 429 会自己重试，那等于
// 把上游的限流放大成两倍请求量。
//
// 判据分工（这是实测出来的，不是设计出来的）：
//   - X-Newgate-Chain 说「试过谁、结果如何」，形如
//     `upstream-a/model-large(429) -> upstream-b/model-large(ok)`——**认候选**看它。
//   - X-Newgate-Failover 只说「发生过转移」，形如
//     `<链头 profile> -> <最终 profile> (upstream 429; see: newgate logs)`。
//     同一个 profile 内部换候选时两头是同一个名字（这里就是 `cheap -> cheap`），
//     所以它证明不了换过谁——只证明「换过」。
//
// 只有状态码 200 是不够的：万一第一次就没失败呢。
func TestFailoverMovesToNextCandidate(t *testing.T) {
	h := system.Start(t)

	h.Upstream.FailNext(http.StatusTooManyRequests)

	resp := h.Post("/a/claude/v1/messages", anthropicBody("heavy"))
	body := system.ReadBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("链路头被 429 打掉后状态 = %d, want 200（应转移到下一个候选）；body=%s",
			resp.StatusCode, body)
	}
	if resp.Header.Get("X-Newgate-Failover") == "" {
		t.Fatalf("发生了转移但没报 X-Newgate-Failover（不静默是硬要求）；headers=%v",
			resp.Header)
	}
	chain := resp.Header.Get("X-Newgate-Chain")
	if !strings.Contains(chain, "upstream-a/model-large(429)") {
		t.Errorf("X-Newgate-Chain = %q, 期望含 upstream-a/model-large(429)（被打掉的那一步）", chain)
	}
	if !strings.Contains(chain, "upstream-b/model-large(ok)") {
		t.Errorf("X-Newgate-Chain = %q, 期望含 upstream-b/model-large(ok)（接住的那一步）", chain)
	}
}

// TestRequestsAreNotJSONRoundTripped 锁住纯字节手术这条硬规则。
//
// 请求体里有大整数 9007199254740993（2^53+1）和未知字段。走一遍 JSON 往返
// 的话前者会被浮点化变成 …92，后者会被丢掉——两者都是静默的，客户端永远
// 不知道自己的请求被动过。所以断言代理发出去的字节里这两样原样还在。
//
// 用 up 的请求记录而不是响应，是因为要验的正是「我们发出去的那一发」。
func TestRequestsAreNotJSONRoundTripped(t *testing.T) {
	h := system.Start(t)

	const bigInt = "9007199254740993"
	body := `{"model":"normal","max_tokens":16,` +
		`"messages":[{"role":"user","content":"hi"}],` +
		`"x_unknown_field":{"deep":[1,2,3]},"x_big":` + bigInt + `}`

	resp := h.Post("/a/claude/v1/messages", body)
	system.ReadBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态 = %d, want 200", resp.StatusCode)
	}
	sent := string(h.Upstream.Requests()[len(h.Upstream.Requests())-1].Body)
	if !strings.Contains(sent, bigInt) {
		t.Errorf("上游收到的请求里大整数被改坏了（JSON 往返的典型症状）：\n%s", sent)
	}
	if !strings.Contains(sent, "x_unknown_field") {
		t.Errorf("上游收到的请求里未知字段丢了（JSON 往返的典型症状）：\n%s", sent)
	}
}

// TestCountTokensIsForwardedToUpstream 验 count_tokens 拿的是上游真值而不是
// 本地粗估。
//
// 假上游对它回 {"input_tokens":42}——一个本地估算绝不会凑巧给出的数（估算走
// 字节数/4，我们的 body 远不止 168 字节）。所以 42 是「转发成功」的指纹。
func TestCountTokensIsForwardedToUpstream(t *testing.T) {
	h := system.Start(t)

	resp := h.Post("/a/claude/v1/messages/count_tokens", anthropicBody("mid"))
	body := system.ReadBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态 = %d, want 200；body=%s", resp.StatusCode, body)
	}
	var payload struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("解析响应: %v（原文 %s）", err, body)
	}
	if payload.InputTokens != 42 {
		t.Errorf("input_tokens = %d, want 42（假上游的真值；本地粗估不会凑巧是它）",
			payload.InputTokens)
	}
}

// TestStreamingPassesThrough 验流式请求真的逐块透传而不是被缓冲成一坨。
//
// 判据不是「响应里有 SSE 帧」（缓冲后也会有），而是**客户端能读到和上游一样
// 的帧序列**。这里断言首帧、尾帧和 [DONE] 都在。上游的块间延迟让缓冲实现在
// 实测上会露馅，但作为回归测试，帧序列的完整性已经能挡住「把流式当非流式处理」
// 这类改动。
func TestStreamingPassesThrough(t *testing.T) {
	h := system.Start(t)

	body := `{"model":"normal","max_tokens":16,"stream":true,` +
		`"messages":[{"role":"user","content":"hi"}]}`
	resp := h.Post("/a/claude/v1/messages", body)
	got := system.ReadBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态 = %d, want 200；body=%s", resp.StatusCode, got)
	}
	for _, want := range []string{"message_start", "content_block_delta", "message_stop"} {
		if !strings.Contains(got, want) {
			t.Errorf("流式响应里缺 %s：\n%s", want, got)
		}
	}
}
