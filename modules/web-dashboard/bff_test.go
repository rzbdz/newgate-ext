package webdashboard

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/rzbdz/newgate/lib/view"
)

// BFF 的职责只有三条：把概念端出去、把修改**转交**给贡献它的那位、发静态资源。
// 这几条测试锁的就是「它没有第四条」——多加一条（比如自己去读配置）就意味着
// 它开始认识某个模块，而那正是这一版删掉的东西。

func testAssets() fs.FS {
	return fstest.MapFS{
		"index.html":              {Data: []byte("<html>app</html>")},
		"app.js":                  {Data: []byte("console.log(1)")},
		"assets/index-deadbee.js": {Data: []byte("console.log(2)")},
	}
}

func newHandler() *Handler {
	return NewHandler(testAssets(), view.NewRegistry())
}

// contribute 登记一个贡献者：产出函数每次被调用都原样交出这批概念。
func contribute(t *testing.T, h *Handler, source string, concepts ...view.Concept) {
	t.Helper()
	if _, err := h.views.Register(source, view.Title(func() string { return source }),
		func() ([]view.Concept, error) { return concepts, nil }); err != nil {
		t.Fatal(err)
	}
}

// TestLiveSurvivesTheWire：Live 是贡献者在 Go 那边声明的，而界面靠 JSON 里那个
// 字段决定「哪几个源要每几秒重问一次」（见前端 App.svelte 的 liveSources）。
//
// 这一跳断了**不会报错**：字段全丢 → 一个源都不刷 → 页面安静地停在打开那一刻，
// 而「自动刷新」那个复选框看起来一切正常。所以它值得一条断言，而不是等谁在
// 浏览器里发现计数器不动了。
func TestLiveSurvivesTheWire(t *testing.T) {
	h := newHandler()
	contribute(t, h, "somewhere",
		view.Concept{ID: "live.one", Kind: view.KindSeries, Title: "Counters", Live: true},
		view.Concept{ID: "static.one", Kind: view.KindTable, Title: "Modules"},
	)

	rec := get(t, h, "/api/snapshot")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot 该 200，实际 %d", rec.Code)
	}
	var doc snapshotDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	byID := map[string]conceptDoc{}
	for _, c := range doc.Concepts {
		byID[c.ID] = c
	}
	if !byID["live.one"].Live {
		t.Error("声明了 Live 的概念必须把它端到前端——丢了它，自动刷新谁都不问")
	}
	if byID["static.one"].Live {
		t.Error("没声明的概念不该被当成会变的：那会把整个源拖进几秒一次的轮询")
	}

	// 同一个源的**其他**概念也一起被端出去：界面的刷新粒度是源，不是这一条
	// （见 core/lib/view 里 Concept.Live 的注释）。
	if byID["live.one"].Source != byID["static.one"].Source {
		t.Fatal("这条用例的前提是两个概念同源")
	}
}

// TestSectionsSurviveTheWire 是侧栏那一栏的**名字**从模块走到浏览器的整条路。
//
// 名字由各模块在登记时报（`view.Title`），BFF 一个模块都不认识、只搬。这条断了
// **不会报错**：前端的兜底是把来源名当标题画出来（那是刻意的兼容那条路），于是
// 侧栏里出现的是 config / plugin-manager / opencode-omo 这种机器标记，看着只像
// 「没翻」，不像一条 bug。所以它值得一条断言，而不是等谁在浏览器里看出来。
func TestSectionsSurviveTheWire(t *testing.T) {
	h := newHandler()
	// 一个**此刻产不出任何概念**的源：配置目录整个读不了、omo 还没接管过——那一栏
	// 仍然要在侧栏里有一个位置（它消失了，用户会以为那个模块没装）。
	if _, err := h.views.Register("quiet", view.Title(func() string { return "Quiet module" }),
		func() ([]view.Concept, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	contribute(t, h, "config", view.Concept{ID: "config.state", Kind: view.KindToggles, Title: "Global settings"})

	rec := get(t, h, "/api/snapshot")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot 该 200，实际 %d", rec.Code)
	}
	var doc snapshotDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	titles := map[string]string{}
	for _, s := range doc.Sections {
		titles[s.Source] = s.Title
	}
	if len(doc.Sections) != 2 {
		t.Fatalf("两个登记过的源都该在栏目表里，实际 %+v", doc.Sections)
	}
	if titles["quiet"] != "Quiet module" {
		t.Errorf("没产出概念的源也该带着名字出现: %+v", doc.Sections)
	}
	// 标题来自登记时报的那一句，不是来源名（来源名是机器标记：它也要端出去，
	// 但那是给前端对号用的，不是给人看的）。
	if titles["config"] != "config" {
		t.Errorf("这条用例的前提是测试里那个源名与标题同形: %+v", doc.Sections)
	}
	if got := doc.Sections[0].Source; got != "config" {
		t.Errorf("栏目表按 source 排序（界面顺序要跨重启稳定），第一个该是 config，实际 %q", got)
	}
}

// TestFilteredSnapshotStillCarriesEverySection：按来源过滤的那次快照（界面每几秒
// 刷一次）必须**照旧带上完整的栏目表**。
//
// 反过来的做法（只回被点名那几位的栏目）看起来更"一致"，实际是把侧栏变成一块
// 会缩水的板子：刷一次少一栏、再刷回来，而那几秒一次、人未必盯着。栏目表不调用
// 任何产出函数，带上它一分钱不花。
func TestFilteredSnapshotStillCarriesEverySection(t *testing.T) {
	h := newHandler()
	var cheap, expensive atomic.Int64
	contributeCount(t, h, "gateway", &cheap)
	contributeCount(t, h, "config", &expensive)

	rec := get(t, h, "/api/snapshot?source=gateway")
	if rec.Code != http.StatusOK {
		t.Fatalf("按来源过滤该 200，实际 %d", rec.Code)
	}
	var doc snapshotDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Concepts) != 1 {
		t.Fatalf("概念该只剩被点名那位，实际 %d", len(doc.Concepts))
	}
	if len(doc.Sections) != 2 {
		t.Errorf("栏目表该是完整的（侧栏不该跟着刷新缩水），实际 %+v", doc.Sections)
	}
}

// loopback 是测试里默认的 Host。
//
// 必须显式设：httptest.NewRequest 默认填 `example.com`，而 BFF 现在只认本机的
// Host（见 loopbackHost）——不设的话每条用例都会撞在 403 上，看起来像功能坏了。
const loopback = "127.0.0.1:8899"

// raw 是「按我给的 Host / Content-Type 发一次」——专门给那两道门的用例用
// （别的用例走 get/post，它们的 Host 与 Content-Type 都是常规值）。
func raw(t *testing.T, h http.Handler, method, path, body, host, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if host != "" {
		req.Host = host
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = loopback
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func post(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Host = loopback
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestSnapshotCarriesWhateverTheLedgerHolds：BFF 不认识任何模块，它只搬账本。
func TestSnapshotCarriesWhateverTheLedgerHolds(t *testing.T) {
	h := newHandler()
	contribute(t, h, "somewhere",
		view.Concept{ID: "something.x", Kind: view.KindSeries, Title: "Counters",
			Data: map[string]any{"n": 3}},
		view.Concept{ID: "read.only", Kind: view.KindCode, Title: "A file",
			Data: map[string]any{"path": "x.json"}},
	)

	rec := get(t, h, "/api/snapshot")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot 该 200，实际 %d", rec.Code)
	}
	var doc snapshotDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Contract != Contract {
		t.Errorf("契约版本要对得上: %d", doc.Contract)
	}
	if len(doc.Concepts) != 2 {
		t.Fatalf("该端出 2 个概念，实际 %d", len(doc.Concepts))
	}
	// 只读的那条必须标明只读——前端据此决定要不要给保存按钮。
	byID := map[string]conceptDoc{}
	for _, c := range doc.Concepts {
		byID[c.ID] = c
	}
	if byID["read.only"].Writable {
		t.Error("没有 Apply 的概念该标成只读")
	}
	if byID["something.x"].Kind != view.KindSeries || byID["something.x"].Source != "somewhere" {
		t.Errorf("概念的身份与来源该原样端出去: %+v", byID["something.x"])
	}
}

// TestApplyRoutesToTheConceptThatOwnsTheData：修改**转交**，不自己处理。
func TestApplyRoutesToTheConceptThatOwnsTheData(t *testing.T) {
	h := newHandler()
	var gotEdit string
	var gotBase string
	contribute(t, h, "x", view.Concept{
		ID: "a.b", Kind: view.KindCode, Title: "A",
		Apply: func(edit json.RawMessage, base string) (string, error) {
			gotEdit, gotBase = string(edit), base
			return "sha256:after", nil
		},
	})
	rec := post(t, h, "/api/apply", `{"id":"a.b","base":"sha256:before","edit":{"text":"hi"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("该 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var resp applyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Base != "sha256:after" {
		t.Errorf("该把新基线回给前端（续着改不用刷新页面）: %+v", resp)
	}
	if gotEdit != `{"text":"hi"}` || gotBase != "sha256:before" {
		t.Errorf("编辑内容与基线该**原样**转交: edit=%s base=%s", gotEdit, gotBase)
	}
}

// TestApplyReportsConflictWithBothSides：冲突必须带上两边的原文。只回一句
// 「过期了」等于把「我丢了什么」交给用户去猜。
func TestApplyReportsConflictWithBothSides(t *testing.T) {
	h := newHandler()
	contribute(t, h, "x", view.Concept{
		ID: "a.b", Kind: view.KindCode, Title: "A",
		Apply: func(json.RawMessage, string) (string, error) {
			return "", &view.Conflict{
				Concept: "a.b", Path: "mappings/a.json",
				Base: "sha256:mine", Current: "sha256:theirs",
				Yours: `{"roles":{}}`, Theirs: `{"roles":{"heavy":[]}}`,
			}
		},
	})
	rec := post(t, h, "/api/apply", `{"id":"a.b","base":"sha256:mine","edit":{}}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("冲突该是 409，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var resp applyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Conflict == nil {
		t.Fatal("409 必须带冲突内容")
	}
	if resp.Conflict.Yours == "" || resp.Conflict.Theirs == "" || resp.Conflict.Path == "" {
		t.Errorf("冲突该把两边原文与路径都给出来: %+v", resp.Conflict)
	}
}

// TestApplyRejectsUnknownAndReadOnly：两种「不该写」的情况都要说清楚。
func TestApplyRejectsUnknownAndReadOnly(t *testing.T) {
	h := newHandler()
	contribute(t, h, "x", view.Concept{ID: "ro", Kind: view.KindCode, Title: "Read only"})
	// 未知 ID：前端手里的快照过期了（比如模块刚被关掉）。
	if rec := post(t, h, "/api/apply", `{"id":"ghost","edit":{}}`); rec.Code != http.StatusNotFound {
		t.Errorf("未知概念该 404（前端该刷新页面），实际 %d", rec.Code)
	}
	// 只读概念：说清楚是只读，而不是回一个含糊的失败。
	rec := post(t, h, "/api/apply", `{"id":"ro","edit":{}}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("只读概念该被拒，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "read-only") {
		t.Errorf("报错该说清是只读: %s", rec.Body.String())
	}
	if rec := post(t, h, "/api/apply", `{not json`); rec.Code != http.StatusBadRequest {
		t.Errorf("坏 JSON 该 400，实际 %d", rec.Code)
	}
}

// TestApplyIsPostOnly：写操作不该能用一个 <img src> 触发。
func TestApplyIsPostOnly(t *testing.T) {
	h := newHandler()
	if rec := get(t, h, "/api/apply"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/apply 该 405，实际 %d", rec.Code)
	}
}

// TestOnlyAddressesNotNamesAreAnswered：DNS rebinding。
//
// 只监听 loopback 挡得住外面的连接，挡不住**别人网页上的一段脚本**——攻击者把
// 自己的域名解析到 127.0.0.1，浏览器就认为同源。浏览器唯一不骗人的东西是它自己
// 填的 Host，所以判据是「这次请求是冲着哪个名字来的」。
//
// 2026-09-20 放宽了一次，**按的仍是同一条判据**：名字（域名）一律拒，IP 一律收。
// 原来只收回环，是因为那时只打算给本机用；用户要从 Windows / 局域网 / Tailscale
// 打开界面时，那些地址全是 IP 字面量，而攻击者要伪造 Host 必须让用户的浏览器
// 访问那个 IP——那已经不是 rebinding 了。写操作另有第二道门（必须是 JSON，浏览器
// 会先发预检），所以「收 IP」不等于「任意网页都能改配置」。
func TestOnlyAddressesNotNamesAreAnswered(t *testing.T) {
	h := newHandler()
	contribute(t, h, "config", view.Concept{
		ID: "config.secretish", Kind: view.KindCode, Title: "A file",
		Data: map[string]any{"path": "x.json", "text": "something worth stealing"},
	})

	accepted := []string{
		loopback, "localhost:8899", "[::1]:8899",
		"10.0.50.11:8899",     // 局域网 / 另一台机器
		"100.101.224.37:8899", // Tailscale（100.64.0.0/10）
		"172.24.1.5:8899",     // WSL / 容器网段
	}
	for _, host := range accepted {
		if rec := raw(t, h, http.MethodGet, "/api/snapshot", "", host, ""); rec.Code != http.StatusOK {
			t.Errorf("Host %q 该被接受（它是地址，不是名字），实际 %d", host, rec.Code)
		}
	}
	// **名字**一律拒——这才这道门真正挡的东西。
	for _, host := range []string{"evil.com:8899", "localhost.evil.com:8899", "newgate.local:8899"} {
		rec := raw(t, h, http.MethodGet, "/api/snapshot", "", host, "")
		if rec.Code != http.StatusForbidden {
			t.Errorf("Host %q 该被拒（实际 %d）——域名正是 rebinding 的载体", host, rec.Code)
		}
		// 拒绝了就不许漏内容：这条响应里带着配置原文（脱敏过的那些）。
		if strings.Contains(rec.Body.String(), "worth stealing") {
			t.Errorf("Host %q 被拒了，却还是把内容发出去了", host)
		}
	}
}

// TestSavingNeedsAJSONContentType：跨站表单与 no-cors 的 fetch 都发不出
// application/json，但它们**能**发一个 body 长得像 JSON 的 text/plain——而解码器
// 并不看 Content-Type。所以这一条是那条路唯一的墙。
func TestSavingNeedsAJSONContentType(t *testing.T) {
	h := newHandler()
	var applied bool
	contribute(t, h, "x", view.Concept{
		ID: "a.b", Kind: view.KindCode, Title: "A",
		Apply: func(json.RawMessage, string) (string, error) {
			applied = true
			return "sha256:new", nil
		},
	})

	body := `{"id":"a.b","base":"","edit":{"text":"x"}}`
	rec := raw(t, h, http.MethodPost, "/api/apply", body, loopback, "text/plain;charset=UTF-8")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain 的写请求该被拒（415），实际 %d", rec.Code)
	}
	if applied {
		t.Error("被拒的请求不该碰到底下的 Apply")
	}
	// 正规的前端走这条路，必须照常能用。
	if rec := raw(t, h, http.MethodPost, "/api/apply", body, loopback, "application/json"); rec.Code != http.StatusOK {
		t.Fatalf("application/json 该照常放行，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if !applied {
		t.Error("正规请求没有走到 Apply")
	}
}

// TestSnapshotKeepsBrokenConceptsVisible：一个读不出来的概念（文件被删了、JSON
// 坏了）要**留在列表里并写上原因**，而不是消失。消失了用户会以为它不存在，然后
// 去别处找；而整页失败则是让一个坏文件弄白整个界面。
func TestSnapshotKeepsBrokenConceptsVisible(t *testing.T) {
	h := newHandler()
	contribute(t, h, "config",
		view.Concept{ID: "config.profile.ds", Kind: view.KindMapping, Title: "ds",
			Broken: "open mappings/ds.kv: no such file or directory"},
		view.Concept{ID: "config.state", Kind: view.KindToggles, Title: "Global settings",
			Data: map[string]any{"items": []any{}}},
	)
	rec := get(t, h, "/api/snapshot")
	if rec.Code != http.StatusOK {
		t.Fatalf("一个坏概念不该让整次快照失败，实际 %d", rec.Code)
	}
	var doc snapshotDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Concepts) != 2 {
		t.Fatalf("坏掉的那张卡片该照常出现，实际 %d 个概念", len(doc.Concepts))
	}
	byID := map[string]conceptDoc{}
	for _, c := range doc.Concepts {
		byID[c.ID] = c
	}
	if byID["config.profile.ds"].Error == "" {
		t.Error("坏卡片要写上原因")
	}
	if byID["config.profile.ds"].Writable {
		t.Error("读不出来的概念不该可写")
	}
	if byID["config.state"].Error != "" {
		t.Error("好的那张卡片不该被连累")
	}
}

// TestSnapshotCanBeAskedForOneSource：自动刷新只问计数器与日志那几位（便宜），
// 别顺带把配置那一位也叫醒——它要重读并重新解析每一份 profile 与每一个源文件。
// 这条锁的是「刷新真的省下了那笔开销」，不只是少回几个字段。
func TestSnapshotCanBeAskedForOneSource(t *testing.T) {
	h := newHandler()
	var cheap, expensive atomic.Int64
	contributeCount(t, h, "gateway", &cheap)
	contributeCount(t, h, "config", &expensive)

	if rec := get(t, h, "/api/snapshot"); rec.Code != http.StatusOK {
		t.Fatalf("不带 source 该问全部，实际 %d", rec.Code)
	}
	if cheap.Load() != 1 || expensive.Load() != 1 {
		t.Fatalf("首次加载该问全部: gateway=%d config=%d", cheap.Load(), expensive.Load())
	}

	rec := get(t, h, "/api/snapshot?source=gateway")
	if rec.Code != http.StatusOK {
		t.Fatalf("按来源过滤该 200，实际 %d", rec.Code)
	}
	if cheap.Load() != 2 {
		t.Errorf("点名的那位该被再问一次: %d", cheap.Load())
	}
	if expensive.Load() != 1 {
		t.Errorf("没点名的那位不该被吵醒: %d 次", expensive.Load())
	}
	var doc snapshotDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Concepts) != 1 || doc.Concepts[0].Source != "gateway" {
		t.Errorf("只该回被点名那位的概念: %+v", doc.Concepts)
	}
}

func contributeCount(t *testing.T, h *Handler, source string, n *atomic.Int64) {
	t.Helper()
	if _, err := h.views.Register(source, view.Title(func() string { return source }),
		func() ([]view.Concept, error) {
			n.Add(1)
			return []view.Concept{{ID: source + ".x", Kind: view.KindSeries, Title: source}}, nil
		}); err != nil {
		t.Fatal(err)
	}
}

// TestSnapshotFailureIsLoud：整组产不出来（配置根整个读不了）时要说清楚是哪一步
// 坏的，而不是回一份空快照——空快照在界面上表现成「一个模块都没有」。
func TestSnapshotFailureIsLoud(t *testing.T) {
	h := newHandler()
	if _, err := h.views.Register("config", view.Title(func() string { return "Configuration" }),
		func() ([]view.Concept, error) {
			return nil, errors.New("permission denied")
		}); err != nil {
		t.Fatal(err)
	}
	rec := get(t, h, "/api/snapshot")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("该 500，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "permission denied") {
		t.Errorf("报错该带上真正的原因: %s", rec.Body.String())
	}
}

// TestStaticServesTheAppAndFallsBackForRoutes：静态资源 + SPA 兜底；但**拼错的
// 资源不许**兜成 index.html——那会让浏览器报的错指向别处。
func TestStaticServesTheAppAndFallsBackForRoutes(t *testing.T) {
	h := newHandler()
	if rec := get(t, h, "/"); !strings.Contains(rec.Body.String(), "app") {
		t.Errorf("根路径该发 index.html，实际 %q", rec.Body.String())
	}
	if ct := get(t, h, "/app.js").Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("js 的 Content-Type 不对: %q", ct)
	}
	if rec := get(t, h, "/some/route"); !strings.Contains(rec.Body.String(), "app") {
		t.Errorf("前端路由该兜回 index.html，实际 %q", rec.Body.String())
	}
	if rec := get(t, h, "/typo.js"); rec.Code != http.StatusNotFound {
		t.Errorf("拼错的资源该 404，实际 %d（兜成 HTML 会让报错指向别处）", rec.Code)
	}
}

// TestTheEntryDocumentIsNeverCached：入口文件必须每次问服务器。
//
// 这一条防的是**最贵的一种「你没做」**：发版之后用户看到的还是上一版界面，而
// 服务端完全正常（curl 拿到的是新产物）。哈希文件名让这个问题有两层——旧
// index.html 指着旧 JS 的名字，而那份旧 JS 也还在用户的缓存里，于是页面照常
// 渲染、只是渲染的是旧的。没有报错、没有白屏，用户只会觉得「你说的功能没做」。
//
// 所以三条各自的理由都钉住：入口 no-store（它是唯一知道哈希变了的那份文件）、
// 哈希产物 immutable（同一个名字永远同一份字节）、其余不带哈希的走 no-cache
// （宁可多一个往返，也不把改过内容的同名文件钉死在用户机器上）。
func TestTheEntryDocumentIsNeverCached(t *testing.T) {
	h := newHandler()

	cc := func(path string) string { return get(t, h, path).Header().Get("Cache-Control") }
	if got := cc("/"); got != "no-store" {
		t.Errorf("入口该 no-store（发版后用户必须立刻看到新版），实际 %q", got)
	}
	if got := cc("/some/route"); got != "no-store" {
		t.Errorf("SPA 兜底回的也是入口，同样 no-store，实际 %q", got)
	}
	if got := cc("/assets/index-deadbee.js"); !strings.Contains(got, "immutable") {
		t.Errorf("带内容哈希的产物该可长期缓存，实际 %q", got)
	}
	if got := cc("/app.js"); got != "no-cache" {
		t.Errorf("没哈希的文件该每次校验，实际 %q", got)
	}
}
