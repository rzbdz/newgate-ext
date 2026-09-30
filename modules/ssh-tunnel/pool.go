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

// 一条 target 连接的状态。这几个词同时出现在 CLI、web 那张表与日志里——**一份
// 词汇**，否则「终端说 idle、界面说 disconnected」会让人以为是两回事。
const (
	StateIdle       = "idle"       // 还没有人用过，或者空闲超时已经断开
	StateConnecting = "connecting" // 正在拨（有人已经在拨了，别人在等）
	StateUp         = "up"
	StateDown       = "down" // 上一次拨号失败，原因在 lastErr
)

// pool 管所有 target 的 SSH 连接：懒拨、复用、空闲回收、常驻保活。
//
// # 为什么要有池，而不是每个请求拨一次
//
// SSH 握手是**公钥运算**（curve25519 + ed25519），不是一个 TCP 往返。内网实测
// 一次握手 50–200ms，比远端那个界面自己的一次 API 往返还贵。每个请求拨一次的话，
// 打开一张控制台要付几十次握手的钱，而这些握手完全一样——所以连接复用是这个模块
// 有没有实用价值的决定因素，不是一条优化。
//
// # 并发形状
//
// 每个 target 一个 entry，entry 自带一把锁。**拨号不在这把锁里面**：拨一次要几百
// 毫秒到十几秒，占着锁会让同一个 target 上的 Release 一起卡住（而 Release 是在
// 请求路径上的）。所以用「一个 in-flight 通道」表达「有人在拨」：别人等那条通道
// 关闭再重试，而不是排队占锁。
type pool struct {
	dialer Dialer

	mu      sync.Mutex
	entries map[string]*entry
	// closed 是 atomic 而不是放进 mu 里：Acquire 是**先拿 entry 的锁、再问关没关**
	// 的，而 entry() 是**先拿 mu、再拿 entry 的锁**的。把 closed 放进 mu 就凑成了
	// 一对必然互锁的箭头（ABBA），而那种死锁只在「停机正好撞上第一个请求」时出现
	// ——平时的测试一条都碰不到。
	closed atomic.Bool
	// wg 数的是**在途的租约**。停机时要等它归零，否则关掉连接会把正在跑的请求
	// 腰斩（症状是浏览器里一个莫名其妙的 502）。
	wg sync.WaitGroup
}

func newPool(d Dialer) *pool {
	return &pool{dialer: d, entries: map[string]*entry{}}
}

// entry 是一个 target 的连接与它的统计。
type entry struct {
	target Target

	mu      sync.Mutex
	sess    Session
	refs    int
	last    time.Time
	state   string
	lastErr string
	// since 是当前状态是什么时候进去的（界面要显示「连了 3 分钟」）。
	since time.Time
	// inflight 非 nil = 有人正在拨。等待者读它、等它关闭。
	inflight chan struct{}
	// stats 是给人看的那三个数：
	dials  int
	reused int
	lastOK time.Time
	// nextAttempt / backoff 只给常驻目标用：拨失败之后要等一会儿再试，
	// 否则一秒两轮的循环会变成对着一台关机的机器猛敲（也确实会把它敲醒）。
	nextAttempt time.Time
	backoff     time.Duration
}

// lease 是一次借用。用完必须 Release——它数着「这个连接此刻有几个用户在跑」，
// 而能不能回收全靠这个数。
type lease struct {
	p    *pool
	e    *entry
	sess Session
	once sync.Once
}

// Conn 是这次借到的那条连接。
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

// Acquire 借一条到 t 的连接，必要时现拨。
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
			// 别人在拨：等它，然后从头上再走一遍（可能成功，也可能该我重拨）。
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
			// 失败**不进缓存**：下一次请求再拨。缓存失败会让「网络恢复之后还要
			// 等一段退避」这种事发生，而用户看到的只是「我重启了路由器它还是连不上」。
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

// drop 关掉某个 target 当前的连接（下一次请求会重新拨）。
//
// 用在两处：空闲回收，以及「有人发现这条连接已经死了」。
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

// Forget 忘掉一个 target（它从配置里被删了）。
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
	// 目标地址改了（用户在界面上把 host 改了）：旧连接指向的是别处，必须断。
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

// Snapshot 是某个 target 此刻的连接状况（界面与 CLI 都读它）。
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

// reap 做两件事：关掉空闲的连接，探一探在场的连接还活着没有。
//
// 探活是必要的：一条 SSH 连接可能已经被对端（或中间的 NAT）悄悄掐掉了，而我们
// 这边的 socket 还开着。不探的话，用户点开界面看到的是**第一个请求卡住**，而不是
// 「自动重连之后再成功」。探活失败就丢掉这条连接，下一个请求自然重拨。
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
		// 常驻目标不按空闲断——那正是「常驻」的意思。它仍然要探活：一条被对端或
		// NAT 悄悄掐掉的连接，不探的话用户点开界面看到的是第一个请求卡住。
		if !keep && idle > 0 && now.Sub(last) > time.Duration(idle)*time.Second {
			p.drop(e.target.ID, i18n.T("idle for {n}s", i18n.A{"n": idle}))
			continue
		}
		if _, _, err := sess.SendRequest("keepalive@openssh.com", true, nil); err != nil {
			p.drop(e.target.ID, i18n.T("it stopped answering: {err}", i18n.A{"err": err}))
		}
	}
}

// Warm 让一个常驻目标保持连着。失败**不返回错误**：调用方是一个循环，它下一轮
// 还会来，而那时该说的话是日志里那一行，不是一次返回值。
//
// 退避 1s→30s：一台关机的机器被每秒敲两次是没意义的，而「它回来了我多久知道」
// 有 30 秒的上限对界面来说够用（用户按一下刷新会走懒连接那条路，立刻重拨）。
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
	// 立刻还回去：借约只表示「有人在用」，而常驻要的效果是**连接留着**——
	// refs 归零之后它才可能被回收，而回收只对非常驻目标发生。
	l.Release()
	e.mu.Lock()
	e.backoff = 0
	e.nextAttempt = time.Time{}
	e.mu.Unlock()
}

// Retain 忘掉不在列表里的 target（配置里删掉了）。
//
// 不做的后果是**连着的 SSH 连接永远不关**：配置里已经没有它了，没有任何一条路径
// 会再去碰它，而它占着一条 SSH 连接与它的 goroutine（症状是「删了之后
// `ss -tnp` 里还挂着一条到那台机器的连接」，离案发现场很远）。
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

// Close 关掉所有连接。
//
// 先等租约归零（在途请求跑完），再关——顺序反了的话，正在写响应的一次代理会被
// 从底下抽掉连接，用户看到的是一个没有正文的 502。等待有上限：一条卡住的请求不该
// 让 daemon 停不下来。
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

// humanizeSince 把「这个状态多久了」说成人话（CLI 与界面共用）。
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
