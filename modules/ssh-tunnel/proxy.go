package sshtunnel

import (
	"bytes"
	"context"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Prefix is this section's foothold on the shared port.
//
// It hangs under web-dashboard's `/ui` because **it is that kind of thing**: a UI, only
// elsewhere. porthub matches by segment, longest prefix first, so `/ui/remote/...` lands
// here and `/ui/...` still lands on web-dashboard - neither side knows the other exists
// (which is also why the prefix is "/ui/remote" and not "/ui": taking a taken prefix errors).
//
// Not `/a`, `/v1` (data plane) or `/__newgate` (control plane): porthub rejects them.
const Prefix = "/ui/remote"

// proxy is the handler of the shared port's route (`Prefix` already stripped by the caller).
type proxy struct {
	pool *pool
	// loaded reads config **per request**: a target edited in the browser, or a CLI `add`,
	// must apply to the next request. Caching it turns "just changed, not effective" into
	// a weird phenomenon only a restart fixes.
	loaded func() Loaded

	mu         sync.Mutex
	transports map[string]transportEntry
}

type transportEntry struct {
	target Target
	tr     *http.Transport
}

func newProxy(p *pool, loaded func() Loaded) *proxy {
	return &proxy{pool: p, loaded: loaded, transports: map[string]transportEntry{}}
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Each route contributor copies this gate (porthub does not provide one). This uses
	// arch-diagram's **strict** version (loopback IPs and localhost only), not
	// web-dashboard's (it allows LAN IPs too, as it has its own second write gate).
	//
	// Why stricter here: behind this route is **another machine**, and the local gate is
	// the only one - the remote checks the Host it receives, and connections from here
	// carry the browser's Host we preserved (see reverseFor). So "who can open this"
	// rests entirely on this line.
	if !loopbackHost(r) {
		writePlain(w, http.StatusForbidden, i18n.T(
			"this tunnel answers on loopback only; open it as http://127.0.0.1:{port}{prefix}/",
			i18n.A{"port": portOf(r), "prefix": Prefix}))
		return
	}

	if r.URL.Path == "" {
		// Exactly `/ui/remote` with no trailing slash: browsers resolve relative addresses
		// against the current document, so from `/ui/remote` `api/snapshot` becomes
		// `/ui/remote/api/snapshot` - api read as a target name. 302 (not 301): a permanent
		// redirect is cached near-forever, so a future prefix change could not be undone.
		http.Redirect(w, r, Prefix+"/", http.StatusFound)
		return
	}

	// After StripPrefix: "..." (`/ui/remote/` yields "/"), or "/<id>", "/<id>/...".
	rest := strings.TrimPrefix(r.URL.Path, "/")
	id, hasTail := rest, false
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		id, hasTail = rest[:i], true
	}

	loaded := p.loaded()
	if id == "" {
		p.index(w, loaded)
		return
	}
	t, ok := find(loaded, id)
	if !ok {
		p.unknown(w, loaded, id)
		return
	}
	if !hasTail {
		// Only a **bare id** (`/ui/remote/ds`) gets a slash. The criterion is "nothing
		// after the id", **not** "does not end with a slash" - the latter is true for
		// `/ui/remote/ds/api/snapshot`, and adding a slash would turn every API call into
		// a 302 (measured: no request ever reached the remote).
		//
		// 302 (not 301): a permanent redirect is cached near-forever by browsers.
		http.Redirect(w, r, Prefix+"/"+url.PathEscape(id)+"/", http.StatusFound)
		return
	}

	// The remote path is computed **once** here, and anything climbing out of the remote
	// `/ui` tree is refused at this step - reasons in remotePath. The value goes straight
	// to the reverse proxy (not recomputed in Rewrite): refusal and forwarding share it.
	remote, ok := remotePath(id, r.URL.Path)
	if !ok {
		writePlain(w, http.StatusNotFound, i18n.T(
			"this route only forwards the remote interface: {path} points outside it",
			i18n.A{"path": r.URL.Path}))
		return
	}

	w.Header().Set("X-Newgate-Tunnel", id)
	p.reverseFor(t, remote).ServeHTTP(w, r)
}

// reverseFor assembles this route's ReverseProxy.
//
// Building a ReverseProxy per request is cheap (one struct); what must be cached is
// the **http.Transport** behind it - the pool lives there (see transportFor).
func (p *proxy) reverseFor(t Target, remote string) *httputil.ReverseProxy {
	local := LocalPrefix(t.ID)
	return &httputil.ReverseProxy{
		Transport: p.transportFor(t),
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			// Host is only a key for Transport: it does not dial by it (DialContext always
			// dials the configured pair). A stable value keeps the pool to one slot.
			pr.Out.URL.Host = "tunnel"
			// remote is computed and verified by the caller (in ServeHTTP). **Not recomputed**
			// here: two computations mean a future edit to one side sends refused paths (or the
			// reverse) - and that kind of error is green in tests.
			pr.Out.URL.Path = remote
			pr.Out.URL.RawPath = ""

			// **Keep the client's Host** (Go's default replaces it with the target host).
			//
			// This line decides whether the route works at all, for the remote side's reason:
			// the remote BFF has a DNS-rebinding gate that inspects **the request's Host** -
			// only IP literals and localhost pass. The browser's Host is where it navigated to
			// (127.0.0.1:8899, or a LAN IP), both pass. Replaced with "tunnel" or any domain,
			// the remote returns **403** while everything locally looks fine - an error that
			// sends people from rewriting through to the remote sshd, to find one header.
			pr.Out.Host = pr.In.Host

			// Drop Accept-Encoding: **compressed bodies cannot be rewritten** (see rewrite.go).
			//
			// The cost, stated: one less compression, over localhost plus an SSH channel (a
			// remote page is a few hundred KB). Not dropping it **whites out every page** -
			// the remote gzips, we cannot rewrite, and the browser gets local /ui/assets URLs.
			pr.Out.Header.Del("Accept-Encoding")
		},
		// -1 = flush at once. The remote UI polls (a snapshot every few seconds); without
		// flushing, each waits for a full buffer or the end of the response - interaction stutters.
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			// Location is its own step (see rewriteLocation: unrewritten, it silently jumps to
			// the local UI, which looks almost identical).
			if loc := resp.Header.Get("Location"); loc != "" {
				resp.Header.Set("Location", rewriteLocation(loc, local))
			}
			if !shouldRewrite(resp) {
				return nil
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				// Broken mid-read: hand the error back to ReverseProxy, which routes it to
				// ErrorHandler. **Never** return a half body - that is an HTML page that "looks
				// fine, but is truncated", far worse than a clear error.
				return err
			}
			next := rewritePrefix(body, local)
			resp.Body = io.NopCloser(bytes.NewReader(next))
			resp.ContentLength = int64(len(next))
			resp.Header.Set("Content-Length", strconv.Itoa(len(next)))
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			tunnelError(w, t, err)
		},
	}
}

// remotePath joins the part after this route's prefix onto the remote's own absolute prefix.
//
//	local  /ui/remote/ds/            -> remote /ui/
//	local  /ui/remote/ds/api/snap    -> remote /ui/api/snap
//
// Argued once, because it is the foundation of all rewriting: the browser's address
// **already is** `/ui/remote/<id>/...`, i.e. "the remote address with a prefix". So
// stripping it is what the remote wants - and absolute addresses in bodies
// (`/ui/assets/...`) rewritten to `/ui/remote/<id>/...` come back exactly under this
// rule. **Two sides of one thing**; change one and you must change the other.
//
// # Second return: did this path climb out of the remote's `/ui`
//
// The joined path may carry `..` (`/ui/remote/ds/../__newgate/upgrade` -> after joining
// `/ui/../__newgate/upgrade`). **Must be normalized and refused**, same reason as the
// kernel's "never blindly send `/__newgate/upgrade`": the remote's `/__newgate` is its
// control plane (`upgrade` forks a new process), `/v1` its data plane.
//
// Today that path **happens** not to reach the control plane (the remote porthub hands
// it to web-dashboard under `/ui`, the SPA fallback), but that is **luck**: if any
// layer normalizes the path once (a proxy, a mux, a future implementation),
// `/ui/../__newgate/upgrade` becomes `/__newgate/upgrade`. A security boundary cannot
// rest on "nothing else on this chain cleans paths today".
func remotePath(id, local string) (string, bool) {
	rel := strings.TrimPrefix(local, "/")
	rel = strings.TrimPrefix(rel, id)
	rel = strings.TrimPrefix(rel, "/")
	raw := RemoteUIPrefix + rel
	p := path.Clean(raw)
	if p != strings.TrimSuffix(RemoteUIPrefix, "/") && !strings.HasPrefix(p, RemoteUIPrefix) {
		return "", false
	}
	// Clean trims a trailing slash, but **the remote tree's root must keep it**: `/ui`
	// is a 302 on the remote (to `/ui/`), and we rewrite that Location back to this route,
	// costing the browser one more round trip - correct, but no reason to pay for it.
	if strings.HasSuffix(raw, "/") && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p, true
}

// transportFor returns a target's http.Transport.
//
// Channels over one SSH connection are pooled by **this Transport**: HTTP keep-alive
// reuse, idle conns closed after 30s - and closing releases the lease, letting idle
// reaping drop the SSH connection too. The two timeouts are **tied together**.
func (p *proxy) transportFor(t Target) *http.Transport {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.transports[t.ID]; ok && e.target == t {
		return e.tr
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return p.dialTunnel(ctx, t)
		},
		// No compression: see dropping Accept-Encoding in Rewrite. The two are **one thing's**
		// two sides (what we send, and whether Go decompresses); miss one and rewriting
		// becomes "sometimes works, sometimes not".
		DisableCompression: true,
		// The channel's far end is the remote's plaintext port, no second hop.
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       30 * time.Second,
		ExpectContinueTimeout: time.Second,
		// No ResponseHeaderTimeout: if the remote port really stalls, a refresh retries; a
		// short timeout would also kill "the remote is busy", which looks like a disconnect.
	}
	if old, ok := p.transports[t.ID]; ok {
		// Address changed: the old Transport's connections no longer point here. Close the
		// idle ones, let in-flight ones finish (CloseIdleConnections leaves them alone).
		old.tr.CloseIdleConnections()
	}
	p.transports[t.ID] = transportEntry{target: t, tr: tr}
	return tr
}

// closeIdle closes all targets' idle HTTP connections.
//
// Shutdown must do this first: those **idle connections still hold an SSH lease** (see
// leasedConn), and pool.Close waits for in-flight leases to hit zero. Otherwise shutdown
// waits for the pool's own IdleConnTimeout (30s) - "does not stop cleanly".
func (p *proxy) closeIdle() {
	p.mu.Lock()
	trs := make([]*http.Transport, 0, len(p.transports))
	for _, e := range p.transports {
		trs = append(trs, e.tr)
	}
	p.mu.Unlock()
	for _, tr := range trs {
		tr.CloseIdleConnections()
	}
}

// dialTunnel opens an SSH channel for one HTTP connection, tying a lease to its lifetime.
func (p *proxy) dialTunnel(ctx context.Context, t Target) (net.Conn, error) {
	l, err := p.pool.Acquire(ctx, t)
	if err != nil {
		return nil, err
	}
	conn, err := l.Conn().Dial("tcp", t.RemoteAddr())
	if err != nil {
		l.Release()
		return nil, err
	}
	return &leasedConn{Conn: conn, lease: l}, nil
}

// leasedConn makes "the HTTP connection closed" and "the SSH borrow returned" one thing.
//
// Untied, every idle connection in the pool holds a borrow forever (refs never zero), so
// the SSH connection is **never idle-reaped** - invisible functionally; the daemon just
// keeps a pile of unused SSH connections.
type leasedConn struct {
	net.Conn
	lease *lease
	once  sync.Once
}

func (c *leasedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.lease.Release() })
	return err
}

// ---------- a few pages ----------

// index is the index at `/ui/remote/`.
//
// It is **plain HTML**, no front-end artifacts: this route must work in an assembly
// without web-dashboard (where `/ui` does not exist and tunnel should still list). That
// also keeps it "just a few links", with no room for a blank page.
func (p *proxy) index(w http.ResponseWriter, loaded Loaded) {
	var b strings.Builder
	b.WriteString("<!doctype html><meta charset=\"utf-8\"><title>")
	b.WriteString(html.EscapeString(i18n.T("remote interfaces", nil)))
	b.WriteString("</title><style>body{font:14px/1.6 ui-monospace,monospace;margin:2rem;max-width:48rem}" +
		"li{margin:.4rem 0}code{background:#8881;padding:.1rem .3rem}</style><h1>")
	b.WriteString(html.EscapeString(i18n.T("remote interfaces", nil)))
	b.WriteString("</h1>")

	if len(loaded.Targets) == 0 {
		b.WriteString("<p>" + html.EscapeString(i18n.T(
			"no targets yet — add one with `newgate tunnel add <id> --host <host>`", nil)) + "</p>")
	} else {
		b.WriteString("<ul>")
		for _, t := range loaded.Targets {
			link := Prefix + "/" + url.PathEscape(t.ID) + "/"
			b.WriteString("<li><a href=\"" + html.EscapeString(link) + "\">" +
				html.EscapeString(t.Label) + "</a> <code>" +
				html.EscapeString(t.User+"@"+t.DialAddr()+" → "+t.RemoteAddr()) + "</code>")
			if t.LocalPort != 0 {
				b.WriteString(" · <code>http://127.0.0.1:" + strconv.Itoa(t.LocalPort) + "/ui/</code>")
			}
			b.WriteString("</li>")
		}
		b.WriteString("</ul>")
	}
	for _, bad := range loaded.Rejected {
		b.WriteString("<p><b>" + html.EscapeString(i18n.T("ignored a broken target", nil)) + "</b> " +
			html.EscapeString(bad.Line) + "</p>")
	}
	b.WriteString("<p><small>" + html.EscapeString(i18n.T(
		"this page is a best-effort copy of the remote interface: paths inside it are rewritten. "+
			"if something looks broken, use the forwarding port instead — that one is byte-for-byte.",
		nil)) + "</small></p>")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, b.String())
}

// unknown is "this id is not recognized".
//
// It deliberately **lists the known ones**: typing the address by hand is this route's
// most common use (copying a link), and a page with only a 404 makes a typo look broken.
func (p *proxy) unknown(w http.ResponseWriter, loaded Loaded, id string) {
	ids := make([]string, 0, len(loaded.Targets))
	for _, t := range loaded.Targets {
		ids = append(ids, t.ID)
	}
	sort.Strings(ids)
	msg := i18n.T("there is no target called {id}", i18n.A{"id": id})
	if len(ids) == 0 {
		msg += " — " + i18n.T("none are configured yet", nil)
	} else {
		msg += " — " + i18n.T("configured: {list}", i18n.A{"list": strings.Join(ids, ", ")})
	}
	writePlain(w, http.StatusNotFound, msg)
}

// tunnelError is the page when "cannot dial through".
//
// It must **say where it stopped**: SSH unreachable (network / bad address), unknown
// host key, nobody listening on the remote port - three different next actions. An empty
// 502 sends people off debugging "is my browser broken".
func tunnelError(w http.ResponseWriter, t Target, err error) {
	writePlain(w, http.StatusBadGateway, i18n.T("{id}: {err}", i18n.A{"id": t.ID, "err": err})+
		"\n\n"+i18n.T("run `newgate tunnel test {id}` for a step-by-step answer",
		i18n.A{"id": t.ID}))
}

// writePlain is a plain-text body.
//
// text/plain, not HTML: the content is **raw error text** (addresses, sshd's words,
// paths), so HTML needs escaping, and one miss is an injection point.
func writePlain(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, msg+"\n")
}

// ---------- gates and tools ----------

// loopbackHost reports whether this request's Host header **points at this machine**.
//
// **Not** lib/httpx.IsLoopback: that judges outbound dialing ("is my target local"), so
// empty and 0.0.0.0 count as local there - a reasonable default when dialing. This is an
// ingress criterion: "which name is this request for"; empty Host must be **refused**
// (HTTP/1.0 may omit Host - the usual shape of script tools). Two questions, two answers.
//
// Being word-for-word identical to modules/arch-diagram/handler.go is **intentional**:
// this route brings in a remote machine's console, more sensitive than the arch diagram,
// so its criterion should be stricter, never looser. A third copy is a real smell (it
// should move to lib/), but that is a kernel change - the comment for the next reader.
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

func portOf(r *http.Request) string {
	if _, port, err := net.SplitHostPort(r.Host); err == nil {
		return port
	}
	return ""
}

func find(l Loaded, id string) (Target, bool) {
	for _, t := range l.Targets {
		if t.ID == id {
			return t, true
		}
	}
	return Target{}, false
}
