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

// TestControlStop covers the cross-user stop endpoint: method, token
// and trigger, none of which may be fuzzy. Get one wrong and the
// stop triggers anyway - any local process could kill the daemon.
func TestControlStop(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	_ = os.MkdirAll(filepath.Join(dir, "mappings"), 0o755)
	// snap() (no watcher) goes through store.Load: both providers + state must be readable
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

	// GET is always rejected (so a browser / preflight mistake does not stop the daemon)
	resp, err := http.Get(front.URL + "/__newgate/stop")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Fatalf("GET should be 405, got %d", resp.StatusCode)
	}

	for _, auth := range []string{"", "Bearer wrong-token", "Bearer " + tok + "x"} {
		if code := post(auth); code != 403 {
			t.Fatalf("Authorization %q should be 403, got %d", auth, code)
		}
	}
	select {
	case <-srv.StopRequested():
		t.Fatal("the stop was triggered without verifying the token")
	default:
	}

	// right token: 200 + stop triggered (the handler triggers asynchronously, wait a bit)
	if code := post("Bearer " + tok); code != 200 {
		t.Fatalf("with the right token it should be 200, got %d", code)
	}
	select {
	case <-srv.StopRequested():
	case <-time.After(2 * time.Second):
		t.Fatal("after the token verified, the stop was not triggered")
	}

	// idempotent: repeated calls do not panic, the channel is not closed twice
	srv.RequestStop()
	srv.RequestStop()
}

// TestControlStopNoToken: when state.json has no token (the window before an old-version
// upgrade), the endpoint must always answer 403, not let requests through unprotected.
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
			t.Fatalf("without a token %q should be 403, got %d: %s", auth, resp.StatusCode, b)
		}
		if !strings.Contains(string(b), "token") {
			t.Fatalf("the 403 should say it is a token problem: %s", b)
		}
	}
}
