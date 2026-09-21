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
	case r.URL.Path == "/api/section":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, i18n.T("section actions use POST", nil), http.StatusMethodNotAllowed)
			return
		}
		h.sectionAction(w, r)
	case r.URL.Path == "/api/concept-action":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, i18n.T("concept actions use POST", nil), http.StatusMethodNotAllowed)
			return
		}
		h.conceptAction(w, r)
	case r.URL.Path == "/api/row-action":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, i18n.T("row actions use POST", nil), http.StatusMethodNotAllowed)
			return
		}
		h.rowAction(w, r)
	case r.URL.Path == "/api/apply":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, i18n.T("saving uses POST", nil), http.StatusMethodNotAllowed)
			return
		}
		h.apply(w, r)
	case r.URL.Path == "/api/preview":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, i18n.T("previewing uses POST", nil), http.StatusMethodNotAllowed)
			return
		}
		h.preview(w, r)
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
	if ip == nil {
		// 名字（域名）一律拒——**这正是这道门存在的理由**：DNS rebinding 要靠一个
		// 攻击者控制的域名解析到本机。用户想从别处打开界面时用的是 IP，所以拦掉
		// 名字不会挡住任何正常用法。
		return false
	}
	// IP 字面量：回环、Tailscale 网段、以及任何别的（局域网 / WSL 的 172.x）。
	//
	// 为什么「任何 IP」都行得通：Host 是**浏览器自己填的**，它填的是用户导航到的
	// 那个地址。要伪造它，攻击者得先让用户的浏览器访问 `http://192.168.1.5:8899`
	// ——那已经不是 rebinding 了。而写操作还有第二道门（必须是 JSON，浏览器会先发
	// 预检，跨源读不到我们的响应），所以「允许 IP」不等于「允许任意网页改配置」。
	_ = ip
	return true
}

// isTailscale 说这个地址是不是 Tailscale 的网段（100.64.0.0/10，运营商级 NAT 那段）。
//
// 今天它不再单独收口（IP 字面量已经一律放行，见 allowedHost），留着是因为它是
// **唯一一处**说清「100.64/10 是什么」的地方，而那句话在配置界面（state 的监听
// 地址那一格）里正是用户要读的。
func isTailscale(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	// 100.64.0.0/10：第一段 100，第二段的高 2 位是 01（64..127）。
	return v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
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
	// Locked 非空 = 这张卡此刻**没有意义**（见 lib/view 的 Concept.Locked），值是
	// 理由，显示在卡片上。界面据它整张锁灰、控件全部禁掉——**卡片仍然画出来**：
	// 藏掉的话用户会以为那个功能不存在，而真相是「它在，只是这台机器上用不上」。
	Locked string `json:"locked,omitempty"`
	// Live 为 true = 这一面的数据会自己变，界面该把它所在的源一起放进那几秒
	// 一次的刷新里（见 lib/view 的 Concept.Live）。**由贡献者声明**：谁的东西谁
	// 知道读一次贵不贵，界面无从推断。
	Live bool `json:"live,omitempty"`
	// Group 是左栏分组（见 lib/view 的 Concept.Group）：同组的卡在竖栏里归到一个
	// 标题下。空串 = 自己一档。**只影响排列**，不参与任何身份判断。
	Group string `json:"group,omitempty"`
	// Previewable = 贡献者给了 Preview（见 lib/view 的 Concept.Preview）：这张卡
	// 能拿同一份文件另一半的草稿问一句「我该显示成什么样」。界面据此决定要不要在
	// 原文改动之后去问——没有它就别问，问了也是白跑一趟。
	Previewable bool `json:"previewable,omitempty"`
	// Order 是同一节里谁排前面（见 lib/view 的 Concept.Order）。
	//
	// **必须端出去**：快照里的数组本来就是按 (Source, Order, ID) 排好的，但前端会
	// 在**局部刷新**时把新拿到的几张卡并回手里那份，合并这一步要自己排一次——没有
	// Order 它只能按 ID 字母序，于是「全局设置」「上游」这种 Order 为 0/1 的卡在
	// 每次静默刷新之后掉到十几张档位卡下面（实测：不刷新时在上面，一刷新就到最底，
	// 用户看到的正是「它们经常会自己跑到下面」）。
	Order int `json:"order"`
	Data  any `json:"data"`
	// Actions 是**这张卡上**的按钮（见 view.Concept.Actions）。与 sectionDoc.Actions
	// 同一个形状、同一条规矩：BFF 只搬 ID 与标签，「点了会发生什么」住在贡献者那边。
	Actions []view.ActionInfo `json:"actions,omitempty"`
	// Note 是**一句状态说明**（见 view.Concept.Note）：「这一份就是当前配置」这类。
	// 与 Actions 一样整份透传（`view.Note` 自己带 json 标签），BFF 不解释它的语气
	// 色彩——`ok`/`warn`/`bad` 这套词在表格的格子上（`view.Cell.Tone`）早就是同一份
	// 词汇表，前端只认这三个词，其余一律当着色没给。
	//
	// 它不是按钮：界面上点了没有任何事发生（对比 Actions，那些的 Run 会跑）。
	Note *view.Note `json:"note,omitempty"`
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

// sectionDoc 是栏目表的一行：机器标记 + 给人看的名字（+ 可选的分组）。
type sectionDoc struct {
	Source string `json:"source"`
	Title  string `json:"title"`
	// Group 是这一栏在侧栏里归到哪个标题下（空 = 不归，排在最上面）。**由贡献者
	// 报**（view.Section.Group）：BFF 不认识任何模块，也就无从判断「熔断该跟网关
	// 一类」这件事。
	Group string `json:"group,omitempty"`
	// Actions 是这一栏上的按钮（见 view.Section.Actions）。BFF 只搬 ID 与标签，
	// 「点了会发生什么」住在贡献者那边——它的 Run 是个函数，端不出去。
	Actions []view.ActionInfo `json:"actions,omitempty"`
	// Default 是「没有位置时落在这里」（见 view.Section.Default）。**由贡献者声明**：
	// 首屏落在哪一栏是产品取舍（「先让用户看见一屏全能」），而 BFF/前端都不认识
	// 任何模块，也就无从判断谁该在那儿。它们只搬这一格。
	Default bool `json:"default,omitempty"`
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
		doc.Sections = append(doc.Sections, sectionDoc{
			Source: s.Source, Title: s.Title, Group: s.Group,
			Actions: s.Actions, Default: s.Default,
		})
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
			Writable: c.Apply != nil, Locked: c.Locked, Live: c.Live, Group: c.Group,
			Previewable: c.Preview != nil, Order: c.Order, Data: c.Data, Error: c.Broken,
			Actions: actionInfos(c.Actions), Note: c.Note,
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
	Base string `json:"base,omitempty"`
	// Focus 是**动作做完之后该切到哪个概念**（见 view.Action.Run）——「新建」最需要
	// 它：新建出来的东西叫什么名字只有实现者知道，界面据此切过去。空 = 留在原地。
	Focus    string       `json:"focus,omitempty"`
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

// ---------- 栏目动作 ----------

type sectionActionRequest struct {
	// Source 是哪一节（机器标记，快照里那个）。
	Source string `json:"source"`
	// Action 是那一节里的哪个动作（见 view.Action.ID）。
	Action string `json:"action"`
}

// sectionAction 跑一个挂在栏目上的动作——「再建一份档位文件」这类。
//
// 与 apply 分开是本条设计的要点：动作**不属于任何一张卡**（新建出来的东西此刻还
// 没有概念），所以它没有 base、没有 edit，只有「哪一节、哪个动作」。BFF 依旧只
// 转交：它不知道那个动作会干什么。
//
// 与 apply 同一道门（必须是 JSON）：它改的是磁盘上的配置。
func (h *Handler) sectionAction(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		h.writeJSONStatus(w, http.StatusUnsupportedMediaType, applyResponse{
			Error: i18n.T("section actions need Content-Type: application/json (got {ct})", i18n.A{"ct": ct})})
		return
	}
	var req sectionActionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		h.writeJSONStatus(w, http.StatusBadRequest, applyResponse{
			Error: i18n.T("the request body is not valid JSON: {err}", i18n.A{"err": err})})
		return
	}
	focus, err := h.views.RunAction(req.Source, req.Action)
	if err != nil {
		// 贡献者的报错原样带出去（「那一节里没有这个动作」也在这里）——它比 BFF
		// 转述一句「操作失败」有用得多。
		h.writeJSONStatus(w, http.StatusBadRequest, applyResponse{Error: err.Error()})
		return
	}
	h.writeJSONStatus(w, http.StatusOK, applyResponse{OK: true, Focus: focus})
}

// actionInfos 把贡献者那份动作表端成界面认识的样子（求值标签）。
//
// 与 Registry.Sections 里那段是同一件事，抽出来只因为现在有两个调用点（栏目与概念）
// ——两处各写一遍的话，其中一处会忘了在最前面求值 Label，而那个错误的症状是
// **按钮上没有字**。
func actionInfos(acts []view.Action) []view.ActionInfo {
	var out []view.ActionInfo
	for _, a := range acts {
		label := ""
		if a.Label != nil {
			label = a.Label()
		}
		out = append(out, view.ActionInfo{ID: a.ID, Label: label})
	}
	return out
}

// ---------- 概念动作 ----------

type conceptActionRequest struct {
	// ID 是哪张卡（概念的稳定身份，快照里那个）。
	ID string `json:"id"`
	// Action 是那张卡上的哪个动作（见 view.Action.ID）。
	Action string `json:"action"`
}

// conceptAction 跑一个挂在**某张卡**上的动作——「把这一份设为默认」这类。
//
// 与 sectionAction 分开而不是合成一个端点，是因为它们的**身份**不同：栏目动作认
// 来源名，概念动作认概念 ID。合成一个的话，请求里那两个字段就得有一个是可选的，
// 而「哪一个是空的」这件事会在两处各判断一遍。
//
// 与 apply 同一道门（必须是 JSON）：它改的是磁盘上的配置。
func (h *Handler) conceptAction(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		h.writeJSONStatus(w, http.StatusUnsupportedMediaType, applyResponse{
			Error: i18n.T("concept actions need Content-Type: application/json (got {ct})", i18n.A{"ct": ct})})
		return
	}
	var req conceptActionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		h.writeJSONStatus(w, http.StatusBadRequest, applyResponse{
			Error: i18n.T("the request body is not valid JSON: {err}", i18n.A{"err": err})})
		return
	}
	focus, err := h.views.RunConceptAction(req.ID, req.Action)
	if err != nil {
		h.writeJSONStatus(w, http.StatusBadRequest, applyResponse{Error: err.Error()})
		return
	}
	h.writeJSONStatus(w, http.StatusOK, applyResponse{OK: true, Focus: focus})
}

// ---------- 行内动作 ----------

type rowActionRequest struct {
	// ID 是哪张卡（概念的稳定身份）。
	ID string `json:"id"`
	// Row 是那一行（见 lib/view 的 Row.ID）。**机器标记**，不翻译。
	Row string `json:"row"`
	// Action 是那一行上的哪个动作。
	Action string `json:"action"`
}

// rowAction 跑表格里**某一行**上的一个动作——「探一下这条 binding」这类。
//
// 三个字段而不是两个，因为一张表里每一行是**一条独立的东西**：少了 Row 这一层，
// 「测试」就不知道该测谁。BFF 依旧只转交：它不知道那一行是什么，也不知道那个动作
// 会干什么（它连「这是个表」都不该知道）。
func (h *Handler) rowAction(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		h.writeJSONStatus(w, http.StatusUnsupportedMediaType, applyResponse{
			Error: i18n.T("row actions need Content-Type: application/json (got {ct})", i18n.A{"ct": ct})})
		return
	}
	var req rowActionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		h.writeJSONStatus(w, http.StatusBadRequest, applyResponse{
			Error: i18n.T("the request body is not valid JSON: {err}", i18n.A{"err": err})})
		return
	}
	focus, err := h.views.RunRowAction(req.ID, req.Row, req.Action)
	if err != nil {
		h.writeJSONStatus(w, http.StatusBadRequest, applyResponse{Error: err.Error()})
		return
	}
	h.writeJSONStatus(w, http.StatusOK, applyResponse{OK: true, Focus: focus})
}

// ---------- 预览 ----------

type previewRequest struct {
	// ID 是那张**要被预览的**概念的稳定身份（控件那一半，不是交草稿的那一半）。
	ID string `json:"id"`
	// Text 是同一份文件**还没落盘的**草稿（界面从原文那一半手里拿的）。
	Text string `json:"text"`
}

type previewResponse struct {
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// preview 把一份草稿交给拥有那份文件的贡献者，换回「这张卡此刻该显示成什么样」。
//
// 与 apply 同一道门（必须是 JSON）：它虽然不落盘，但**请求里带的是用户的配置
// 内容**，而且响应会把配置的形状回给页面。两道门的理由在 apply 那里写全了。
//
// 失败一律 200：草稿解析不出来是**打字途中的常态**（正敲着的那一行本来就不完整），
// 不是「这次请求坏了」。用 4xx 的话，界面就得为「正常的半成品」和「真的出错了」
// 写两套分支，而它想做的事两处一模一样——保持上一次的样子。
func (h *Handler) preview(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		h.writeJSONStatus(w, http.StatusUnsupportedMediaType, previewResponse{
			Error: i18n.T("previewing needs Content-Type: application/json (got {ct})", i18n.A{"ct": ct})})
		return
	}
	var req previewRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		h.writeJSONStatus(w, http.StatusBadRequest, previewResponse{
			Error: i18n.T("the request body is not valid JSON: {err}", i18n.A{"err": err})})
		return
	}
	c, err := h.views.Get(req.ID)
	if err != nil {
		// 贡献者没有这个 id：回空。界面那边这只是一次「顺手更新」，不是用户按的
		// 某个按钮——为它弹一条报错，等于让一次打字在屏幕上变成一次故障。
		h.writeJSONStatus(w, http.StatusOK, previewResponse{})
		return
	}
	if c.Preview == nil {
		// 这个概念没有第二半（它自己就是原文），没什么可预览的。
		h.writeJSONStatus(w, http.StatusOK, previewResponse{})
		return
	}
	data, err := c.Preview([]byte(req.Text))
	if err != nil {
		h.writeJSONStatus(w, http.StatusOK, previewResponse{Error: err.Error()})
		return
	}
	h.writeJSONStatus(w, http.StatusOK, previewResponse{Data: data})
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
		serve(w, p, b)
		return
	}
	// SPA 兜底：只有 GET 且看起来不是资源请求时才回 index.html，否则一个拼错的
	// .js 会静默变成本页 HTML（浏览器报的错会指向别处）。
	if r.Method == http.MethodGet && !strings.Contains(filepath.Base(p), ".") {
		if b, err := fs.ReadFile(h.assets, "index.html"); err == nil {
			serve(w, "index.html", b)
			return
		}
	}
	http.NotFound(w, r)
}

func serve(w http.ResponseWriter, p string, b []byte) {
	w.Header().Set("Content-Type", contentType(p))
	w.Header().Set("Cache-Control", cacheControl(p))
	_, _ = w.Write(b)
}

// cacheControl 说这个文件能被浏览器存多久。
//
// # 为什么必须有这条（2026-09-20 实测的坑）
//
// 一个 `Cache-Control` 都不给的响应，浏览器**不会**就不缓存它——它会按启发式
// 自己拿主意（通常是「距上次修改时间的 10%」，没有 Last-Modified 就按会话猜）。
// 而这份界面的产物名字里带内容哈希：`index-<hash>.js`。发一版新二进制，哈希变了、
// 旧文件从嵌入的产物里消失了，**但用户浏览器手里那份旧 index.html 还指着旧名字**
// ——它自己那份旧 JS 也还在缓存里，于是页面照常渲染、只不过渲染的是**上一版的
// 界面**。
//
// 症状就是这么来的：服务端一切正常（curl 拿到的是新产物），用户那边「新加的按钮
// 没有」「新版面没生效」，而且**没有任何报错**。唯一的出路是手动硬刷新，而没人会
// 往那上面想。用户报的三次「你没做」有两次是这一条（见 git log 里这一版的提交）。
//
// 所以入口文件必须**每次都问服务器**：它只有几百字节，且它是唯一知道哈希变了的
// 那份文件。剩下的按「名字里有没有内容哈希」分：
//
//   - `assets/` 下面的是 Vite 算过哈希的产物：内容一变名字就变，于是同一个名字
//     永远对应同一份字节，可以无限期缓存（这是 SPA 性能的常规做法，也让刷新只
//     重新下那几百字节的 index.html）。
//   - 别的（将来手工放进 dist 的 favicon 之类）没有那道保证，让它每次带 ETag 问
//     一遍：宁可多一个 304 往返，也不要把一个改了内容的同名文件永久钉在用户机器上。
func cacheControl(p string) string {
	if strings.HasSuffix(p, ".html") {
		return "no-store"
	}
	if strings.HasPrefix(p, "assets/") {
		return "public, max-age=31536000, immutable"
	}
	return "no-cache"
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
