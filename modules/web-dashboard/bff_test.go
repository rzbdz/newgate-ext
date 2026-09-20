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
		"index.html": {Data: []byte("<html>app</html>")},
		"app.js":     {Data: []byte("console.log(1)")},
	}
}

func newHandler() *Handler {
	return NewHandler(testAssets(), view.NewRegistry())
}

// contribute 登记一个贡献者：产出函数每次被调用都原样交出这批概念。
func contribute(t *testing.T, h *Handler, source string, concepts ...view.Concept) {
	t.Helper()
	if _, err := h.views.Register(source, func() ([]view.Concept, error) { return concepts, nil }); err != nil {
		t.Fatal(err)
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

// TestOnlyLoopbackHostsAreAnswered：DNS rebinding。
//
// 只监听 loopback 挡得住外面的连接，挡不住**别人网页上的一段脚本**——攻击者把
// 自己的域名解析到 127.0.0.1，浏览器就认为同源。浏览器唯一不骗人的东西是它自己
// 填的 Host，所以判据是「这次请求是冲着哪个名字来的」。
func TestOnlyLoopbackHostsAreAnswered(t *testing.T) {
	h := newHandler()
	contribute(t, h, "config", view.Concept{
		ID: "config.secretish", Kind: view.KindCode, Title: "A file",
		Data: map[string]any{"path": "x.json", "text": "something worth stealing"},
	})

	for _, host := range []string{loopback, "localhost:8899", "[::1]:8899"} {
		if rec := raw(t, h, http.MethodGet, "/api/snapshot", "", host, ""); rec.Code != http.StatusOK {
			t.Errorf("Host %q 该被接受，实际 %d", host, rec.Code)
		}
	}
	for _, host := range []string{"evil.com:8899", "10.0.50.11:8899", "localhost.evil.com:8899"} {
		rec := raw(t, h, http.MethodGet, "/api/snapshot", "", host, "")
		if rec.Code != http.StatusForbidden {
			t.Errorf("Host %q 该被拒（实际 %d）——它指向的名字不是本机", host, rec.Code)
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
	if _, err := h.views.Register(source, func() ([]view.Concept, error) {
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
	if _, err := h.views.Register("config", func() ([]view.Concept, error) {
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
