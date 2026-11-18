package forward

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/lib/porthub"
)

// Who the port goes to -- the two halves of this seam. It locks the **direction**: the data
// plane does not know the mount table, it only knows it has a "root handler" slot, and what
// gets installed there is the daemon entry's decision.
//
// Why two tests: get this seam wrong and nothing else goes red -- it compiles, forwarding
// works, and the symptom is "the web UI 404s in the browser while the log looks normal". And
// the easiest way to break it again is precisely "just look up the table in the catch-all",
// which is the rejected implementation.

// TestTheDataPlaneServesItsOwnPortByDefault: with no root handler installed, the port is the
// data plane's own -- how it looked before this mechanism existed (testing/system's harness
// takes this path).
func TestTheDataPlaneServesItsOwnPortByDefault(t *testing.T) {
	isolate(t)
	s := newTestServer()

	rec := httptest.NewRecorder()
	s.rootHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/index.html", nil))

	// No usable upstream in the test, so the answer is the data plane's failure -- the point is
	// that this path **reached the data plane** (it has something to say), not that someone caught
	// it, and not a blank response.
	if rec.Body.Len() == 0 {
		t.Fatal("with no root handler the port should be answered by the data plane, but nothing was said at all")
	}
	if rec.Code == http.StatusOK {
		t.Errorf("without an upstream the data plane must not return 200 (this case exists to confirm the request really reached the data plane), got %d: %s",
			rec.Code, rec.Body.String())
	}
}

// TestSetRootHandsThePortOver: once a root handler is installed, it answers on the port -- the
// data plane is no longer the master of this path. This is the precondition for a web UI
// living on the same port.
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
		t.Fatalf("once the root handler is installed it should answer, got: %q", got)
	}
}

// TestHandlerIsStable: Handler() gets called twice (porthub takes it as the fallback, Start may
// ask again). Only getting **the same one** twice counts -- otherwise porthub falls back to the
// first mux while the port serves the second, and the difference between them is silent.
func TestHandlerIsStable(t *testing.T) {
	isolate(t)
	s := newTestServer()
	if s.Handler() != s.Handler() {
		t.Error("Handler() returned two different muxes on two calls - the fallback and the port service would not be the same thing")
	}
}

// TestRootSeamComposesWithTheHub: run the two halves joined (what the daemon entry does: take
// the data plane's handler as the fallback, install the hub's root). What is verified is the
// dispatch **after composition**: a mounted service wins, and unclaimed paths fall back to the
// data plane.
func TestRootSeamComposesWithTheHub(t *testing.T) {
	isolate(t)
	s := newTestServer()
	hub := porthub.NewRegistry()
	if _, err := hub.Mount("/ui", "web", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("web"))
	})); err != nil {
		t.Fatalf("mount failed: %v", err)
	}
	root, err := hub.Root("gateway", s.Handler())
	if err != nil {
		t.Fatalf("Root failed: %v", err)
	}
	s.SetRoot(root)

	rec := httptest.NewRecorder()
	s.rootHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/index.html", nil))
	if got := rec.Body.String(); got != "web" {
		t.Errorf("the service mounted at /ui got no request, got: %q", got)
	}

	// The data plane's own paths are unaffected (`/a/...` is the data plane's forwarding path).
	rec2 := httptest.NewRecorder()
	s.rootHandler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if strings.Contains(rec2.Body.String(), "web") {
		t.Errorf("/v1/models was caught by web - the mount table stole a data-plane path: %q", rec2.Body.String())
	}
}
