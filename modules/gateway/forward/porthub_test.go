package forward

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/lib/porthub"
)

// 端口交给谁——这条接缝的两半。它锁的是**方向**：数据面不认识那张挂载表，
// 它只知道自己有一个「根 handler」的空位，装进来的是什么由守护进程入口决定。
//
// 为什么值得两条测试：这个接缝错了不会红任何别的东西——编译过、转发照常，
// 症状是「web 界面在浏览器里 404，而日志里一切正常」。而它最容易被改回去的
// 方式恰恰是「顺手在 catch-all 里查一下表」，那正是被否掉的那版实现。

// TestTheDataPlaneServesItsOwnPortByDefault：没人装根 handler 时，端口就是数据面
// 自己的——也就是这个机制出现之前的样子（`testing/system` 的 harness 走的这条）。
func TestTheDataPlaneServesItsOwnPortByDefault(t *testing.T) {
	isolate(t)
	s := newTestServer()

	rec := httptest.NewRecorder()
	s.rootHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/index.html", nil))

	// 测试里没有可用上游，所以答案是数据面的失败——关键是这个路径**走到了数据面**
	// （它有话要说），而不是被谁接住、也不是空白。
	if rec.Body.Len() == 0 {
		t.Fatal("with no root handler the port should be answered by the data plane, but nothing was said at all")
	}
	if rec.Code == http.StatusOK {
		t.Errorf("without an upstream the data plane must not return 200 (this case exists to confirm the request really reached the data plane), got %d: %s",
			rec.Code, rec.Body.String())
	}
}

// TestSetRootHandsThePortOver：装了根 handler 之后，端口上应答的就是它——数据面
// 不再是这条路的主人。这就是 web 界面能住在同一个端口上的前提。
func TestSetRootHandsThePortOver(t *testing.T) {
	isolate(t)
	s := newTestServer()

	const body = "served by someone else"
	s.SetRoot(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))

	rec := httptest.NewRecorder()
	s.rootHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/index.html", nil))
	if got := rec.Body.String(); got != body {
		t.Fatalf("once the root handler is installed it should answer, got: %q", got)
	}
}

// TestHandlerIsStable：Handler() 会被调用两次（porthub 拿它当兜底，Start 也可能
// 再问一次）。两次拿到**同一份**才算数——不然 porthub 兜底兜的是第一份 mux，
// 而端口服务的是第二份，中间那段差异是静默的。
func TestHandlerIsStable(t *testing.T) {
	isolate(t)
	s := newTestServer()
	if s.Handler() != s.Handler() {
		t.Error("Handler() returned two different muxes on two calls - the fallback and the port service would not be the same thing")
	}
}

// TestRootSeamComposesWithTheHub：把两半接起来跑一遍（守护进程入口做的事：
// 拿数据面的 handler 当兜底，换上 hub 的根）。这里验的是**合成之后**的分派：
// 挂载的服务优先，没人认领的落回数据面。
func TestRootSeamComposesWithTheHub(t *testing.T) {
	isolate(t)
	s := newTestServer()
	hub := porthub.NewRegistry()
	if _, err := hub.Mount("/ui", "web", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("web"))
	})); err != nil {
		t.Fatalf("mount failed: %v", err)
	}
	root, err := hub.Root("gateway", s.Handler())
	if err != nil {
		t.Fatalf("Root failed: %v", err)
	}
	s.SetRoot(root)

	rec := httptest.NewRecorder()
	s.rootHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/index.html", nil))
	if got := rec.Body.String(); got != "web" {
		t.Errorf("the service mounted at /ui got no request, got: %q", got)
	}

	// 数据面自己的路径不受影响（`/a/…` 是数据面的转发路径）。
	rec2 := httptest.NewRecorder()
	s.rootHandler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if strings.Contains(rec2.Body.String(), "web") {
		t.Errorf("/v1/models was caught by web - the mount table stole a data-plane path: %q", rec2.Body.String())
	}
}
