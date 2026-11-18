package archdiagram

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This diagram lays out the whole internal structure (module names, package paths,
// who depends on whom), so each of its two tests deserves one: **answer only to this
// machine**, **read only**. The third thing to pin is degradation -- with no source tree
// it should still draw the diagram (the assembly half), not a blank screen or a 500.

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
	// root empty = no source tree (that is how a deployed binary looks). The assembly
	// diagram is still drawn, and the import half is written as an explanation.
	h := NewHandler(&service{})
	rec := fetch(t, h, http.MethodGet, "127.0.0.1:8899")
	if rec.Code != http.StatusOK {
		t.Fatalf("the diagram should still render without a source tree, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<svg") && !strings.Contains(body, "const DATA") {
		t.Error("the response does not look like the diagram")
	}
	if !strings.Contains(body, "not scanned") {
		t.Error("the import half should say 'not scanned' instead of drawing an empty graph (an empty graph looks like there are no dependencies)")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("wrong Content-Type: %q", ct)
	}
}

func TestHandlerAnswersLoopbackOnly(t *testing.T) {
	h := NewHandler(&service{})
	for _, host := range []string{"127.0.0.1:8899", "localhost:8899", "[::1]:8899"} {
		if rec := fetch(t, h, http.MethodGet, host); rec.Code != http.StatusOK {
			t.Errorf("Host %q should be accepted, got %d", host, rec.Code)
		}
	}
	for _, host := range []string{"evil.com:8899", "10.0.50.11:8899", ""} {
		rec := fetch(t, h, http.MethodGet, host)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Host %q should be rejected (got %d)", host, rec.Code)
		}
		// Say the empty Host case separately: an HTTP/1.0 request may have no
		// Host, and that is the usual shape for script tools -- it must never
		// be let through because "an empty string looks like this machine".
		if strings.Contains(rec.Body.String(), "const DATA") {
			t.Errorf("Host %q was rejected, yet the diagram was still sent out", host)
		}
	}
}

func TestHandlerIsReadOnly(t *testing.T) {
	h := NewHandler(&service{})
	rec := fetch(t, h, http.MethodPost, "127.0.0.1:8899")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST should get 405 (allowing it leaves a way to trigger a real go list with a casual POST), got %d", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "GET") {
		t.Errorf("405 should state what is allowed, got Allow=%q", allow)
	}
}
