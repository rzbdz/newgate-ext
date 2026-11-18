package forward

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestControlUpgradeAuth 优雅交接端点的鉴权底线：错令牌绝不能触发交接。
// 交接会换掉整个 daemon 进程，这里错一点就是把运行时拱手让人。
func TestControlUpgradeAuth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	_ = os.MkdirAll(filepath.Join(dir, "mappings"), 0o755)
	_ = ioutil.WriteFile(filepath.Join(dir, "providers.json"),
		[]byte(`{"providers":{}}`), 0o600)
	const tok = "ctrl-tok-0123456789abcdef0123456789abcdef"
	_ = ioutil.WriteFile(filepath.Join(dir, "state.json"),
		[]byte(`{"default_profile":"ds","port":1,"control_token":"`+tok+`"}`), 0o600)

	srv := newTestServer() // s.ln == nil：鉴权通过后会 503，正好不真交接
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.handleControlUpgrade(w, r)
	}))
	defer front.Close()

	// GET 一律 405
	resp, err := http.Get(front.URL + "/__newgate/upgrade")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Fatalf("GET 应 405，实际 %d", resp.StatusCode)
	}

	// 错令牌：403，而不是 503（503=鉴权已过、只差监听器——顺序不能反）
	for _, auth := range []string{"", "Bearer wrong", "Bearer " + tok + "x"} {
		req, _ := http.NewRequest(http.MethodPost, front.URL+"/__newgate/upgrade", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Fatalf("Authorization %q 应 403，实际 %d: %s", auth, resp.StatusCode, b)
		}
	}

	// 对令牌 + 没有监听器：503 说明「是谁」已经验过、卡在「没东西可交」
	req, _ := http.NewRequest(http.MethodPost, front.URL+"/__newgate/upgrade", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("鉴权通过但监听器未就绪应 503，实际 %d", resp.StatusCode)
	}
}

// TestControlUpgradeNoToken 没有令牌的 daemon（老配置升级窗口期）：
// 交接端点必须一律 403，不能裸奔。
func TestControlUpgradeNoToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	_ = os.MkdirAll(filepath.Join(dir, "mappings"), 0o755)
	_ = ioutil.WriteFile(filepath.Join(dir, "providers.json"),
		[]byte(`{"providers":{}}`), 0o600)
	_ = ioutil.WriteFile(filepath.Join(dir, "state.json"),
		[]byte(`{"default_profile":"ds","port":1}`), 0o600)

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.handleControlUpgrade(w, r)
	}))
	defer front.Close()

	req, _ := http.NewRequest(http.MethodPost, front.URL+"/__newgate/upgrade", nil)
	req.Header.Set("Authorization", "Bearer anything")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || !strings.Contains(string(b), "token") {
		t.Fatalf("无令牌应 403 且说明原因，实际 %d: %s", resp.StatusCode, b)
	}
}
