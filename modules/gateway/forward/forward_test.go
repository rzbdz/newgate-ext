package forward

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"context"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/gateway/metrics"
	"github.com/rzbdz/newgate-ext/modules/gateway/policy"
)

// 一段有代表性的 SSE：含 tool_call 分片、Unicode、空 data、大整数。
var sseChunks = []string{
	`data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n",
	`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"background_cancel","arguments":""}}]}}]}` + "\n\n",
	`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"id\":\"CJK\"}"}}]}}]}` + "\n\n",
	`data: {"usage":{"total_tokens":9007199254740993}}` + "\n\n",
	"data: [DONE]\n\n",
}

// fakeUpstream 逐块吐 SSE，块之间有间隔——用来验证代理没有缓冲。
func fakeUpstream(t *testing.T, gap time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := ioutil.ReadAll(r.Body)
		var m map[string]json.RawMessage
		if err := json.Unmarshal(body, &m); err != nil {
			w.WriteHeader(400)
			return
		}
		// 断言收到的是真实模型名，不是档位名
		var got string
		_ = json.Unmarshal(m["model"], &got)
		if got != "real-model-1" {
			t.Errorf("upstream received model = %q, want real-model-1", got)
		}
		if r.Header.Get("Authorization") != "Bearer sk-real" {
			t.Errorf("upstream received Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		for _, c := range sseChunks {
			_, _ = io.WriteString(w, c)
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(gap)
		}
	}))
}

// TestStreamByteFaithful 断言：响应体逐字节等于上游发出的内容。
func TestStreamByteFaithful(t *testing.T) {
	up := fakeUpstream(t, 0)
	defer up.Close()

	body, arrivals := streamThroughProxy(t, up.URL, 0)
	want := strings.Join(sseChunks, "")
	if body != want {
		t.Errorf("the response body was changed\n want: %q\n got:  %q", want, body)
	}
	if len(arrivals) == 0 {
		t.Fatal("received no chunks at all")
	}
}

// TestStreamNotBuffered 断言：块是陆续到的，不是最后一次性倒出来。
// 这是流式体验的命门——缓冲了内容测试照样过，但用户看起来像���死。
func TestStreamNotBuffered(t *testing.T) {
	gap := 60 * time.Millisecond
	up := fakeUpstream(t, gap)
	defer up.Close()

	_, arrivals := streamThroughProxy(t, up.URL, gap)
	if len(arrivals) < 2 {
		t.Fatalf("received data only %d times, cannot tell whether it was buffered", len(arrivals))
	}
	first, last := arrivals[0], arrivals[len(arrivals)-1]
	spread := last - first
	// 上游总共铺开了 len(sseChunks)*gap，客户端观测到的时间跨度
	// 应该同量级；若被缓冲，spread 会接近 0
	if spread < gap {
		t.Errorf("looks buffered: the gap between the first and last chunk is only %v (upstream gap %v, %d chunks)",
			spread, gap, len(sseChunks))
	}
	t.Logf("first chunk arrived after %v, first-to-last span %v, %d reads in total - not buffered",
		first, spread, len(arrivals))
}

// streamThroughProxy 起一个真实的代理实例，发一个流式请求，
// 返回完整响应体和每次读到数据的相对时刻。
func streamThroughProxy(t *testing.T, upstreamURL string, _ time.Duration) (string, []time.Duration) {
	t.Helper()

	srv := newTestServer()
	handler := http.HandlerFunc(srv.handleProxy)

	// 用一个只认 real-model-1 的解析器替代真实配置：
	// 直接构造 route，绕过 config 读盘
	testChain = func(role string) []resolve.Step {
		if role != "heavy" {
			return nil
		}
		return []resolve.Step{{
			Profile:  "test",
			Binding:  domain.Binding{Provider: "test-prov", Model: "real-model-1"},
			Provider: testProvider(upstreamURL),
		}}
	}
	defer func() { testChain = nil }()

	front := httptest.NewServer(handler)
	defer front.Close()

	reqBody := `{"model":"newgate/heavy","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(front.URL+"/v1/chat/completions",
		"application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := ioutil.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	if te := resp.Header.Get("Transfer-Encoding"); te != "" {
		t.Errorf("Transfer-Encoding must not be passed through to the client, got %q", te)
	}

	start := time.Now()
	var out bytes.Buffer
	var arrivals []time.Duration
	br := bufio.NewReader(resp.Body)
	buf := make([]byte, 4096)
	for {
		n, err := br.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
			arrivals = append(arrivals, time.Since(start))
		}
		if err != nil {
			break
		}
	}
	return out.String(), arrivals
}

func testProvider(baseURL string) domain.Provider {
	return domain.Provider{BaseURL: baseURL + "/v1", APIKey: "sk-real", Protocol: "openai"}
}

// TestClientCancelAbortsUpstream 断言：客户端中途断开时，上游请求也被取消。
// 没这个的话，用户在 opencode 里按 ESC，上游还会把整个响应跑完并照样计费。
func TestClientCancelAbortsUpstream(t *testing.T) {
	upstreamDone := make(chan error, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		for i := 0; i < 50; i++ {
			if _, err := io.WriteString(w, fmt.Sprintf("data: chunk-%d\n\n", i)); err != nil {
				upstreamDone <- err
				return
			}
			if fl != nil {
				fl.Flush()
			}
			select {
			case <-r.Context().Done():
				upstreamDone <- r.Context().Err() // 上游观测到取消
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
		upstreamDone <- nil // 跑完了 = 取消没传播过去
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{
			Profile:  "test",
			Binding:  domain.Binding{Provider: "test-prov", Model: "real-model-1"},
			Provider: testProvider(up.URL),
		}}
	}
	defer func() { testChain = nil }()

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	req, _ := http.NewRequest("POST", front.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"newgate/heavy","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	// 读到前几块就断开
	buf := make([]byte, 256)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatalf("could not even read the first chunk: %v", err)
	}
	_ = resp.Body.Close()

	select {
	case err := <-upstreamDone:
		if err == nil {
			t.Error("the upstream ran all 50 chunks - the cancellation did not propagate, which burns tokens for nothing")
		} else {
			t.Logf("upstream observed the cancellation: %v - propagation is correct", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("within 3 seconds the upstream neither finished nor observed the cancellation")
	}
}

// TestNonStreamResponseByteFaithful 非流式响应也必须逐字节原样返回，
// 且不能漏掉/篡改上游的头。
func TestNonStreamResponseByteFaithful(t *testing.T) {
	// 故意用奇怪的 key 顺序、大整数、Unicode、非紧凑空白
	upBody := "{\n  \"z\": 1e10,\n  \"a\": 9007199254740993,\n" +
		"  \"txt\": \"café \\\"quotes\\\" and \\\\ backslashes\",\n  \"nil\": null\n}"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Trace", "abc-123")
		w.Header().Set("Eo-Cache-Status", "MISS")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, upBody)
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding:  domain.Binding{Provider: "p", Model: "real-model-1"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"newgate/heavy"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := ioutil.ReadAll(resp.Body)

	if string(got) != upBody {
		t.Errorf("the response body was changed\n want: %q\n got:  %q", upBody, got)
	}
	// 上游的业务头必须透传
	if resp.Header.Get("X-Upstream-Trace") != "abc-123" {
		t.Error("a custom upstream header was lost")
	}
	if resp.Header.Get("Eo-Cache-Status") != "MISS" {
		t.Error("an upstream cache-status header was lost")
	}
	// 我们自己的诊断头应该在
	if resp.Header.Get("X-Newgate-Route") == "" {
		t.Error("missing X-Newgate-Route")
	}
}

// TestErrorResponseByteFaithful 上游报错时，错误原文必须一字不改地送到客户端
// ——吞掉上游错误信息是排查噩梦的主要来源。
func TestErrorResponseByteFaithful(t *testing.T) {
	errBody := `{"error":{"message":"Invalid schema for function 'background_cancel': null is not of type \"array\"","type":"invalid_request_error"}}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, errBody)
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding:  domain.Binding{Provider: "p", Model: "real-model-1"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"newgate/heavy"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := ioutil.ReadAll(resp.Body)

	if resp.StatusCode != 400 {
		t.Errorf("the status code should pass through 400, got %d", resp.StatusCode)
	}
	if string(got) != errBody {
		t.Errorf("the upstream error text was changed\n want: %q\n got:  %q", errBody, got)
	}
	if resp.Header.Get("X-Newgate-Evidence") == "" {
		t.Error("4xx should carry X-Newgate-Evidence pointing at the evidence file")
	}
}

// shapeTestFilter 是 forward 这一层的测试策略：认「点名了 reasoning_content
// 又要求逐字回传」的 400，并给它留一份现场。
//
// 为什么在 forward 的测试里自己写一条，而不是 import modules/breaker 或
// modules/deepseek：转发路径**不认识任何一位策略**（这正是 2026-09-18 倒置
// 成立的理由——判据由知道那件事的模块注册进策略账本，core 只读 Verdict.Evidence
// 里那几个不透明字符串）。在单测里 import deepseek 等于把刚拆掉的耦合从测试里
// 接回去：真判据改了文案，这里会跟着红，而它红的原因跟 forward 的行为毫无关系。
//
// 分工是「测试跟着谁知道这件事走」：真判据的真值表在 modules/deepseek/shape_test.go，
// breaker 把形状 400 翻成判决的真值表在 modules/breaker/plane_test.go，判据从
// 模块 Start 一路接到转发路径的**接线**在 testing/system（真组件图 + 假上游）；
// 这里只锁 forward 与账本的契约——Evidence 非空时它该做什么。
// advance 表达的是这条策略对「认领之后要不要换站」的回答：零值（false）=
// 「不换站」（链尾那一发，锁终局路径），true = 「继续沿链」（breaker 对形状
// 400 的真实判决就是这一条：同一份 body 换谁发都一样错，但换一家校验更松的
// 上游是唯一可能成功的路）。两条收场都必须留痕，用例各锁一边。
type shapeTestFilter struct{ advance bool }

func (shapeTestFilter) Name() string { return "shape-test" }
func (shapeTestFilter) Why() string {
	return "forward unit test: recognizes the reasoning pass-back 400"
}

func (f shapeTestFilter) Judge(o policy.Outcome) policy.Verdict {
	if o.Kind != policy.RejectedStatus || o.Status != 400 {
		return policy.Verdict{}
	}
	if !bytes.Contains(o.Body, []byte("reasoning_content")) ||
		!bytes.Contains(o.Body, []byte("must be passed back")) {
		return policy.Verdict{}
	}
	// 形状错误：不记账，但留痕。
	return policy.Verdict{
		Stop:     !f.advance,
		Evidence: &policy.Evidence{Tag: "shape-400", Subject: "test-shape", Archive: true},
	}
}

func (shapeTestFilter) Observe(policy.Outcome) {}

// TestShapeErrorIsCountedThenLoggedWithEvidence 锁住转发路径对「要留痕的结局」
// 的全部义务。策略给出 Evidence 之后，forward 要：
//
//  1. 原样透传上游原文（不静默，客户端看到的就是上游说的）；
//  2. 日志里打出**标记**（`[shape-400]`）与**是谁认领的**；
//  3. 外加一行「现场已存档」；
//  4. 现场落进 dump/<标记>-<判据>/（专用目录，不参与 req-*/err-* 的滚动清理）。
//
// 现场动机（2026-09-17，health.json + 日志）：deepseek-flash 的这条 400 被当成
// 可用性失败记进熔断器，连续两发就把一条本来能用的 binding 摘掉；而 probe 一直
// 绿——探活发的是最小请求，触发不到「思考模式要求逐字回传」。摘要写得明确：
// 「同一份 body 换哪个 provider 都一样错」的问题不该记在任何一家的账上。
//
// 记账那半边（形状 400 不进可用性账本）现在是 breaker 的判决，在
// modules/breaker/plane_test.go 与 testing/system 里验；这里只验数据面的义务。
func TestShapeErrorIsCountedThenLoggedWithEvidence(t *testing.T) {
	sandboxState(t, `{"port": 0}`)
	metrics.Default.Reset()
	t.Cleanup(func() { metrics.Default.Reset(); testChain = nil })

	reasoningErr := `{"type":"error","message":"The ` + "`reasoning_content`" +
		` in the thinking mode must be passed back to the API."}`
	hits := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, reasoningErr)
	}))
	defer up.Close()

	// 单站链（IsLast）：只有链尾那一发才走「定案」分支，也就是打专属日志、
	// 存实地证据的那条路径。多站链测不到它。
	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "ds",
			Binding:  domain.Binding{Provider: "ds-shape", Model: "deepseek-flash"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv, logBuf := newLoggingTestServer()
	if _, err := srv.Filters.Register(shapeTestFilter{}); err != nil {
		t.Fatalf("registering the policy: %v", err)
	}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	// 连发 3 次：判决恒定，所以每一发都该留下同一份现场。
	for i := 0; i < 3; i++ {
		resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
			strings.NewReader(`{"model":"newgate/heavy"}`))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("request %d status = %d, want 400 (should pass through as is, not another error)", i+1, resp.StatusCode)
		}
		if string(got) != reasoningErr {
			t.Fatalf("request %d upstream text was changed\n want: %q\n got:  %q", i+1, reasoningErr, got)
		}
	}
	if hits != 3 {
		t.Fatalf("upstream was hit %d times, want 3", hits)
	}
	// 不算在任何一条 binding 头上：判决没给 Attribute，计数就不该动。
	if n := atomic.LoadUint64(&srv.failures); n != 0 {
		t.Errorf("an outcome that leaves a trace but is not booked added failures %d times", n)
	}

	// 标记与判据名必须进日志：形状判据是多家上游各自的，只说「命中了形状错误」
	// 在有两家同时报 400 时毫无用处。转发路径不认识那些字符串，名字是它唯一的
	// 线索——所以它只能原样打出来。
	logs := logBuf.String()
	if !strings.Contains(logs, "[shape-400]") || !strings.Contains(logs, "test-shape") {
		t.Errorf("the log does not say which criterion claimed it:\n%s", logs)
	}
	// 存档那一行的机器标记是证据目录名 `shape-400-<判据>`：文案会跟着语言走，
	// 目录名不会（这才是排障时 grep 得动的东西）。
	if !strings.Contains(logs, "shape-400-") {
		t.Errorf("evidence was not written (these 400s are rare and fatal; once lost they cannot be reproduced):\n%s", logs)
	}

	// 证据落进专用目录，名字取自标记 + 判据 → 换一位策略自动多一个目录，
	// 转发路径不改。
	dir := filepath.Join(os.Getenv("NEWGATE_HOME"), "dump", "shape-400-test-shape")
	ents, err := ioutil.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the evidence directory %s: %v (this kind of scene must be archived separately)", dir, err)
	}
	if len(ents) == 0 {
		t.Fatalf("the evidence directory %s is empty", dir)
	}
	for _, want := range []string{"client-sent.json", "we-sent.json", "upstream-said.json", "audit.txt"} {
		if _, err := os.Stat(filepath.Join(dir, ents[0].Name(), want)); err != nil {
			t.Errorf("the evidence directory is missing %s: %v", want, err)
		}
	}
}

// TestShapeAdvanceKeepsClaimedEvidence 锁住**沿链转移**那一发的留痕：当裁决
// 说「继续走」（fallback_on_400=true 那条路；形状判决本来就该换一家再试），
// 而判据又认了这一发 400 时，`[shape-400]` 那行日志与专用目录归档**都必须
// 发生**——哪怕这一发最终由下一站回了 200。
//
// 现场（2026-09-17，见 docs/11-troubleshooting.md）：形状 400 悄悄降级到别家、
// 客户端拿到 200、日志里看不出来。旧实现把留痕留在终局分支里，而转移恰恰是
// 它最常见的收场——于是既不归档也不打那行，docs 承诺的「不参与滚动清理、
// 永不丢」就成了一句空话。
//
// 同时锁住「尽量少影响」那半边：转移路径**不落**通用 err-* 证据（整包 body
// dump 会让每次 fallback 多几 MB 落盘/日志），所以这里断言 dump 里没有
// err-400-*，也证明这次修复只动了日志与归档、没动裁决。
func TestShapeAdvanceKeepsClaimedEvidence(t *testing.T) {
	sandboxState(t, `{"port": 0, "chain": {"fallback_on_400": true}}`)
	t.Cleanup(func() { testChain = nil })

	reasoningErr := `{"type":"error","message":"The ` + "`reasoning_content`" +
		` in the thinking mode must be passed back to the API."}`
	var shapeHits, okHits int
	shape := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		shapeHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, reasoningErr)
	}))
	defer shape.Close()
	okUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer okUp.Close()

	// 两站链：第一站挑食（形状 400）→ 裁决为继续走 → 第二站收下。
	testChain = func(role string) []resolve.Step {
		return []resolve.Step{
			{Profile: "ds", Binding: domain.Binding{Provider: "ds-shape", Model: "deepseek-flash"},
				Provider: testProvider(shape.URL)},
			{Profile: "other", Binding: domain.Binding{Provider: "other-ok", Model: "real-model-1"},
				Provider: testProvider(okUp.URL)},
		}
	}
	defer func() { testChain = nil }()

	srv, logBuf := newLoggingTestServer()
	if _, err := srv.Filters.Register(shapeTestFilter{advance: true}); err != nil {
		t.Fatalf("registering the policy: %v", err)
	}
	// 旁观者只记录事实，用来证明这一发确实走在 fallback_on_400 打开的链上。
	watcher := newScriptedFilter()
	if _, err := srv.Filters.Register(watcher); err != nil {
		t.Fatalf("registering the observer policy: %v", err)
	}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"newgate/heavy","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(got) != `{"ok":true}` {
		t.Fatalf("should fail over along the chain to the second stop and get 200, got %d %q", resp.StatusCode, got)
	}
	if shapeHits != 1 || okHits != 1 {
		t.Fatalf("upstream hits = shape %d / ok %d, want 1/1 (a real failover must have happened)", shapeHits, okHits)
	}
	var sawFallback400 bool
	for _, o := range watcher.seen() {
		if o.Provider == "ds-shape" && o.Kind == policy.RejectedStatus && o.FallbackOn400 {
			sawFallback400 = true
		}
	}
	if !sawFallback400 {
		t.Fatal("this request did not go over a chain with fallback_on_400=true - the fixture had no effect, so the case cannot lock the failover path")
	}

	// 标记与判据名必须进日志：形状判据是多家上游各自的，转发路径不认识那些
	// 字符串，名字是它唯一的线索，所以只能原样打出来。
	logs := logBuf.String()
	if !strings.Contains(logs, "[shape-400]") || !strings.Contains(logs, "test-shape") {
		t.Errorf("the failover path did not say which criterion claimed it (that is the shape of a 'silent downgrade'):\n%s", logs)
	}
	// 存档那一行的机器标记是证据目录名 `shape-400-<判据>`：文案跟语言走，
	// 目录名不跟——这才是排障时 grep 得动的东西。
	if !strings.Contains(logs, "shape-400-test-shape") {
		t.Errorf("the failover path did not archive the scene (these 400s are rare and fatal; once lost they cannot be reproduced):\n%s", logs)
	}
	dir := filepath.Join(os.Getenv("NEWGATE_HOME"), "dump", "shape-400-test-shape")
	ents, err := ioutil.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the evidence directory %s: %v (the failover path must archive too)", dir, err)
	}
	if len(ents) != 1 {
		t.Fatalf("one failover should leave exactly one scene, got %d", len(ents))
	}
	for _, want := range []string{"client-sent.json", "we-sent.json", "upstream-said.json", "audit.txt"} {
		if _, err := os.Stat(filepath.Join(dir, ents[0].Name(), want)); err != nil {
			t.Errorf("the evidence directory is missing %s: %v", want, err)
		}
	}
	// 「尽量少影响」那半边：转移路径不落通用 err-* 整包 body dump。
	dumpDir := filepath.Join(os.Getenv("NEWGATE_HOME"), "dump")
	if all, err := ioutil.ReadDir(dumpDir); err == nil {
		for _, e := range all {
			if strings.HasPrefix(e.Name(), "err-400-") {
				t.Errorf("the failover path dropped a generic err-* evidence (each fallback adds a few MB, violating minimal impact): %s", e.Name())
			}
		}
	}
}

// TestShapeAdvanceWithoutClaimLeavesNoArchive 是反向用例：链上第一站回了一个
// **没命中**形状判据的 400，裁决照样是「继续走」（fallback_on_400=true），
// 但没人认领这一发——于是什么都不归档，`[shape-400]` 那行也不该出现。
//
// 它守着留痕的边界：证据是「判据认领」的结果，不是「看见 400」的结果。判据一
// 宽，专用目录就会被普通校验错误塞满，docs/11-troubleshooting.md 承诺的那份
// 「永不丢」的证据反而被淹没。
func TestShapeAdvanceWithoutClaimLeavesNoArchive(t *testing.T) {
	sandboxState(t, `{"port": 0, "chain": {"fallback_on_400": true}}`)
	t.Cleanup(func() { testChain = nil })

	schemaErr := `{"error":{"message":"Invalid schema: missing required field 'name'"}}`
	var okHits int
	badSchema := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, schemaErr)
	}))
	defer badSchema.Close()
	okUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer okUp.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{
			{Profile: "bad", Binding: domain.Binding{Provider: "schema-bad", Model: "real-model-1"},
				Provider: testProvider(badSchema.URL)},
			{Profile: "other", Binding: domain.Binding{Provider: "other-ok", Model: "real-model-1"},
				Provider: testProvider(okUp.URL)},
		}
	}
	defer func() { testChain = nil }()

	srv, logBuf := newLoggingTestServer()
	// 判据在册，只是这一份 body 不命中它——正是要锁的边界。
	if _, err := srv.Filters.Register(shapeTestFilter{advance: true}); err != nil {
		t.Fatalf("registering the policy: %v", err)
	}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"newgate/heavy","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(got) != `{"ok":true}` {
		t.Fatalf("a non-shape 400 should continue along the chain and get 200, got %d %q", resp.StatusCode, got)
	}
	if okHits != 1 {
		t.Fatalf("the second stop was hit %d times, want 1 (if the case never reaches the failover path it passes vacuously)", okHits)
	}
	logs := logBuf.String()
	if strings.Contains(logs, "[shape-400]") {
		t.Errorf("the log claims shape evidence although no criterion matched:\n%s", logs)
	}
	if strings.Contains(logs, "scene archived to") {
		t.Errorf("it claims to have stored a scene although no criterion matched:\n%s", logs)
	}
	dir := filepath.Join(os.Getenv("NEWGATE_HOME"), "dump", "shape-400-test-shape")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("it created the dedicated evidence directory %s although no criterion matched (err=%v)", dir, err)
	}
}

// TestShapeDirNameIsPathSafe：标记与判据名都会进文件路径，而它们都来自**别的
// 模块**注册进来的策略——不设防就等于让一个注册项决定往哪写文件。
func TestShapeDirNameIsPathSafe(t *testing.T) {
	tests := []struct{ tag, subject, want string }{
		{"shape-400", "deepseek", "shape-400-deepseek"},
		{"shape-400", "a.b_c-1", "shape-400-a.b_c-1"},
		{"shape-400", "../escape", "shape-400-.._escape"},
		{"../x", "deepseek", ".._x-deepseek"},
		{"shape-400", "a/b", "shape-400-a_b"},
		{"shape-400", "", "shape-400-unknown"},
		{"", "deepseek", "shape-deepseek"},
		{"shape-400", "éé", "shape-400-__"},
	}
	for _, tt := range tests {
		if got := shapeDirName(tt.tag, tt.subject); got != tt.want {
			t.Errorf("shapeDirName(%q, %q) = %q, want %q", tt.tag, tt.subject, got, tt.want)
		}
	}
	// 结果里绝不能出现分隔符：路径拼接的反面教材是「策略说了算的字符串」。
	for _, name := range []string{"../x", "a/b", "a\\b"} {
		for _, got := range []string{shapeDirName(name, "d"), shapeDirName("shape-400", name)} {
			if strings.ContainsAny(got, `/\`) {
				t.Errorf("shapeDirName returned %q which still contains a path separator", got)
			}
		}
	}
}

// TestOtherClientErrorDoesNotBlameTheProvider 锁住**最小系统**那一侧：账本上
// 一张纸条都没有时，数据面对 400 的全部动作就是**原样转达**——不换站、不记账、
// 不留痕、不写证据。
//
// 这条测试 2026-09-17 的方向是反的（当时断言「非形状 400 连发两次必须开闸」，
// 理由是「schema 真坏的 provider 否则永远摘不掉」）。翻转它的理由：
//
//   - 400 的含义就是「你这份请求不对」，而这到底是上游的错还是请求的错，**不是
//     数据面能下的结论**；按 400 摘牌等于让一个补丁的盲区决定摘谁，正好是
//     2026-09-17 那次 reasoning 回传 400 事故的形状；
//   - 「这家上游根本不通」不是被动路径能下的结论，`newgate probe` 才是权威
//     手段：探活发的是最小合法请求，base 错/版本错的 provider 会当场被 probe
//     摘掉，而它永远不会因为用户某一轮的对话形状被误判。
//
// 判断这条 400 算谁的账、要不要留痕，是**策略**的事（今天那一份在
// modules/breaker/plane.go 的 Judge 里，真值表在同目录的 plane_test.go）。
// 数据面的义务只有一条：把事实如实报出去，然后照判决执行。
func TestOtherClientErrorDoesNotBlameTheProvider(t *testing.T) {
	schemaErr := `{"error":{"message":"Invalid schema: missing required field 'name'"}}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, schemaErr)
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding:  domain.Binding{Provider: "schema-bad", Model: "real-model-1"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv, logBuf := newLoggingTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	for i := 0; i < 3; i++ {
		resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
			strings.NewReader(`{"model":"newgate/heavy"}`))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 400 || string(got) != schemaErr {
			t.Fatalf("request %d should pass through 400 and the upstream text as is, got %d %q", i+1, resp.StatusCode, got)
		}
	}
	if n := atomic.LoadUint64(&srv.failures); n != 0 {
		t.Errorf("the minimal system booked %d of the 400s as upstream failures - no policy means no verdict, the kernel books nothing by default", n)
	}
	// 没人要现场，存档那一行就不该出现。断言的是**源语言原文**（源语言在运行时
	// 是恒等路径，CI 上 LANG=C 稳定），不是译文。
	if logs := logBuf.String(); strings.Contains(logs, "scene archived to") {
		t.Errorf("nobody asked for a scene, yet the data plane stored one:\n%s", logs)
	}
}

// TestMinimalSystemForwardsEveryOutcomeToNobody 断言「最小系统」是可用的：
// 策略账本为空时，成功的转发照常发生（可用性全放行、按机制换站、不记账）。
//
// 这条守着 policy 的兜底方向。四个决策点的内核默认分别是「全部候选可用」
// 「响应没开始写且还有下一站就继续」「控制面少一节」「无事可做」——任何一处
// 写反了（比如默认不让候选进链），网关就变成了「没装熔断器就一个请求都发不出去」，
// 而那是比熔断器坏掉严重得多的故障。
func TestMinimalSystemForwardsEveryOutcomeToNobody(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding:  domain.Binding{Provider: "p", Model: "real-model-1"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv := newTestServer()
	if !srv.Filters.Empty() {
		t.Fatal("newTestServer should give an empty ledger (minimal system)")
	}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"newgate/heavy"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := ioutil.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(got) != `{"ok":true}` {
		t.Fatalf("minimal-system forward failed: %d %q", resp.StatusCode, got)
	}
}

// TestClientCancelDuringConnectDoesNotBurnTheChain 断言：客户端在**连接阶段**
// 就取消时，不沿链重试、不记失败、**也不把这一发交给任何策略**。
//
// 现场是这么坏的（10.0.50.11 的日志）：一次取消被当成 smt-claude 连接失败，
// 沿链换 smt-deepseek——可 context 已经死了，于是每个候选都瞬间失败，一次
// ESC 把整条链上三个 provider 的熔断器全打开。之后真正的请求没候选可用，
// 回 502，用户根本查不到源头。
//
// 「不交给策略」是这轮倒置之后新加的一层保险：旧实现里数据面自己判断
// 「这不是上游的错」，于是取消**压根不会**走到记账那一步。倒置之后判断权在
// 策略手里，万一哪个策略把 ConnectionFailed 一律算账，取消就又能烧链了——
// 所以数据面干脆不报：对面已经没人接了，这一发没有任何人需要知道。
func TestClientCancelDuringConnectDoesNotBurnTheChain(t *testing.T) {
	hit := make(chan string, 8)
	// 上游装死：收到请求就挂住。上限兜底，免得断言失败时整个测试卡死。
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- r.URL.Path
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer up.Close()

	provs := []string{"prov-a", "prov-b", "prov-c"}
	testChain = func(role string) []resolve.Step {
		var out []resolve.Step
		for _, p := range provs {
			out = append(out, resolve.Step{
				Profile:  "test",
				Binding:  domain.Binding{Provider: p, Model: "real-model-1"},
				Provider: testProvider(up.URL),
			})
		}
		return out
	}
	defer func() { testChain = nil }()

	srv := newTestServer()
	watcher := newScriptedFilter()
	// 一律算账——模拟「最贪心」的策略。数据面必须连问都不问它。
	watcher.verdict = func(policy.Outcome) policy.Verdict {
		return policy.Verdict{Attribute: true}
	}
	if _, err := srv.Filters.Register(watcher); err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", front.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"newgate/heavy","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")

	done := make(chan struct{})
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(ioutil.Discard, resp.Body)
			resp.Body.Close()
		}
		close(done)
	}()

	select {
	case <-hit: // 第一个候选已经打上去了
	case <-time.After(3 * time.Second):
		t.Fatal("the upstream received no request")
	}
	cancel()
	<-done
	time.Sleep(150 * time.Millisecond) // 等 handler 收尾

	// 链上后面的候选一个都不该被打
	select {
	case p := <-hit:
		t.Fatalf("the client already cancelled, yet the chain retried the next candidate (%s)", p)
	default:
	}
	if n := atomic.LoadUint64(&srv.failures); n != 0 {
		t.Errorf("a client cancellation was booked as %d upstream failures", n)
	}
	// 关键：取消压根不该出现在任何策略的输入里。
	for _, o := range watcher.seen() {
		t.Errorf("a client cancellation was reported to the policy as %q (%s) - nobody is on the other end anymore,"+
			"nobody needs to know about this request; reporting it would turn 'one ESC burns the whole chain' into"+
			"the policy's decision", o.Kind, o.Binding)
	}
}
