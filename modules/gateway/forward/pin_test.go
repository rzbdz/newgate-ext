package forward

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/gateway/policy"
	"github.com/rzbdz/newgate-ext/modules/gateway/thinkcache"
)

// 这一组锁的是 2026-11-04 现场的另一半：点名（`/p/<name>`）在建链前就把
// profile 集合收敛到被点名的那一份（pinnedProfiles）。
//
// 只靠解析之后的 steps[:1] 不够：被点名的 profile 一个可用候选都没有时
// （唯一 binding 被熔断），链头位置站的已经是别人的 profile，截断留下的正是
// 「静默换到别人」。这里用**真配置 + 真上游**把两件事分开验：
//   - 不点名时，点名的真实模型名不可用 → 按同档位链 fallback 到别的 profile，
//     请求照常 200（这是 resolve 那半边的行为）；
//   - 点名时，同一份配置 → 绝不碰别人的上游，如实 404 并说明那条 binding 被摘了。

// denyBinding 只回答一件事：这条 binding 现在不可用（模拟熔断）。
type denyBinding struct {
	provider string
	model    string
	reason   string
}

func (d denyBinding) Name() string { return "test-deny-binding" }
func (d denyBinding) Why() string  { return "pin fallback test: reject one binding by name" }
func (d denyBinding) Admit(provider, model string) (bool, string) {
	if provider == d.provider && model == d.model {
		return false, d.reason
	}
	return true, ""
}
func (d denyBinding) Rank(provider, model string, _ int) int { return policy.NeutralRank }

// pinRig 是这条测试的现场：两份 profile + 两台假上游。
type pinRig struct {
	srv    *Server
	logs   *syncBuffer
	front  *httptest.Server
	hitsA  *int64
	hitsDS *int64
}

func newPinRig(t *testing.T) *pinRig {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	t.Setenv("NEWGATE_TARGET_DIR", dir)
	t.Setenv("HOME", dir)
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var hA, hDS int64
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hA, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upA.Close)
	upDS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hDS, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upDS.Close)

	providers := map[string]interface{}{
		"providers": map[string]interface{}{
			"a":  map[string]string{"base_url": upA.URL, "api_key": "sk-a"},
			"ds": map[string]string{"base_url": upDS.URL, "api_key": "sk-ds"},
		},
	}
	raw, err := json.Marshal(providers)
	if err != nil {
		t.Fatal(err)
	}
	write("providers.json", string(raw))
	write("state.json", `{"default_profile":"a","chain":{"max_attempts":5}}`)
	write("mappings/a.kv", "mid=a/a-model\n")
	write("mappings/ds.kv", "prio=19\nmid=ds/deepseek-flash\n")

	thinkcache.Default = thinkcache.New(32<<20, 0)

	srv, logs := newLoggingTestServer()
	if _, err := srv.Filters.Register(denyBinding{
		provider: "ds", model: "deepseek-flash", reason: "circuit open"}); err != nil {
		t.Fatalf("registering the admission policy: %v", err)
	}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	t.Cleanup(front.Close)
	return &pinRig{srv: srv, logs: logs, front: front, hitsA: &hA, hitsDS: &hDS}
}

func (r *pinRig) post(t *testing.T, path string) (int, string) {
	t.Helper()
	resp, err := http.Post(r.front.URL+path, "application/json",
		strings.NewReader(`{"model":"deepseek-flash","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(body)
}

// TestUnpinnedKnownModelUnavailableFallsBack：没点名时，模型名仍然认识它属于
// mid，只是那条 binding 现在被摘了 → 按 mid 链的其它候选走（2026-11-04 之前
// 这里报的是「模型不在任何 profile 的绑定里」）。
func TestUnpinnedKnownModelUnavailableFallsBack(t *testing.T) {
	rig := newPinRig(t)
	code, body := rig.post(t, "/v1/chat/completions")
	if code != 200 || body != `{"ok":true}` {
		t.Fatalf("should fall back to the mid candidates of a and return 200; got %d %q\n%s", code, body, rig.logs.String())
	}
	if n := atomic.LoadInt64(rig.hitsA); n == 0 {
		t.Error("the upstream of a was never hit - no fallback happened")
	}
	// 换人要在日志里看得见：客户端点名的是 deepseek-flash，实际落到 a-model。
	if !strings.Contains(rig.logs.String(), "deepseek-flash -> a/a-model") {
		t.Errorf("the provider/model actually used should be in the log; log:\n%s", rig.logs.String())
	}
}

// TestPinnedProfileNeverSwitchesProfile：点名 ds 时，ds 的候选被摘掉也**不许**
// 拿 a 的候选顶上——那正是点名要禁止的静默换人。404 里要说清是「认出档位了，
// 但它此刻没有可用候选」，不再是「这个模型不存在」。
func TestPinnedProfileNeverSwitchesProfile(t *testing.T) {
	rig := newPinRig(t)
	code, body := rig.post(t, "/a/claude/p/ds/v1/chat/completions")
	if code != 404 {
		t.Fatalf("with ds pinned and no usable candidate for ds it should be 404; got %d %q\n%s", code, body, rig.logs.String())
	}
	if n := atomic.LoadInt64(rig.hitsA); n != 0 {
		t.Errorf("ds was pinned yet the upstream of a was hit %d times - the silent profile switch is back", n)
	}
	if n := atomic.LoadInt64(rig.hitsDS); n != 0 {
		t.Errorf("the removed binding must not be hit, got %d times", n)
	}
	if strings.Contains(body, "neither a known tier") {
		t.Errorf("the model is bound to the mid tier of ds, so it must not say 'not in any profile binding': %q", body)
	}
	if !strings.Contains(body, "bound to tier mid") {
		t.Errorf("the 404 should say which tier the model is bound to and why there is no usable candidate: %q", body)
	}
	if !strings.Contains(rig.logs.String(), "circuit open") {
		t.Errorf("the reason should stay among the skip reasons (never silent); log:\n%s", rig.logs.String())
	}
}
