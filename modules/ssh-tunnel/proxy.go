package sshtunnel

import (
	"bytes"
	"context"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Prefix 是这一节在共享端口上的落脚点。
//
// 挂在 web-dashboard 的 `/ui` 底下，因为**它就是那一类东西**：一个界面，只是人在
// 别处。porthub 按段匹配、最长前缀优先，所以 `/ui/remote/…` 落到这里、`/ui/…`
// 照旧落到 web-dashboard——两边都不用知道对方存在（这也是为什么这个前缀是
// "/ui/remote" 而不是 "/ui"：抢别人已经挂上的前缀，porthub 会当场报错）。
//
// 不用 `/a`、`/v1`（数据面）与 `/__newgate`（控制面）：porthub 会拒。
const Prefix = "/ui/remote"

// proxy 是共享端口上那条 route 的 handler（`Prefix` 已经由调用方剥掉）。
type proxy struct {
	pool *pool
	// loaded 每次请求**现读配置**：用户在浏览器里改一个 target、或者 CLI 敲一条
	// add，下一个请求就该按新的走。缓存它的话，「刚改完没生效」会变成一条只能靠
	// 重启绕过的怪现象。
	loaded func() Loaded

	mu         sync.Mutex
	transports map[string]transportEntry
}

type transportEntry struct {
	target Target
	tr     *http.Transport
}

func newProxy(p *pool, loaded func() Loaded) *proxy {
	return &proxy{pool: p, loaded: loaded, transports: map[string]transportEntry{}}
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 这道门与别的 route 贡献者各自复制一份（porthub 不提供）。这里用的是
	// arch-diagram 那份**严格版**（只认回环 IP 与 localhost），不是 web-dashboard
	// 那份（那份还放行局域网 IP，因为它自己有第二道写门）。
	//
	// 为什么这里更严：这条 route 背后是**另一台机器**，而本地这道门是唯一一道
	// ——远端那道查的是它自己收到的 Host，而从这里过去的 Host 是我们保留下来的
	// 浏览器填的那个（见 reverseFor 里那一段）。所以「谁能打开这个地址」这件事，
	// 全押在这一行上。
	if !loopbackHost(r) {
		writePlain(w, http.StatusForbidden, i18n.T(
			"this tunnel answers on loopback only; open it as http://127.0.0.1:{port}{prefix}/",
			i18n.A{"port": portOf(r), "prefix": Prefix}))
		return
	}

	if r.URL.Path == "" {
		// 正好是 `/ui/remote` 而没有尾斜杠：浏览器会把相对地址按当前文档解析，
		// 从 `/ui/remote` 出发 `api/snapshot` 会变成 `/ui/remote/api/snapshot`
		// ——那里把 api 当成了 target 的名字。302（不是 301）：永久重定向会被
		// 浏览器近乎永久地缓存，将来改前缀就纠正不回来了。
		http.Redirect(w, r, Prefix+"/", http.StatusFound)
		return
	}

	// StripPrefix 之后："…"（`/ui/remote/` 给的是 "/"）、或者 "/<id>"、"/<id>/…"。
	rest := strings.TrimPrefix(r.URL.Path, "/")
	id, hasTail := rest, false
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		id, hasTail = rest[:i], true
	}

	loaded := p.loaded()
	if id == "" {
		p.index(w, loaded)
		return
	}
	t, ok := find(loaded, id)
	if !ok {
		p.unknown(w, loaded, id)
		return
	}
	if !hasTail {
		// 只有**光秃秃一个 id**（`/ui/remote/ds`）要补斜杠。判据是「id 后面没有
		// 别的段」，**不是**「路径不以斜杠结尾」——后者对
		// `/ui/remote/ds/api/snapshot` 也成立，而补一个斜杠会把每一个 API 调用
		// 都变成一次 302（实测踩过：整条 route 上没有一个请求真的到过远端）。
		//
		// 302（不是 301）：永久重定向会被浏览器近乎永久地缓存。
		http.Redirect(w, r, Prefix+"/"+url.PathEscape(id)+"/", http.StatusFound)
		return
	}

	w.Header().Set("X-Newgate-Tunnel", id)
	p.reverseFor(t).ServeHTTP(w, r)
}

// reverseFor 组装这条 route 的 ReverseProxy。
//
// 每次现建一个 ReverseProxy 是廉价的（一个结构体）；真正要缓存的是它背后的
// **http.Transport**——连接池在它里面（见 transportFor）。
func (p *proxy) reverseFor(t Target) *httputil.ReverseProxy {
	local := LocalPrefix(t.ID)
	return &httputil.ReverseProxy{
		Transport: p.transportFor(t),
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			// Host 只是给 Transport 看的键：它不按这个拨号（DialContext 恒拨配置里
			// 那一对）。给一个稳定值，让连接池只有一格。
			pr.Out.URL.Host = "tunnel"
			pr.Out.URL.Path = remotePath(t.ID, pr.In.URL.Path)
			pr.Out.URL.RawPath = ""

			// **保留客户端的 Host**（Go 的默认行为是把它换成目标主机）。
			//
			// 这一行是整条 route 能不能工作的关键，理由在远端那一侧：远端 BFF 有
			// 一道 DNS-rebinding 门，它查的是**请求的 Host 头**——只放行 IP 字面量
			// 与 localhost。浏览器填的 Host 是它导航到的地址（127.0.0.1:8899，或者
			// 局域网 IP），两种都过。换成 "tunnel" 或任何域名，远端会**当场 403**，
			// 而本机这一侧一切正常——那种错会让人从改写一路查到远端 sshd，最后才
			// 发现是一行 header。
			pr.Out.Host = pr.In.Host

			// 摘掉 Accept-Encoding：**压缩过的正文没法改写**（见 rewrite.go）。
			//
			// 代价说清楚：这条路上少了一次压缩，而多走的是本地回环 + 一条 SSH 通道
			// （远端界面一个页面几百 KB）。不摘的后果是**每个页面都白屏**——远端会
			// gzip，我们改不了，浏览器拿到的是指向本机 /ui/assets 的地址。
			pr.Out.Header.Del("Accept-Encoding")
		},
		// -1 = 立刻刷。远端界面是轮询式的（几秒一次快照），不刷的话每次都要等
		// 缓冲区满或响应结束才到浏览器——交互会「一顿一顿」。
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			// Location 单独一处（见 rewriteLocation 的注释：不改的症状是悄悄跳到
			// 本机自己的界面上，而两者长得几乎一样）。
			if loc := resp.Header.Get("Location"); loc != "" {
				resp.Header.Set("Location", rewriteLocation(loc, local))
			}
			if !shouldRewrite(resp) {
				return nil
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				// 读到一半断了：把错误交回 ReverseProxy，它会走 ErrorHandler。
				// **不能**返回一份半截的正文——那在浏览器里是一个「看起来正常、
				// 其实被截断」的 HTML，比一个明确的错误糟得多。
				return err
			}
			next := rewritePrefix(body, local)
			resp.Body = io.NopCloser(bytes.NewReader(next))
			resp.ContentLength = int64(len(next))
			resp.Header.Set("Content-Length", strconv.Itoa(len(next)))
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			tunnelError(w, t, err)
		},
	}
}

// remotePath 把本机这条 route 后面的那一段，接到远端自己的绝对前缀上。
//
//	本机 /ui/remote/ds/            → 远端 /ui/
//	本机 /ui/remote/ds/api/snap    → 远端 /ui/api/snap
//
// 论证一次，因为它是整套改写的地基：浏览器看到的地址**本来就是**
// `/ui/remote/<id>/…`，也就是「远端地址前面多了一段」。所以剥掉那一段就是远端要
// 的东西——而正文里的绝对地址（`/ui/assets/…`）被我们改写成 `/ui/remote/<id>/…`
// 之后，浏览器发回来正好又落在这条规则上。**两边是同一件事的两面**，改了一边就
// 得改另一边。
func remotePath(id, local string) string {
	rel := strings.TrimPrefix(local, "/")
	rel = strings.TrimPrefix(rel, id)
	rel = strings.TrimPrefix(rel, "/")
	return RemoteUIPrefix + rel
}

// transportFor 拿某个 target 的 http.Transport。
//
// 一条 SSH 连接上开出来的通道由**这条 Transport 的连接池**管着：它按 HTTP 的
// keep-alive 复用，空闲 conn 超时（30s）后关掉——而关掉会释放借约，于是空闲回收
// 才有机会把 SSH 连接本身也断掉。两个超时是**配着来的**，改一个要看另一个。
func (p *proxy) transportFor(t Target) *http.Transport {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.transports[t.ID]; ok && e.target == t {
		return e.tr
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return p.dialTunnel(ctx, t)
		},
		// 不压缩：见 Rewrite 里摘 Accept-Encoding 那段。两处是**同一件事**的两面
		// （一个管发出去的 Accept-Encoding，一个管 Go 自己要不要解压），少一个就会
		// 出现「有时能改写、有时不能」。
		DisableCompression: true,
		// 通道的尽头就是远端那个明文端口，没有第二跳。
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       30 * time.Second,
		ExpectContinueTimeout: time.Second,
		// 不设 ResponseHeaderTimeout：远端那个端口如果真的卡住，用户按一下刷新就能
		// 重来；设一个短的超时会把「远端在忙」也一并杀掉，而那种失败看起来像断线。
	}
	if old, ok := p.transports[t.ID]; ok {
		// 地址变了：旧 Transport 里那些连接指向的已经不是这里了。关掉空闲的，
		// 在途的让它跑完（CloseIdleConnections 不碰在途的）。
		old.tr.CloseIdleConnections()
	}
	p.transports[t.ID] = transportEntry{target: t, tr: tr}
	return tr
}

// closeIdle 关掉所有 target 的空闲 HTTP 连接。
//
// 停机时必须先走这一步：那些**空闲连接还占着一次 SSH 借约**（见 leasedConn），
// 而 pool.Close 会等在途借约归零。不关的话，停机要一直等到连接池自己的
// IdleConnTimeout（30s）到期才走得动——看起来就是「停不干净」。
func (p *proxy) closeIdle() {
	p.mu.Lock()
	trs := make([]*http.Transport, 0, len(p.transports))
	for _, e := range p.transports {
		trs = append(trs, e.tr)
	}
	p.mu.Unlock()
	for _, tr := range trs {
		tr.CloseIdleConnections()
	}
}

// dialTunnel 为一条 HTTP 连接开一条 SSH 通道，并把借约绑在它的生命周期上。
func (p *proxy) dialTunnel(ctx context.Context, t Target) (net.Conn, error) {
	l, err := p.pool.Acquire(ctx, t)
	if err != nil {
		return nil, err
	}
	conn, err := l.Conn().Dial("tcp", t.RemoteAddr())
	if err != nil {
		l.Release()
		return nil, err
	}
	return &leasedConn{Conn: conn, lease: l}, nil
}

// leasedConn 让「HTTP 连接关掉」与「SSH 连接的借用还回去」是同一件事。
//
// 不绑的话，连接池里每一条空闲连接都会永远占着一次借用（refs 永不归零），于是
// SSH 连接**永远不会被空闲回收**——而这件事在功能上完全看不出来，只是 daemon 会
// 一直挂着一堆不再使用的 SSH 连接。
type leasedConn struct {
	net.Conn
	lease *lease
	once  sync.Once
}

func (c *leasedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.lease.Release() })
	return err
}

// ---------- 几个页面 ----------

// index 是 `/ui/remote/` 上那张索引。
//
// 它是**朴素 HTML**，不依赖前端产物：这条 route 必须在 web-dashboard 缺席的装配里
// 也成立（那时 `/ui` 根本不存在，而 tunnel 照常该能列出来）。这也让它「就是几个
// 链接」，没有一页白屏的余地。
func (p *proxy) index(w http.ResponseWriter, loaded Loaded) {
	var b strings.Builder
	b.WriteString("<!doctype html><meta charset=\"utf-8\"><title>")
	b.WriteString(html.EscapeString(i18n.T("remote interfaces", nil)))
	b.WriteString("</title><style>body{font:14px/1.6 ui-monospace,monospace;margin:2rem;max-width:48rem}" +
		"li{margin:.4rem 0}code{background:#8881;padding:.1rem .3rem}</style><h1>")
	b.WriteString(html.EscapeString(i18n.T("remote interfaces", nil)))
	b.WriteString("</h1>")

	if len(loaded.Targets) == 0 {
		b.WriteString("<p>" + html.EscapeString(i18n.T(
			"no targets yet — add one with `newgate tunnel add <id> --host <host>`", nil)) + "</p>")
	} else {
		b.WriteString("<ul>")
		for _, t := range loaded.Targets {
			link := Prefix + "/" + url.PathEscape(t.ID) + "/"
			b.WriteString("<li><a href=\"" + html.EscapeString(link) + "\">" +
				html.EscapeString(t.Label) + "</a> <code>" +
				html.EscapeString(t.User+"@"+t.DialAddr()+" → "+t.RemoteAddr()) + "</code>")
			if t.LocalPort != 0 {
				b.WriteString(" · <code>http://127.0.0.1:" + strconv.Itoa(t.LocalPort) + "/ui/</code>")
			}
			b.WriteString("</li>")
		}
		b.WriteString("</ul>")
	}
	for _, bad := range loaded.Rejected {
		b.WriteString("<p><b>" + html.EscapeString(i18n.T("ignored a broken target", nil)) + "</b> " +
			html.EscapeString(bad.Line) + "</p>")
	}
	b.WriteString("<p><small>" + html.EscapeString(i18n.T(
		"this page is a best-effort copy of the remote interface: paths inside it are rewritten. "+
			"if something looks broken, use the forwarding port instead — that one is byte-for-byte.",
		nil)) + "</small></p>")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, b.String())
}

// unknown 是「这个 id 不认识」。
//
// 刻意**列出认识的那些**：手敲地址是这条 route 最常见的用法（用户从终端复制一个
// 链接过来），而敲错一个字符时，一个只写着 404 的页面会让人以为整条 tunnel 坏了。
func (p *proxy) unknown(w http.ResponseWriter, loaded Loaded, id string) {
	ids := make([]string, 0, len(loaded.Targets))
	for _, t := range loaded.Targets {
		ids = append(ids, t.ID)
	}
	sort.Strings(ids)
	msg := i18n.T("there is no target called {id}", i18n.A{"id": id})
	if len(ids) == 0 {
		msg += " — " + i18n.T("none are configured yet", nil)
	} else {
		msg += " — " + i18n.T("configured: {list}", i18n.A{"list": strings.Join(ids, ", ")})
	}
	writePlain(w, http.StatusNotFound, msg)
}

// tunnelError 是「拨不过去」时给的那一页。
//
// 它必须**说清卡在哪**：SSH 连不上（网络 / 地址错）、主机密钥不认识、远端那个端口
// 没人听——这三种在用户那里的下一步动作完全不同。一个空的 502 会让人从「是不是我
// 的浏览器有问题」开始排查。
func tunnelError(w http.ResponseWriter, t Target, err error) {
	writePlain(w, http.StatusBadGateway, i18n.T("{id}: {err}", i18n.A{"id": t.ID, "err": err})+
		"\n\n"+i18n.T("run `newgate tunnel test {id}` for a step-by-step answer",
		i18n.A{"id": t.ID}))
}

// writePlain 是一段纯文本。
//
// 用 text/plain 而不是 HTML：内容是**错误原文**（里面有地址、有 sshd 的话、有路径），
// 包进 HTML 就得转义，而转义漏一处就是一个注入点——这些字符串有一半来自远端。
func writePlain(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, msg+"\n")
}

// ---------- 门与工具 ----------

// loopbackHost 报告这次请求的 Host 头**指向本机**。
//
// **不是** lib/httpx.IsLoopback：那个判的是出站拨号（「我要连的目标是不是本机」），
// 所以空串与 0.0.0.0 都算本机——拨号时那是合理的默认值。这里是入口判据，问的是
// 「这次请求是冲着哪个名字来的」，空 Host 必须**拒**（HTTP/1.0 的请求可以没有
// Host，而那正是脚本工具的常见形状）。两个问题，两个答案，不要合并。
//
// 与 modules/arch-diagram/handler.go 那份逐字相同是**有意的**：这条 route 把一台
// 远端机器的控制台带进来，比那份架构图敏感，判据只该更严不该更松。第三份副本是
// 一个真实的坏味道（该提到 lib/ 去），但那是内核仓库的改动，与这次不是一件事
// ——留这条注释，是为了让下一个读到它的人知道这已经是第三份了。
func loopbackHost(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func portOf(r *http.Request) string {
	if _, port, err := net.SplitHostPort(r.Host); err == nil {
		return port
	}
	return ""
}

func find(l Loaded, id string) (Target, bool) {
	for _, t := range l.Targets {
		if t.ID == id {
			return t, true
		}
	}
	return Target{}, false
}
