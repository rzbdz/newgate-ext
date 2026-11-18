package porthub

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func body(s string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(s)) })
}

// hit 走一遍真正的分派：查表 → 命中就交给它。返回响应体（未命中返回 ""）。
func hit(t *testing.T, r *Registry, path string) string {
	t.Helper()
	h, ok := r.Lookup(path)
	if !ok {
		return ""
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Body.String()
}

func TestLookupMatchesPrefixOnSegmentBoundary(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Mount("/ui", "web", body("ui")); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"/ui":        "ui",
		"/ui/":       "ui",
		"/ui/api/x":  "ui",
		"/ui/deep/x": "ui",
		"/uix":       "", // 按字节前缀匹配会在这里多认一个 service
		"/u":         "",
		"/":          "",
		"/v1/models": "",
	}
	for path, want := range cases {
		if got := hit(t, r, path); got != want {
			t.Errorf("Lookup(%q) = %q，想要 %q", path, got, want)
		}
	}
}

// TestLongestPrefixWins：`/ui` 与 `/ui/api` 同时挂着时，后者该接住更深的路径。
func TestLongestPrefixWins(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Mount("/ui", "outer", body("outer")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Mount("/ui/api", "inner", body("inner")); err != nil {
		t.Fatal(err)
	}
	if got := hit(t, r, "/ui/api/snapshot"); got != "inner" {
		t.Errorf("/ui/api/snapshot → %q，想要 inner", got)
	}
	if got := hit(t, r, "/ui/page"); got != "outer" {
		t.Errorf("/ui/page → %q，想要 outer", got)
	}
}

// TestReservedPrefixesAreRefused 保留前缀被抢的症状是「本该发给本机的请求被当
// 数据面转发给上游」，所以这里必须当场报错，而不是让后来者覆盖。
func TestReservedPrefixesAreRefused(t *testing.T) {
	r := NewRegistry()
	for _, p := range []string{"/v1", "/v1/models", "/a", "/a/claude", "/__newgate", "/__newgate/config"} {
		_, err := r.Mount(p, "sneaky", body("x"))
		if err == nil {
			t.Errorf("挂 %q 该被拒绝", p)
			continue
		}
		// 断言英文原文（源语言，CI 上稳定），不是译文——译文会变，判据不该跟着变。
		if !strings.Contains(err.Error(), "reserved prefix") {
			t.Errorf("挂 %q 的报错该说清原因，实际: %v", p, err)
		}
	}
}

func TestDuplicatePrefixIsRefused(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Mount("/ui", "web", body("a")); err != nil {
		t.Fatal(err)
	}
	_, err := r.Mount("/ui", "other", body("b"))
	if err == nil {
		t.Fatal("同一前缀挂两次该报错——否则两个 service 里只有一个能收到请求，还是静默的")
	}
	if !strings.Contains(err.Error(), "web") {
		t.Errorf("报错要点名先挂的那个（谁占了），实际: %v", err)
	}
}

func TestReleaseRemovesTheMount(t *testing.T) {
	r := NewRegistry()
	rel, err := r.Mount("/ui", "web", body("ui"))
	if err != nil {
		t.Fatal(err)
	}
	rel()
	if got := hit(t, r, "/ui"); got != "" {
		t.Errorf("Release 之后不该还命中，实际 %q", got)
	}
	rel() // 幂等
	// 撤掉之后同一个前缀可以被别人挂上。
	if _, err := r.Mount("/ui", "other", body("other")); err != nil {
		t.Fatalf("撤掉后该能重挂: %v", err)
	}
	if got := hit(t, r, "/ui"); got != "other" {
		t.Errorf("重挂之后 → %q，想要 other", got)
	}
}

// TestReleaseDoesNotStealSomeoneElsesMount：先挂的撤销时不能把后来者的删掉。
func TestReleaseDoesNotStealSomeoneElsesMount(t *testing.T) {
	r := NewRegistry()
	first, err := r.Mount("/ui", "first", body("first"))
	if err != nil {
		t.Fatal(err)
	}
	first() // 撤掉自己
	if _, err := r.Mount("/ui", "second", body("second")); err != nil {
		t.Fatal(err)
	}
	first() // 再撤一次（幂等，且不许误删 second 那条）
	if got := hit(t, r, "/ui"); got != "second" {
		t.Errorf("后来者的挂载被误删了: %q", got)
	}
}

func TestRejectsMalformedPrefixAndNilHandler(t *testing.T) {
	r := NewRegistry()
	for _, p := range []string{"", "ui", "/"} {
		if _, err := r.Mount(p, "web", body("x")); err == nil {
			t.Errorf("前缀 %q 该被拒绝", p)
		}
	}
	if _, err := r.Mount("/ui", "web", nil); err == nil {
		t.Error("nil handler 该被拒绝")
	}
}

func TestMountsSnapshotIsSorted(t *testing.T) {
	r := NewRegistry()
	for _, p := range []string{"/svc-z", "/svc-a", "/svc-m"} {
		if _, err := r.Mount(p, "owner-"+p, body(p)); err != nil {
			t.Fatal(err)
		}
	}
	got := r.Mounts()
	want := []string{"/svc-a", "/svc-m", "/svc-z"}
	for i, m := range got {
		if m.Prefix != want[i] {
			t.Fatalf("Mounts()[%d] = %q，想要 %q（诊断输出要稳定，按前缀排序）", i, m.Prefix, want[i])
		}
	}
}

// TestRootIsThePortsDispatch 锁「端口归谁」：Root 交出的 handler 就是那个端口的
// 分派——挂载的服务优先，没人认领的落回兜底（数据面）。
func TestRootIsThePortsDispatch(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Mount("/ui", "web", body("web")); err != nil {
		t.Fatal(err)
	}
	root, err := r.Root("datapath", body("datapath"))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"/ui/x":        "web",
		"/v1/messages": "datapath", // 没挂过 → 兜底
		"/":            "datapath",
	}
	for path, want := range cases {
		rec := httptest.NewRecorder()
		root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Body.String(); got != want {
			t.Errorf("root(%q) = %q，想要 %q", path, got, want)
		}
	}
	if got := r.FallbackOwner(); got != "datapath" {
		t.Errorf("FallbackOwner() = %q，想要 datapath", got)
	}
}

// TestRootRejectsNilFallback：nil 兜底在**装的时候**就报错，而不是等到一个请求
// 打进来才在热路径上炸（那时候的现场是 500 + 一段 panic 栈，没人会联想到是启动
// 时的一次调用少传了参数）。
func TestRootRejectsNilFallback(t *testing.T) {
	if _, err := NewRegistry().Root("datapath", nil); err == nil {
		t.Error("nil 兜底该被拒绝——一个 nil handler 挂上去的报错会出现在每个请求里")
	}
}

// TestMountsIncludeTheFallback：诊断输出要能完整回答「这个端口上都在跑什么」，
// 所以兜底在快照里表现为一条 `/` 的记录（它接住的正是所有没被更长前缀认领的路径）。
func TestMountsIncludeTheFallback(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Mount("/ui", "web", body("web")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Root("datapath", body("dp")); err != nil {
		t.Fatal(err)
	}
	got := r.Mounts()
	if len(got) != 2 || got[0].Prefix != "/" || got[0].Owner != "datapath" || got[1].Prefix != "/ui" {
		t.Fatalf("Mounts() = %+v，想要 [/ → datapath, /ui → web]", got)
	}
}

// TestConcurrentMountAndLookup 是「两个前端同时跑」的那条 race 保护点：分派每
// 请求现查表，而挂载/撤销发生在模块的 Start/Stop。跑 -race 才有意义。
func TestConcurrentMountAndLookup(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		prefix := fmt.Sprintf("/svc%d", i)
		go func() {
			defer wg.Done()
			rel, err := r.Mount(prefix, "owner", body(prefix))
			if err != nil {
				t.Errorf("Mount(%s): %v", prefix, err)
				return
			}
			rel()
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_, _ = r.Lookup(prefix + "/x")
			}
		}()
	}
	wg.Wait()
}
