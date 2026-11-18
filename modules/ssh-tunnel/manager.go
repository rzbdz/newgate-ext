package sshtunnel

import (
	"context"
	"log"
	"sync"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// manager is all runtime state: the pool, one forwarded port per target, and the loop
// that keeps both following config.
//
// # Why it only lives in the daemon
//
// This module's `Start` **runs on every `newgate ...` command** (assembly is single-phase
// in the kernel). The kernel already summed up listening inside Start: typing one
// `newgate status` starts a server. So Serve only registers with `lib/serving` and waits
// for the owner of the port (the daemon) to say "I am serving" - the web-dashboard rule.
//
// # Why lazy connections need no loop
//
// Route requests Acquire themselves (dial first, then reuse). So **with no persistent
// targets this loop only does two small things**: idle reap and keepalive. Not
// "required" on purpose: one less "the loop must run to work" part, and one less
// class of "loop died but the UI still looks fine".
type manager struct {
	pool *pool
	// proxy is only used at shutdown: its HTTP pools leave idle connections around, and
	// those hold SSH leases (see proxy.closeIdle).
	proxy *proxy
	// loaded reads config live. The loop reads once per round, so a target added in the
	// browser with a local_port gets its port within one round (2s).
	loaded func() Loaded

	mu       sync.Mutex
	forwards map[string]*forwarder

	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func newManager(p *pool, loaded func() Loaded) *manager {
	return &manager{pool: p, loaded: loaded, forwards: map[string]*forwarder{}, done: make(chan struct{})}
}

// Serve starts the forwarded ports and the loop, returning a shutdown finalizer.
//
// It is a `serving.OnServe` starter, **called once, only in the daemon**.
func (m *manager) Serve() (func(), error) {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.sync() // bring up every configured port first: users expect them after a daemon restart
	go m.loop(ctx)

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-m.done
			m.shutdown()
		})
	}, nil
}

// loop is that loop: every 2s make runtime catch up with config, reaping idle connections.
//
// 2s is a trade: a target edited in the UI takes effect within two seconds; one pass over
// a few entries is negligible (no network - a liveness check is one SSH request, gated
// by reap's criterion).
func (m *manager) loop(ctx context.Context) {
	defer close(m.done)
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.sync()
			m.pool.reap(time.Now())
		}
	}
}

// sync makes runtime catch up with config: start needed ports, close unneeded, warm the persistent.
func (m *manager) sync() {
	loaded := m.loaded()

	want := map[string]Target{}
	for _, t := range loaded.Targets {
		if t.LocalPort != 0 {
			want[t.ID] = t
		}
	}

	m.mu.Lock()
	// Close forwarded ports no longer in config, or whose port changed (a changed port
	// means the old one should not hold on).
	for id, f := range m.forwards {
		t, still := want[id]
		if still && t.LocalPort == f.target.LocalPort {
			continue
		}
		delete(m.forwards, id)
		go func(f *forwarder) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = f.close(ctx)
		}(f)
	}
	m.mu.Unlock()

	// Start those not yet present, and **those that failed before and are due for retry**.
	now := time.Now()
	for id, t := range want {
		m.mu.Lock()
		cur, exists := m.forwards[id]
		m.mu.Unlock()
		if exists && !cur.due(now) {
			continue
		}
		if exists {
			// Retry: drop the failure record first (it has no listener, close is a no-op).
			m.mu.Lock()
			delete(m.forwards, id)
			m.mu.Unlock()
		}
		f, err := startForward(t, m.pool)
		if err != nil {
			// **Not silent**: this error says "your port did not take effect", and its only
			// outlet is the log and the UI status (see view.go). Other targets proceed.
			// Record it and **schedule the next retry**: without that, the graceful-handover
			// failure stays forever - the "forwarded port dead after upgrade, route fine"
			// shape (seen live).
			log.Printf("[tunnel] %s", err.Error())
			m.mu.Lock()
			m.forwards[id] = failedForward(t, err.Error())
			m.mu.Unlock()
			continue
		}
		m.mu.Lock()
		m.forwards[id] = f
		m.mu.Unlock()
	}

	// Warm the persistent ones. Last, because it is the only step that takes time (real dialing).
	for _, t := range loaded.Targets {
		if t.Persistent {
			m.pool.Warm(context.Background(), t)
		}
	}
	// Targets removed from config: forget their connections.
	m.pool.Retain(loaded.Targets)
}

// shutdown closes everything: forwarded ports first (their connections are pooled), then the pool.
func (m *manager) shutdown() {
	m.mu.Lock()
	fws := make([]*forwarder, 0, len(m.forwards))
	for _, f := range m.forwards {
		fws = append(fws, f)
	}
	m.forwards = map[string]*forwarder{}
	m.mu.Unlock()

	// First release the HTTP pool's idle connections: they hold leases, and without
	// returning them pool.Close waits for IdleConnTimeout (30s) - "does not stop cleanly".
	if m.proxy != nil {
		m.proxy.closeIdle()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, f := range fws {
		wg.Add(1)
		go func(f *forwarder) {
			defer wg.Done()
			_ = f.close(ctx)
		}(f)
	}
	wg.Wait()
	if err := m.pool.Close(ctx); err != nil {
		log.Printf("[tunnel] %s", i18n.T("closing the SSH connections: {err}", i18n.A{"err": err}))
	}
}

// TargetStatus is all of one target's facts right now (read by CLI and the web table).
type TargetStatus struct {
	Target  Target
	Conn    Snapshot
	Forward forwardStatus
	// ForwardErr is why the forwarded port did not start (empty = started, or not wanted).
	ForwardErr string
}

// Status is the snapshot for UI/CLI.
func (m *manager) Status() []TargetStatus {
	loaded := m.loaded()
	m.mu.Lock()
	fws := make(map[string]*forwarder, len(m.forwards))
	for id, f := range m.forwards {
		fws[id] = f
	}
	m.mu.Unlock()

	out := make([]TargetStatus, 0, len(loaded.Targets))
	for _, t := range loaded.Targets {
		snap, _ := m.pool.Snapshot(t.ID)
		st := TargetStatus{Target: t, Conn: snap}
		if f := fws[t.ID]; f != nil {
			st.Forward = f.status()
			st.ForwardErr = f.err
		}
		out = append(out, st)
	}
	return out
}

// Rejected is the unreadable config (the UI draws it, see config.go's Problem).
func (m *manager) Rejected() []Problem { return m.loaded().Rejected }
