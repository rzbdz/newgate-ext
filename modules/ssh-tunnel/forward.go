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

// forwarder 是本机的一个转发端口：`127.0.0.1:<local_port>` 上的每一条连接都被送到
// 远端那台机器的 remote_host:remote_port。
//
// 这就是 ssh(1) 的 `-L`，而它存在的理由是**保真**：路径一个字节都不动，远端界面
// 里那些写死的绝对地址（/ui/assets/…）原样可用。共享端口上那条 route 做不到这一点
// （要改写），所以两条路都在。
type forwarder struct {
	target Target
	// ln 为 nil 表示**这个转发端口没起来**，原因在 err 里。留一个「空的 forwarder」
	// 而不是不放它，是为了让界面上那一行能说出「你配了 9401，但它没起来」——
	// 少一行看起来只像「你没配」。
	ln   net.Listener
	err  string
	pool *pool

	wg     sync.WaitGroup
	closed atomic.Bool
	mu     sync.Mutex
	// live 是此刻在跑的那些连接。
	//
	// 为什么要记着它们：一条 HTTP keep-alive 连接**不会自己结束**——splice 要等
	// 两端都关，而浏览器会把它留着复用。所以「等在途的连接跑完」这条在停机时会
	// 永远等下去（实测：停一次要挂满整个超时）。记着它们，才能在宽限期之后**强制
	// 关掉**——顺序与 http.Server 的 Shutdown/Close 那一对是同一个道理。
	live map[net.Conn]struct{}
	// lastErr 是最近一次 accept 失败的原因（给诊断看）。accept 失败通常是监听器
	// 已经关了，那是停机的正常路径，不该当成故障报。
	lastErr string
	// accepted / failed 是两个计数器，界面上「这条通道有没有在用」全靠它。
	accepted atomic.Int64
	failed   atomic.Int64
	// nextTry 只对**没起来**的那些有意义（见 failedForward）。
	nextTry time.Time
}

// failedForward 是「这个端口没起来」的那一行。
//
// nextTry 是**什么时候再试一次**。为什么不一次失败就永远放弃：最常见的那次失败
// 恰恰发生在**优雅交接**之后——老进程还占着那个端口在排空，新进程一上来绑当然
// 绑不上（实机踩过：升级完 9401 一直是死的，而 route 那条好好的，看起来像
// 「只有转发端口坏了」）。老进程几秒后就走了，重试一次就好了。
func failedForward(t Target, why string) *forwarder {
	return &forwarder{target: t, err: why, closed: *new(atomic.Bool),
		nextTry: time.Now().Add(forwardRetry)}
}

// forwardRetry 是端口没绑上之后多久再试一次。
//
// 2 秒：短到「交接完就自己好了」在一两次重试内发生，长到被别的程序长期占着时
// 日志里两秒一行还不算刷屏（而那一行是用户唯一能看到的「你配的端口没生效」）。
const forwardRetry = 2 * time.Second

// due 报告这一条是不是该再试一次了。
func (f *forwarder) due(now time.Time) bool {
	if f == nil || f.ln != nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return !now.Before(f.nextTry)
}

// startForward 起一个转发端口。
//
// **只绑 127.0.0.1，没有配置项可改**。理由不是审美：这个端口背后是一台**没有
// Host 门**的远端界面——远端 BFF 那道 rebinding 判据查的是它自己收到的 Host，
// 而从这里过去的连接，Host 就是「别的什么人填的」。绑到 0.0.0.0 等于把一台远端
// 机器的控制台对局域网敞开，而本机这一侧没有任何一道门能挡。要给别人用，该让别人
// 自己建一条 tunnel（他们有自己的 SSH 凭据）。
func startForward(t Target, p *pool) (*forwarder, error) {
	if t.LocalPort == 0 {
		return nil, nil
	}
	addr := net.JoinHostPort("127.0.0.1", itoa(t.LocalPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// 端口被占**不静默**：这是用户唯一能知道「我配的那个端口没生效」的地方。
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
			// accept 的错误分两类：临时的（EMFILE 之类）值得再试，永久的（监听器
			// 没了）该退出。不区分的话，一条永久错误会让这里变成一个每毫秒打一行
			// 日志的忙循环。
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

// handle 把一条本机连接接到远端去。
func (f *forwarder) handle(local net.Conn) {
	f.track(local)
	defer f.untrack(local)
	defer local.Close()

	// 借连接（必要时现拨）。用请求方自己的上下文做不到——这里没有 HTTP 请求，
	// 只有一条裸 TCP；给一个够长的预算，够一次 SSH 握手就够。
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

// splice 双向拷贝，**任一端结束就把两端都关掉**。
//
// 关另一端是必需的：一条 HTTP 连接的上游结束而下游还开着（或者反过来）时，只关
// 一边会让另一个 goroutine 永远挂在 Read 上——那是连接泄漏，而它的症状是「跑几天
// 之后 fd 用光了」，离案发现场很远。
func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		// 半关闭：先把「我这边写完了」传过去（HTTP 的 EOF 靠它），再等对面也结束。
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

// close 停掉这个转发端口。
//
// 两段：先关监听器（不再有新连接）、等在途的连接**自然**跑完（宽限期），然后
// **强制关掉**还在的那些。
//
// 第二段不能省。一条 HTTP keep-alive 连接不会自己结束（splice 要等两端都关，而
// 浏览器会把它留着复用），所以「一直等到没有在途连接」在停机时会永远等下去——
// 实测的表现是停一次挂满整个超时，而日志里只有一句「放弃了 N 条在途连接」。
// 这与 http.Server 的 Shutdown（宽限）→ Close（强制）是同一对。
func (f *forwarder) close(ctx context.Context) error {
	if f == nil || !f.closed.CompareAndSwap(false, true) {
		return nil
	}
	if f.ln == nil {
		return nil // 本来就没起来
	}
	err := f.ln.Close()

	done := make(chan struct{})
	go func() { f.wg.Wait(); close(done) }()
	select {
	case <-done:
		return err
	case <-ctx.Done():
	}

	// 宽限期到了：剩下的必须强关，否则停机永远走不完。
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

// Status 是这个转发端口此刻的样子（界面与 CLI 读它）。
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

// dialBudget 是一次「请求驱动的拨号」愿意等多久。
//
// 15 秒：比一次 SSH 握手（内网 50–200ms）宽两个数量级，又短到让一个卡住的请求
// 在浏览器自己超时之前先失败——先失败的那一方能给出理由。
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
