package sshtunnel

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// 这一族是**这套改写**的全部判据。它值得单独一个文件、写得比代码还长，因为改写
// 是这条 route 里唯一一处「错了也不会报错」的地方：
//
//   - 少改一处 → 浏览器去本机要一个不存在的资源 → 白屏（看得见，但没人知道为什么）；
//   - 多改一处 → 用户数据被动；
//   - Location 没改 → **悄悄跳到本机自己的界面上**，而两者长得几乎一样。
//
// 最后那条是这三条里唯一「看起来是好的」的，所以它单独一条测试。

// ---------- 前缀替换 ----------

func TestOnlyTheAbsolutePrefixIsRewritten(t *testing.T) {
	local := LocalPrefix("ds")
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "index.html 里的 assets 引用",
			in:   `<script type="module" crossorigin src="/ui/assets/index-CpR3oezi.js"></script>`,
			want: `<script type="module" crossorigin src="/ui/remote/ds/assets/index-CpR3oezi.js"></script>`,
		},
		{
			name: "favicon",
			in:   `<link rel="icon" type="image/svg+xml" href="/ui/favicon.svg" />`,
			want: `<link rel="icon" type="image/svg+xml" href="/ui/remote/ds/favicon.svg" />`,
		},
		{
			// 这条是让整个界面能工作的那条：Vite 在构建期把
			// `import.meta.env.BASE_URL + "api"` 折成了这个字面量。
			// 不改它，浏览器拿 /ui/api 去打**本机自己**的 web-dashboard——
			// 于是看到一个本地界面里混着远端数据的页面，比白屏更糟。
			name: "JS 里折出来的 API 基址",
			in:   `const API="/ui/"+"api";`,
			want: `const API="/ui/remote/ds/"+"api";`,
		},
		{
			name: "同一份正文里出现多次",
			in:   `/ui/a /ui/b /ui/c`,
			want: `/ui/remote/ds/a /ui/remote/ds/b /ui/remote/ds/c`,
		},
		{
			name: "相对地址不碰（它跟着当前文档走，本来就对）",
			in:   `<script src="./assets/x.js"></script>`,
			want: `<script src="./assets/x.js"></script>`,
		},
		{
			name: "带 scheme 的绝对地址不碰（它指向的是别处）",
			in:   `<a href="https://example.com/ui/x">`,
			want: `<a href="https://example.com/ui/x">`,
		},
		{
			name: "没有尾斜杠的 /ui 不碰——它不是前缀，是另一个地址",
			in:   `<a href="/ui">`,
			want: `<a href="/ui">`,
		},
		{
			name: "另一个前缀 /uix 不被误伤",
			in:   `<a href="/uix/thing">`,
			want: `<a href="/uix/thing">`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(rewritePrefix([]byte(tc.in), local))
			if got != tc.want {
				t.Errorf("\n in: %s\nwant: %s\n got: %s", tc.in, tc.want, got)
			}
		})
	}
}

// TestTheApiBaseInsideTheBundleIsRewritten：这条是**用户实际撞上的那个 bug**。
//
// 现象：打开隧道里的界面，它渲染得好好的，但数据全是**本机**的。原因是产物里
// `import.meta.env.BASE_URL + "api"` 在构建期被折成了一个字面量
// `const Fn="/ui/api"`，而这条 route 当时只改写 text/html——JS 原样过去，于是
// 那个页面照着本机的 /ui/api 发请求。
//
// 断言的是**那段真实的产物片段**：拿一段长得像 bundle 的文本，确认里面的 API 基址
// 被搬到了这条 route 上。
func TestTheApiBaseInsideTheBundleIsRewritten(t *testing.T) {
	local := LocalPrefix("snode1")
	bundle := `const Zc=1,Fn="/ui/api";function ec(n){return fetch(Fn+"/snapshot")}`
	got := string(rewritePrefix([]byte(bundle), local))
	if !strings.Contains(got, `Fn="/ui/remote/snode1/api"`) {
		t.Fatalf("产物里的 API 基址没被改写，那个页面会去打本机自己的 API：\n%s", got)
	}
}

func TestRewriteLeavesUntouchedBodiesAlone(t *testing.T) {
	// 绝大多数响应都不含那个前缀（JSON、图片、404 的那一句话）。这条锁的是
	// 「没命中就原样返回同一个切片」——不是为了省一次分配，而是为了让
	// 「改写到底动过什么」这件事在测试里可断言。
	body := []byte(`{"ok":true,"items":[]}`)
	got := rewritePrefix(body, LocalPrefix("ds"))
	if !bytes.Equal(got, body) {
		t.Errorf("没有 /ui/ 的正文被改了：%s", got)
	}
}

// ---------- Location ----------

func TestRedirectsStayInsideTheRoute(t *testing.T) {
	// 不改 Location 的症状是**悄悄跳到本机自己的界面上**：远端 BFF 对 `/ui`
	// （没有尾斜杠）会 302 到 `/ui/`，而那个地址在本机上是 web-dashboard——
	// 用户点一下，就从「远端的界面」跳到了「本地界面」，而两者长得几乎一样。
	// 这是整套改写里唯一一处看不出来的错。
	local := LocalPrefix("ds")
	cases := []struct{ in, want string }{
		{"/ui/", "/ui/remote/ds/"},
		{"/ui/s/config", "/ui/remote/ds/s/config"},
		// 站外与相对地址一律不碰。
		{"https://example.com/ui/", "https://example.com/ui/"},
		{"other/page", "other/page"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := rewriteLocation(tc.in, local); got != tc.want {
			t.Errorf("Location %q → %q，想要 %q", tc.in, got, tc.want)
		}
	}
}

// ---------- 该不该改 ----------

func resp(status int, ctype, encoding string) *http.Response {
	h := http.Header{}
	if ctype != "" {
		h.Set("Content-Type", ctype)
	}
	if encoding != "" {
		h.Set("Content-Encoding", encoding)
	}
	return &http.Response{StatusCode: status, Header: h}
}

func TestOnlyHTMLIsRewritten(t *testing.T) {
	cases := []struct {
		name string
		resp *http.Response
		want bool
	}{
		{"index.html", resp(200, "text/html; charset=utf-8", ""), true},
		{"错误页也是它自己那套 HTML", resp(404, "text/html", ""), true},
		// **JS 必须改**：那个 /ui/ 不是资源地址，是 vite 把 BASE_URL 烘进产物的
		// 结果，运行期被用来拼 API——产品里就是 `const Fn="/ui/api"`。不改它，
		// 隧道里那页会照着**本机自己**的 API 发请求：界面照常渲染，数据全是本机的。
		{"JS 要改（API 基址烘在里面）", resp(200, "text/javascript; charset=utf-8", ""), true},
		{"application/javascript 也要改", resp(200, "application/javascript", ""), true},
		{"CSS 要改（url() 里的绝对前缀）", resp(200, "text/css", ""), true},
		{"JSON 不改（它是数据，不是地址）", resp(200, "application/json", ""), false},
		{"图片不改", resp(200, "image/svg+xml", ""), false},
		// 压缩过的正文里没有明文路径，改了就是毁掉这份响应。
		{"压缩过的不改", resp(200, "text/html", "gzip"), false},
		// 5xx 通常是代理/网关自己生成的，不是远端那套界面。
		{"5xx 不改", resp(502, "text/html", ""), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRewrite(tc.resp); got != tc.want {
				t.Errorf("shouldRewrite = %v，想要 %v", got, tc.want)
			}
		})
	}
}

// ---------- 路径映射 ----------

func TestLocalPathsMapOntoTheRemoteOnes(t *testing.T) {
	// 这条是整套改写的地基，也是**两边必须同时成立**的地方：
	// 正文里的绝对地址被改成 /ui/remote/<id>/…，浏览器发回来的就是它，
	// 而这里把它映射回远端要的 /ui/…。改了一边没改另一边，症状是每个资源
	// 都 404——而 404 看起来像「远端没装那个东西」。
	cases := []struct {
		local, want string
		ok          bool
	}{
		{local: "/ds/", want: "/ui/", ok: true},
		{local: "/ds", want: "/ui/", ok: true},
		{local: "/ds/api/snapshot", want: "/ui/api/snapshot", ok: true},
		{local: "/ds/assets/index-CpR3oezi.js", want: "/ui/assets/index-CpR3oezi.js", ok: true},
		{local: "/ds/ui/assets/x.js", want: "/ui/ui/assets/x.js", ok: true},
		// 爬出 /ui 的一律拒。远端 `/__newgate` 是它的控制面（upgrade 会 fork 一个
		// 新进程）、`/v1` 是它的数据面。今天那条路径碰巧打不到控制面，但那靠的是
		// 「这条链路上没有别人会 clean 路径」——安全边界不能建在这上面。
		{local: "/ds/../__newgate/upgrade"},
		{local: "/ds/../v1/messages"},
		{local: "/ds/../../etc/passwd"},
	}
	for _, tc := range cases {
		got, ok := remotePath("ds", tc.local)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("remotePath(%q) = (%q, %v)，想要 (%q, %v)", tc.local, got, ok, tc.want, tc.ok)
		}
	}
}

func TestLocalPrefixKeepsItsTrailingSlash(t *testing.T) {
	// 少一个斜杠的症状是远端的每个请求都打到 /ui/remoteds/… 上——404，而且在
	// 浏览器里完全看不出来是拼接的锅。
	if got := LocalPrefix("ds"); got != "/ui/remote/ds/" {
		t.Errorf("LocalPrefix = %q", got)
	}
}

// ---------- 门 ----------

func TestOnlyLoopbackHostsGetIn(t *testing.T) {
	// 这道门是这条 route 的**唯一**一道本地防线（远端那道查的是它自己收到的
	// Host，而那个头是我们保留下来的浏览器填的那个）。所以它的判据要在这里钉死。
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1:8899", true},
		{"127.0.0.1", true},
		{"[::1]:8899", true},
		{"localhost:8899", true},
		{"LOCALHOST", true},
		// 名字一律拒：DNS rebinding 靠的就是一个攻击者控制的域名解析到本机。
		{"evil.com:8899", false},
		{"newgate.local:8899", false},
		// 空 Host 拒（HTTP/1.0 的请求可以没有 Host，而那正是脚本工具的常见形状）。
		{"", false},
		// 局域网地址也拒——这条 route 背后是另一台机器，判据只该更严。
		{"10.0.50.11:8899", false},
		{"192.168.1.5:8899", false},
	}
	for _, tc := range cases {
		r := &http.Request{Host: tc.host}
		if got := loopbackHost(r); got != tc.want {
			t.Errorf("Host %q: loopbackHost = %v，想要 %v", tc.host, got, tc.want)
		}
	}
}

func TestPortOfSurvivesAnEmptyHost(t *testing.T) {
	if got := portOf(&http.Request{Host: "127.0.0.1:8899"}); got != "8899" {
		t.Errorf("portOf = %q", got)
	}
	if got := portOf(&http.Request{Host: "127.0.0.1"}); got != "" {
		t.Errorf("没有端口时该是空串，得到 %q", got)
	}
}
