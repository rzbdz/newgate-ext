package proto

import (
	"context"
	"log"
	"math/rand"
	"strings"
	"time"

	"github.com/rzbdz/newgate/lib/i18n"
)

// 退避参数：失败后 30 → 60 → 120 → 300（封顶），成功即回基准。
//
// 基准与上限不是同一个数：正常节奏（interval，默认 30s）可以比失败后的第一次
// 退避短。这两件事的语义不同——一个是"多久检查一次有没有新配置"，另一个是
// "对端已经出问题了，多久再去试一次"。
const (
	backoffBase = 30 * time.Second
	backoffMax  = 300 * time.Second
	jitterFrac  = 0.2
)

// puller 是循环依赖的那一件事（给测试留的缝：不用真网络就能驱动整个循环）。
type puller interface {
	Pull(ctx context.Context, force bool) (Outcome, error)
}

// Poller 是 daemon 里的后台拉取。
type Poller struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// StartPoller 按机器本地的设置起后台拉取。
//
// 返回 (nil, nil) 是**正常情况**，不是错误：这台机器没在用配置共享（role 没设）、
// 或者显式关了 auto_pull。绝大多数机器就是这样，启动路径上不该为此报错。
//
// # 为什么由 cli.Serve 调用，而不是在模块的 Start 里起
//
// 不只是躲 import 环，更是个真 bug：app.New() 在**每次调用**都建整张图，
// 包括 `newgate status`，以及最要命的 `newgate claude`（PATH shim 那条路，
// cmd/newgate/main.go 先 app.New 再分发 wrapper）。在 Start 里起 poller =
// 每个短命 CLI 进程、包括 agent 启动的那一次，都往 agent 的启动路径里塞一次
// 网络拉取。而"我自己是不是那个 daemon"的 self-PID 守卫在**优雅交接**时会读到
// 错的 pid（pidfile 正在进程之间过渡，cli.Serve 里有专门注释），结果是新 daemon
// 静默地没有 poller——对后台同步来说，这是最坏的失败模式。
func StartPoller(ctx context.Context, version string, lg *log.Logger) (*Poller, error) {
	settings, err := LoadSettings()
	if err != nil {
		return nil, err
	}
	if !settings.IsClient() || !settings.Auto() {
		return nil, nil
	}
	rootKey, fromEnv, err := RootKeyFromEnv()
	if !fromEnv {
		if rootKey, err = LoadRootKey(); err != nil {
			// fail-open：daemon 照常转发，只是不拉配置。说清楚怎么修。
			lg.Printf("[configshare] %s", i18n.T(
				"background sync did not start: {err} (on the host run newgate config secrets init to get the blob, then on this machine run newgate config trust <blob>)",
				i18n.A{"err": err}))
			return nil, nil
		}
	} else if err != nil {
		return nil, err
	}
	c, err := New(Config{
		Endpoint: settings.Endpoint,
		RootKey:  rootKey,
		Version:  version,
		Log:      lg,
	})
	if err != nil {
		return nil, err
	}
	interval := settings.Interval()
	p := runLoop(ctx, c, interval, lg, nil)
	lg.Printf("[configshare] %s", i18n.T(
		"background sync started (replica): {endpoint}, one round every {interval}, root key fingerprint {fingerprint}",
		i18n.A{"endpoint": settings.Endpoint, "interval": interval, "fingerprint": RootKeyFingerprint(rootKey)}))
	return p, nil
}

// runLoop 起循环。probe 是测试用的观察点（每一轮结束时回调一次），生产传 nil。
func runLoop(ctx context.Context, pl puller, interval time.Duration, lg *log.Logger, probe func(round int, delay time.Duration)) *Poller {
	pctx, cancel := context.WithCancel(ctx)
	p := &Poller{cancel: cancel, done: make(chan struct{})}
	go p.loop(pctx, pl, interval, lg, probe)
	return p
}

// Stop 停掉后台拉取并等它退干净。
//
// cancel 会掐断在飞的 HTTP 请求（它挂在 pctx 上），所以这个等待是有界的：
// 不会因为对端不响应而卡住 shutdown。优雅交接时这一点很重要——旧 daemon 要
// 在排空后退出，不该被一个卡住的同步拖着。
func (p *Poller) Stop() {
	if p == nil {
		return
	}
	p.cancel()
	<-p.done
}

func (p *Poller) loop(ctx context.Context, pl puller, interval time.Duration, lg *log.Logger, probe func(int, time.Duration)) {
	defer close(p.done)
	var rs roundState
	for round := 1; ; round++ {
		if ctx.Err() != nil {
			return
		}
		// 立刻拉第一轮（不等一个间隔）：daemon 重启后应当马上收敛，
		// 而不是先空转 30 秒——而那 30 秒里用户看到的还是旧配置。
		out, err := pl.Pull(ctx, false)
		if err != nil {
			// 连记账都写不下去（磁盘满、权限）：当成一次失败走退避，别退出循环。
			out.Failed = true
			out.ErrText = i18n.T("cannot write the bookkeeping: {err}", i18n.A{"err": err.Error()})
		}
		rs.report(lg, out, interval)

		delay := nextDelay(interval, rs.fails, rand.Float64())
		if probe != nil {
			probe(round, delay)
		}
		if !wait(ctx, delay) {
			return
		}
	}
}

// roundState 是循环跨越两轮之间要记住的东西（失败次数与上次的错误串）。
type roundState struct {
	fails    int
	lastErr  string
	lastWarn string
}

// report 按日志政策渲染一轮的结果，并维护失败计数。
//
// # 日志政策（不静默，但不刷屏）
//
// 照抄 watcher 的"只在状态跃迁时报"精神：
//   - 成功（此前有失败）→ 报一次"同步恢复"；
//   - 失败 → 第 1/2/5/10/25/50 次报，之后每 100 次；**错误串一变立刻报**
//     （换了一种错就是新信息，不该被抑制吞掉）；
//   - 拉到新配置 → 报一行"已更新配置"，一秒后 watcher 自己还会再报一行
//     "[config] 配置热更新（第 N 代）"。**两行都真**，来自互不知情的两层
//     （我们写了盘、watcher 发现了变化），这是正确结果，不是冗余。
//   - 空转 → **完全静默**（gen 没变时连解密都不做，自然也没有日志）。
func (r *roundState) report(lg *log.Logger, out Outcome, interval time.Duration) {
	if out.Failed {
		r.fails++
		changed := out.ErrText != r.lastErr
		r.lastErr = out.ErrText
		if shouldLog(r.fails, changed) {
			lg.Printf("[configshare] ⚠ %s", i18n.T("sync failed (attempt {n}, retrying in {delay}): {err}",
				i18n.A{"n": r.fails, "delay": humanDur(nextDelay(interval, r.fails, 0.5)), "err": out.ErrText}))
		}
		return
	}
	if r.fails > 0 {
		lg.Printf("[configshare] %s", i18n.T("sync recovered (after {n} consecutive failures)", i18n.A{"n": r.fails}))
	}
	r.fails = 0
	r.lastErr = ""
	for _, note := range out.Notes {
		lg.Printf("[configshare] %s", note)
	}

	// 只有真的落盘了东西才报"已更新"：代数涨了但字节没变（宿主重新发布了一份
	// 一模一样的内容）不是更新，说成更新就是撒谎。
	if out.Config.Changed {
		changed := len(out.Config.Written) + len(out.Config.Removed)
		lg.Printf("[configshare] %s", i18n.N(
			"configuration updated (gen {from} → {to}, {n} file){files}",
			"configuration updated (gen {from} → {to}, {n} files){files}",
			changed, i18n.A{"from": out.PrevGen, "to": out.Config.Gen, "files": fileSummary(out.Config)}))
		if out.Config.Archive != "" {
			lg.Printf("[configshare] %s", i18n.T("local changes archived to {dir} (restore from there)",
				i18n.A{"dir": out.Config.Archive}))
		}
	}
	if out.Secrets.Changed {
		lg.Printf("[configshare] %s", i18n.T("secrets updated (gen {gen})", i18n.A{"gen": out.Secrets.Gen}))
	}

	// 警告只在**内容变了**的时候报一次。它们是每轮重新算出来的，无条件打印会
	// 变成每 30 秒一句同样的话——"不静默"不等于"刷屏"。
	warns := strings.Join(out.Config.Warnings, " | ")
	if warns != "" && warns != r.lastWarn {
		for _, w := range out.Config.Warnings {
			lg.Printf("[configshare] %s", i18n.T("note: {msg}", i18n.A{"msg": w}))
		}
	}
	r.lastWarn = warns
}

// nextDelay 算下一轮的等待时长。
//
// interval 是正常节奏；有失败就换成退避序列（30 → 60 → 120 → 300 封顶）。
// 两种都加 ±20% 抖动。
func nextDelay(interval time.Duration, fails int, rnd float64) time.Duration {
	d := interval
	if fails > 0 {
		d = backoffBase
		for i := 1; i < fails && d < backoffMax; i++ {
			d *= 2
		}
		if d > backoffMax {
			d = backoffMax
		}
	}
	return jitter(d, rnd)
}

// jitter 给一个时长加 ±20% 抖动。
//
// 为什么必须有：多台机器的 ticker 会在启动时**相位锁定**（同一批机器、同样的
// 30 秒），于是每 30 秒一起砸宿主一次，中间 30 秒全空。抖动把它们错开，也让
// 宿主侧的负载从"尖峰"变成"平的"。这对一个"宿主挂了大家照常转发"的设计来说
// 是锦上添花，但对宿主的日志与连接数是实打实的改善。
func jitter(d time.Duration, rnd float64) time.Duration {
	if d <= 0 {
		return d
	}
	// rnd ∈ [0,1) → 系数 ∈ [0.8, 1.2)
	out := time.Duration(float64(d) * (1 + jitterFrac*(2*rnd-1)))
	if out <= 0 {
		return d
	}
	return out
}

// shouldLog 是日志抑制策略（纯函数，便于测）。
//
// 第 1 次报，然后 2/5/10/25/50，之后每 100 次；错误串一变立刻报。
// 第 1 次就报是硬要求：一个新问题**必须**在第一个周期就被说出来，否则
// "不静默"就成了空话（用户会以为一切正常）。
func shouldLog(fails int, errChanged bool) bool {
	if fails <= 1 || errChanged {
		return true
	}
	switch fails {
	case 2, 5, 10, 25, 50:
		return true
	}
	return fails > 50 && fails%100 == 0
}

// wait 等一个时长；ctx 结束返回 false（该收摊了）。
//
// 用 Timer 而不是 Ticker：每轮的间隔是变的（退避），Ticker 得反复重置，
// 而 Timer 天然就是"这一轮等这么久"。
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// fileSummary 把"这一轮动了哪几个文件"拼成短摘要。新增的照写，下线的前面加个
// `-`——"删了什么"和"写了什么"是两件事，不该看起来一样。
func fileSummary(ch Channel) string {
	names := append([]string(nil), ch.Written...)
	for _, r := range ch.Removed {
		names = append(names, "-"+r)
	}
	if len(names) == 0 {
		return ""
	}
	const maxShow = 6
	// 截断时给的是**总数**而不是剩下的个数（"等 8 个"）：用户要的是"这一轮动了多少"，
	// 不是"还有几个没列出来"。TestFileSummaryTruncates 钉着这一条。
	if len(names) > maxShow {
		return i18n.T(": {names} and {n} in total", i18n.A{"names": strings.Join(names[:maxShow], ", "), "n": len(names)})
	}
	return i18n.T(": {names}", i18n.A{"names": strings.Join(names, ", ")})
}

// humanDur 把时长写成给人看的样子（"30s" / "5m0s"），并且**截到整秒**——日志里
// 不需要亚秒精度，而抖动加了 ±20% 之后会出现 27.4s 这种数，读起来像是在暗示
// 一个并不存在的精确。
func humanDur(d time.Duration) time.Duration { return d.Truncate(time.Second) }
