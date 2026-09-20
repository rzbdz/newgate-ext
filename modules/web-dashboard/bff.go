package webdashboard

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
)

// Contract 是前端与 BFF 之间的契约版本。
//
// 前端**必须**先看它：不认识的版本号宁可白屏报一句「界面比后端旧/新」，也不要
// 拿旧前端去猜新形状——猜错的表现是「界面上少了一个开关」，没人会发现。
const Contract = 1

// Handler 是 BFF：一组 JSON 端点 + 静态资源。
//
// # 它不认识任何模块
//
// 这个文件里没有 config、没有 gateway、没有 profile 这个词——它只做三件事：
// 把账本里的**概念**端给前端、把前端的修改**转交**给贡献它的那个模块、发静态
// 资源。所以加一个模块的界面不用改这里（模块自己 Optional 依赖 view 能力，
// 在它自己的 Start 里注册概念，见 core/lib/view 的包注释）。
//
// 这不是洁癖：BFF 一旦自己读配置、自己写文件，它就必须知道文件的字段、目录的
// 布局、哪些字段不能碰——那些知识会长在这里，然后每加一个模块就再长一块。
// 第一版就是这么写的，然后被删掉了。
//
// 挂在 /ui 前缀下（porthub 挂载时 StripPrefix），所以这里的路径都以 / 开头。
type Handler struct {
	assets fs.FS
	views  *view.Registry
}

func NewHandler(assets fs.FS, views *view.Registry) *Handler {
	return &Handler{assets: assets, views: views}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 第一道门：这次请求**确实是冲着本机来的**（见 loopbackHost）。它挡的不是
	// 外面的人——那个由「只监听 loopback」挡——而是**别人网页上的一段脚本**。
	if !loopbackHost(r) {
		h.writeJSONStatus(w, http.StatusForbidden, map[string]any{
			"error": i18n.T("this interface answers on loopback only; reach it as http://127.0.0.1:{port} "+
				"(the host name in this request was {host})", i18n.A{"port": portOf(r), "host": r.Host}),
		})
		return
	}
	switch {
	case r.URL.Path == "/api/snapshot":
		doc, err := h.snapshot(r.URL.Query()["source"]...)
		if err != nil {
			// 贡献者自己产不出来（配置目录整个读不了这类）。说清楚是哪一步坏了，
			// 而不是回一份空快照——空快照在界面上表现成「一个模块都没有」。
			h.writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		h.writeJSONStatus(w, http.StatusOK, doc)
	case r.URL.Path == "/api/health":
		h.writeJSONStatus(w, http.StatusOK, map[string]any{"ok": true, "contract": Contract})
	case r.URL.Path == "/api/apply":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, i18n.T("saving uses POST", nil), http.StatusMethodNotAllowed)
			return
		}
		h.apply(w, r)
	default:
		h.static(w, r)
	}
}

// ---------- 这道界面只认本机 ----------

// loopbackHost 报告这次请求的 Host 头**指向本机**。
//
// 为什么必须查：这个界面能改配置、能拨运行期开关。只监听 loopback 挡得住外面的
// 连接，挡不住**别人网页上的一段脚本**——攻击者把自己控制的域名解析到 127.0.0.1
// （DNS rebinding），浏览器就认为那是同源，于是可以带任意 Content-Type、读任意
// 响应。浏览器唯一不会骗人的东西是它自己填的 Host：请求发给 evil.com，Host 就是
// evil.com，哪怕那个名字此刻指向 127.0.0.1。
//
// 允许的名字：127.0.0.1、::1、localhost（端口随意）。别的——包括这台机器在局域网
// 里的 IP、以及任何域名——一律拒绝。**代价说清楚**：在 /etc/hosts 里给自己起个
// newgate.local 的人会被挡在外面，报错里写明了该用什么地址打开。
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

// portOf 取这次请求连的端口。只为了报错里能拼出一个**能用的** URL——「请用
// 127.0.0.1 打开」而不说端口，用户还得自己猜。
func portOf(r *http.Request) string {
	if _, port, err := net.SplitHostPort(r.Host); err == nil {
		return port
	}
	return ""
}

// ---------- 快照 ----------

type conceptDoc struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Source 是谁贡献的：前端按它分栏、也用它把概念跟 sectionDoc 对上。
	// **机器标记**（模块名），不翻译——界面那一栏叫什么由 sectionDoc 说。
	Source string `json:"source"`
	// Writable 为 false = 这个概念只读（贡献者没给 Apply）。
	Writable bool `json:"writable"`
	// Live 为 true = 这一面的数据会自己变，界面该把它所在的源一起放进那几秒
	// 一次的刷新里（见 lib/view 的 Concept.Live）。**由贡献者声明**：谁的东西谁
	// 知道读一次贵不贵，界面无从推断。
	Live bool `json:"live,omitempty"`
	Data any  `json:"data"`
	// Error 非空 = 这个概念**此刻读不出来**（文件被删了、JSON 坏了）。卡片照
	// 常出现、写着原因，而不是从列表里消失——消失了用户会以为它不存在。
	Error string `json:"error,omitempty"`
}

type snapshotDoc struct {
	Contract    int    `json:"contract"`
	GeneratedAt string `json:"generated_at"`
	// Lang 是这个进程**解析出来**的语言（NEWGATE_LANG > 配置 > LC_ALL > …，见
	// modules/locale）。前端拿它挑自己那份界面文案——概念标题已经是后端翻译好的，
	// 而按钮、提示这些界面骨架上的字属于前端自己。
	//
	// 为什么由后端给而不是前端猜：语言的解析规则只有一处实现（那个模块），前端
	// 再猜一遍（navigator.language？）就等于有了第二处，两处不一致时页面会中英
	// 混排，而那种错没人会报成 bug。
	Lang string `json:"lang"`
	// Sections 是侧栏的栏目表：**登记过的全部来源**，包括此刻一条概念都产不出来
	// 的那几位（配置目录整个读不了、omo 还没接管过）。从概念里反推来源会让那些
	// 栏目消失，而用户看到的是「这个模块不存在」——然后去别处找。
	//
	// 名字由各模块在登记时报（`view.Title`），BFF 只搬——它不认识任何模块，
	// 也不该认识。
	Sections []sectionDoc `json:"sections"`
	Concepts []conceptDoc `json:"concepts"`
}

// sectionDoc 是栏目表的一行：机器标记 + 给人看的名字。
type sectionDoc struct {
	Source string `json:"source"`
	Title  string `json:"title"`
}

// snapshot 问一遍贡献者要这一刻的样子。
//
// 「问」这件事本身是有代价的（有人要重新读盘、重新算指标），所以它只发生在这里
// ——有人真的打开界面/点刷新的时候。装配期一次都不问。
//
// `?source=` 是给**自动刷新**用的（可以重复给）：界面每隔几秒刷的是计数器与日志，
// 而那两位的产出是便宜的；配置那一位要重读并重新解析每一份 profile、每一个源
// 文件，不该被顺带叫醒。不带 = 全部（首次加载要的就是全部）。
func (h *Handler) snapshot(sources ...string) (snapshotDoc, error) {
	doc := snapshotDoc{
		Contract:    Contract,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Lang:        i18n.Current(),
	}
	// 栏目表**每次都带**，即使这是一次按来源过滤的刷新：它不调用任何产出函数
	// （只是把登记的栏目名取出来），所以不花钱；而侧栏的徽标数与「这个模块还在
	// 不在」正是那几秒一次的刷新最该跟上的东西。
	for _, s := range h.views.Sections() {
		doc.Sections = append(doc.Sections, sectionDoc{Source: s.Source, Title: s.Title})
	}
	if doc.Sections == nil {
		doc.Sections = []sectionDoc{}
	}
	concepts, err := h.views.Snapshot(sources...)
	if err != nil {
		return doc, err
	}
	for _, c := range concepts {
		doc.Concepts = append(doc.Concepts, conceptDoc{
			ID: c.ID, Kind: c.Kind, Title: c.Title, Source: c.Source,
			Writable: c.Apply != nil, Live: c.Live, Data: c.Data, Error: c.Broken,
		})
	}
	if doc.Concepts == nil {
		doc.Concepts = []conceptDoc{}
	}
	return doc, nil
}

// ---------- 保存 ----------

type applyRequest struct {
	// ID 是概念的稳定身份（快照里那个）。
	ID string `json:"id"`
	// Base 是前端加载时拿到的基线。空串表示「加载时它还不存在」。
	Base string `json:"base"`
	// Edit 的形状**由这个概念自己定义**：BFF 不解析它，原样转交。
	Edit json.RawMessage `json:"edit"`
}

type applyResponse struct {
	OK bool `json:"ok"`
	// Base 是写完之后的**新基线**：前端续着改不用刷新页面。
	Base     string       `json:"base,omitempty"`
	Conflict *conflictDoc `json:"conflict,omitempty"`
	Error    string       `json:"error,omitempty"`
}

type conflictDoc struct {
	Concept string `json:"concept"`
	Path    string `json:"path"`
	Base    string `json:"base"`
	Current string `json:"current"`
	Yours   string `json:"yours"`
	Theirs  string `json:"theirs"`
}

func (h *Handler) apply(w http.ResponseWriter, r *http.Request) {
	// 第二道门：**写操作必须声明自己是 JSON**。
	//
	// 这不是格式洁癖，是把「别的网页替我按保存」挡在门外：跨站的表单提交、以及
	// no-cors 的 fetch 都发不出 `Content-Type: application/json`（那会触发预检，
	// 而我们不回任何 CORS 头），但它们**能**发一个 body 长得像 JSON 的 text/plain
	// 请求——而解码器并不看 Content-Type。所以这一条是那条路唯一的墙。
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		h.writeJSONStatus(w, http.StatusUnsupportedMediaType, applyResponse{
			Error: i18n.T("saving needs Content-Type: application/json (got {ct})", i18n.A{"ct": ct})})
		return
	}
	var req applyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		h.writeJSONStatus(w, http.StatusBadRequest, applyResponse{Error: i18n.T("the request body is not valid JSON: {err}", i18n.A{"err": err})})
		return
	}
	c, err := h.views.Get(req.ID)
	if errors.Is(err, view.ErrUnknownConcept) {
		// 404 而不是静默成功：前端的 ID 是从快照里来的，对不上只可能是**它手里的
		// 那份快照已经过期**（比如模块刚被关掉）。说清楚比装作写好了强。
		h.writeJSONStatus(w, http.StatusNotFound, applyResponse{
			Error: i18n.T("no such concept: {id} — the page is probably stale, reload it", i18n.A{"id": req.ID})})
		return
	}
	if err != nil {
		h.writeJSONStatus(w, http.StatusInternalServerError, applyResponse{Error: err.Error()})
		return
	}
	if c.Apply == nil {
		h.writeJSONStatus(w, http.StatusConflict, applyResponse{
			Error: i18n.T("{title} is read-only", i18n.A{"title": c.Title})})
		return
	}
	newBase, err := c.Apply(req.Edit, req.Base)
	if err != nil {
		var conflict *view.Conflict
		if errors.As(err, &conflict) {
			h.writeJSONStatus(w, http.StatusConflict, applyResponse{Conflict: &conflictDoc{
				Concept: conflict.Concept, Path: conflict.Path, Base: conflict.Base,
				Current: conflict.Current, Yours: conflict.Yours, Theirs: conflict.Theirs,
			}})
			return
		}
		// 贡献者的报错原样带出去：那是**它**对自己数据的说法（哪个字段读不出来、
		// 哪一行不合法），BFF 转述只会转丢信息。
		h.writeJSONStatus(w, http.StatusBadRequest, applyResponse{Error: err.Error()})
		return
	}
	h.writeJSONStatus(w, http.StatusOK, applyResponse{OK: true, Base: newBase})
}

// ---------- 静态资源 ----------

// static 从嵌进二进制的产物里发文件；找不到的路径落回 index.html（前端自己
// 路由）。这样前端加一个页面不需要在后端登记路径。
func (h *Handler) static(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	if b, err := fs.ReadFile(h.assets, p); err == nil {
		w.Header().Set("Content-Type", contentType(p))
		_, _ = w.Write(b)
		return
	}
	// SPA 兜底：只有 GET 且看起来不是资源请求时才回 index.html，否则一个拼错的
	// .js 会静默变成本页 HTML（浏览器报的错会指向别处）。
	if r.Method == http.MethodGet && !strings.Contains(filepath.Base(p), ".") {
		if b, err := fs.ReadFile(h.assets, "index.html"); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(b)
			return
		}
	}
	http.NotFound(w, r)
}

func contentType(p string) string {
	switch {
	case strings.HasSuffix(p, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(p, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(p, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(p, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(p, ".json"):
		return "application/json; charset=utf-8"
	case strings.HasSuffix(p, ".woff2"):
		return "font/woff2"
	}
	return "application/octet-stream"
}

func (h *Handler) writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
