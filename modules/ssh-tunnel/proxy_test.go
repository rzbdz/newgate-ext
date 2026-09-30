package sshtunnel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// remoteUI 是一台假的「远端 newgate 界面」：它**照真产品的样子**应答——
// 绝对前缀 /ui/、Host 门、以及一个会回显它收到了什么路径的端点。
//
// 照着真东西做而不是随便一个 httptest handler，是因为这里要验的正是**改写与转发
// 的对应关系**：假的上游如果不带 /ui/ 前缀，测试就永远发现不了「本地剥了前缀、
// 远端却没加回去」这种错。
type remoteUI struct {
	srv *httptest.Server
	// seen 记下每一次请求的路径与 Host（断言用）。
	mu    chan struct{}
	paths []string
	hosts []string
}

func newRemoteUI(t *testing.T) *remoteUI {
	r := &remoteUI{mu: make(chan struct{}, 1)}
	r.mu <- struct{}{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		<-r.mu
		r.paths = append(r.paths, req.URL.Path)
		r.hosts = append(r.hosts, req.Host)
		r.mu <- struct{}{}

		// 远端自己的那道 Host 门（与 web-dashboard 的 loopbackHost 同一套判据）。
		// 它在这里是**必要**的：反代如果不保留客户端的 Host，这一关会当场 403，
		// 而那正是我们要提前发现的一种失效。
		if req.Host != "" && !strings.HasPrefix(req.Host, "127.0.0.1") &&
			!strings.HasPrefix(req.Host, "localhost") {
			http.Error(w, "host not allowed", http.StatusForbidden)
			return
		}
		switch req.URL.Path {
		case "/ui/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<!doctype html><html><head>`+
				`<link rel="icon" href="/ui/favicon.svg">`+
				`<script type="module" src="/ui/assets/index-abc.js"></script>`+
				`</head><body><div id="app"></div></body></html>`)
		case "/ui/api/snapshot":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"where":"remote"}`)
		case "/ui/redirect":
			http.Redirect(w, req, "/ui/s/config", http.StatusFound)
		case "/ui/external":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<a href="https://example.com/ui/x">out</a>`)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *remoteUI) seen() []string {
	<-r.mu
	defer func() { r.mu <- struct{}{} }()
	return append([]string(nil), r.paths...)
}

func (r *remoteUI) seenHosts() []string {
	<-r.mu
	defer func() { r.mu <- struct{}{} }()
	return append([]string(nil), r.hosts...)
}

// harness 把一条 route 装起来（与 module.go 里那一行逐字相同）。
type harness struct {
	proxy  *proxy
	pool   *pool
	dialer *fakeDialer
	remote *remoteUI
	loaded Loaded
	h      http.Handler
}

func newHarness(t *testing.T, targets ...Target) *harness {
	t.Helper()
	remote := newRemoteUI(t)
	d := newFakeDialer(strings.TrimPrefix(remote.srv.URL, "http://"))
	p := newPool(d)
	h := &harness{pool: p, dialer: d, remote: remote}
	h.loaded = Loaded{Targets: targets}
	h.proxy = newProxy(p, func() Loaded { return h.loaded })
	// 与 module.go 同一条形状：porthub 不剥前缀，我们自己在 handler 外面剥。
	h.h = http.StripPrefix(Prefix, h.proxy)
	t.Cleanup(func() {
		// 顺序与生产停机一致：先让连接池放掉空闲连接（它们占着借约），再关池。
		h.proxy.closeIdle()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = p.Close(ctx)
	})
	return h
}

// do 发一次请求，Host 是 127.0.0.1:8899（浏览器会填的那个）。
func (h *harness) do(method, path string) *httptest.ResponseRecorder {
	return h.doAs(method, path, "127.0.0.1:8899")
}

// doAs 指定 Host。**空串是一个有意义的输入**（HTTP/1.0 的请求可以没有 Host，
// 而那正是脚本工具的常见形状），所以它不能和「用默认值」共用一个签名——
// 共用过一次，结果是一条「空 Host 该被拒」的断言静默地测了 127.0.0.1。
func (h *harness) doAs(method, path, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	return rec
}

func target(id string) Target {
	return Target{ID: id, Host: "example.invalid", User: "u"}.normalize()
}

// ---------- 路径映射真的通了 ----------

func TestTheRouteForwardsOntoTheRemotePrefix(t *testing.T) {
	// 这条是整个功能的骨架：本机 /ui/remote/ds/X → 远端 /ui/X。
	// 两边任何一边错了，症状都是远端 404——而 404 看起来像「远端没装那个东西」。
	h := newHarness(t, target("ds"))

	rec := h.do("GET", Prefix+"/ds/api/snapshot")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态 %d，正文 %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, `"where":"remote"`) {
		t.Errorf("拿到的不是远端那份正文：%s", got)
	}
	seen := h.remote.seen()
	if len(seen) != 1 || seen[0] != "/ui/api/snapshot" {
		t.Errorf("远端收到的路径是 %v，想要 [/ui/api/snapshot]", seen)
	}
	if got := rec.Header().Get("X-Newgate-Tunnel"); got != "ds" {
		t.Errorf("X-Newgate-Tunnel = %q（调试时靠它认这一页是从哪来的）", got)
	}
}

func TestTheRootOfTheRouteMapsToTheRemoteRoot(t *testing.T) {
	// `/ui/remote/ds/` 必须落到远端的 `/ui/`，不能是 `/ui`（那个远端会 302 到
	// `/ui/`，而那个 Location 一旦没被改写，浏览器就跳到**本机自己**的界面上）。
	h := newHarness(t, target("ds"))

	rec := h.do("GET", Prefix+"/ds/")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态 %d", rec.Code)
	}
	if seen := h.remote.seen(); len(seen) != 1 || seen[0] != "/ui/" {
		t.Errorf("远端收到的路径是 %v，想要 [/ui/]", seen)
	}
}

func TestAPathThatClimbsOutOfTheRemoteUIRootIsRefused(t *testing.T) {
	// 这条 route 带进来的是一台**远端机器**，而它上面除了 `/ui/` 还有 `/__newgate`
	// （控制面：升级、停守护、改状态）与 `/v1`（数据面：真金白银的模型调用）。
	// `/ui/remote/ds/../__newgate/upgrade` 这种地址，如果只是把后缀拼上去转发，
	// 本机就成了一台**替任何人转发远端控制面**的机器——而这条 route 的门只看 Host，
	// 不看路径。
	//
	// 浏览器自己会把 `..` 规范化掉，所以这条防的是**不规范的客户端**（curl
	// --path-as-is、脚本、别的程序）。正因为正常的浏览器碰不到它，它才必须有一条
	// 测试：坏了没有任何症状。
	h := newHarness(t, target("ds"))

	for _, path := range []string{
		Prefix + "/ds/../__newgate/upgrade",
		Prefix + "/ds/../v1/messages",
		Prefix + "/ds/../../etc/passwd",
		Prefix + "/ds/..%2f..%2f__newgate/upgrade",
	} {
		rec := h.do("GET", path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s：状态 %d，想要 404", path, rec.Code)
		}
	}
	// 更要紧的是这一条：被拒的请求**一个字节都不该碰到远端**。
	if seen := h.remote.seen(); len(seen) != 0 {
		t.Errorf("爬出 /ui 的路径碰到远端了：%v", seen)
	}
}

// ---------- 改写 ----------

func TestTheRemoteIndexComesBackRewritten(t *testing.T) {
	h := newHarness(t, target("ds"))

	rec := h.do("GET", Prefix+"/ds/")
	body := rec.Body.String()
	for _, want := range []string{
		`href="/ui/remote/ds/favicon.svg"`,
		`src="/ui/remote/ds/assets/index-abc.js"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("正文里没有 %s：\n%s", want, body)
		}
	}
	// 一个**没有**被改写的 /ui/… 意味着浏览器会去本机要那个资源。
	if strings.Contains(body, `"/ui/assets/`) {
		t.Errorf("还有没改写的绝对前缀，浏览器会打到本机自己的界面上：\n%s", body)
	}
}

func TestTheRemoteRedirectComesBackRewritten(t *testing.T) {
	// 这条是这套改写里唯一「看不出来」的错：Location 不改，用户点一下就从一个
	// 远端界面跳到了**本机自己的**界面，而两者长得几乎一样。
	h := newHarness(t, target("ds"))

	rec := h.do("GET", Prefix+"/ds/redirect")
	if rec.Code != http.StatusFound {
		t.Fatalf("状态 %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/ui/remote/ds/s/config" {
		t.Errorf("Location = %q，想要 /ui/remote/ds/s/config", got)
	}
}

func TestExternalLinksAreNotHijacked(t *testing.T) {
	// 外链里恰好也含 /ui/（别人的站点路径）。把它改掉等于**悄悄把用户的出站
	// 链接指回我们自己**——而这件事错了没有任何症状。
	h := newHarness(t, target("ds"))

	rec := h.do("GET", Prefix+"/ds/external")
	if got := rec.Body.String(); !strings.Contains(got, `https://example.com/ui/x`) {
		t.Errorf("外链被改道了：%s", got)
	}
}

// ---------- 远端的门 ----------

func TestTheClientsHostReachesTheRemote(t *testing.T) {
	// 反代默认会把 Host 换成目标主机。远端 BFF 的 rebinding 门只放行 IP 字面量
	// 与 localhost，所以换掉之后远端会**当场 403**，而本机这一侧一切正常——
	// 那种错会让人从改写一路查到远端 sshd。
	//
	// 这里用 `localhost:8899` 而不是局域网 IP：两者都该过，而前者在测试里不依赖
	// 这台机器真实的网卡。
	h := newHarness(t, target("ds"))

	if rec := h.doAs("GET", Prefix+"/ds/api/snapshot", "localhost:8899"); rec.Code != http.StatusOK {
		t.Fatalf("状态 %d，正文 %s", rec.Code, rec.Body.String())
	}
	if hosts := h.remote.seenHosts(); len(hosts) != 1 || hosts[0] != "localhost:8899" {
		t.Errorf("远端收到的 Host 是 %v，想要 [localhost:8899]——换掉它远端会 403", hosts)
	}
}

func TestTheRouteItselfRefusesNonLoopbackHosts(t *testing.T) {
	// 本地这道门是这条 route 的**唯一**一道：远端那道查的是我们转过去的 Host，
	// 而那个值就是浏览器填的。
	h := newHarness(t, target("ds"))

	for _, host := range []string{"evil.com:8899", "10.0.50.11:8899", ""} {
		rec := h.doAs("GET", Prefix+"/ds/", host)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Host %q 该被拒，实际 %d", host, rec.Code)
		}
	}
	if seen := h.remote.seen(); len(seen) != 0 {
		t.Errorf("被拒的请求不该碰到远端，实际 %v", seen)
	}
}

// ---------- 边界 ----------

func TestTheBareRouteRedirectsToTheSlashForm(t *testing.T) {
	// 没有尾斜杠时，浏览器会把相对地址按当前文档解析——从 `/ui/remote/ds` 出发，
	// `api/snapshot` 会变成 `/ui/remote/api/snapshot`，那里把 api 当成了 id。
	for _, path := range []string{Prefix, Prefix + "/ds"} {
		h := newHarness(t, target("ds"))
		rec := h.do("GET", path)
		if rec.Code != http.StatusFound {
			t.Fatalf("%s：状态 %d，想要 302", path, rec.Code)
		}
		want := Prefix + "/"
		if path != Prefix {
			want = Prefix + "/ds/"
		}
		if got := rec.Header().Get("Location"); got != want {
			t.Errorf("%s → Location %q，想要 %q", path, got, want)
		}
	}
}

func TestAnUnknownTargetListsTheOnesThatExist(t *testing.T) {
	// 手敲地址是这条 route 最常见的用法（从终端复制一个链接过来）。敲错一个字符
	// 时，一句光秃秃的 404 会让人以为整条 tunnel 坏了。
	h := newHarness(t, target("ds"), target("ark"))

	rec := h.do("GET", Prefix+"/nope/")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("状态 %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"nope", "ark", "ds"} {
		if !strings.Contains(body, want) {
			t.Errorf("404 正文里没有 %q：%s", want, body)
		}
	}
}

func TestTheIndexListsEveryTarget(t *testing.T) {
	h := newHarness(t, target("ds"), target("ark"))
	rec := h.do("GET", Prefix+"/")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态 %d", rec.Code)
	}
	for _, want := range []string{Prefix + "/ark/", Prefix + "/ds/"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("索引里没有 %s：%s", want, rec.Body.String())
		}
	}
	if seen := h.remote.seen(); len(seen) != 0 {
		t.Errorf("索引是本机自己画的，不该碰远端：%v", seen)
	}
}

// ---------- 拨号与失败 ----------

func TestTheRouteDialsOnceAndThenReuses(t *testing.T) {
	// 复用是这个模块有没有实用价值的决定因素：一次 SSH 握手是公钥运算
	// （内网 50–200ms），比远端界面自己的一次 API 往返还贵。每个请求拨一次的话，
	// 打开一张控制台要付几十次握手的钱。
	h := newHarness(t, target("ds"))

	for i := 0; i < 5; i++ {
		if rec := h.do("GET", Prefix+"/ds/api/snapshot"); rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次请求状态 %d", i, rec.Code)
		}
	}
	if dials, _ := h.dialer.counts(); dials != 1 {
		t.Errorf("五次请求拨了 %d 次，想要 1 次", dials)
	}
	snap, _ := h.pool.Snapshot("ds")
	if snap.State != StateUp {
		t.Errorf("状态是 %q，想要 up", snap.State)
	}
}

func TestAFailedDialIsNotCached(t *testing.T) {
	// 失败**不进缓存**：缓存失败会让「网络恢复之后还要等一段退避」这种事发生，
	// 而用户看到的只是「我重启了路由器它还是连不上」。
	h := newHarness(t, target("ds"))
	h.dialer.setFail(errors.New("boom"))

	if rec := h.do("GET", Prefix+"/ds/"); rec.Code != http.StatusBadGateway {
		t.Fatalf("状态 %d，想要 502", rec.Code)
	}
	if snap, _ := h.pool.Snapshot("ds"); snap.State != StateDown || snap.LastErr == "" {
		t.Errorf("失败之后状态该是 down 并带上原因，实际 %+v", snap)
	}

	// 恢复之后立刻就能连上（没有退避）。
	h.dialer.setFail(nil)
	if rec := h.do("GET", Prefix+"/ds/"); rec.Code != http.StatusOK {
		t.Fatalf("恢复之后状态 %d，想要 200", rec.Code)
	}
}

func TestTheErrorPageNamesTheTargetAndTheNextStep(t *testing.T) {
	// 一个空的 502 会让人从「是不是我的浏览器有问题」开始排查。
	h := newHarness(t, target("ds"))
	h.dialer.setFail(errors.New("connection refused by 10.0.50.11:22"))

	rec := h.do("GET", Prefix+"/ds/")
	body := rec.Body.String()
	if !strings.Contains(body, "connection refused") {
		t.Errorf("错误正文里没有原因：%s", body)
	}
	if !strings.Contains(body, "newgate tunnel test ds") {
		t.Errorf("错误正文里没有下一步：%s", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q（这些字符串一半来自远端，不包进 HTML 就不用转义）", ct)
	}
}

func TestTheRouteFollowsTheConfigWithoutRestart(t *testing.T) {
	// 用户在浏览器里加了一个 target，下一个请求就该按新的走。缓存配置的话，
	// 「刚改完没生效」会变成一条只能靠重启绕过的怪现象。
	h := newHarness(t) // 一开始一个都没有

	if rec := h.do("GET", Prefix+"/ds/"); rec.Code != http.StatusNotFound {
		t.Fatalf("还没有 target 时状态 %d，想要 404", rec.Code)
	}
	h.loaded = Loaded{Targets: []Target{target("ds")}}
	if rec := h.do("GET", Prefix+"/ds/api/snapshot"); rec.Code != http.StatusOK {
		t.Fatalf("加了 target 之后状态 %d，想要 200", rec.Code)
	}
}
