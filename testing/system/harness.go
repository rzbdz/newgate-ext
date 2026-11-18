// Package system 是进程内的**系统级**测试：真组件图 + 真转发服务 + 假上游。
//
// 它不是端到端。端到端在这份仓库里是 mock/ 下那两条 shell（真二进制、真进程、
// 真接管配置、真还原），那是唯一能验「装出来的东西能不能跑」的地方——见
// docs/10-testing-security.md 的分层表。这里跑的是**同一个进程里装起来的一张
// 真组件图**：没有子进程、没有 PATH shim、没有 systemd。叫它 e2e 会误导人
// 以为它覆盖了那些，所以叫 system。
//
// 为什么这一层值得存在
//
// 单测用 httptest 打桩，验的是「函数行为对不对」；shell 那份是黑盒，断言靠
// grep 输出、失败时只能看日志。中间缺一层：能直接断言结构（哪个 provider 收到
// 什么、响应头说什么），又能拿到真实的端到端数据面行为（路由、fallback、
// thinkcache、流式分帧）。按「基础通用的功能可以做大测试」的原则，这一层覆盖
// 的正是那些不常改、但一改就全线出问题的东西：档位解析、字节手术、转移。
//
// 各模块自己的特殊行为**不在这里**——那些放回模块内部，用 testing/testkit
// 起一张只含该模块与其依赖的最小图去测（只有模块自己清楚它要什么、怎么动态
// 更新）。这一层只测「合起来还对」。
//
// 与线上 daemon 的关系：Harness 监听一个**向内核要来的临时端口**（不是 8899），
// 所以它和跑着的线上 daemon 完全不冲突，可以一边用着 newgate、一边跑这套。
package system

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/lib/httpx"
	"github.com/rzbdz/newgate-ext/manifest"
	breakerapi "github.com/rzbdz/newgate-ext/modules/breaker"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/forward"
	gwpolicy "github.com/rzbdz/newgate-ext/modules/gateway/policy"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	"github.com/rzbdz/newgate-ext/testing/upstream"
	"github.com/rzbdz/newgate/app"
	modules "github.com/rzbdz/newgate/component"
)

// logSink 收代理自己的日志，失败时才吐到测试输出。
//
// 为什么不直接 io.Discard：路由类断言失败时（404「没有可用候选」是典型），
// 真正的证据在代理那一行 `跳过原因：…` 里——哪个候选、为什么被跳过。丢掉它，
// 测试只告诉你「404 不是 200」，然后人要手工复现一遍才能知道原因。
//
// 为什么先攒着而不是直接 t.Logf：日志来自服务端 goroutine，测试结束后再写
// 会 panic。这里在 Start 里最先注册清理，于是它最后执行（t.Cleanup 是后进
// 先出），那时服务端已经停干净了。成功时这些行是噪音，所以只在失败时倒。
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *logSink) dump(t *testing.T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buf.Len() == 0 {
		return
	}
	t.Logf("代理日志：\n%s", s.buf.String())
}

func (s *logSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// Harness 是一个跑起来的 newgate 实例。
type Harness struct {
	t *testing.T

	// ProxyURL 是转发服务根地址，形如 http://127.0.0.1:34567。
	ProxyURL string
	// Upstream 是它这次指向的假上游。
	Upstream *upstream.Server
	// Env 是隔离出来的配置目录（NEWGATE_HOME 等）。
	Env *testkit.Env
	// Components 是实际启动顺序，用来断言依赖。
	Components []string

	graph   *app.App
	server  *forward.Server
	watcher *store.Watcher
	client  *http.Client
	// health 是组件图里那一张健康表（它自己经 RegisterFilter 挂进了数据面）。
	health breakerapi.Breaker
	// sink 攒着代理自己写的日志，供测试断言「不静默」那类要求。
	sink *logSink
}

// DefaultGraph is the graph Start installs: this distribution's flagship spec.
//
// The harness used to install the kernel's own graph. Since the kernel ships no
// modules (2026-11-18), "a real graph" is by definition a product graph — this
// distribution's. StartWith remains for a test that wants a different one.
func DefaultGraph() modules.Loader {
	return manifest.Specs()[manifest.DefaultSpec]
}

// Start 起一整套：沙箱配置 → 假上游 → 真组件图 → 真转发服务（临时端口）。
//
// 端口是向内核要的（listen :0 再关掉），所以同一台机器上并行跑多份互不冲突。
// 拿到端口后写进 state.json，再让 forward 监听它——顺序不能反，因为 forward
// 的端口就来自配置。
//
// It installs DefaultGraph(); pass StartWith to install a different one.
func Start(t *testing.T) *Harness {
	t.Helper()
	return StartWith(t, DefaultGraph())
}

// StartWith 与 Start 逐字相同，只是装配清单由调用方给。
//
// 为什么要有它（2026-09-20）：**测试跟着拥有者走**。发行版带的那几个模块的行为
// （DeepSeek 的尾部形状修补之类）该由发行版自己断言，而它需要「真组件图 + 真转发
// 服务」这套设施——那套设施是内核的（真图才有的副作用：special 插件注册、健康表
// 挂进数据面、confighook 装客户端目录）。没有这个入口，发行版只有两条路：把内核
// 的测试设施复制一份（必然漂移），或者把自己的模块塞进内核的默认图里（内核就再也
// 测不干净）。两条都比多一个函数贵。
func StartWith(t *testing.T, loader modules.Loader) *Harness {
	t.Helper()

	env := testkit.Sandbox(t)
	up := upstream.New(t)

	// 代理日志攒起来，失败时倒出来（注册得最早 → 清理时最后执行）。
	sink := &logSink{}
	t.Cleanup(func() {
		if t.Failed() {
			sink.dump(t)
		}
	})

	// 1. 铺一份默认配置，然后把上游指到假上游。
	if _, err := store.Init(false); err != nil {
		t.Fatalf("system: 初始化沙箱配置: %v", err)
	}
	pointAtUpstream(t, up.URL())

	// 2. 端口必须在写 state 之前拿到，否则 forward 监听的端口和配置里写的
	//    对不上（客户端按 state 里的端口来找我们）。
	port := freePort(t)
	st := store.LoadState()
	st.Port = port
	if err := store.SaveState(st); err != nil {
		t.Fatalf("system: 写 state.json: %v", err)
	}

	// 3. 真组件图。这一步的副作用正是我们要测的东西：gateway 把 special 插件
	//    注册进默认 registry、confighook 装出客户端目录、config 接上角色提供者。
	graph, err := app.New(context.Background(), loader)
	if err != nil {
		t.Fatalf("system: 装配组件图: %v", err)
	}
	t.Cleanup(func() { _ = graph.Stop(context.Background()) })

	// 4. 真转发服务。watcher 与生产一致（1 秒轮询），所以改配置热更新这条路径
	//    也在这层覆盖范围里。
	//
	//    策略账本取自**真组件图**里那一本（不是新 policy.New()）：这样贡献者的
	//    注册才算真的走通了「模块 Start → gateway.RegisterFilter → 数据面」，
	//    健康表也是生产用的那一张。forward 只是被注入方——它从不注册任何东西，
	//    只读账本（见 forward.go 的 [shape-400] 分支）。
	//
	//    取值走 policy.Default()（gateway 在 Start 里装上的实例），而不是
	//    modules.MustGet(graph.Context(), gatewayapi.Capability)：后者拿到的是
	//    *port，而数据面要的是那一本账。这条与健康表那条的取法不同，原因是
	//    「账本」本身就是插入口（见 modules/gateway/policy），不需要再经一层。
	watcher, err := store.NewWatcher(time.Second)
	if err != nil {
		t.Fatalf("system: 配置 watcher: %v", err)
	}
	t.Cleanup(watcher.Close)
	watcher.Start()

	health := modules.MustGet(graph.Context(), breakerapi.Capability)
	server := forward.New("", port, log.New(sink, "", 0), watcher, gwpolicy.Default())
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Start() }()
	t.Cleanup(func() {
		server.Shutdown()
		select {
		case err := <-serveErr:
			// Shutdown 之后 Serve 返回 http.ErrServerClosed 是正常的。
			if err != nil && !strings.Contains(err.Error(), "Server closed") {
				t.Errorf("system: 转发服务异常退出: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Errorf("system: 转发服务 3 秒内没退出")
		}
	})

	h := &Harness{
		t:          t,
		ProxyURL:   "http://127.0.0.1:" + strconv.Itoa(port),
		Upstream:   up,
		Env:        env,
		Components: graph.ComponentNames(),
		graph:      graph,
		server:     server,
		watcher:    watcher,
		health:     health,
		sink:       sink,
		// 显式绕开环境代理：CI 或开发机上常有 HTTP_PROXY，不绕的话
		// 127.0.0.1 的请求会被劫到代理去（本机实测踩过）。
		client: httpx.LocalClient(15 * time.Second),
	}
	h.waitReady()
	return h
}

// Client 返回已经绕开环境代理的 HTTP 客户端，超时 15 秒。
func (h *Harness) Client() *http.Client { return h.client }

// Breaker 返回**组件图里那一张**健康表（就是数据面用的那一张）。
//
// 为什么要有这个入口：形状判据是由模块在 Start 里注册进来的，测试若自己
// newTable() 就绕过了注册，断言会变成「什么检测器都没有时 400 也不摘牌」——
// 那是真的但没意义，恰是 2026-09-17 之前那条测试变成空转的原因。拿真表才能
// 断言「判据真的被注册、真的认领了、转发路径真的把 Shape 读出来了」。
func (h *Harness) Breaker() breakerapi.Breaker { return h.health }

// Logs 返回代理到目前为止写下的日志（快照，调用后可继续追加）。
//
// 断言「不静默」用：形状判据认领一发 400 时，日志里必须有 [shape-400] 那一行，
// 且那行要说出**是哪条判据**认的。只断言健康表计数器的话，日志掉了一句也没人
// 发现——而排查现场时人手里只有日志。
func (h *Harness) Logs() string { return h.sink.String() }

// WaitForLog 轮询等一段日志出现（代理写日志与客户端拿到响应之间没有顺序保证，
// 尤其是上游回错、我们还要存证据的那种路径）。超时返回 false。
func (h *Harness) WaitForLog(t *testing.T, sub string) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(h.Logs(), sub) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// Post 发一个 JSON 请求到代理，返回响应。path 以 / 开头，例如
// "/a/claude/v1/messages"。
func (h *Harness) Post(path, body string) *http.Response {
	h.t.Helper()
	resp, err := h.post(path, body)
	if err != nil {
		h.t.Fatalf("system: POST %s: %v", path, err)
	}
	return resp
}

// PostRaw 同 Post，但把网络错误交回给调用方（测断连之类要用）。
func (h *Harness) PostRaw(path, body string) (*http.Response, error) {
	return h.post(path, body)
}

func (h *Harness) post(path, body string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, h.ProxyURL+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer newgate-local")
	return h.client.Do(req)
}

// Get 发一个 GET。
func (h *Harness) Get(path string) *http.Response {
	h.t.Helper()
	resp, err := h.client.Get(h.ProxyURL + path)
	if err != nil {
		h.t.Fatalf("system: GET %s: %v", path, err)
	}
	return resp
}

// ReadBody 读空响应体并关闭，返回字符串。
func ReadBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("system: 读响应体: %v", err)
	}
	return string(b)
}

// waitReady 轮询直到代理能应答。端口已经 listen 成功才返回，所以这里只等
// HTTP 层就绪——但 socket 和 HTTP handler 之间有几毫秒差，不轮询会偶发失败。
func (h *Harness) waitReady() {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := h.client.Get(h.ProxyURL + "/__newgate/status")
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("system: 代理 5 秒内没就绪（%s）", h.ProxyURL)
}

// pointAtUpstream 把所有 provider 指到假上游。
//
// base_url 带 /v1（openai 方言拼 /chat/completions）；anthropic_url 不带 /v1
// ——这正是两种字段的既定语义，也顺带验证了「客户端说哪种方言就走哪个 base」
// 这条路径在集成层是通的。
func pointAtUpstream(t *testing.T, url string) {
	t.Helper()
	providers, err := store.LoadProviders()
	if err != nil {
		t.Fatalf("system: 读 providers.json: %v", err)
	}
	if len(providers.Providers) == 0 {
		t.Fatal("system: 默认配置里没有 provider")
	}
	for name, p := range providers.Providers {
		p.BaseURL = url + "/v1"
		p.AnthropicURL = url
		p.APIKey = "sk-fake-" + name
		// key 走环境变量的话我们这里设不了，会被判成「没 key」而拒绝转发。
		p.APIKeyEnv = ""
		// 必须写回 map：range 的 value 是拷贝，光改 p 是在改副本——
		// 症状是所有候选都被判「provider 没有 api_key」，请求 404 而不是转发。
		providers.Providers[name] = p
	}
	if err := store.SaveProviders(providers); err != nil {
		t.Fatalf("system: 写 providers.json: %v", err)
	}
}

// freePort 向内核要一个当前空闲的端口。有 TOCTOU 窗口（关掉到重新 listen 之间
// 别人可能抢），但并行测试里每个进程各要一个，实际不会撞。
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("system: 要空闲端口: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}
