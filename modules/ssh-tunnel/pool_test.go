package sshtunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// This family verifies **this module's own** behavior: when it dials, how often, when it
// closes, what the next request does after a close. All with a fake Dialer, so they run
// on every commit and need no sshd, keys or network on this machine.

// ---------- borrow and reuse ----------

func TestAcquireDialsOnceAndHandsOutLeases(t *testing.T) {
	d := newFakeDialer("127.0.0.1:1")
	p := newPool(d)
	defer closePool(t, p)
	tt := target("ds")

	first, err := p.Acquire(context.Background(), tt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Acquire(context.Background(), tt)
	if err != nil {
		t.Fatal(err)
	}
	if dials, _ := d.counts(); dials != 1 {
		t.Errorf("dialed %d times, want 1", dials)
	}
	if snap, _ := p.Snapshot("ds"); snap.Refs != 2 {
		t.Errorf("after two loans refs = %d, want 2", snap.Refs)
	}
	first.Release()
	second.Release()
	first.Release() // idempotent: one extra release must not drive the count negative
	if snap, _ := p.Snapshot("ds"); snap.Refs != 0 {
		t.Errorf("after releasing refs = %d, want 0 (one extra release must not become -1)", snap.Refs)
	}
}

func TestConcurrentAcquiresShareOneDial(t *testing.T) {
	// Two simultaneous requests share one handshake. Otherwise a page load's dozen requests
	// each dial - and an SSH handshake is public-key math, not one TCP round trip.
	d := newFakeDialer("127.0.0.1:1")
	d.hold = make(chan struct{})
	p := newPool(d)
	defer closePool(t, p)

	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			l, err := p.Acquire(context.Background(), target("ds"))
			if err == nil {
				l.Release()
			}
			errs <- err
		}()
	}
	// Wait until both reach "someone is dialing", then let go.
	waitFor(t, "all requests started waiting", func() bool {
		snap, _ := p.Snapshot("ds")
		return snap.State == StateConnecting
	})
	close(d.hold)
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if dials, _ := d.counts(); dials != 1 {
		t.Errorf("%d concurrent requests dialed %d times, want 1", n, dials)
	}
}

// ---------- errors ----------

func TestAFailedAcquireLeavesNothingBehind(t *testing.T) {
	d := newFakeDialer("127.0.0.1:1")
	d.setFail(errors.New("no route to host"))
	p := newPool(d)
	defer closePool(t, p)

	if _, err := p.Acquire(context.Background(), target("ds")); err == nil {
		t.Fatal("it should fail")
	}
	snap, _ := p.Snapshot("ds")
	if snap.State != StateDown {
		t.Errorf("status %q, want down", snap.State)
	}
	if !strings.Contains(snap.LastErr, "no route to host") {
		t.Errorf("LastErr = %q (the reason must be kept; the cell in the UI relies on it)", snap.LastErr)
	}
	if snap.Refs != 0 {
		t.Errorf("after the failure refs = %d, want 0", snap.Refs)
	}

	// Retrying **really dials again** (failures are not cached).
	d.setFail(nil)
	l, err := p.Acquire(context.Background(), target("ds"))
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	if dials, _ := d.counts(); dials != 2 {
		t.Errorf("dialed %d times, want 2", dials)
	}
}

// ---------- idle reap ----------

func TestIdleConnectionsAreLetGo(t *testing.T) {
	// An idle SSH connection costs (a server process, a local goroutine, a conntrack entry).
	// The whole point of a lazy connection is that it closes itself.
	d := newFakeDialer("127.0.0.1:1")
	p := newPool(d)
	defer closePool(t, p)
	tt := target("ds")
	tt.IdleSeconds = 1

	l, err := p.Acquire(context.Background(), tt)
	if err != nil {
		t.Fatal(err)
	}
	l.Release()

	p.reap(time.Now()) // just used: must not close
	if d.openSessions() != 1 {
		t.Fatal("it broke right after being loaned")
	}
	p.reap(time.Now().Add(2 * time.Second)) // past idle: must close
	if got := d.openSessions(); got != 0 {
		t.Errorf("%d connections remain after the idle timeout", got)
	}
	if snap, _ := p.Snapshot("ds"); snap.State != StateIdle {
		t.Errorf("after disconnect status %q, want idle", snap.State)
	}
}

func TestPersistentTargetsSurviveIdle(t *testing.T) {
	// "Persistent" means it is **not** closed for idleness. Closed, the next page open waits
	// for a handshake again - the very wait persistence was configured to avoid.
	d := newFakeDialer("127.0.0.1:1")
	p := newPool(d)
	defer closePool(t, p)
	tt := target("ds")
	tt.IdleSeconds = 1
	tt.Persistent = true

	l, _ := p.Acquire(context.Background(), tt)
	l.Release()
	p.reap(time.Now().Add(time.Hour))
	if got := d.openSessions(); got != 1 {
		t.Errorf("the persistent connection was reclaimed by idle timeout (%d remain)", got)
	}
}

func TestADeadConnectionIsDroppedByTheKeepalive(t *testing.T) {
	// An SSH connection may already be silently dropped by the peer (or a NAT in between),
	// while our socket stays open. Unprobed, the user's first request **hangs** instead
	// of "reconnected automatically, then succeeded".
	d := newFakeDialer("127.0.0.1:1")
	p := newPool(d)
	defer closePool(t, p)
	tt := target("ds")

	l, _ := p.Acquire(context.Background(), tt)
	l.Release()
	// Mark the underlying fake session as "the peer is gone".
	d.mu.Lock()
	d.sessions[0].closed.Store(true)
	d.mu.Unlock()

	p.reap(time.Now())
	if got := d.openSessions(); got != 0 {
		t.Errorf("the probe did not notice it was already dead (%d remain)", got)
	}
	// The next request redials - that is what users see (a reconnect, not a hang).
	l2, err := p.Acquire(context.Background(), tt)
	if err != nil {
		t.Fatal(err)
	}
	l2.Release()
	if dials, _ := d.counts(); dials != 2 {
		t.Errorf("dialed %d times, want 2 (one original + one reconnect)", dials)
	}
}

// ---------- config changed ----------

func TestChangingTheAddressDropsTheOldConnection(t *testing.T) {
	// The user changed host in the UI, and the old connection points at the **old machine**.
	// Kept, the symptom is "I changed the address but still get the old one".
	d := newFakeDialer("127.0.0.1:1")
	p := newPool(d)
	defer closePool(t, p)

	l, _ := p.Acquire(context.Background(), target("ds"))
	l.Release()

	moved := target("ds")
	moved.Host = "elsewhere.example"
	l2, err := p.Acquire(context.Background(), moved)
	if err != nil {
		t.Fatal(err)
	}
	l2.Release()
	if dials, _ := d.counts(); dials != 2 {
		t.Errorf("after the address changed it dialed %d times, want 2", dials)
	}
}

func TestRetainForgetsTargetsThatAreGone(t *testing.T) {
	// A target removed from config is touched by no path and holds an SSH connection.
	// Not reaped, the symptom is "after deleting, ss still shows a connection", far from the scene.
	d := newFakeDialer("127.0.0.1:1")
	p := newPool(d)
	defer closePool(t, p)

	l, _ := p.Acquire(context.Background(), target("ds"))
	l.Release()
	l, _ = p.Acquire(context.Background(), target("ark"))
	l.Release()

	p.Retain([]Target{target("ark")})
	if _, ok := p.Snapshot("ds"); ok {
		t.Error("ds is no longer in the config, yet it is still in the pool")
	}
	if got := d.openSessions(); got != 1 {
		t.Errorf("%d connections remain, want 1", got)
	}
}

// ---------- shutdown ----------

func TestCloseReleasesEverything(t *testing.T) {
	d := newFakeDialer("127.0.0.1:1")
	p := newPool(d)

	l, _ := p.Acquire(context.Background(), target("ds"))
	l.Release()
	closePool(t, p)

	if got := d.openSessions(); got != 0 {
		t.Errorf("%d connections were left open after shutdown", got)
	}
	if _, err := p.Acquire(context.Background(), target("ds")); !errors.Is(err, errShuttingDown) {
		t.Errorf("a connection can still be borrowed after shutdown: %v", err)
	}
}

func TestCloseWaitsForInFlightWork(t *testing.T) {
	// Without waiting, a response being written loses its connection underneath - a
	// bodyless 502, far from "I just restarted the service".
	d := newFakeDialer("127.0.0.1:1")
	p := newPool(d)

	l, _ := p.Acquire(context.Background(), target("ds"))
	done := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.Close(ctx)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("shutdown finished while a loan was still out")
	case <-time.After(50 * time.Millisecond):
	}
	l.Release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("after the loan was returned, shutdown is still not done")
	}
}

// ---------- forwarded port ----------

// freePort asks for a currently free port number.
//
// It closes before returning, so two calls may get the same one - fine in tests (one at a time).
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestTheForwardingPortMovesBytesUntouched(t *testing.T) {
	// The whole value of this route is "verbatim": hardcoded absolute addresses in the
	// remote UI (/ui/assets/...) must not change one byte. That is why the faithful route exists.
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "path="+r.URL.Path)
	}))
	defer remote.Close()

	d := newFakeDialer(strings.TrimPrefix(remote.URL, "http://"))
	p := newPool(d)
	defer closePool(t, p)

	tt := target("ds")
	tt.LocalPort = freePort(t)
	f, err := startForward(tt, p)
	if err != nil {
		t.Fatal(err)
	}
	// A path **without the /ui prefix**: forwarding never touches paths; /x/y is /x/y remotely.
	resp, err := http.Get("http://127.0.0.1:" + itoa(tt.LocalPort) + "/x/y")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "path=/x/y" {
		t.Errorf("the remote received %q, want path=/x/y (the forward path does not rewrite paths)", body)
	}

	// Shutdown: that connection is HTTP keep-alive, **it never ends by itself**, so after the
	// grace period it must be force-closed. Without that, shutdown hangs the whole timeout
	// (measured) with one log line "gave up on N in-flight". Assert it returns after a short grace.
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := f.close(ctx); err != nil {
		t.Errorf("closing the forward port failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("closing once took %v - the keep-alive connections were not forcibly closed", elapsed)
	}
}

func TestABusyForwardingPortIsReportedNotHidden(t *testing.T) {
	// The port the user configured did not take effect - the **only** way to see it outside it.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	d := newFakeDialer("127.0.0.1:1")
	p := newPool(d)
	defer closePool(t, p)

	tt := target("ds")
	tt.LocalPort = busy.Addr().(*net.TCPAddr).Port
	_, err = startForward(tt, p)
	if err == nil {
		t.Fatal("an occupied port should error")
	}
	if !strings.Contains(err.Error(), "cannot listen on") {
		t.Errorf("the error does not make clear it is about the port: %v", err)
	}
}

func TestForwardingOnlyListensOnLoopback(t *testing.T) {
	// Behind this port is a remote UI **without a Host gate**, and this side has no gate
	// either. Binding elsewhere opens it to the LAN - so this must not be configurable.
	d := newFakeDialer("127.0.0.1:1")
	p := newPool(d)
	defer closePool(t, p)

	tt := target("ds")
	tt.LocalPort = freePort(t)
	f, err := startForward(tt, p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = f.close(ctx)
	}()

	addr := f.ln.Addr().(*net.TCPAddr)
	if !addr.IP.IsLoopback() {
		t.Errorf("listening on %s, it must bind loopback only", addr)
	}
	if got := f.status().Addr; !strings.HasPrefix(got, "127.0.0.1:") {
		t.Errorf("the reported address is %q, want 127.0.0.1:...", got)
	}
}

func TestAFailedForwardingPortDoesNotStopTheOthers(t *testing.T) {
	// One port failing (taken) must not break other targets - that looks like "the whole
	// tunnel is broken", while only one config entry is at fault.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	d := newFakeDialer("127.0.0.1:1")
	p := newPool(d)
	defer closePool(t, p)

	bad := target("bad")
	bad.LocalPort = busy.Addr().(*net.TCPAddr).Port
	good := target("good")
	good.LocalPort = freePort(t)

	loaded := Loaded{Targets: []Target{bad, good}}
	m := newManager(p, func() Loaded { return loaded })
	m.sync()

	st := m.Status()
	if len(st) != 2 {
		t.Fatalf("status has %d entries, want 2", len(st))
	}
	byID := map[string]TargetStatus{}
	for _, s := range st {
		byID[s.Target.ID] = s
	}
	if byID["bad"].ForwardErr == "" {
		t.Error("the one that could not start did not report a reason")
	}
	if byID["good"].Forward.Addr == "" {
		t.Error("the good one was dragged down with it")
	}
	m.shutdown()
}

// ---------- index page ----------

func closePool(t *testing.T, p *pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.Close(ctx)
}

func TestABusyForwardingPortIsRetried(t *testing.T) {
	// The most common `net.Listen` failure comes right after a **graceful handover**: the
	// old process still holds the port draining, so the new one cannot bind. It leaves in
	// seconds, so a failure must be **recoverable** - unretried, the forwarded port stays
	// dead after an upgrade while the route is fine, as if only that entry broke.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := busy.Addr().(*net.TCPAddr).Port

	d := newFakeDialer("127.0.0.1:1")
	p := newPool(d)
	defer closePool(t, p)

	tt := target("ds")
	tt.LocalPort = port
	loaded := Loaded{Targets: []Target{tt}}
	m := newManager(p, func() Loaded { return loaded })
	defer m.shutdown()

	m.sync()
	if got := m.Status()[0].ForwardErr; got == "" {
		t.Fatal("an occupied port should be reported")
	}

	// "The old process left": the port is free but the retry window is not here yet - this
	// step must **still** fail, or the assertion does not test the retry (only luck).
	_ = busy.Close()
	m.sync()
	if got := m.Status()[0].ForwardErr; got == "" {
		t.Fatal("retried too fast: it bound again the instant the port freed up (backoff not in effect)")
	}

	// Past the backoff window, the next sync should pick it up.
	time.Sleep(forwardRetry + 100*time.Millisecond)
	m.sync()
	st := m.Status()[0]
	if st.ForwardErr != "" {
		t.Fatalf("it still did not bind after the backoff: %s", st.ForwardErr)
	}
	if st.Forward.Addr == "" {
		t.Fatal("it bound, but the address is empty")
	}
}
