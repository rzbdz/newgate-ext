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
		t.Fatal("没装根 handler 时端口该由数据面应答，实际什么都没说")
	}
	if rec.Code == http.StatusOK {
		t.Errorf("没有上游时数据面不该给出 200（这条用例是来确认请求真的走到了数据面），实际 %d: %s",
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
		t.Fatalf("根 handler 装上去之后该由它应答，实际: %q", got)
	}
}

// TestHandlerIsStable：Handler() 会被调用两次（porthub 拿它当兜底，Start 也可能
// 再问一次）。两次拿到**同一份**才算数——不然 porthub 兜底兜的是第一份 mux，
// 而端口服务的是第二份，中间那段差异是静默的。
func TestHandlerIsStable(t *testing.T) {
	isolate(t)
	s := newTestServer()
	if s.Handler() != s.Handler() {
		t.Error("Handler() 两次给了两份不同的 mux——兜底与端口服务的就不是同一个东西了")
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
		t.Fatalf("挂载失败: %v", err)
	}
	root, err := hub.Root("gateway", s.Handler())
	if err != nil {
		t.Fatalf("Root 失败: %v", err)
	}
	s.SetRoot(root)

	rec := httptest.NewRecorder()
	s.rootHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/index.html", nil))
	if got := rec.Body.String(); got != "web" {
		t.Errorf("挂在 /ui 的服务没接到请求，实际: %q", got)
	}

	// 数据面自己的路径不受影响（`/a/…` 是数据面的转发路径）。
	rec2 := httptest.NewRecorder()
	s.rootHandler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if strings.Contains(rec2.Body.String(), "web") {
		t.Errorf("/v1/models 被 web 接住了——挂载表抢了数据面的路径: %q", rec2.Body.String())
	}
}
