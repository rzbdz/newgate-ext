package sshtunnel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// remoteUI is a fake "remote newgate UI": it **behaves like the real product** - the
// absolute /ui/ prefix, a Host gate, and an endpoint that echoes which path it got.
//
// Copying the real thing rather than a random httptest handler, because what is verified
// here is **the rewrite and forwarding correspondence**: without the /ui/ prefix, tests
// could never catch "prefix stripped locally, not added back remotely".
type remoteUI struct {
	srv *httptest.Server
	// seen records each request's path and Host (for assertions).
	mu    chan struct{}
	paths []string
	hosts []string
}

func newRemoteUI(t *testing.T) *remoteUI {
	r := &remoteUI{mu: make(chan struct{}, 1)}
	r.mu <- struct{}{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		<-r.mu
		r.paths = append(r.paths, req.URL.Path)
		r.hosts = append(r.hosts, req.Host)
		r.mu <- struct{}{}

		// The remote's own Host gate (same criteria as web-dashboard's loopbackHost).
		// It is **necessary** here: if the reverse proxy does not keep the client's Host,
		// this gate returns 403 - a failure we want to find early.
		if req.Host != "" && !strings.HasPrefix(req.Host, "127.0.0.1") &&
			!strings.HasPrefix(req.Host, "localhost") {
			http.Error(w, "host not allowed", http.StatusForbidden)
			return
		}
		switch req.URL.Path {
		case "/ui/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<!doctype html><html><head>`+
				`<link rel="icon" href="/ui/favicon.svg">`+
				`<script type="module" src="/ui/assets/index-abc.js"></script>`+
				`</head><body><div id="app"></div></body></html>`)
		case "/ui/api/snapshot":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"where":"remote"}`)
		case "/ui/redirect":
			http.Redirect(w, req, "/ui/s/config", http.StatusFound)
		case "/ui/external":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<a href="https://example.com/ui/x">out</a>`)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *remoteUI) seen() []string {
	<-r.mu
	defer func() { r.mu <- struct{}{} }()
	return append([]string(nil), r.paths...)
}

func (r *remoteUI) seenHosts() []string {
	<-r.mu
	defer func() { r.mu <- struct{}{} }()
	return append([]string(nil), r.hosts...)
}

// harness mounts a route (word for word as in module.go).
type harness struct {
	proxy  *proxy
	pool   *pool
	dialer *fakeDialer
	remote *remoteUI
	loaded Loaded
	h      http.Handler
}

func newHarness(t *testing.T, targets ...Target) *harness {
	t.Helper()
	remote := newRemoteUI(t)
	d := newFakeDialer(strings.TrimPrefix(remote.srv.URL, "http://"))
	p := newPool(d)
	h := &harness{pool: p, dialer: d, remote: remote}
	h.loaded = Loaded{Targets: targets}
	h.proxy = newProxy(p, func() Loaded { return h.loaded })
	// Same shape as module.go: porthub does not strip the prefix; we strip outside the handler.
	h.h = http.StripPrefix(Prefix, h.proxy)
	t.Cleanup(func() {
		// Same order as production shutdown: let the pool drop idle conns (they hold leases), then close.
		h.proxy.closeIdle()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = p.Close(ctx)
	})
	return h
}

// do sends one request, Host is 127.0.0.1:8899 (what a browser would fill).
func (h *harness) do(method, path string) *httptest.ResponseRecorder {
	return h.doAs(method, path, "127.0.0.1:8899")
}

// doAs names a Host. **An empty string is a meaningful input** (HTTP/1.0 may omit Host,
// the common shape of script tools), so it cannot share a signature with "use the
// default" - sharing once made a "empty Host must be refused" assertion test 127.0.0.1.
func (h *harness) doAs(method, path, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	return rec
}

func target(id string) Target {
	return Target{ID: id, Host: "example.invalid", User: "u"}.normalize()
}

// ---------- path mapping really works ----------

func TestTheRouteForwardsOntoTheRemotePrefix(t *testing.T) {
	// This is the feature's skeleton: local /ui/remote/ds/X -> remote /ui/X. Either side
	// wrong, the symptom is a remote 404 - and 404 looks like "the remote lacks it".
	h := newHarness(t, target("ds"))

	rec := h.do("GET", Prefix+"/ds/api/snapshot")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, `"where":"remote"`) {
		t.Errorf("what came back is not the remote body: %s", got)
	}
	seen := h.remote.seen()
	if len(seen) != 1 || seen[0] != "/ui/api/snapshot" {
		t.Errorf("the remote received path %v, want [/ui/api/snapshot]", seen)
	}
	if got := rec.Header().Get("X-Newgate-Tunnel"); got != "ds" {
		t.Errorf("X-Newgate-Tunnel = %q (debugging relies on it to tell where this page came from)", got)
	}
}

func TestTheRootOfTheRouteMapsToTheRemoteRoot(t *testing.T) {
	// `/ui/remote/ds/` must land on the remote's `/ui/`, not `/ui` (the remote 302s that
	// to `/ui/`, and if that Location is unrewritten the browser jumps to **this machine**).
	h := newHarness(t, target("ds"))

	rec := h.do("GET", Prefix+"/ds/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if seen := h.remote.seen(); len(seen) != 1 || seen[0] != "/ui/" {
		t.Errorf("the remote received path %v, want [/ui/]", seen)
	}
}

func TestAPathThatClimbsOutOfTheRemoteUIRootIsRefused(t *testing.T) {
	// This route brings in a **remote machine**, which has `/__newgate` (control plane:
	// upgrade, stop the daemon, change state) and `/v1` (data plane: real model calls)
	// besides `/ui/`. For `/ui/remote/ds/../__newgate/upgrade`, forwarding the suffix
	// would make this machine **forward anyone to the remote control plane** - and the
	// route's gate only looks at Host, not path.
	//
	// Browsers normalize `..` themselves, so this guards against **nonconforming clients**
	// (curl --path-as-is, scripts, other programs). Precisely because a normal browser
	// never hits it, it must have a test: when it breaks there is no symptom.
	h := newHarness(t, target("ds"))

	for _, path := range []string{
		Prefix + "/ds/../__newgate/upgrade",
		Prefix + "/ds/../v1/messages",
		Prefix + "/ds/../../etc/passwd",
		Prefix + "/ds/..%2f..%2f__newgate/upgrade",
	} {
		rec := h.do("GET", path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, rec.Code)
		}
	}
	// Even more important: a refused request must **never touch the remote**, not one byte.
	if seen := h.remote.seen(); len(seen) != 0 {
		t.Errorf("a path climbing out of /ui reached the remote: %v", seen)
	}
}

// ---------- rewriting ----------

func TestTheRemoteIndexComesBackRewritten(t *testing.T) {
	h := newHarness(t, target("ds"))

	rec := h.do("GET", Prefix+"/ds/")
	body := rec.Body.String()
	for _, want := range []string{
		`href="/ui/remote/ds/favicon.svg"`,
		`src="/ui/remote/ds/assets/index-abc.js"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the body does not contain %s:\n%s", want, body)
		}
	}
	// An **unrewritten** /ui/... means the browser will ask this machine for that asset.
	if strings.Contains(body, `"/ui/assets/`) {
		t.Errorf("an absolute prefix was left unwritten, so the browser will hit this machine's own UI:\n%s", body)
	}
}

func TestTheRemoteRedirectComesBackRewritten(t *testing.T) {
	// The only "invisible" error in this rewriting: an unrewritten Location sends the user
	// from a remote UI to **this machine's own** with one click, and they look alike.
	h := newHarness(t, target("ds"))

	rec := h.do("GET", Prefix+"/ds/redirect")
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/ui/remote/ds/s/config" {
		t.Errorf("Location = %q, want /ui/remote/ds/s/config", got)
	}
}

func TestExternalLinksAreNotHijacked(t *testing.T) {
	// External links happen to contain /ui/ too (someone else's site path). Rewriting them
	// **silently points the user's outbound link back at us** - and when wrong, no symptom.
	h := newHarness(t, target("ds"))

	rec := h.do("GET", Prefix+"/ds/external")
	if got := rec.Body.String(); !strings.Contains(got, `https://example.com/ui/x`) {
		t.Errorf("an external link was redirected: %s", got)
	}
}

// ---------- the remote gate ----------

func TestTheClientsHostReachesTheRemote(t *testing.T) {
	// A reverse proxy replaces Host with the target by default. The remote BFF's rebinding
	// gate only lets IP literals and localhost through, so a replacement is an **immediate
	// 403** remotely while everything local is fine - an error that sends people from
	// rewriting all the way to the remote sshd.
	//
	// `localhost:8899` rather than a LAN IP: both should pass, and the former needs no real NIC.
	h := newHarness(t, target("ds"))

	if rec := h.doAs("GET", Prefix+"/ds/api/snapshot", "localhost:8899"); rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if hosts := h.remote.seenHosts(); len(hosts) != 1 || hosts[0] != "localhost:8899" {
		t.Errorf("the remote received Host %v, want [localhost:8899] - changing it makes the remote 403", hosts)
	}
}

func TestTheRouteItselfRefusesNonLoopbackHosts(t *testing.T) {
	// The local gate is this route's **only** one: the remote checks the Host we forward,
	// and that value is what the browser filled in.
	h := newHarness(t, target("ds"))

	for _, host := range []string{"evil.com:8899", "10.0.50.11:8899", ""} {
		rec := h.doAs("GET", Prefix+"/ds/", host)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Host %q should be rejected, got %d", host, rec.Code)
		}
	}
	if seen := h.remote.seen(); len(seen) != 0 {
		t.Errorf("a rejected request must not reach the remote, got %v", seen)
	}
}

// ---------- boundaries ----------

func TestTheBareRouteRedirectsToTheSlashForm(t *testing.T) {
	// Without a trailing slash, browsers resolve relative addresses against the document -
	// from `/ui/remote/ds`, `api/snapshot` becomes `/ui/remote/api/snapshot`, api as an id.
	for _, path := range []string{Prefix, Prefix + "/ds"} {
		h := newHarness(t, target("ds"))
		rec := h.do("GET", path)
		if rec.Code != http.StatusFound {
			t.Fatalf("%s: status %d, want 302", path, rec.Code)
		}
		want := Prefix + "/"
		if path != Prefix {
			want = Prefix + "/ds/"
		}
		if got := rec.Header().Get("Location"); got != want {
			t.Errorf("%s -> Location %q, want %q", path, got, want)
		}
	}
}

func TestAnUnknownTargetListsTheOnesThatExist(t *testing.T) {
	// Typing the address by hand is this route's most common use (copy a link from a
	// terminal). One wrong character, and a bare 404 makes the whole tunnel look broken.
	h := newHarness(t, target("ds"), target("ark"))

	rec := h.do("GET", Prefix+"/nope/")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"nope", "ark", "ds"} {
		if !strings.Contains(body, want) {
			t.Errorf("the 404 body has no %q: %s", want, body)
		}
	}
}

func TestTheIndexListsEveryTarget(t *testing.T) {
	h := newHarness(t, target("ds"), target("ark"))
	rec := h.do("GET", Prefix+"/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	for _, want := range []string{Prefix + "/ark/", Prefix + "/ds/"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("the index has no %s: %s", want, rec.Body.String())
		}
	}
	if seen := h.remote.seen(); len(seen) != 0 {
		t.Errorf("the index is drawn by this machine itself and must not touch the remote: %v", seen)
	}
}

// ---------- dialing and failure ----------

func TestTheRouteDialsOnceAndThenReuses(t *testing.T) {
	// Reuse decides whether this module has practical value: an SSH handshake is public-key
	// math (50-200ms on a LAN), costlier than one API round trip to the remote UI. Per
	// request, opening a console pays dozens of handshakes.
	h := newHarness(t, target("ds"))

	for i := 0; i < 5; i++ {
		if rec := h.do("GET", Prefix+"/ds/api/snapshot"); rec.Code != http.StatusOK {
			t.Fatalf("request %d status %d", i, rec.Code)
		}
	}
	if dials, _ := h.dialer.counts(); dials != 1 {
		t.Errorf("five requests dialed %d times, want 1", dials)
	}
	snap, _ := h.pool.Snapshot("ds")
	if snap.State != StateUp {
		t.Errorf("status is %q, want up", snap.State)
	}
}

func TestAFailedDialIsNotCached(t *testing.T) {
	// Failures are **not cached**: caching adds a backoff wait after the network recovers,
	// and users only see "I rebooted the router and it still will not connect".
	h := newHarness(t, target("ds"))
	h.dialer.setFail(errors.New("boom"))

	if rec := h.do("GET", Prefix+"/ds/"); rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", rec.Code)
	}
	if snap, _ := h.pool.Snapshot("ds"); snap.State != StateDown || snap.LastErr == "" {
		t.Errorf("after a failure the status should be down with a reason, got %+v", snap)
	}

	// After recovery it connects at once (no backoff).
	h.dialer.setFail(nil)
	if rec := h.do("GET", Prefix+"/ds/"); rec.Code != http.StatusOK {
		t.Fatalf("after recovery status %d, want 200", rec.Code)
	}
}

func TestTheErrorPageNamesTheTargetAndTheNextStep(t *testing.T) {
	// An empty 502 sends people off debugging "is my browser broken".
	h := newHarness(t, target("ds"))
	h.dialer.setFail(errors.New("connection refused by 10.0.50.11:22"))

	rec := h.do("GET", Prefix+"/ds/")
	body := rec.Body.String()
	if !strings.Contains(body, "connection refused") {
		t.Errorf("the error body has no reason: %s", body)
	}
	if !strings.Contains(body, "newgate tunnel test ds") {
		t.Errorf("the error body has no next step: %s", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q (half of these strings come from the remote; not wrapping them in HTML means no escaping)", ct)
	}
}

func TestTheRouteFollowsTheConfigWithoutRestart(t *testing.T) {
	// A target added in the browser must apply to the next request. Caching config turns
	// "just changed, not effective" into a weird phenomenon only a restart fixes.
	h := newHarness(t) // none of them exists at the start

	if rec := h.do("GET", Prefix+"/ds/"); rec.Code != http.StatusNotFound {
		t.Fatalf("with no targets yet status %d, want 404", rec.Code)
	}
	h.loaded = Loaded{Targets: []Target{target("ds")}}
	if rec := h.do("GET", Prefix+"/ds/api/snapshot"); rec.Code != http.StatusOK {
		t.Fatalf("after adding a target status %d, want 200", rec.Code)
	}
}
