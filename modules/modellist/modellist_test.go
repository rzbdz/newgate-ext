package modellist

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	modules "github.com/rzbdz/newgate/component"
)

// This file's cases touch only modellist's own knowledge: both dialects' address/auth/pagination,
// how grouping converges, when the cache should be invalidated, and why "cannot ask" is not an
// error.
//
// They all go through httptest (no network, see docs/10-testing-security.md); providers.json is
// fenced into a temp directory by testkit.Sandbox and does not touch the real config.

func useProvider(t *testing.T, name string, p domain.Provider) {
	t.Helper()
	testkit.Sandbox(t)
	if err := store.SaveProviders(&domain.Providers{Providers: map[string]domain.Provider{name: p}}); err != nil {
		t.Fatalf("writing providers.json: %v", err)
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
		t.Fatalf("the openai adapter should succeed, got %+v", cat)
	}
	if path != "/models" {
		t.Errorf("after /v1/models 404s on the first hop it should fall to /models, got %q as the final request", path)
	}
	if auth != "Bearer sk-test" {
		t.Errorf("the OpenAI dialect should use Bearer, got %q", auth)
	}
	if len(cat.Models) != 2 {
		t.Errorf("duplicate ids should be removed (upstream pagination boundaries may repeat), got %+v", cat.Models)
	}
	if cat.Models[0].Group != "chat" || cat.Models[1].Group != "reasoning" {
		t.Errorf("owned_by should become the group, got %+v", cat.Models)
	}
	if len(cat.Groups) != 2 || cat.Groups[0] != "chat" {
		t.Errorf("groups are ordered by first appearance, got %v", cat.Groups)
	}
	// The declared protocol is openai and the first route succeeded; anthropic should not be tried.
	if cat.Endpoint == "" || !strings.HasSuffix(cat.Endpoint, "/models") {
		t.Errorf("Endpoint should be the address actually queried, got %q", cat.Endpoint)
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
		t.Fatalf("the anthropic adapter should succeed, got %+v", cat)
	}
	if calls != 2 {
		t.Errorf("has_more should fetch the second page, got %d requests", calls)
	}
	if key != "sk-ant" || version == "" {
		t.Errorf("the Anthropic dialect should send x-api-key and anthropic-version, got key=%q version=%q", key, version)
	}
	if len(cat.Models) != 2 || cat.Models[1].Name != "Claude B" {
		t.Errorf("the two pages should merge into one list (display_name -> name), got %+v", cat.Models)
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
		t.Fatalf("failing to query is not an HTTP error: the UI wants supported=false plus a reason, got error=%v", err)
	}
	if cat.Supported {
		t.Fatal("when the upstream 404s everything it must not say it is supported")
	}
	if cat.Models == nil || len(cat.Models) != 0 {
		t.Errorf("Models should be an empty array, not nil (the front end need not write two branches), got %#v", cat.Models)
	}
	if !strings.Contains(cat.Note, "no model list") {
		t.Errorf("Note should state the reason, got %q", cat.Note)
	}
	if atomic.LoadInt32(&hits) < 2 {
		t.Errorf("both adapters times all candidate addresses should have been tried, only %d requests were made", hits)
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
		t.Fatal("with no models at all the UI must fall back to the declared list, not draw an empty menu")
	}
	if !strings.Contains(cat.Note, "empty model list") {
		t.Errorf("Note should say 'empty list', got %q", cat.Note)
	}
	if cat.Adapter != "openai" || cat.Endpoint == "" {
		t.Errorf("the reason should keep 'where it asked', got adapter=%q endpoint=%q", cat.Adapter, cat.Endpoint)
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
		t.Fatalf("an auth failure must also become supported=false (the UI falls back to the declared list), got error=%v", err)
	}
	if cat.Supported || !strings.Contains(cat.Note, "401") {
		t.Errorf("Note should name HTTP 401, got supported=%v note=%q", cat.Supported, cat.Note)
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
		t.Fatalf("first call: %+v / %v", first, err)
	}
	second, err := svc.List(context.Background(), "p", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Cached {
		t.Error("the second call should hit the cache - opening the dropdown a dozen times must not ask the upstream a dozen times")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("on a cache hit the upstream should be hit only once, got %d", got)
	}
	third, err := svc.List(context.Background(), "p", Options{Refresh: true})
	if err != nil || third.Cached {
		t.Fatalf("refresh should bypass the cache: cached=%v err=%v", third.Cached, err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("after refresh it should ask again, got %d", got)
	}
}

func TestUnknownProviderIsAnError(t *testing.T) {
	useProvider(t, "known", domain.Provider{BaseURL: "http://127.0.0.1:9", Protocol: "openai"})
	if _, err := newTestService().List(context.Background(), "missing", Options{}); err == nil {
		t.Fatal("an unknown name is a real error (the caller asked wrong); it must not be disguised as 'unsupported'")
	}
}

func TestSafeURLStripsCredentials(t *testing.T) {
	got := safeURL("https://user:secret@example.com/v1/models?x=1")
	if strings.Contains(got, "secret") || strings.Contains(got, "user@") {
		t.Errorf("userinfo in the URL must not reach the UI or the log, got %q", got)
	}
	if !strings.Contains(got, "example.com/v1/models") {
		t.Errorf("the address itself must be kept (the reader needs to know where it asked), got %q", got)
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
				t.Errorf("ids = %v, want %v", ids, c.ids)
			}
			if strings.Join(groups, ",") != strings.Join(c.group, ",") {
				t.Errorf("groups = %v, want %v", groups, c.group)
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
		t.Fatalf("the extension endpoint should produce a Catalog, got %#v", out)
	}
	if _, err := svc.handle(webapi.ExtensionRequest{Context: context.Background()}); err == nil {
		t.Error("an empty request body should error (it does not say whom to ask)")
	}
	if _, err := svc.handle(webapi.ExtensionRequest{Context: context.Background(), Body: json.RawMessage(`{`)}); err == nil {
		t.Error("bad JSON should error")
	}
}

// fakeWeb is the stand-in for web-dashboard's port: it only remembers who registered what.
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
		t.Fatalf("with the UI installed it should register %q, got %v", Endpoint, fake.registered)
	}
	// The Service port itself must be there too: the feature still works without the UI installed.
	if _, ok := testkit.Maybe(g, Capability); !ok {
		t.Fatal("the Service port should be provided unconditionally - a missing UI just means no entry point")
	}
	g.Stop()
	if fake.released != 1 || len(fake.registered) != 0 {
		t.Errorf("Stop should unregister, got released=%d registered=%v", fake.released, fake.registered)
	}
}
