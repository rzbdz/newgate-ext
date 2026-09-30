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

// 这一族验的是**这个模块自己**的行为：什么时候拨、拨几次、什么时候断、断了之后
// 下一个请求怎么办。全部用一个假的 Dialer，所以它们在每次提交里都跑，而且不依赖
// 这台机器上有没有 sshd、有没有密钥、网络通不通。

// ---------- 借用与复用 ----------

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
		t.Errorf("拨了 %d 次，想要 1 次", dials)
	}
	if snap, _ := p.Snapshot("ds"); snap.Refs != 2 {
		t.Errorf("两次借用之后 refs = %d，想要 2", snap.Refs)
	}
	first.Release()
	second.Release()
	first.Release() // 幂等：多放一次不该把计数放到负数去
	if snap, _ := p.Snapshot("ds"); snap.Refs != 0 {
		t.Errorf("放完之后 refs = %d，想要 0（多放一次不该变成 -1）", snap.Refs)
	}
}

func TestConcurrentAcquiresShareOneDial(t *testing.T) {
	// 两个人同时打进来时只能有一次握手。不挡的话，一个页面加载时发的十几个请求
	// 会各自拨一次——而 SSH 握手是公钥运算，不是一次 TCP 往返。
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
	// 等它们都进到「有人在拨」那一格，再放行。
	waitFor(t, "所有请求都开始等", func() bool {
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
		t.Errorf("%d 个并发请求拨了 %d 次，想要 1 次", n, dials)
	}
}

// ---------- 出错 ----------

func TestAFailedAcquireLeavesNothingBehind(t *testing.T) {
	d := newFakeDialer("127.0.0.1:1")
	d.setFail(errors.New("no route to host"))
	p := newPool(d)
	defer closePool(t, p)

	if _, err := p.Acquire(context.Background(), target("ds")); err == nil {
		t.Fatal("该失败")
	}
	snap, _ := p.Snapshot("ds")
	if snap.State != StateDown {
		t.Errorf("状态 %q，想要 down", snap.State)
	}
	if !strings.Contains(snap.LastErr, "no route to host") {
		t.Errorf("LastErr = %q（原因要留着，界面上那一格靠它说话）", snap.LastErr)
	}
	if snap.Refs != 0 {
		t.Errorf("失败之后 refs = %d，想要 0", snap.Refs)
	}

	// 再试一次会**真的再拨**（失败不进缓存）。
	d.setFail(nil)
	l, err := p.Acquire(context.Background(), target("ds"))
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	if dials, _ := d.counts(); dials != 2 {
		t.Errorf("拨了 %d 次，想要 2", dials)
	}
}

// ---------- 空闲回收 ----------

func TestIdleConnectionsAreLetGo(t *testing.T) {
	// 一条 SSH 连接挂在那里是有成本的（服务端一个进程、本机一个 goroutine、
	// 中间设备一条 conntrack）。懒连接的全部意义就是它该自己断掉。
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

	p.reap(time.Now()) // 刚用完：不该断
	if d.openSessions() != 1 {
		t.Fatal("刚借完就断了")
	}
	p.reap(time.Now().Add(2 * time.Second)) // 超过空闲：该断
	if got := d.openSessions(); got != 0 {
		t.Errorf("空闲超时之后还剩 %d 条连接", got)
	}
	if snap, _ := p.Snapshot("ds"); snap.State != StateIdle {
		t.Errorf("断开之后状态 %q，想要 idle", snap.State)
	}
}

func TestPersistentTargetsSurviveIdle(t *testing.T) {
	// 「常驻」的意思就是它**不**按空闲断。断了的话，用户下次打开界面要重新等一次
	// 握手——而那个等待正是他配置常驻想避免的东西。
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
		t.Errorf("常驻连接被空闲回收掉了（剩 %d 条）", got)
	}
}

func TestADeadConnectionIsDroppedByTheKeepalive(t *testing.T) {
	// 一条 SSH 连接可能已经被对端（或中间的 NAT）悄悄掐掉了，而我们这边的 socket
	// 还开着。不探的话，用户点开界面看到的是**第一个请求卡住**，而不是
	// 「自动重连之后再成功」。
	d := newFakeDialer("127.0.0.1:1")
	p := newPool(d)
	defer closePool(t, p)
	tt := target("ds")

	l, _ := p.Acquire(context.Background(), tt)
	l.Release()
	// 把底层那条假会话标记成「对端已经走了」。
	d.mu.Lock()
	d.sessions[0].closed.Store(true)
	d.mu.Unlock()

	p.reap(time.Now())
	if got := d.openSessions(); got != 0 {
		t.Errorf("探活没发现它已经死了（还剩 %d 条）", got)
	}
	// 下一个请求会重新拨——这才是用户看到的效果（重连，而不是卡住）。
	l2, err := p.Acquire(context.Background(), tt)
	if err != nil {
		t.Fatal(err)
	}
	l2.Release()
	if dials, _ := d.counts(); dials != 2 {
		t.Errorf("拨了 %d 次，想要 2（一次原始的 + 一次重连）", dials)
	}
}

// ---------- 配置变了 ----------

func TestChangingTheAddressDropsTheOldConnection(t *testing.T) {
	// 用户在界面上把 host 改了，而旧连接指向的是**原来那台机器**。留着它的话，
	// 症状是「我明明改了地址，打开的却还是旧那台」——而那个界面看起来是对的。
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
		t.Errorf("换了地址之后拨了 %d 次，想要 2", dials)
	}
}

func TestRetainForgetsTargetsThatAreGone(t *testing.T) {
	// 配置里删掉的 target 没有任何一条路径会再碰它，而它占着一条 SSH 连接。
	// 不回收的症状是「删了之后 ss 里还挂着一条到那台机器的连接」，离案发现场很远。
	d := newFakeDialer("127.0.0.1:1")
	p := newPool(d)
	defer closePool(t, p)

	l, _ := p.Acquire(context.Background(), target("ds"))
	l.Release()
	l, _ = p.Acquire(context.Background(), target("ark"))
	l.Release()

	p.Retain([]Target{target("ark")})
	if _, ok := p.Snapshot("ds"); ok {
		t.Error("ds 已经不在配置里了，池子里还留着它")
	}
	if got := d.openSessions(); got != 1 {
		t.Errorf("还剩 %d 条连接，想要 1", got)
	}
}

// ---------- 停机 ----------

func TestCloseReleasesEverything(t *testing.T) {
	d := newFakeDialer("127.0.0.1:1")
	p := newPool(d)

	l, _ := p.Acquire(context.Background(), target("ds"))
	l.Release()
	closePool(t, p)

	if got := d.openSessions(); got != 0 {
		t.Errorf("停机之后还剩 %d 条连接没关", got)
	}
	if _, err := p.Acquire(context.Background(), target("ds")); !errors.Is(err, errShuttingDown) {
		t.Errorf("停机之后还能借到连接：%v", err)
	}
}

func TestCloseWaitsForInFlightWork(t *testing.T) {
	// 不等的话，一条正在写的响应会被从底下抽掉连接——用户看到的是一个没有正文的
	// 502，而它离「我刚才停了一下服务」很远。
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
		t.Fatal("在途的借用还没还回去，停机就完成了")
	case <-time.After(50 * time.Millisecond):
	}
	l.Release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("借用还回去之后停机还是没完成")
	}
}

// ---------- 转发端口 ----------

// freePort 要一个此刻空闲的端口号。
//
// 关掉再返回它，所以两次调用可能拿到同一个——测试里够用（同一时刻只有一个在用）。
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
	// 这条路的全部价值就在「原样」：远端界面里那些写死的绝对地址
	// （/ui/assets/…）必须一个字节都不被动。这是保真那条路存在的理由。
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
	// 一个**不带 /ui 前缀**的路径：转发那条路完全不碰路径，远端的 /x/y 就是 /x/y。
	resp, err := http.Get("http://127.0.0.1:" + itoa(tt.LocalPort) + "/x/y")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "path=/x/y" {
		t.Errorf("远端收到的是 %q，想要 path=/x/y（转发这条路不改路径）", body)
	}

	// 停机：那条连接是 HTTP keep-alive 的，**它自己不会结束**，所以宽限期一到必须
	// 被强制关掉。不给它这一手的话，停机要挂满整个超时（实测过），而日志里只有
	// 一句「放弃了 N 条在途连接」。给一个短的宽限期，断言它在宽限期之后就回来。
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := f.close(ctx); err != nil {
		t.Errorf("关转发端口报错：%v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("关一次花了 %v——keep-alive 的连接没被强制关掉", elapsed)
	}
}

func TestABusyForwardingPortIsReportedNotHidden(t *testing.T) {
	// 用户配的那个端口没生效，是他**唯一**能在这条命令之外发现的地方。
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
		t.Fatal("端口被占时该报错")
	}
	if !strings.Contains(err.Error(), "cannot listen on") {
		t.Errorf("错误没说清是端口的事：%v", err)
	}
}

func TestForwardingOnlyListensOnLoopback(t *testing.T) {
	// 这个端口背后是一台**没有 Host 门**的远端界面，而本机这一侧没有任何一道门
	// 能挡。绑到别的地址等于把它对局域网敞开——所以这件事必须是不可配的。
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
		t.Errorf("监听在 %s 上，必须只绑回环", addr)
	}
	if got := f.status().Addr; !strings.HasPrefix(got, "127.0.0.1:") {
		t.Errorf("报告的地址是 %q，想要 127.0.0.1:…", got)
	}
}

func TestAFailedForwardingPortDoesNotStopTheOthers(t *testing.T) {
	// 一个端口没起来（被占）不该让别的 target 一起失效——那看起来像「整个 tunnel
	// 坏了」，而实际上只有那一条配置有问题。
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
		t.Fatalf("状态里有 %d 条，想要 2", len(st))
	}
	byID := map[string]TargetStatus{}
	for _, s := range st {
		byID[s.Target.ID] = s
	}
	if byID["bad"].ForwardErr == "" {
		t.Error("起不来的那条没有报出原因")
	}
	if byID["good"].Forward.Addr == "" {
		t.Error("好的那条被带坏了")
	}
	m.shutdown()
}

// ---------- 索引页 ----------

func closePool(t *testing.T, p *pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.Close(ctx)
}
