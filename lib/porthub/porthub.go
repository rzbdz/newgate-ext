// Package porthub is the mechanism for "several services on one port": a table of **prefix ->
// http.Handler**, and anyone who wants another service on this port mounts an entry into it.
//
// Why it is a mechanism: a gateway listens on only one port, a hard constraint
// of deployment (firewalls, reverse proxies, and Tailscale ACLs are all configured
// per port). The web UI, and other services in the future, all want to live on this port,
// and "mounting things into the same table" has nothing to do with what product you sell.
//
// It does four things: remember who mounted what, look up by **longest prefix**, refuse to hand
// over reserved prefixes, and **hand out this port's root handler** (`Root`: the mount table wins,
// and anything unclaimed falls back to the fallback service -- the fallback is the data plane).
//
// **It does not listen**: the socket belongs to the daemon (graceful handover
// of the fd, draining, and the pidfile/lock are all process-level matters, and they must keep
// working when porthub is off). So mounting here is an in-process registration: it creates no
// socket and no goroutine, which also makes it safe to call in every `newgate …` command (a
// module's Start runs on every command; see the comment in modules/i18n for that constraint).
//
// Direction (settled 2026-09-20): **the data plane does not know this table**.
// The daemon entry point hands the data plane's handler to `Root`, then hands
// the resulting root handler to http.Server -- so "who answers" is decided by
// this table, and the name porthub does not even appear in gateway/forward.
//
// It differs from pluginmanager's switch: that one is "do we want this feature", this one is
// "which path is this service mounted at". The consequence of turning off the porthub module
// is that **consumers cannot get this table** (the capability is absent), so they fall back to
// listening on their own port -- clean semantics, with no special case at the dispatch site.
package porthub

import (
	"net/http"
	"sort"
	"strings"
	"sync"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Mount is one mounted record, used for diagnostics and status.
type Mount struct {
	Prefix string
	Owner  string // 谁挂的（模块名）；冲突时报错要点名
}

// Registry is the mount table. The zero value is unusable; use NewRegistry or Default.
type Registry struct {
	mu     sync.RWMutex
	mounts map[string]mount
	// fallback is where "unclaimed requests" go: in this process, the data plane.
	// It is not an entry mounted at "/" -- "/" is the common prefix of every
	// prefix, and making it an entry would turn "who mounted /" and "who is the
	// fallback" into two things that can coexist and know nothing of each other.
	fallback http.Handler
	fbOwner  string
}

type mount struct {
	handler http.Handler
	owner   string
}

// reserved is the set of prefixes that cannot be taken.
//
// The reason is not "it looks bad": `/a` and `/v1` are the data plane
// (forwarding paths) and `/__newgate` is the control-plane namespace. The symptom
// of a takeover is that **a request meant for the local control plane gets forwarded
// upstream as data-plane traffic** -- the line in forward.go, "never blindly send
// /__newgate/upgrade (it would be forwarded upstream by the catch-all)", records this
// exact pit. So it errors out here rather than let a later caller quietly override.
var reserved = []string{"/v1", "/a", "/__newgate"}

// NewRegistry builds an empty table.
func NewRegistry() *Registry {
	return &Registry{mounts: map[string]mount{}}
}

var (
	defaultOnce sync.Once
	defaultReg  *Registry
)

// Default is the process-level table.
//
// Why a global is allowed here: it is the same kind of thing as metrics.Default
// -- apart from "who mounted what" it has no other state, and in a process there
// is naturally only one of those (there is only one http.Server too). Things
// with a lifecycle still go through a capability (modules/porthub); the global
// only solves the single problem of "how the dispatch site gets the table".
func Default() *Registry {
	defaultOnce.Do(func() { defaultReg = NewRegistry() })
	return defaultReg
}

// Mount mounts h under prefix. owner is the mounter's name, used only for errors and diagnostics.
//
// The returned Release undoes this mount and is idempotent; it must be called in Stop (no
// Release means no lifecycle; see the counter-example analysis in modules/gateway/module.go).
func (r *Registry) Mount(prefix, owner string, h http.Handler) (func(), error) {
	p, err := normalize(prefix)
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, i18n.E("porthub: the handler {owner} wants to mount at {prefix} is nil",
			i18n.A{"owner": owner, "prefix": p})
	}
	for _, res := range reserved {
		if p == res || strings.HasPrefix(p, res+"/") || strings.HasPrefix(res, p+"/") {
			return nil, i18n.E("porthub: prefix {prefix} (mounted by {owner}) collides with the "+
				"reserved prefix {reserved}, which belongs to the data plane / control plane "+
				"namespace — claiming it would send requests meant for this machine upstream "+
				"as if they were data-plane traffic", i18n.A{"prefix": p, "owner": owner, "reserved": res})
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if prev, dup := r.mounts[p]; dup {
		return nil, i18n.E("porthub: prefix {prefix} is already mounted by {owner} — two services "+
			"on one path means only one of them ever sees a request, so this errors instead of "+
			"letting the later one win silently", i18n.A{"prefix": p, "owner": prev.owner})
	}
	r.mounts[p] = mount{handler: h, owner: owner}
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if m, ok := r.mounts[p]; ok && m.owner == owner {
			delete(r.mounts, p)
		}
	}, nil
}

// Lookup looks up by longest prefix. A false second return means this table does not
// handle the path; the caller goes its own way (gateway falls back to forwarding).
func (r *Registry) Lookup(path string) (http.Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	best := ""
	var found mount
	for p, m := range r.mounts {
		if !matches(p, path) {
			continue
		}
		if len(p) > len(best) {
			best, found = p, m
		}
	}
	if best == "" {
		return nil, false
	}
	return found.handler, true
}

// Root registers fallback as this port's fallback
// service and hands out **the port's root handler**.
//
// This is where "who owns the port" lands: the caller (the daemon entry point) no
// longer decides who answers itself; it passes in the data plane and gets back a
// handler to give http.Server -- so - services in the mount table (the web UI and
// the like) win; - anything unclaimed falls back to the data plane; - the data
// plane **does not know this table** (it is just a handler passed in), and this
// table does not know the data plane either (it only knows it has a fallback).
//
// The last point is the whole reason for this change: making the data plane import a mechanism
// for "how web services share a port" would write the deployment shape into the hot path.
func (r *Registry) Root(owner string, fallback http.Handler) (http.Handler, error) {
	if fallback == nil {
		return nil, i18n.E("porthub: {owner} wants to serve the port with a nil fallback handler",
			i18n.A{"owner": owner})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fallback, r.fbOwner = fallback, owner
	return http.HandlerFunc(r.serve), nil
}

// serve is the root handler: ask the mount table first, hand to the fallback if not found.
//
// **Look up on every request**, do not copy the table into a mux: module Start
// order is decided by the dependency graph and does not guarantee that the mounting
// module comes before the server side; a copy would also void the release after Stop
// (by then the handler is long built). Reads and writes are guarded by an RWMutex
// -- that is the race protection point for "the CLI and the dashboard run at once".
func (r *Registry) serve(w http.ResponseWriter, req *http.Request) {
	if h, ok := r.Lookup(req.URL.Path); ok {
		h.ServeHTTP(w, req)
		return
	}
	r.mu.RLock()
	fb := r.fallback
	r.mu.RUnlock()
	// No nil check here: Root rejects a nil fallback, so once this handler is out there,
	// fb is necessarily non-nil. A "just-in-case 404" is exactly the kind of code this
	// repo wants least -- the reader would think that path can happen and reason from it.
	fb.ServeHTTP(w, req)
}

// FallbackOwner is who is doing the fallback (for diagnostics).
func (r *Registry) FallbackOwner() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.fbOwner
}

// Mounts returns a snapshot sorted by prefix (for diagnostics and `newgate status`).
//
// The fallback service shows up here as an entry with prefix `/` -- which
// is the truth: what it catches is "all paths not claimed by a longer prefix".
// Diagnostic output can therefore fully answer "what all is running on this port".
func (r *Registry) Mounts() []Mount {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Mount, 0, len(r.mounts)+1)
	if r.fallback != nil {
		out = append(out, Mount{Prefix: "/", Owner: r.fbOwner})
	}
	for p, m := range r.mounts {
		out = append(out, Mount{Prefix: p, Owner: m.owner})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Prefix < out[j].Prefix })
	return out
}

// matches is the prefix-match test: `/ui` matches `/ui` and `/ui/...` but **not**
// `/uix` -- a byte-prefix match would quietly acknowledge one extra service here.
func matches(prefix, path string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// normalize tightens it: must start with /, cannot be / (the catch-all), trailing / stripped.
func normalize(prefix string) (string, error) {
	if prefix == "" || prefix[0] != '/' {
		return "", i18n.E("porthub: a prefix must start with /, got {prefix}",
			i18n.A{"prefix": prefix})
	}
	p := strings.TrimSuffix(prefix, "/")
	if p == "" {
		return "", i18n.E("porthub: the prefix cannot be / — that is the whole port. Give a path "+
			"prefix instead, e.g. /ui", nil)
	}
	return p, nil
}
