package modellist

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/store"
	"github.com/rzbdz/newgate/testing/testkit"
)

// 本文件的用例只碰 modellist 自己的知识：两种方言的地址/认证/分页、分组怎么收敛、
// 缓存什么时候该失效，以及「问不到」为什么不是错误。
//
// 它们都走 httptest（不出网，见 docs/10-testing-security.md）；providers.json 由
// testkit.Sandbox 圈在临时目录里，不碰真实配置。

func useProvider(t *testing.T, name string, p domain.Provider) {
	t.Helper()
	testkit.Sandbox(t)
	if err := store.SaveProviders(&domain.Providers{Providers: map[string]domain.Provider{name: p}}); err != nil {
		t.Fatalf("写 providers.json: %v", err)
	}
}

func newTestService() *service { return newService() }

func TestOpenAIListsModelsWithGroups(t *testing.T) {
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		if r.URL.Path == "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"id":"deepseek-chat","owned_by":"chat"},
			{"id":"deepseek-v3.2-reasoner","owned_by":"reasoning"},
			{"id":"deepseek-chat","owned_by":"chat"}
		]}`))
	}))
	defer srv.Close()

	useProvider(t, "ds", domain.Provider{
		BaseURL: srv.URL, Protocol: "openai", APIKey: "sk-test",
	})
	cat, err := newTestService().List(context.Background(), "ds", Options{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !cat.Supported || cat.Adapter != "openai" {
		t.Fatalf("应该是 openai 适配器成功，实际 %+v", cat)
	}
	if path != "/models" {
		t.Errorf("第一跳 /v1/models 404 之后该落到 /models，实际最后请求 %q", path)
	}
	if auth != "Bearer sk-test" {
		t.Errorf("OpenAI 方言该用 Bearer，实际 %q", auth)
	}
	if len(cat.Models) != 2 {
		t.Errorf("重复 id 该去掉（上游分页边界可能重复），实际 %+v", cat.Models)
	}
	if cat.Models[0].Group != "chat" || cat.Models[1].Group != "reasoning" {
		t.Errorf("owned_by 该变成分组，实际 %+v", cat.Models)
	}
	if len(cat.Groups) != 2 || cat.Groups[0] != "chat" {
		t.Errorf("分组按第一次出现排序，实际 %v", cat.Groups)
	}
	// 声明的协议是 openai，第一条路就成功了；不该再去试 anthropic。
	if cat.Endpoint == "" || !strings.HasSuffix(cat.Endpoint, "/models") {
		t.Errorf("Endpoint 该是实际问到的地址，实际 %q", cat.Endpoint)
	}
}

func TestAnthropicPaginatesWithItsOwnHeaders(t *testing.T) {
	var calls int
	var key, version string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		key, version = r.Header.Get("x-api-key"), r.Header.Get("anthropic-version")
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("after_id") == "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-a","display_name":"Claude A"}],"has_more":true,"last_id":"claude-a"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-b","display_name":"Claude B"}],"has_more":false}`))
	}))
	defer srv.Close()

	useProvider(t, "cc", domain.Provider{
		BaseURL: srv.URL, Protocol: "anthropic", APIKey: "sk-ant",
	})
	cat, err := newTestService().List(context.Background(), "cc", Options{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !cat.Supported || cat.Adapter != "anthropic" {
		t.Fatalf("应该是 anthropic 适配器成功，实际 %+v", cat)
	}
	if calls != 2 {
		t.Errorf("has_more 该走第二页，实际请求 %d 次", calls)
	}
	if key != "sk-ant" || version == "" {
		t.Errorf("Anthropic 方言该带 x-api-key 与 anthropic-version，实际 key=%q version=%q", key, version)
	}
	if len(cat.Models) != 2 || cat.Models[1].Name != "Claude B" {
		t.Errorf("两页该并成一份清单（display_name → name），实际 %+v", cat.Models)
	}
}

func TestUnsupportedIsAnAnswerNotAnError(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	useProvider(t, "nope", domain.Provider{BaseURL: srv.URL, Protocol: "openai"})
	cat, err := newTestService().List(context.Background(), "nope", Options{})
	if err != nil {
		t.Fatalf("问不到不是 HTTP 错误：界面要的是 supported=false + 原因，实际 error=%v", err)
	}
	if cat.Supported {
		t.Fatal("上游全 404 时不该说它支持")
	}
	if cat.Models == nil || len(cat.Models) != 0 {
		t.Errorf("Models 该是空数组而不是 nil（前端不必写两条分支），实际 %#v", cat.Models)
	}
	if !strings.Contains(cat.Note, "no model list") {
		t.Errorf("Note 该说清原因，实际 %q", cat.Note)
	}
	if atomic.LoadInt32(&hits) < 2 {
		t.Errorf("两条适配器 × 候选地址都该试过，实际只打了 %d 次", hits)
	}
}

func TestAnEmptyListFallsBackLikeUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer srv.Close()

	useProvider(t, "empty", domain.Provider{BaseURL: srv.URL, Protocol: "openai"})
	cat, err := newTestService().List(context.Background(), "empty", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if cat.Supported {
		t.Fatal("一个模型都没有时界面必须回落到声明清单，不能画一个空菜单")
	}
	if !strings.Contains(cat.Note, "empty model list") {
		t.Errorf("Note 该说「空清单」，实际 %q", cat.Note)
	}
	if cat.Adapter != "openai" || cat.Endpoint == "" {
		t.Errorf("原因里该保留「问过哪儿」，实际 adapter=%q endpoint=%q", cat.Adapter, cat.Endpoint)
	}
}

func TestAuthFailureIsReportedAsAReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid key"}`))
	}))
	defer srv.Close()

	useProvider(t, "bad", domain.Provider{BaseURL: srv.URL, Protocol: "openai"})
	cat, err := newTestService().List(context.Background(), "bad", Options{})
	if err != nil {
		t.Fatalf("认证失败也要变成 supported=false（界面回落到声明清单），实际 error=%v", err)
	}
	if cat.Supported || !strings.Contains(cat.Note, "401") {
		t.Errorf("Note 该点名 HTTP 401，实际 supported=%v note=%q", cat.Supported, cat.Note)
	}
}

func TestCacheAndRefresh(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()

	useProvider(t, "p", domain.Provider{BaseURL: srv.URL, Protocol: "openai"})
	svc := newTestService()

	first, err := svc.List(context.Background(), "p", Options{})
	if err != nil || !first.Supported {
		t.Fatalf("第一次: %+v / %v", first, err)
	}
	second, err := svc.List(context.Background(), "p", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Cached {
		t.Error("第二次该命中缓存——下拉打开十几次不该问上游十几次")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("缓存命中时上游只该被打一次，实际 %d", got)
	}
	third, err := svc.List(context.Background(), "p", Options{Refresh: true})
	if err != nil || third.Cached {
		t.Fatalf("refresh 该绕过缓存: cached=%v err=%v", third.Cached, err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("refresh 之后该再问一次，实际 %d", got)
	}
}

func TestUnknownProviderIsAnError(t *testing.T) {
	useProvider(t, "known", domain.Provider{BaseURL: "http://127.0.0.1:9", Protocol: "openai"})
	if _, err := newTestService().List(context.Background(), "missing", Options{}); err == nil {
		t.Fatal("名字不存在是一个真的错误（调用方问错了），不该伪装成「不支持」")
	}
}

func TestSafeURLStripsCredentials(t *testing.T) {
	got := safeURL("https://user:secret@example.com/v1/models?x=1")
	if strings.Contains(got, "secret") || strings.Contains(got, "user@") {
		t.Errorf("URL 里的 userinfo 不该端到界面/日志上，实际 %q", got)
	}
	if !strings.Contains(got, "example.com/v1/models") {
		t.Errorf("地址本身要留着（看的人才知道问的是哪儿），实际 %q", got)
	}
}

func TestParseModelShapes(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		ids   []string
		group []string
	}{
		{"data array", `{"data":[{"id":"a"},{"id":"b"}]}`, []string{"a", "b"}, []string{"", ""}},
		{"models array", `{"models":[{"slug":"a"}]}`, []string{"a"}, []string{""}},
		{"root array of strings", `["a","b"]`, []string{"a", "b"}, []string{"", ""}},
		{"nested data.models", `{"data":{"models":[{"id":"a"}]}}`, []string{"a"}, []string{""}},
		{"group fields", `{"data":[{"id":"a","group":"g"},{"id":"b","groups":["h"]},{"id":"c","provider":"p"}]}`,
			[]string{"a", "b", "c"}, []string{"g", "h", "p"}},
		{"slash prefix", `{"data":[{"id":"anthropic/claude"}]}`, []string{"anthropic/claude"}, []string{"anthropic"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			models, _, _, _, err := parseModels([]byte(c.raw))
			if err != nil {
				t.Fatalf("parseModels: %v", err)
			}
			var ids, groups []string
			for _, m := range models {
				ids = append(ids, m.ID)
				groups = append(groups, m.Group)
			}
			if strings.Join(ids, ",") != strings.Join(c.ids, ",") {
				t.Errorf("ids = %v，想要 %v", ids, c.ids)
			}
			if strings.Join(groups, ",") != strings.Join(c.group, ",") {
				t.Errorf("groups = %v，想要 %v", groups, c.group)
			}
		})
	}
}

func TestHandleReadsTheExtensionRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()
	useProvider(t, "p", domain.Provider{BaseURL: srv.URL, Protocol: "openai"})
	svc := newTestService()

	out, err := svc.handle(webapi.ExtensionRequest{Context: context.Background(), Body: json.RawMessage(`{"provider":"p"}`)})
	if err != nil {
		t.Fatal(err)
	}
	cat, ok := out.(*Catalog)
	if !ok || !cat.Supported {
		t.Fatalf("扩展端点该交出 Catalog，实际 %#v", out)
	}
	if _, err := svc.handle(webapi.ExtensionRequest{Context: context.Background()}); err == nil {
		t.Error("空请求体该报错（他说不清要问谁）")
	}
	if _, err := svc.handle(webapi.ExtensionRequest{Context: context.Background(), Body: json.RawMessage(`{`)}); err == nil {
		t.Error("坏 JSON 该报错")
	}
}

// fakeWeb 是 web-dashboard 那个端口的替身：只记住谁登记了什么。
type fakeWeb struct {
	registered map[string]webapi.ExtensionHandler
	released   int
}

func (f *fakeWeb) Register(name string, h webapi.ExtensionHandler) (modules.Release, error) {
	if f.registered == nil {
		f.registered = map[string]webapi.ExtensionHandler{}
	}
	f.registered[name] = h
	return func() error { f.released++; delete(f.registered, name); return nil }, nil
}

func TestModuleRegistersAndReleasesTheEndpoint(t *testing.T) {
	testkit.Sandbox(t)
	fake := &fakeWeb{}
	web := modules.Component{
		Name: "fake-web",
		Type: "cli",
		Provides: []modules.Provision{
			modules.Provide(webapi.ExtensionCapability, webapi.ExtensionService(fake)),
		},
	}
	g := testkit.Start(t, web, New())
	g.Before("fake-web", Name)
	if _, ok := fake.registered[Endpoint]; !ok {
		t.Fatalf("装上了界面就该登记 %q，实际 %v", Endpoint, fake.registered)
	}
	// Service 端口本身也要在：没装界面时功能仍然可用。
	if _, ok := testkit.Maybe(g, Capability); !ok {
		t.Fatal("Service 端口该无条件提供——界面不在只是没有入口")
	}
	g.Stop()
	if fake.released != 1 || len(fake.registered) != 0 {
		t.Errorf("Stop 该撤销登记，实际 released=%d registered=%v", fake.released, fake.registered)
	}
}
