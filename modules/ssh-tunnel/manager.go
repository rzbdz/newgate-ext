package sshtunnel

import (
	"context"
	"log"
	"sync"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// manager 是运行态的全部：连接池、每 target 一个转发端口、以及那个让两者跟着配置
// 走的循环。
//
// # 它为什么只在 daemon 里活着
//
// 这个模块的 `Start` **每一条 `newgate …` 命令都会跑一遍**（内核的装配是单阶段的）。
// 在 Start 里起监听器或起循环的后果，内核已经用一句话记过：敲一次 `newgate status`
// 就开一台服务器。所以这里的 Serve 只登记到 `lib/serving` 上，等真正拥有端口的
// 那位（守护进程）说「我要开始服务了」——与 web-dashboard 的退路是同一条规矩。
//
// # 懒连接为什么不需要这个循环
//
// 走 route 的那些请求自己会 Acquire（第一发拨号，之后复用）。所以**没有常驻目标
// 的配置下，这个循环只做两件小事**：空闲回收与 keepalive。它不是一个「必需」的
// 部件，这是有意的——少一个「必须有后台循环才能工作」的部件，就少一类「循环挂了
// 但界面看起来正常」的故障。
type manager struct {
	pool *pool
	// proxy 只在停机时用得上：它持有的那些 HTTP 连接池把空闲连接留在那里，
	// 而那些连接占着 SSH 借约（见 proxy.closeIdle）。
	proxy *proxy
	// loaded 现读配置。循环每一轮都读一次，所以用户在浏览器里加了一个带
	// local_port 的 target，最多一轮（2s）之后它的端口就起来了。
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

// Serve 起转发端口与那个循环，返回一个停机的收尾函数。
//
// 它是 `serving.OnServe` 的 starter，**只在守护进程里被调用一次**。
func (m *manager) Serve() (func(), error) {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.sync() // 先把配置里要的端口都起上（用户重启 daemon 时期望它们马上在）
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

// loop 是那个循环：每 2 秒让运行态追上配置，顺带回收空闲连接。
//
// 2 秒是个取舍：用户在图里改完一个 target，两秒内看见它生效；而两秒一次遍历几条
// 配置的成本可以忽略（不碰网络——探活是**只在有连接时**发的一个 SSH 请求，频率
// 由 reap 的判据挡着）。
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

// sync 让运行态追上配置：该有的端口起来、不该有的关掉、常驻的保温。
func (m *manager) sync() {
	loaded := m.loaded()

	want := map[string]Target{}
	for _, t := range loaded.Targets {
		if t.LocalPort != 0 {
			want[t.ID] = t
		}
	}

	m.mu.Lock()
	// 关掉那些配置里已经没有、或者端口变了的转发端口（端口变了 = 旧的那个不该
	// 再占着）。
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

	// 起那些还没有的。
	for id, t := range want {
		m.mu.Lock()
		_, exists := m.forwards[id]
		m.mu.Unlock()
		if exists {
			continue
		}
		f, err := startForward(t, m.pool)
		if err != nil {
			// **不静默**：这条错误说的是「你配的那个端口没生效」，而它唯一的
			// 出口就是日志与界面上的状态（见 view.go 那一行）。其余 target 照常。
			log.Printf("[tunnel] %s", err.Error())
			m.mu.Lock()
			m.forwards[id] = &forwarder{target: t, err: err.Error()}
			m.mu.Unlock()
			continue
		}
		m.mu.Lock()
		m.forwards[id] = f
		m.mu.Unlock()
	}

	// 常驻的保温。放最后，因为它是唯一会花时间的一步（真的去拨号）。
	for _, t := range loaded.Targets {
		if t.Persistent {
			m.pool.Warm(context.Background(), t)
		}
	}
	// 配置里已经删掉的 target：忘掉它的连接。
	m.pool.Retain(loaded.Targets)
}

// shutdown 关掉一切：先转发端口（它们上面的连接归池管），再池。
func (m *manager) shutdown() {
	m.mu.Lock()
	fws := make([]*forwarder, 0, len(m.forwards))
	for _, f := range m.forwards {
		fws = append(fws, f)
	}
	m.forwards = map[string]*forwarder{}
	m.mu.Unlock()

	// 先放掉 HTTP 连接池里的空闲连接：它们占着借约，不还回去的话 pool.Close
	// 要一直等到 IdleConnTimeout（30s）——看起来就是「停不干净」。
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

// TargetStatus 是一个 target 此刻的全部事实（CLI 与 web 那张表都读它）。
type TargetStatus struct {
	Target  Target
	Conn    Snapshot
	Forward forwardStatus
	// ForwardErr 是转发端口没起来的原因（空 = 起来了，或者本来就没要）。
	ForwardErr string
}

// Status 是给界面/CLI 的快照。
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

// Rejected 是那份读不出来的配置（界面要把它画出来，见 config.go 的 Problem）。
func (m *manager) Rejected() []Problem { return m.loaded().Rejected }
