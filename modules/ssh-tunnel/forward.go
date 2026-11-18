package sshtunnel

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// forwarder is one local forwarded port: every connection on `127.0.0.1:<local_port>` goes
// to the remote machine's remote_host:remote_port.
//
// This is ssh(1)'s `-L`, and it exists for **faithfulness**: path untouched, hardcoded
// absolute addresses (/ui/assets/...) in the remote UI work as is. The shared-port route
// cannot do that (needs rewriting), so both exist.
type forwarder struct {
	target Target
	// ln nil means **this forwarded port did not start**; reason in err. Keeping an "empty
	// forwarder" rather than omitting it lets the UI row say "you configured 9401, it failed"
	// - a missing row only looks like "you configured nothing".
	ln   net.Listener
	err  string
	pool *pool

	wg     sync.WaitGroup
	closed atomic.Bool
	mu     sync.Mutex
	// live is the connections running right now.
	//
	// Why track them: an HTTP keep-alive connection **does not end by itself** - splice
	// waits for both ends, and the browser keeps it for reuse. So "wait for in-flight
	// connections" hangs forever on shutdown (measured: a stop hangs the whole timeout).
	// Track them to **force-close** after the grace period - same as http.Server Shutdown/Close.
	live map[net.Conn]struct{}
	// lastErr is the latest accept failure (for diagnostics). An accept failure is usually
	// a closed listener - normal shutdown, not a fault to report.
	lastErr string
	// accepted / failed are counters; "is this channel in use" on the UI depends on them.
	accepted atomic.Int64
	failed   atomic.Int64
	// nextTry only matters for the ones that **did not start** (see failedForward).
	nextTry time.Time
}

// failedForward is the "this port did not start" row.
//
// nextTry is **when to try again**. Why not give up: the most common failure happens
// right after a **graceful handover** - the old process still holds the port draining,
// the new one cannot bind (seen live: after an upgrade 9401 stayed dead while the route
// was fine, looking like "only the forwarded port broke"). Retry once and it is gone.
func failedForward(t Target, why string) *forwarder {
	return &forwarder{target: t, err: why, closed: *new(atomic.Bool),
		nextTry: time.Now().Add(forwardRetry)}
}

// forwardRetry is how long to wait before retrying an unbound port.
//
// 2s: short enough that "fixed after the handover" lands within a retry or two,
// long enough that a long-held port logs one line per 2s - not a flood.
const forwardRetry = 2 * time.Second

// due reports whether this one should be retried now.
func (f *forwarder) due(now time.Time) bool {
	if f == nil || f.ln != nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return !now.Before(f.nextTry)
}

// startForward starts one forwarded port.
//
// **Binds 127.0.0.1 only, not configurable**. Not aesthetics: behind this port is a
// remote UI **without a Host gate** - the remote BFF's rebinding check inspects the Host
// it receives, and connections from here carry a Host "someone else filled in". Binding
// 0.0.0.0 opens a remote console to the LAN with no gate on this side. Others should make
// their own tunnel (they have their own SSH credentials).
func startForward(t Target, p *pool) (*forwarder, error) {
	if t.LocalPort == 0 {
		return nil, nil
	}
	addr := net.JoinHostPort("127.0.0.1", itoa(t.LocalPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// Port taken is **not silent**: the only place users learn "my port did not take effect".
		return nil, i18n.E("cannot listen on {addr}: {err}", i18n.A{"addr": addr, "err": err})
	}
	f := &forwarder{target: t, ln: ln, pool: p, live: map[net.Conn]struct{}{}}
	log.Printf("[tunnel] %s", i18n.T("{id}: forwarding http://{addr}/ui/ to {remote} over {ssh}",
		i18n.A{"id": t.ID, "addr": addr, "remote": t.RemoteAddr(), "ssh": t.DialAddr()}))
	go f.serve()
	return f, nil
}

func (f *forwarder) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			if f.closed.Load() {
				return
			}
			f.mu.Lock()
			f.lastErr = err.Error()
			f.mu.Unlock()
			// accept errors split two ways: temporary (EMFILE etc.) is worth retrying, permanent
			// (listener gone) should exit. Without the split, one permanent error makes this a
			// busy loop logging a line per millisecond.
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			log.Printf("[tunnel] %s", i18n.T("{id}: the forwarding port stopped accepting: {err}",
				i18n.A{"id": f.target.ID, "err": err}))
			return
		}
		f.accepted.Add(1)
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			f.handle(conn)
		}()
	}
}

// handle connects one local connection through to the remote side.
func (f *forwarder) handle(local net.Conn) {
	f.track(local)
	defer f.untrack(local)
	defer local.Close()

	// Borrow a connection (dial if needed). The requester's own context does not work -
	// no HTTP request here, just a bare TCP; a generous budget covers one SSH handshake.
	ctx, cancel := context.WithTimeout(context.Background(), dialBudget)
	defer cancel()
	l, err := f.pool.Acquire(ctx, f.target)
	if err != nil {
		f.failed.Add(1)
		log.Printf("[tunnel] %s", i18n.T("{id}: cannot reach {remote}: {err}",
			i18n.A{"id": f.target.ID, "remote": f.target.RemoteAddr(), "err": err}))
		return
	}
	defer l.Release()

	remote, err := l.Conn().Dial("tcp", f.target.RemoteAddr())
	if err != nil {
		f.failed.Add(1)
		log.Printf("[tunnel] %s", i18n.T("{id}: SSH is up but {remote} refuses the connection: {err}",
			i18n.A{"id": f.target.ID, "remote": f.target.RemoteAddr(), "err": err}))
		return
	}
	defer remote.Close()

	splice(local, remote)
}

// splice copies both ways, **closing both ends when either ends**.
//
// Closing the other end is required: when one side of an HTTP connection ends while the
// other stays open, closing only one leaves a goroutine stuck in Read forever - a leak
// whose symptom ("fds exhausted after a few days") is far from the scene.
func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		// Half-close: pass "I am done writing" along first (HTTP EOF relies on it), then wait.
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}

// close stops this forwarded port.
//
// Two phases: close the listener (no new connections), wait for in-flight ones to finish
// **naturally** (grace period), then **force-close** the rest.
//
// Phase two is not optional. An HTTP keep-alive connection never ends by itself (splice
// waits for both ends; the browser keeps it), so "wait until no in-flight" hangs forever
// - measured as a stop that hangs the whole timeout with only one log line "gave up on N
// in-flight". Same pair as http.Server's Shutdown (grace) then Close (force).
func (f *forwarder) close(ctx context.Context) error {
	if f == nil || !f.closed.CompareAndSwap(false, true) {
		return nil
	}
	if f.ln == nil {
		return nil // it was never up
	}
	err := f.ln.Close()

	done := make(chan struct{})
	go func() { f.wg.Wait(); close(done) }()
	select {
	case <-done:
		return err
	case <-ctx.Done():
	}

	// Grace period over: the rest must be force-closed, or shutdown never finishes.
	f.mu.Lock()
	left := len(f.live)
	for c := range f.live {
		_ = c.Close()
	}
	f.mu.Unlock()
	if left > 0 {
		log.Printf("[tunnel] %s", i18n.T("{id}: closing {n} connection(s) that were still open",
			i18n.A{"id": f.target.ID, "n": left}))
	}
	<-done
	return err
}

func (f *forwarder) track(c net.Conn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.live == nil {
		f.live = map[net.Conn]struct{}{}
	}
	f.live[c] = struct{}{}
}

func (f *forwarder) untrack(c net.Conn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.live, c)
}

// Status is this forwarded port right now (read by the UI and CLI).
type forwardStatus struct {
	Addr     string
	Err      string
	Accepted int64
	Failed   int64
}

func (f *forwarder) status() forwardStatus {
	if f == nil {
		return forwardStatus{}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	st := forwardStatus{
		Err:      f.err,
		Accepted: f.accepted.Load(),
		Failed:   f.failed.Load(),
	}
	if f.ln != nil {
		st.Addr = f.ln.Addr().String()
	}
	if st.Err == "" {
		st.Err = f.lastErr
	}
	return st
}

// dialBudget is how long a "request-driven dial" waits.
//
// 15s: two orders of magnitude above an SSH handshake (50-200ms on a LAN), yet short
// enough for a stuck request to fail before the browser's own timeout - first to fail wins.
const dialBudget = 15 * time.Second

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
