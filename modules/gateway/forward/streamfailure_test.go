package forward

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/gateway/probe"
	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
)

// streamFailUpstream 是「Responses 方言在流里报失败」的假上游：**HTTP 200**，
// 然后一条 response.failed。真实上游就是这个形状（core/mock/fake_upstream.py
// 的 _responses_failed 逐字复刻过），而它正是这条测试存在的理由——按状态码写
// 的判据在这里看不见任何异常。
func streamFailUpstream(t *testing.T, message string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, `event: response.created`+"\n"+
			`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`+"\n\n")
		if fl != nil {
			fl.Flush()
		}
		_, _ = io.WriteString(w, `event: response.failed`+"\n"+
			`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","output":[],`+
			`"error":{"code":"invalid_request_error","message":"`+message+`"}}}`+"\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
}

// TestStreamFailureInsideA200IsLearned 钉住这条路上**唯一**的学习入口。
//
// # 它复现的故障（用户 2026-09-22 报的那一句）
//
// Codex → GLM：上游把拒绝塞在事件流里，HTTP 状态码是 200。数据面那条按状态码
// 走的 learnQuirks（status >= 400 才学）因此**一次都不触发**，quirk 表永远是
// 空的，always-thinks 的 Match 永远不绿——用户看到的是「必现、过一会儿自己好」
// （「好」是因为恰好有人 probe 过那一家，而那是运气，不是机制）。
//
// # 为什么断言「落盘缓存里有这一笔」而不是「quirk 表里有」
//
// 表活在内存里，重启就空；用户真正要的是**下一发不再撞**，那靠的是落盘
// （probe-capabilities.json）+ 下个进程的 LoadCachedCapabilities。所以这里
// 断言到盘上那一笔为止——内存里那位只是这条路的中间站。
//
// 签名是就地注册的：判据的归属是 `modules/thinking`（它拥有那个补丁），而
// forward 只是**用**它。真注册它会把 modules/thinking 拖进本包的测试依赖，
// 成一圈不该有的依赖。
func TestStreamFailureInsideA200IsLearned(t *testing.T) {
	isolate(t)
	quirk.Default.Reset()
	t.Cleanup(quirk.Default.Reset)

	// 判据本身（「报错里出现『始终思考』→ 这个模型不能关思考」）由
	// modules/thinking 在 Start 里注册，而 TestMain 起的整张图把它装进来了
	// ——本测试只是**用**它，不另注册一条（注册会撞名，那是设计好的：
	// RegisterSignature 对同一个 Label 当场报错，见 gateway/quirk）。
	//
	// 这一句把「判据真的在那儿」钉住：哪天 thinking 被摘掉，这里会先红，
	// 而不是让下面的断言从「学到了」悄悄变成一条**测不到东西**的绿。
	if got := quirk.Default.Learn("probe-check", "probe-check", 400, []byte("始终思考")); len(got) == 0 {
		t.Fatal("modules/thinking 的签名没装进来——这条测试会空转，先修装配")
	}
	quirk.Default.Reset()

	const message = "该模型始终思考，不支持关闭思考；请使用 low、high 或 max。"
	up := streamFailUpstream(t, message)
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{
			Profile:  "test",
			Binding:  domain.Binding{Provider: "smt-glm", Model: "glm-5.3"},
			Provider: testProvider(up.URL),
		}}
	}
	defer func() { testChain = nil }()

	srv, logs := newLoggingTestServer()
	// 停机时落盘那一步（真实 daemon 走的是 Shutdown / 优雅交接两条路，
	// 这里是同一个函数）。
	defer srv.flushPendingQuirks()

	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/responses", "application/json",
		strings.NewReader(`{"model":"newgate/heavy","stream":true,"input":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// 前两件是**这条故障为什么难查**的形状：状态码 200、连接正常。
	if resp.StatusCode != 200 {
		t.Fatalf("这条路的形状就是 200，实际 %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "response.failed") {
		t.Fatalf("流里那条失败没原样转给客户端:\n%s", body)
	}

	// 学到手（内存表）
	if !quirk.Default.Has("smt-glm", "glm-5.3", quirk.NoThinkingDisable) {
		t.Fatalf("流里的失败没被学到。日志:\n%s", logs.String())
	}
	// 说出来了（不静默）：用户得知道我们从下一发开始会往请求里加东西。
	if log := logs.String(); !strings.Contains(log, "reported a failure inside the event stream") {
		t.Fatalf("日志里没有「上游在流里报了一次失败」这一行:\n%s", log)
	}
	if log := logs.String(); !strings.Contains(log, "learned smt-glm/glm-5.3") {
		t.Fatalf("日志里没有「学到了」那一行:\n%s", log)
	}

	// 落盘（换版/重启之后还认得）
	srv.flushPendingQuirks()
	disk := probe.DiskQuirks()
	if len(disk) != 1 || disk[0].Provider != "smt-glm" || disk[0].Model != "glm-5.3" {
		t.Fatalf("盘上没有这一笔，重启后又会重撞一次: %v", disk)
	}
}

// TestSuccessfulStreamDoesNotLearn 不误伤：正常收尾的流一个字节都不学。
//
// 这条判据比上一条重要——上一条坏了是「没学到」（用户再撞一次，可恢复），
// 这条坏了是**凭一条正常的流往所有请求里加字段**，而那是不可恢复的静默改
// 用户请求。所以这里连「表是空的」和「没写盘」两件都断言。
func TestSuccessfulStreamDoesNotLearn(t *testing.T) {
	isolate(t)
	quirk.Default.Reset()
	t.Cleanup(quirk.Default.Reset)

	// 判据本身（「报错里出现『始终思考』→ 这个模型不能关思考」）由
	// modules/thinking 在 Start 里注册，而 TestMain 起的整张图把它装进来了
	// ——本测试只是**用**它，不另注册一条（注册会撞名，那是设计好的：
	// RegisterSignature 对同一个 Label 当场报错，见 gateway/quirk）。
	//
	// 这一句把「判据真的在那儿」钉住：哪天 thinking 被摘掉，这里会先红，
	// 而不是让下面的断言从「学到了」悄悄变成一条**测不到东西**的绿。
	if got := quirk.Default.Learn("probe-check", "probe-check", 400, []byte("始终思考")); len(got) == 0 {
		t.Fatal("modules/thinking 的签名没装进来——这条测试会空转，先修装配")
	}
	quirk.Default.Reset()

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		for _, ev := range []string{
			`{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`,
		} {
			_, _ = io.WriteString(w, "data: "+ev+"\n\n")
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{
			Profile:  "test",
			Binding:  domain.Binding{Provider: "smt-glm", Model: "glm-5.3"},
			Provider: testProvider(up.URL),
		}}
	}
	defer func() { testChain = nil }()

	srv := newTestServer()
	defer srv.flushPendingQuirks()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/responses", "application/json",
		strings.NewReader(`{"model":"newgate/heavy","stream":true,"input":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if quirk.Default.Has("smt-glm", "glm-5.3", quirk.NoThinkingDisable) {
		t.Fatal("正常收尾的流被当成失败学了——下一发会给一个没毛病的模型加字段")
	}
	srv.flushPendingQuirks()
	if disk := probe.DiskQuirks(); len(disk) != 0 {
		t.Fatalf("正常收尾的流不该往盘上写任何东西: %v", disk)
	}
}
