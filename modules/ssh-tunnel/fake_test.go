package sshtunnel

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 这一族假件把「SSH」换成一个**真的 TCP 连接**，只是它连的不是远端那台机器，
// 而是测试里的一个 httptest.Server。
//
// 为什么值得这样一个接缝：这个模块自己的行为（懒拨、复用、空闲回收、改写、门）
// 全都可以在没有 sshd 的情况下验干净，而它们是**每次提交都要跑的**。真实的
// OpenSSH 互操作是另一件事，由 mock/e2e_tunnel.sh 那条（真 sshd、真密钥）锁住
// ——两层各管各的，谁也不替谁。
type fakeSession struct {
	addr    string
	closed  atomic.Bool
	closeCh chan struct{}
}

func (s *fakeSession) Dial(network, addr string) (net.Conn, error) {
	// 刻意**无视 addr**：假件活着的时候，远端那个地址就在测试自己手里。
	// 这也是真实现的行为（DialContext 恒拨配置里那一对）。
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

// fakeDialer 记下拨了几次、失败几次，并交出到 `addr` 的假会话。
type fakeDialer struct {
	addr string

	mu     sync.Mutex
	dials  int
	fail   error
	failed int
	// hold 让拨号卡住（测「有人正在拨时别人等待」那条路）。
	hold chan struct{}
	// sessions 是拨出去的那些，测停机时能检查它们真的被关了。
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

// waitFor 等一个条件成立（或者超时）。用于等后台循环那一侧的效果——**不睡固定
// 时长**：睡固定的那种测试在慢机器上会偶发地红，而偶发地红的测试最后会被无视。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等 %s 超时", what)
}
