package archdiagram

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 这张图把内部结构整个摊开（模块名、包路径、谁依赖谁），所以它的两道判据值得
// 各来一条：**只答本机**、**只读**。第三件要锁的是降级——没有源码树时它该照常
// 出图（装配那一半），而不是白屏或者 500。

func fetch(t *testing.T, h http.Handler, method, host string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, Prefix+"/", nil)
	if host != "" {
		req.Host = host
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHandlerDrawsWithoutASourceTree(t *testing.T) {
	// root 为空 = 没有源码树（部署出去的二进制就是这样）。装配图照画，
	// import 那半张写成一句解释。
	h := NewHandler(&service{})
	rec := fetch(t, h, http.MethodGet, "127.0.0.1:8899")
	if rec.Code != http.StatusOK {
		t.Fatalf("没有源码树也该出图，实际 %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<svg") && !strings.Contains(body, "const DATA") {
		t.Error("回的好像不是那张图")
	}
	if !strings.Contains(body, "not scanned") {
		t.Error("import 那半张该写明「没扫」，而不是画一张空图（空图看着像没有依赖）")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type 不对: %q", ct)
	}
}

func TestHandlerAnswersLoopbackOnly(t *testing.T) {
	h := NewHandler(&service{})
	for _, host := range []string{"127.0.0.1:8899", "localhost:8899", "[::1]:8899"} {
		if rec := fetch(t, h, http.MethodGet, host); rec.Code != http.StatusOK {
			t.Errorf("Host %q 该被接受，实际 %d", host, rec.Code)
		}
	}
	for _, host := range []string{"evil.com:8899", "10.0.50.11:8899", ""} {
		rec := fetch(t, h, http.MethodGet, host)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Host %q 该被拒（实际 %d）", host, rec.Code)
		}
		// 空 Host 单独说一句：HTTP/1.0 的请求可以没有 Host，而那是脚本工具的
		// 常见形状——它绝不能因为「空串看着像本机」被放行。
		if strings.Contains(rec.Body.String(), "const DATA") {
			t.Errorf("Host %q 被拒了，却还是把图发出去了", host)
		}
	}
}

func TestHandlerIsReadOnly(t *testing.T) {
	h := NewHandler(&service{})
	rec := fetch(t, h, http.MethodPost, "127.0.0.1:8899")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST 该 405（放行等于给「随手 POST 一下」留一个真跑 go list 的入口），实际 %d", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "GET") {
		t.Errorf("405 该说明允许什么，实际 Allow=%q", allow)
	}
}
