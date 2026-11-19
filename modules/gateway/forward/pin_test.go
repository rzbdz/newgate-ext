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

// This group locks the other half of the 2026-11-04 field event: naming (`/p/<name>`) narrows
// the profile set to the named one before the chain is built (pinnedProfiles).
//
// steps[:1] after resolving is not enough: when the named profile has no usable candidate at
// all (its only binding broken), the chain head already stands on someone else's profile, and
// the truncation leaves exactly "silently swapped to someone else". Here **real config + real
// upstream** separate the two:
//   - not named, the named real model unavailable → fall back to another profile on the same
//     tier chain, request still 200 (that is the resolve half's behavior);
//   - named, same config → never touch anyone else's upstream, honestly 404 and say that
//     binding was pulled.

// denyBinding answers one thing only: this binding is unavailable now (simulating a breaker).
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

// pinRig is this test's scene: two profiles + two fake upstreams.
type pinRig struct {
	srv    *Server
	dir    string
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
	return &pinRig{srv: srv, dir: dir, logs: logs, front: front, hitsA: &hA, hitsDS: &hDS}
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

// TestUnpinnedKnownModelUnavailableFallsBack: when not named, the model name is still known to
// belong to mid, only that binding is pulled now → walk the mid chain's other candidates
// (before 2026-11-04 this reported "the model is not in any profile's bindings").
func TestUnpinnedKnownModelUnavailableFallsBack(t *testing.T) {
	rig := newPinRig(t)
	code, body := rig.post(t, "/v1/chat/completions")
	if code != 200 || body != `{"ok":true}` {
		t.Fatalf("should fall back to the mid candidates of a and return 200; got %d %q\n%s", code, body, rig.logs.String())
	}
	if n := atomic.LoadInt64(rig.hitsA); n == 0 {
		t.Error("the upstream of a was never hit - no fallback happened")
	}
	// The swap must be visible in the log: the client named deepseek-flash, and a-model served it.
	if !strings.Contains(rig.logs.String(), "deepseek-flash -> a/a-model") {
		t.Errorf("the provider/model actually used should be in the log; log:\n%s", rig.logs.String())
	}
}

// TestPinnedProfileNeverSwitchesProfile: with ds named, pulling ds's candidate must **not**
// let a's candidate stand in -- that is exactly the silent swap naming forbids.
//
// The status is 503, not 404 (changed 2026-11-19): the model exists, the profile exists, and the
// tier it belongs to exists; what is missing is a **usable** candidate right now, which is a
// transient upstream condition and the same answer the unpinned path gives for a delisted
// binding. 404 said "there is nothing here", which is what sent the 2026-11-19 incident's user
// looking through their config while the real cause sat in the skip list.
func TestPinnedProfileNeverSwitchesProfile(t *testing.T) {
	rig := newPinRig(t)
	code, body := rig.post(t, "/a/claude/p/ds/v1/chat/completions")
	if code != 503 {
		t.Fatalf("with ds pinned and no usable candidate for ds it should be 503; got %d %q\n%s", code, body, rig.logs.String())
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
	if !strings.Contains(body, "profile ds has no usable candidate") {
		t.Errorf("the error should name the pinned profile and say it has no usable candidate: %q", body)
	}
	if !strings.Contains(rig.logs.String(), "circuit open") {
		t.Errorf("the reason should stay among the skip reasons (never silent); log:\n%s", rig.logs.String())
	}
	// The data plane must not name the policy that skipped it (see modules/gateway/direction_test.go);
	// it points at `newgate tier`, which is where the owner explains itself.
	if strings.Contains(body, "breaker") {
		t.Errorf("forward must not name a concrete strategy in its message: %q", body)
	}
}

// TestFallbackOffPinsTheChainHead is the 2026-11-19 incident as a test.
//
// `newgate fallback off` used to only truncate the chain after building it, so when every
// candidate of the **head profile** was delisted the builder walked into the next profile and
// the truncation kept that head: the user asked for claude, had fallback off, and was served by
// DeepSeek with every mechanism reporting success. Now fallback-off pins the head profile, so
// the request fails loudly instead of silently changing which subscription pays for it.
func TestFallbackOffPinsTheChainHead(t *testing.T) {
	rig := newPinRig(t) // this rig's only profile with a usable mid binding is ds
	// Flip the global switch on disk: this Server has no Watch, so every request re-reads the
	// config (see snap) -- which is also why the test can do this mid-flight.
	writeFile(t, rig.dir, "state.json",
		`{"default_profile":"ds","chain":{"max_attempts":5},"gateway":{"fallback":false}}`)
	code, body := rig.post(t, "/a/claude/v1/chat/completions")
	if code != 503 {
		t.Fatalf("the head profile has nothing usable and fallback is off, so the call must fail "+
			"(503) rather than silently run on a's model; got %d %q\n%s", code, body, rig.logs.String())
	}
	if n := atomic.LoadInt64(rig.hitsA); n != 0 {
		t.Errorf("fallback is off, yet another profile's upstream was hit %d times - the silent "+
			"profile switch is back", n)
	}
	if n := atomic.LoadInt64(rig.hitsDS); n != 0 {
		t.Errorf("a delisted binding must not be hit, got %d times", n)
	}
	if !strings.Contains(body, "fallback is off") || !strings.Contains(body, "profile ds") {
		t.Errorf("the message must say which profile was pinned and why: %q", body)
	}
	if !strings.Contains(rig.logs.String(), "circuit open") {
		t.Errorf("the skip reason must stay in the log (never silent); log:\n%s", rig.logs.String())
	}
}

// writeFile writes one config file under dir, creating the parent directory.
func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
