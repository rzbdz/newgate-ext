package archdiagram

import (
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/rzbdz/newgate/lib/buildinfo"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	"github.com/rzbdz/newgate-ext/tools/archmap"
)

// Handler 服务架构图：每次请求**现生成**一份。
//
// 为什么不缓存：这张图的价值全在「当下」——刚加的那条依赖、刚拆掉的那个模块，
// 刷新一下就该看见。而它的成本是 `go list` 那一趟（本机几百毫秒），是开发者点了
// 一下才付的，不是每个请求都付。
type Handler struct {
	svc *service
	// build 是重的（要跑 go list），串行化：几个人同时刷不会同时开几趟扫描，
	// 而扫描本身也不是并发的（它读同一棵树的同一批文件）。
	build sync.Mutex
}

func NewHandler(svc *service) *Handler { return &Handler{svc: svc} }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 只答本机：这张图把内部结构（模块名、包路径、谁依赖谁）整个摊开，而它挂在
	// 一个 loopback 端口上——挡得住外面的连接，挡不住别人网页上的一段脚本把
	// 自己的域名解析到 127.0.0.1（DNS rebinding）。**只看 Host**，理由与
	// web-dashboard 那道门一字不差（那边有两道，因为那边能写；这边只读，一道够）。
	if !loopbackHost(r) {
		http.Error(w, i18n.T("this diagram answers on loopback only; open it as http://127.0.0.1:{port}{prefix}/",
			i18n.A{"port": portOf(r), "prefix": Prefix}), http.StatusForbidden)
		return
	}
	// 只读，而且只认 GET/HEAD：别的动词没有意义，而放行它们等于给「随手 POST 一下
	// 试试」留一个会真的跑一遍 go list 的入口。
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, i18n.T("the diagram is read-only", nil), http.StatusMethodNotAllowed)
		return
	}

	h.build.Lock()
	// 名单是内核在装配完成后递来的（见 service.SetCatalog）。这张图画的是
	// **这个进程此刻装着什么**——不是「这份产物里有哪些规格书」，理由见那边。
	report := archmap.BuildCatalog("this-process",
		i18n.T("this process", nil),
		i18n.T("what is installed right now: {version}, assembled {took}", i18n.A{
			"version": buildinfo.Version(), "took": buildinfo.BuildTimeDisplay()}),
		h.svc.Components(), h.svc.root)
	h.build.Unlock()
	html, err := archmap.Render(report)
	if err != nil {
		http.Error(w, i18n.T("cannot render the architecture report: {err}",
			i18n.A{"err": err}), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// 不缓存：这张图新鲜才有意义（见上面为什么现生成）。
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(html))
}

// loopbackHost 报告这次请求的 Host 头**指向本机**。
//
// **不是** lib/httpx.IsLoopback：那个判的是出站拨号（「我要连的目标是不是本机」），
// 所以空串与 0.0.0.0 都算本机——拨号时那是合理的默认值。这里是入口判据，问的是
// 「这次请求是冲着哪个名字来的」，空 Host 必须**拒**（HTTP/1.0 的请求可以没有
// Host，而那正是脚本工具的常见形状）。两个问题，两个答案，不要合并。
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

// portOf 取这次请求连的端口（报错里要拼一个能用的地址出来）。
func portOf(r *http.Request) string {
	if _, port, err := net.SplitHostPort(r.Host); err == nil {
		return port
	}
	return ""
}
