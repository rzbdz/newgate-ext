package sshtunnel

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This family of fakes swaps "SSH" for a **real TCP connection** that connects not to
// the remote machine but to an httptest.Server in the test.
//
// Why the seam is worth it: this module's own behavior (lazy dial, reuse, idle reap,
// rewrite, gate) is fully verifiable without sshd, and it **runs on every commit**. Real
// OpenSSH interop is another thing, locked by mock/e2e_tunnel.sh (real sshd, real keys)
// - two layers, each minding its own.
type fakeSession struct {
	addr    string
	closed  atomic.Bool
	closeCh chan struct{}
}

func (s *fakeSession) Dial(network, addr string) (net.Conn, error) {
	// Deliberately **ignores addr**: while the fake is alive, the remote address is the test's.
	// That is also the real behavior (DialContext always dials the configured pair).
	return net.Dial("tcp", s.addr)
}

func (s *fakeSession) SendRequest(string, bool, []byte) (bool, []byte, error) {
	if s.closed.Load() {
		return false, nil, errFakeClosed
	}
	return true, nil, nil
}

func (s *fakeSession) Close() error {
	if s.closed.CompareAndSwap(false, true) {
		close(s.closeCh)
	}
	return nil
}

var errFakeClosed = net.ErrClosed

// fakeDialer records dials and failures, and hands out a fake session to `addr`.
type fakeDialer struct {
	addr string

	mu     sync.Mutex
	dials  int
	fail   error
	failed int
	// hold blocks dialing (to test "others wait while one dials").
	hold chan struct{}
	// sessions are the dials out; shutdown tests check they really were closed.
	sessions []*fakeSession
}

func newFakeDialer(addr string) *fakeDialer { return &fakeDialer{addr: addr} }

func (d *fakeDialer) Dial(ctx context.Context, _ Target) (Session, error) {
	d.mu.Lock()
	d.dials++
	fail := d.fail
	hold := d.hold
	d.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if fail != nil {
		d.mu.Lock()
		d.failed++
		d.mu.Unlock()
		return nil, fail
	}
	s := &fakeSession{addr: d.addr, closeCh: make(chan struct{})}
	d.mu.Lock()
	d.sessions = append(d.sessions, s)
	d.mu.Unlock()
	return s, nil
}

func (d *fakeDialer) counts() (dials, failed int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dials, d.failed
}

func (d *fakeDialer) openSessions() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, s := range d.sessions {
		if !s.closed.Load() {
			n++
		}
	}
	return n
}

func (d *fakeDialer) setFail(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fail = err
}

// waitFor waits for a condition (or times out). For background-loop effects - **never
// sleep a fixed time**: fixed sleeps are flaky on slow machines, and flaky tests end up ignored.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
