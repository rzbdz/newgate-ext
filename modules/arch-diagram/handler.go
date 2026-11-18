package archdiagram

import (
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/rzbdz/newgate-ext/lib/buildinfo"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	"github.com/rzbdz/newgate-ext/tools/archmap"
)

// Handler serves the architecture diagram: **generated fresh** on every request.
//
// Why not cache it: the whole value of this diagram is "right now" -- the dependency just
// added, the module just removed, should show up on refresh. And its cost is the `go list` run
// (a few hundred milliseconds locally), paid when a developer clicks, not on every request.
type Handler struct {
	svc *service
	// build is heavy (it runs go list), so serialize: several people refreshing
	// at once will not start several scans at once, and the scan itself is not
	// concurrent either (it reads the same set of files from the same tree).
	build sync.Mutex
}

func NewHandler(svc *service) *Handler { return &Handler{svc: svc} }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Answer only to this machine: this diagram lays out the internal structure (module names, package
	// paths, who depends on whom) entirely, and it hangs on a loopback port -- that stops outside
	// connections, but not a script on someone's web page resolving its own domain to 127.0.0.1
	// (DNS rebinding). **Look only at Host**, for exactly the same reason as the web-dashboard
	// gate (that one has two, because it can write; this one is read-only, one is enough).
	if !loopbackHost(r) {
		http.Error(w, i18n.T("this diagram answers on loopback only; open it as http://127.0.0.1:{port}{prefix}/",
			i18n.A{"port": portOf(r), "prefix": Prefix}), http.StatusForbidden)
		return
	}
	// Read-only, and only GET/HEAD: other verbs make no sense, and allowing
	// them leaves an entry point where "just try a POST" really runs go list.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, i18n.T("the diagram is read-only", nil), http.StatusMethodNotAllowed)
		return
	}

	h.build.Lock()
	// The list is handed over by the kernel after assembly finishes (see
	// service.SetCatalog). This diagram draws **what this process has installed
	// right now** -- not "which specs this artifact contains"; see there for why.
	report := archmap.BuildCatalog("this-process",
		i18n.T("this process", nil),
		i18n.T("what is installed right now: {version}, assembled {took}", i18n.A{
			"version": buildinfo.Version(), "took": buildinfo.BuildTimeDisplay()}),
		h.svc.Components(), h.svc.root)
	h.build.Unlock()
	html, err := archmap.Render(report)
	if err != nil {
		http.Error(w, i18n.T("cannot render the architecture report: {err}",
			i18n.A{"err": err}), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Not cached: this diagram is only meaningful fresh (see why it is generated per request above).
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(html))
}

// loopbackHost reports whether this request's Host header **points at this machine**.
//
// **Not** lib/httpx.IsLoopback: that one tests outbound dialing ("is the target I
// want to connect to this machine"), so the empty string and 0.0.0.0 both count as local -- a
// reasonable default when dialing. This is an entry gate, asking "which name is this request
// aimed at", and an empty Host must be **refused** (an HTTP/1.0 request may have no Host, and
// that is the usual shape for script tools). Two questions, two answers; do not merge them.
func loopbackHost(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// portOf gets the port this request connected to (the error needs a usable address assembled).
func portOf(r *http.Request) string {
	if _, port, err := net.SplitHostPort(r.Host); err == nil {
		return port
	}
	return ""
}
