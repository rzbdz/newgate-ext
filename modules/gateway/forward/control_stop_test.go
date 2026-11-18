package forward

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"time"
)

// TestControlStop 跨用户停机端点：方法、令牌、触发三件事都不能含糊。
// 错一个就把停机触发了，等于任何本地进程都能把 daemon 弄死。
func TestControlStop(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	_ = os.MkdirAll(filepath.Join(dir, "mappings"), 0o755)
	// snap()（无 watcher）走 store.Load：providers + state 都得能读
	_ = ioutil.WriteFile(filepath.Join(dir, "providers.json"),
		[]byte(`{"providers":{}}`), 0o600)
	const tok = "ctrl-tok-0123456789abcdef0123456789abcdef"
	_ = ioutil.WriteFile(filepath.Join(dir, "state.json"),
		[]byte(`{"default_profile":"ds","port":1,"control_token":"`+tok+`"}`), 0o600)

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.handleControlStop(w, r)
	}))
	defer front.Close()

	post := func(auth string) int {
		req, _ := http.NewRequest(http.MethodPost, front.URL+"/__newgate/stop", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}

	// GET 一律拒绝（浏览器/预检误触不至于停机）
	resp, err := http.Get(front.URL + "/__newgate/stop")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Fatalf("GET 应 405，实际 %d", resp.StatusCode)
	}

	for _, auth := range []string{"", "Bearer wrong-token", "Bearer " + tok + "x"} {
		if code := post(auth); code != 403 {
			t.Fatalf("Authorization %q 应 403，实际 %d", auth, code)
		}
	}
	select {
	case <-srv.StopRequested():
		t.Fatal("没验过令牌就把停机触发了")
	default:
	}

	// 对的令牌：200 + 触发停机（handler 异步触发，稍等）
	if code := post("Bearer " + tok); code != 200 {
		t.Fatalf("带对令牌应 200，实际 %d", code)
	}
	select {
	case <-srv.StopRequested():
	case <-time.After(2 * time.Second):
		t.Fatal("令牌验过后没有触发停机")
	}

	// 幂等：重复调用不 panic、channel 不二次 close
	srv.RequestStop()
	srv.RequestStop()
}

// TestControlStopNoToken state.json 没有令牌时（老版本升级前的窗口期）
// 端点必须一律 403，而不是裸奔放行。
func TestControlStopNoToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	_ = os.MkdirAll(filepath.Join(dir, "mappings"), 0o755)
	_ = ioutil.WriteFile(filepath.Join(dir, "providers.json"),
		[]byte(`{"providers":{}}`), 0o600)
	_ = ioutil.WriteFile(filepath.Join(dir, "state.json"),
		[]byte(`{"default_profile":"ds","port":1}`), 0o600)

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.handleControlStop(w, r)
	}))
	defer front.Close()

	for _, auth := range []string{"", "Bearer anything"} {
		req, _ := http.NewRequest(http.MethodPost, front.URL+"/__newgate/stop", nil)
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
			t.Fatalf("无令牌状态下 %q 应 403，实际 %d: %s", auth, resp.StatusCode, b)
		}
		if !strings.Contains(string(b), "token") {
			t.Fatalf("403 应说明是令牌问题: %s", b)
		}
	}
}
