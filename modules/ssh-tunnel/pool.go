package sshtunnel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// A target connection's state. These words show up in the CLI, the web table and the log
// - **one vocabulary**, or "terminal says idle, UI says disconnected" looks like two things.
const (
	StateIdle       = "idle"       // 还没有人用过，或者空闲超时已经断开
	StateConnecting = "connecting" // 正在拨（有人已经在拨了，别人在等）
	StateUp         = "up"
	StateDown       = "down" // 上一次拨号失败，原因在 lastErr
)

// pool manages all targets' SSH connections: lazy dial, reuse, idle reap, persistent keepalive.
//
// # Why a pool rather than dialing per request
//
// An SSH handshake is **public-key math** (curve25519 + ed25519), not one TCP round trip.
// Measured 50-200ms on a LAN, costlier than one API round trip to the remote UI. Per
// request, opening a console pays dozens of handshakes, all identical - so reuse decides
// whether this module has practical value; it is not an optimization.
//
// # Concurrency shape
//
// One entry per target, each with its own lock. **Dialing is not inside that lock**: a
// dial takes hundreds of ms to ten-plus seconds, and holding the lock stalls Release on
// the same target (and Release is on the request path). So an "in-flight channel"
// expresses "someone is dialing": others wait for it to close and retry, not queue on a lock.
type pool struct {
	dialer Dialer

	mu      sync.Mutex
	entries map[string]*entry
	// closed is atomic, not inside mu: Acquire takes the entry's lock **then** asks whether
	// closed, while entry() takes mu **then** the entry's lock. Putting closed inside mu
	// forms an inevitable ABBA pair, and that deadlock appears only when shutdown hits the
	// first request - no ordinary test touches it.
	closed atomic.Bool
	// wg counts **in-flight leases**. Shutdown waits for it to hit zero; closing connections
	// earlier cuts running requests in half (a mysterious 502 in the browser).
	wg sync.WaitGroup
}

func newPool(d Dialer) *pool {
	return &pool{dialer: d, entries: map[string]*entry{}}
}

// entry is one target's connection and its stats.
type entry struct {
	target Target

	mu      sync.Mutex
	sess    Session
	refs    int
	last    time.Time
	state   string
	lastErr string
	// since is when the current state began (the UI shows "connected 3 minutes").
	since time.Time
	// inflight non-nil = someone is dialing. Waiters read it and wait for close.
	inflight chan struct{}
	// stats are the three numbers for humans:
	dials  int
	reused int
	lastOK time.Time
	// nextAttempt / backoff for persistent targets only: after a failed dial, wait a while,
	// or a two-rounds-a-second loop hammers a shut-down machine (and wakes it up).
	nextAttempt time.Time
	backoff     time.Duration
}

// lease is one borrow. Must Release when done - it counts how many users a connection
// has now, and whether it can be reaped depends entirely on that.
type lease struct {
	p    *pool
	e    *entry
	sess Session
	once sync.Once
}

// Conn is the connection borrowed this time.
func (l *lease) Conn() Session { return l.sess }

func (l *lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		l.e.mu.Lock()
		l.e.refs--
		l.e.last = time.Now()
		l.e.mu.Unlock()
		l.p.wg.Done()
	})
}

// Acquire borrows a connection to t, dialing if needed.
func (p *pool) Acquire(ctx context.Context, t Target) (*lease, error) {
	t = t.normalize()
	e := p.entry(t)

	for {
		e.mu.Lock()
		if p.closed.Load() {
			e.mu.Unlock()
			return nil, errShuttingDown
		}
		if e.sess != nil {
			e.refs++
			e.reused++
			e.last = time.Now()
			sess := e.sess
			e.mu.Unlock()
			p.wg.Add(1)
			return &lease{p: p, e: e, sess: sess}, nil
		}
		if e.inflight != nil {
			// Someone else is dialing: wait, then start over (it may succeed, or I may redial).
			ch := e.inflight
			e.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		ch := make(chan struct{})
		e.inflight = ch
		e.setStateLocked(StateConnecting, "")
		e.mu.Unlock()

		sess, err := p.dialer.Dial(ctx, t)

		e.mu.Lock()
		e.inflight = nil
		close(ch)
		if err != nil {
			// Failures are **not cached**: the next request dials again. Caching adds a backoff
			// wait after the network recovers; users only see "it still will not connect".
			e.setStateLocked(StateDown, err.Error())
			e.dials++
			e.mu.Unlock()
			return nil, err
		}
		e.sess = sess
		e.refs++
		e.dials++
		e.last = time.Now()
		e.lastOK = time.Now()
		e.setStateLocked(StateUp, "")
		e.mu.Unlock()
		p.wg.Add(1)
		return &lease{p: p, e: e, sess: sess}, nil
	}
}

// drop closes a target's current connection (the next request redials).
//
// Used twice: idle reap, and "someone found this connection dead".
func (p *pool) drop(id, why string) {
	p.mu.Lock()
	e, ok := p.entries[id]
	p.mu.Unlock()
	if !ok {
		return
	}
	e.mu.Lock()
	sess := e.sess
	e.sess = nil
	e.setStateLocked(StateIdle, "")
	e.mu.Unlock()
	if sess != nil {
		log.Printf("[tunnel] %s", i18n.T("{id}: closed the connection ({why})",
			i18n.A{"id": id, "why": why}))
		_ = sess.Close()
	}
}

// Forget drops a target (removed from config).
func (p *pool) Forget(id string) {
	p.mu.Lock()
	e, ok := p.entries[id]
	delete(p.entries, id)
	p.mu.Unlock()
	if !ok {
		return
	}
	e.mu.Lock()
	sess := e.sess
	e.sess = nil
	e.mu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
}

func (p *pool) entry(t Target) *entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[t.ID]
	if !ok {
		e = &entry{target: t, state: StateIdle, since: time.Now()}
		p.entries[t.ID] = e
		return e
	}
	// Target address changed (host edited in the UI): the old connection points elsewhere.
	if e.target.Host != t.Host || e.target.Port != t.Port || e.target.User != t.User ||
		e.target.RemoteHost != t.RemoteHost || e.target.RemotePort != t.RemotePort {
		e.mu.Lock()
		sess := e.sess
		e.sess = nil
		e.target = t
		e.setStateLocked(StateIdle, "")
		e.mu.Unlock()
		if sess != nil {
			_ = sess.Close()
		}
	} else {
		e.mu.Lock()
		e.target = t
		e.mu.Unlock()
	}
	return e
}

func (p *pool) isClosed() bool { return p.closed.Load() }

func (e *entry) setStateLocked(state, lastErr string) {
	if e.state != state {
		e.since = time.Now()
	}
	e.state = state
	e.lastErr = lastErr
}

// Snapshot is a target's connection state now (read by UI and CLI).
type Snapshot struct {
	ID       string
	State    string
	LastErr  string
	Since    time.Time
	Refs     int
	Dials    int
	Reused   int
	LastUsed time.Time
}

func (p *pool) Snapshot(id string) (Snapshot, bool) {
	p.mu.Lock()
	e, ok := p.entries[id]
	p.mu.Unlock()
	if !ok {
		return Snapshot{ID: id, State: StateIdle}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return Snapshot{
		ID: id, State: e.state, LastErr: e.lastErr, Since: e.since,
		Refs: e.refs, Dials: e.dials, Reused: e.reused, LastUsed: e.last,
	}, true
}

// reap does two things: close idle connections, and probe live ones for liveness.
//
// Probing is necessary: a peer (or a NAT in between) may silently drop an SSH connection
// while our socket stays open. Without probing the user's first request **hangs** instead
// of "reconnected, then succeeded". A failed probe drops the connection; the next request redials.
func (p *pool) reap(now time.Time) {
	p.mu.Lock()
	list := make([]*entry, 0, len(p.entries))
	for _, e := range p.entries {
		list = append(list, e)
	}
	p.mu.Unlock()

	for _, e := range list {
		e.mu.Lock()
		sess, refs, last, idle, keep := e.sess, e.refs, e.last, e.target.IdleSeconds, e.target.Persistent
		e.mu.Unlock()
		if sess == nil || refs > 0 {
			continue
		}
		// Persistent targets are not closed for idleness - that is "persistent". They still
		// probe: an SSH connection silently dropped by peer or NAT would hang the next request.
		if !keep && idle > 0 && now.Sub(last) > time.Duration(idle)*time.Second {
			p.drop(e.target.ID, i18n.T("idle for {n}s", i18n.A{"n": idle}))
			continue
		}
		if _, _, err := sess.SendRequest("keepalive@openssh.com", true, nil); err != nil {
			p.drop(e.target.ID, i18n.T("it stopped answering: {err}", i18n.A{"err": err}))
		}
	}
}

// Warm keeps a persistent target connected. Failures **return no error**: the caller is
// a loop that comes back next round; what to say then is the log line, not a return.
//
// Backoff 1s to 30s: hammering a shut-down machine twice a second is pointless, and 30s
// is soon enough for "it is back" (a refresh redials via the lazy path at once).
func (p *pool) Warm(ctx context.Context, t Target) {
	e := p.entry(t)
	e.mu.Lock()
	if e.sess != nil || e.inflight != nil || time.Now().Before(e.nextAttempt) {
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()

	l, err := p.Acquire(ctx, t)
	if err != nil {
		e.mu.Lock()
		if e.backoff == 0 {
			e.backoff = time.Second
		} else if e.backoff < 30*time.Second {
			e.backoff *= 2
		}
		wait := e.backoff
		e.nextAttempt = time.Now().Add(wait)
		e.mu.Unlock()
		log.Printf("[tunnel] %s", i18n.T("{id}: cannot keep the connection open, retrying in {n}s: {err}",
			i18n.A{"id": t.ID, "n": int(wait.Seconds()), "err": err}))
		return
	}
	// Return it at once: the lease only means "someone uses it", and a persistent target
	// wants the **connection kept** - refs at zero only allows reaping, for non-persistent ones.
	l.Release()
	e.mu.Lock()
	e.backoff = 0
	e.nextAttempt = time.Time{}
	e.mu.Unlock()
}

// Retain forgets targets not in the list (removed from config).
//
// Skipping it means **connected SSH connections never close**: no longer in config, no
// path touches it, and it holds an SSH connection and its goroutine (symptom: "after
// deleting, `ss -tnp` still shows a connection to that machine", far from the scene).
func (p *pool) Retain(targets []Target) {
	keep := make(map[string]bool, len(targets))
	for _, t := range targets {
		keep[t.ID] = true
	}
	p.mu.Lock()
	var gone []string
	for id := range p.entries {
		if !keep[id] {
			gone = append(gone, id)
		}
	}
	p.mu.Unlock()
	for _, id := range gone {
		p.Forget(id)
	}
}

// Close closes all connections.
//
// Wait for leases to hit zero first (in-flight requests finish), then close - reversed,
// a proxy mid-response loses its connection underneath and the user sees a bodyless 502.
// The wait is bounded: one stuck request must not keep the daemon from stopping.
func (p *pool) Close(ctx context.Context) error {
	p.closed.Store(true)
	p.mu.Lock()
	list := make([]*entry, 0, len(p.entries))
	for _, e := range p.entries {
		list = append(list, e)
	}
	p.mu.Unlock()

	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}

	var errs []error
	for _, e := range list {
		e.mu.Lock()
		sess := e.sess
		e.sess = nil
		e.setStateLocked(StateIdle, "")
		e.mu.Unlock()
		if sess != nil {
			errs = append(errs, sess.Close())
		}
	}
	return errors.Join(errs...)
}

var errShuttingDown = errors.New("ssh-tunnel is shutting down")

// humanizeSince says "how long in this state" in plain words (shared by CLI and UI).
func humanizeSince(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
