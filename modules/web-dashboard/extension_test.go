package webdashboard

import (
	"encoding/json"
	"net/http"
	"testing"
)

// 扩展端点是 BFF 的**第二条口子**：概念账本管「画得出来的东西」，这条管「点一下
// 要做一件事」。它仍然不认识任何模块——只认「有人登记了这个名字」。
//
// 这几条锁的是那句话：BFF 只做形状，不做业务。

func registerExtension(t *testing.T, h *Handler, name string, run ExtensionHandler) func() error {
	t.Helper()
	rel, err := h.extensions.Register(name, run)
	if err != nil {
		t.Fatalf("登记 %s: %v", name, err)
	}
	return rel
}

func TestExtensionForwardsTheRequestOpaque(t *testing.T) {
	h := newHandler()
	var got ExtensionRequest
	registerExtension(t, h, "echo", func(r ExtensionRequest) (any, error) {
		got = r
		return map[string]any{"ok": true, "body": string(r.Body)}, nil
	})

	rec := post(t, h, "/api/ext/echo?x=1", `{"provider":"p","refresh":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("扩展该 200，实际 %d: %s", rec.Code, rec.Body)
	}
	if got.Method != http.MethodPost {
		t.Errorf("扩展该看到原始请求方法，实际 %q", got.Method)
	}
	if got.Query.Get("x") != "1" {
		t.Errorf("查询串该原样传下去，实际 %v", got.Query)
	}
	if string(got.Body) != `{"provider":"p","refresh":true}` {
		t.Errorf("请求体该**逐字**传下去（BFF 不解析它），实际 %s", got.Body)
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if doc["ok"] != true {
		t.Errorf("响应该原样端出扩展的返回值，实际 %v", doc)
	}
}

func TestExtensionErrorsAndGates(t *testing.T) {
	h := newHandler()
	registerExtension(t, h, "boom", func(ExtensionRequest) (any, error) {
		return nil, errString("the provider is unreachable")
	})

	cases := []struct {
		name string
		path string
		body string
		want int
	}{
		{"没登记的名字", "/api/ext/nope", `{}`, http.StatusNotFound},
		{"空名字", "/api/ext/", `{}`, http.StatusNotFound},
		{"名字里带斜杠", "/api/ext/a/b", `{}`, http.StatusNotFound},
		{"扩展自己报错", "/api/ext/boom", `{}`, http.StatusBadRequest},
		{"坏 JSON", "/api/ext/boom", `{`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := post(t, h, c.path, c.body)
			if rec.Code != c.want {
				t.Errorf("%s 该 %d，实际 %d: %s", c.path, c.want, rec.Code, rec.Body)
			}
		})
	}

	// GET 不是这条口子的形状：扩展会去碰上游、改运行期状态，没有理由让它比保存
	// 更宽松。
	rec := get(t, h, "/api/ext/boom")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET 该 405，实际 %d", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodPost {
		t.Errorf("405 该带 Allow: POST，实际 %q", allow)
	}

	// 与 apply 同一道门：跨站表单能发 text/plain，发不出 application/json。
	rec = raw(t, h, http.MethodPost, "/api/ext/boom", `{}`, loopback, "text/plain")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("没声明 application/json 该 415，实际 %d", rec.Code)
	}
}

func TestExtensionRegistrationRejectsCollisions(t *testing.T) {
	h := newHandler()
	rel := registerExtension(t, h, "one", func(ExtensionRequest) (any, error) { return nil, nil })

	if _, err := h.extensions.Register("one", func(ExtensionRequest) (any, error) { return nil, nil }); err == nil {
		t.Fatal("重名该当场报错——否则只有先登记的那个能收到请求，另一个静默失效")
	}
	if _, err := h.extensions.Register("Bad Name", func(ExtensionRequest) (any, error) { return nil, nil }); err == nil {
		t.Fatal("名字里的空格/大写该被拒绝（它会进 URL 路径段）")
	}
	if _, err := h.extensions.Register("nil", nil); err == nil {
		t.Fatal("nil handler 该被拒绝")
	}

	// 撤销之后那个名字该真的消失：界面手里的快照可能还在，于是它下一次点击
	// 得到 404，而不是一个已经停掉的模块还挂着。
	if err := rel(); err != nil {
		t.Fatal(err)
	}
	if rec := post(t, h, "/api/ext/one", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("撤销之后该 404，实际 %d", rec.Code)
	}
}

// errString 是一个只有 Error() 的 error 替身：测试不关心错误类型，只关心那句话
// 原样到了界面上。
type errString string

func (e errString) Error() string { return string(e) }
