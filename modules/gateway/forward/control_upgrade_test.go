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

// TestControlUpgradeAuth is the authentication floor of the graceful handoff
// endpoint: a wrong token must never trigger a handoff. Handoff replaces
// the whole daemon process, so a slip here hands the runtime to anyone.
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

	// GET is always 405
	resp, err := http.Get(front.URL + "/__newgate/upgrade")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Fatalf("GET should be 405, got %d", resp.StatusCode)
	}

	// wrong token: 403, not 503 (503 = authenticated, only the
	// listener is missing - the order must not be reversed)
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
			t.Fatalf("Authorization %q should be 403, got %d: %s", auth, resp.StatusCode, b)
		}
	}

	// right token + no listener: 503 says "who you are" has
	// been verified, and it is stuck at "nothing to hand over"
	req, _ := http.NewRequest(http.MethodPost, front.URL+"/__newgate/upgrade", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("authenticated but the listener is not ready should be 503, got %d", resp.StatusCode)
	}
}

// TestControlUpgradeNoToken: a daemon with no token (the upgrade window of an old
// config): the handoff endpoint must always answer 403, never let requests through.
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
		t.Fatalf("no token should be 403 with a reason, got %d: %s", resp.StatusCode, b)
	}
}
