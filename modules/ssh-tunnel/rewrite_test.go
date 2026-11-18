package sshtunnel

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// This family is **this rewriting's** entire criterion. It deserves its own file, longer
// than the code, because rewriting is the route's only place where "wrong is not an error":
//
//   - one miss -> the browser asks this machine for a missing asset -> blank page;
//   - one extra -> user data is touched;
//   - Location unrewritten -> **silently jumps to this machine's own UI**, which looks alike.
//
// The last is the only one of the three that "looks good", so it has its own test.

// ---------- prefix replacement ----------

func TestOnlyTheAbsolutePrefixIsRewritten(t *testing.T) {
	local := LocalPrefix("ds")
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "the assets references in index.html",
			in:   `<script type="module" crossorigin src="/ui/assets/index-CpR3oezi.js"></script>`,
			want: `<script type="module" crossorigin src="/ui/remote/ds/assets/index-CpR3oezi.js"></script>`,
		},
		{
			name: "favicon",
			in:   `<link rel="icon" type="image/svg+xml" href="/ui/favicon.svg" />`,
			want: `<link rel="icon" type="image/svg+xml" href="/ui/remote/ds/favicon.svg" />`,
		},
		{
			// This is the one that makes the UI work: Vite folded
			// `import.meta.env.BASE_URL + "api"` into this literal at build time.
			// Unrewritten, the browser hits **this machine's own** web-dashboard with
			// /ui/api - a local UI mixing in remote data, worse than a blank page.
			name: "the API base folded out of JS",
			in:   `const API="/ui/"+"api";`,
			want: `const API="/ui/remote/ds/"+"api";`,
		},
		{
			name: "appears several times in the same body",
			in:   `/ui/a /ui/b /ui/c`,
			want: `/ui/remote/ds/a /ui/remote/ds/b /ui/remote/ds/c`,
		},
		{
			name: "relative addresses are left alone (they follow the current document and are already right)",
			in:   `<script src="./assets/x.js"></script>`,
			want: `<script src="./assets/x.js"></script>`,
		},
		{
			name: "absolute addresses with a scheme are left alone (they point elsewhere)",
			in:   `<a href="https://example.com/ui/x">`,
			want: `<a href="https://example.com/ui/x">`,
		},
		{
			name: "/ui without a trailing slash is left alone - it is not a prefix, it is another address",
			in:   `<a href="/ui">`,
			want: `<a href="/ui">`,
		},
		{
			name: "another prefix /uix is not hit by mistake",
			in:   `<a href="/uix/thing">`,
			want: `<a href="/uix/thing">`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(rewritePrefix([]byte(tc.in), local))
			if got != tc.want {
				t.Errorf("\n in: %s\nwant: %s\n got: %s", tc.in, tc.want, got)
			}
		})
	}
}

// TestTheApiBaseInsideTheBundleIsRewritten: this is **the bug users actually hit**.
//
// Symptom: the tunneled UI renders fine, but the data is all from **this machine**. The
// artifact folds `import.meta.env.BASE_URL + "api"` into `const Fn="/ui/api"` at build
// time, and the route then only rewrote text/html - JS passed through, so that page sent
// requests to the local /ui/api.
//
// The assertion uses **that real artifact snippet**: a bundle-like text, confirming the
// API base in it moved onto this route.
func TestTheApiBaseInsideTheBundleIsRewritten(t *testing.T) {
	local := LocalPrefix("snode1")
	bundle := `const Zc=1,Fn="/ui/api";function ec(n){return fetch(Fn+"/snapshot")}`
	got := string(rewritePrefix([]byte(bundle), local))
	if !strings.Contains(got, `Fn="/ui/remote/snode1/api"`) {
		t.Fatalf("the API base in the artifact was not rewritten, so that page would hit this machine's own API:\n%s", got)
	}
}

func TestRewriteLeavesUntouchedBodiesAlone(t *testing.T) {
	// Most responses lack the prefix (JSON, images, the one 404 sentence). This checks
	// "no hit returns the same slice" - not to save an allocation, but to make
	// "what rewriting touched" assertable in tests.
	body := []byte(`{"ok":true,"items":[]}`)
	got := rewritePrefix(body, LocalPrefix("ds"))
	if !bytes.Equal(got, body) {
		t.Errorf("a body without /ui/ was changed: %s", got)
	}
}

// ---------- Location ----------

func TestRedirectsStayInsideTheRoute(t *testing.T) {
	// Without rewriting Location, it **silently jumps to this machine's own UI**: the
	// remote BFF 302s `/ui` (no trailing slash) to `/ui/`, which locally is web-dashboard -
	// one click takes the user from the "remote UI" to the "local UI", and they look
	// alike. The only invisible error in the whole rewriting.
	local := LocalPrefix("ds")
	cases := []struct{ in, want string }{
		{"/ui/", "/ui/remote/ds/"},
		{"/ui/s/config", "/ui/remote/ds/s/config"},
		// Off-site and relative addresses are never touched.
		{"https://example.com/ui/", "https://example.com/ui/"},
		{"other/page", "other/page"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := rewriteLocation(tc.in, local); got != tc.want {
			t.Errorf("Location %q -> %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---------- should it rewrite ----------

func resp(status int, ctype, encoding string) *http.Response {
	h := http.Header{}
	if ctype != "" {
		h.Set("Content-Type", ctype)
	}
	if encoding != "" {
		h.Set("Content-Encoding", encoding)
	}
	return &http.Response{StatusCode: status, Header: h}
}

func TestOnlyHTMLIsRewritten(t *testing.T) {
	cases := []struct {
		name string
		resp *http.Response
		want bool
	}{
		{"index.html", resp(200, "text/html; charset=utf-8", ""), true},
		{"the error page is its own HTML too", resp(404, "text/html", ""), true},
		// **JS must be rewritten**: that /ui/ is not an asset address but vite baking
		// BASE_URL in, used to build API URLs at runtime - `const Fn="/ui/api"`. Unrewritten,
		// the page sends requests to **this machine's own** API: it renders, data is local.
		{"JS should be rewritten (the API base is baked in)", resp(200, "text/javascript; charset=utf-8", ""), true},
		{"application/javascript should be rewritten too", resp(200, "application/javascript", ""), true},
		{"CSS should be rewritten (absolute prefixes inside url())", resp(200, "text/css", ""), true},
		{"JSON is not rewritten (it is data, not addresses)", resp(200, "application/json", ""), false},
		{"images are not rewritten", resp(200, "image/svg+xml", ""), false},
		// A compressed body has no plaintext path; touching it just destroys the response.
		{"compressed bodies are not rewritten", resp(200, "text/html", "gzip"), false},
		// 5xx is usually generated by a proxy/gateway, not the remote's UI.
		{"5xx is not rewritten", resp(502, "text/html", ""), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRewrite(tc.resp); got != tc.want {
				t.Errorf("shouldRewrite = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------- path mapping ----------

func TestLocalPathsMapOntoTheRemoteOnes(t *testing.T) {
	// This is the foundation of all rewriting, and where **both sides must hold**: the
	// body's absolute address becomes /ui/remote/<id>/..., the browser sends that back,
	// and this maps it to the remote's /ui/... . One side changed, every asset 404s - and
	// a 404 looks like "the remote lacks it".
	cases := []struct {
		local, want string
		ok          bool
	}{
		{local: "/ds/", want: "/ui/", ok: true},
		{local: "/ds", want: "/ui/", ok: true},
		{local: "/ds/api/snapshot", want: "/ui/api/snapshot", ok: true},
		{local: "/ds/assets/index-CpR3oezi.js", want: "/ui/assets/index-CpR3oezi.js", ok: true},
		{local: "/ds/ui/assets/x.js", want: "/ui/ui/assets/x.js", ok: true},
		// Anything climbing out of /ui is refused: the remote's `/__newgate` is its control
		// plane (upgrade forks a process), `/v1` its data plane. Today it happens not to reach
		// there, but only because nothing on this chain cleans paths - no boundary on that.
		{local: "/ds/../__newgate/upgrade"},
		{local: "/ds/../v1/messages"},
		{local: "/ds/../../etc/passwd"},
	}
	for _, tc := range cases {
		got, ok := remotePath("ds", tc.local)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("remotePath(%q) = (%q, %v), want (%q, %v)", tc.local, got, ok, tc.want, tc.ok)
		}
	}
}

func TestLocalPrefixKeepsItsTrailingSlash(t *testing.T) {
	// A missing slash makes every remote request hit /ui/remoteds/... - a 404, invisible.
	// in the browser that concatenation is the culprit.
	if got := LocalPrefix("ds"); got != "/ui/remote/ds/" {
		t.Errorf("LocalPrefix = %q", got)
	}
}

// ---------- gates ----------

func TestOnlyLoopbackHostsGetIn(t *testing.T) {
	// This gate is the route's **only** local defense (the remote checks the Host it
	// receives, and that header is the browser's, preserved by us). Pin its criteria here.
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1:8899", true},
		{"127.0.0.1", true},
		{"[::1]:8899", true},
		{"localhost:8899", true},
		{"LOCALHOST", true},
		// Names are refused: DNS rebinding works by resolving an attacker-controlled domain here.
		{"evil.com:8899", false},
		{"newgate.local:8899", false},
		// Empty Host refused (HTTP/1.0 may omit it, the common shape of script tools).
		{"", false},
		// LAN addresses refused too - behind this route is another machine; stricter only.
		{"10.0.50.11:8899", false},
		{"192.168.1.5:8899", false},
	}
	for _, tc := range cases {
		r := &http.Request{Host: tc.host}
		if got := loopbackHost(r); got != tc.want {
			t.Errorf("Host %q: loopbackHost = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestPortOfSurvivesAnEmptyHost(t *testing.T) {
	if got := portOf(&http.Request{Host: "127.0.0.1:8899"}); got != "8899" {
		t.Errorf("portOf = %q", got)
	}
	if got := portOf(&http.Request{Host: "127.0.0.1"}); got != "" {
		t.Errorf("with no port it should be an empty string, got %q", got)
	}
}
