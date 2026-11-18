package forward

import (
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
)

// nakedFixture is a **short-circuiter**: it answers for the upstream, sending not one byte.
//
// Why write one in tests instead of importing the real naked module: that module lives in the
// distribution repo, and what is locked here is the **hot path's** handling of a short-circuit
// (how the response headers echo back, how the log reads), not naked's own criterion. Its truth
// table lives in its own tests -- the same layering rule as shapeTestDetector.
type nakedFixture struct{}

func (nakedFixture) Name() string                { return "naked-fixture" }
func (nakedFixture) Why() string                 { return "short-circuit test fixture" }
func (nakedFixture) Before() []string            { return nil }
func (nakedFixture) After() []string             { return nil }
func (nakedFixture) Match(*special.Request) bool { return false }

func (nakedFixture) Apply(body []byte, _ *special.Request) ([]byte, []string, error) {
	return body, nil, nil
}

func (nakedFixture) Respond([]byte, *special.Request, *domain.State) ([]byte, bool) {
	return []byte(`{"naked":true}`), true
}

// TestShortCircuitEchoesTheProfile locks the profile echo of the short-circuited shot.
//
// Field (2026-09-23, the user's report "the naked feature seems not to work with claude's
// profile-pinned version"): the short-circuit happens **before** building the chain -- by
// design, a plugin answering for the upstream needs no chain -- but the response header had a
// hardcoded `X-Newgate-Profile: n/a`. So short-circuited requests to `/a/claude/p/ds/...` came
// back showing `n/a`, looking like "the profile I named got lost", when the real fact was
// "this shot never resolved to a chain".
//
// Criterion: the `active` computed at the short-circuit point (the value after the
// /p/<profile> override) must be echoed; with no profile at all (skeleton assembly) it falls
// back to `n/a` -- which is what `n/a` was always meant to say; hardcoding it merges two
// distinct facts, "no profile" and "short-circuit", into one.
func TestShortCircuitEchoesTheProfile(t *testing.T) {
	var upstreamHits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding:  domain.Binding{Provider: "anth", Model: "claude-sonnet-4"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	// The short-circuiter registers into a **separate** registry: the package-level one is built
	// by the whole real graph, and adding to it leaks to other cases in the package (and the
	// ordering graph changes with it).
	restore := special.InstallDefault(special.NewRegistry())
	defer restore()
	special.Register(nakedFixture{})

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	// Explicit override: /p/naked-profile/ -- the short-circuit should echo this value back.
	resp, err := http.Post(front.URL+"/a/claude/p/naked-profile/v1/messages", "application/json",
		strings.NewReader(`{"model":"newgate/normal","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 || string(got) != `{"naked":true}` {
		t.Fatalf("the short circuit should return 200 right away with the plugin body, got %d %q", resp.StatusCode, got)
	}
	if hits := resp.Header.Get("X-Newgate-Profile"); hits != "naked-profile" {
		t.Errorf("the profile echoed by the short circuit = %q, want naked-profile (hard-coding n/a makes it impossible to tell which profile handled the request)", hits)
	}
	if route := resp.Header.Get("X-Newgate-Route"); route != "naked:naked-fixture" {
		t.Errorf("X-Newgate-Route = %q, want naked:naked-fixture", route)
	}
	if upstreamHits != 0 {
		t.Errorf("the short-circuiter claimed the request yet the upstream was hit %d times", upstreamHits)
	}
}
