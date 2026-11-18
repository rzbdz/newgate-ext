package gateway

// 本文件是**守护进程的主循环**：`newgate __serve`（由 daemon.Spawn 拉起）。
//
// **为什么它住在这里**（2026-09-18）：它在跑的就是数据面本身——配置热更新的原子
// 换页、thinkcache 的磁盘冷层、转发服务（forward.New）的启停、优雅交接的排空。
// 这些没有一样是「命令行」的事。它留在 modules/cli 的时候，界面为了跑起来得认识
// forward / thinkcache / 健康表 / config,store / runtime,daemon ——界面的依赖表
// 里于是有一半是数据面。
//
// 搬过来之后：界面不认识进程生命周期，进程也不认识界面。`__serve` 是网关自己
// 在 Start 里注册进界面的命令（本模块对 ui 是弱依赖 Optional(cli)），用户看不见
// 它（故意没有 HelpLine）。
//
// 依赖方向：gateway → cli/extension（叶子契约），不是 → modules/cli。

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rzbdz/newgate-ext/lib/buildinfo"
	"github.com/rzbdz/newgate-ext/lib/logx"
	servingapi "github.com/rzbdz/newgate-ext/lib/serving"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/forward"
	"github.com/rzbdz/newgate-ext/modules/gateway/policy"
	"github.com/rzbdz/newgate-ext/modules/gateway/thinkcache"
	porthubapi "github.com/rzbdz/newgate-ext/modules/porthub"
	"github.com/rzbdz/newgate-ext/modules/runtime/daemon"
	entryapi "github.com/rzbdz/newgate/component/entry"
	"github.com/rzbdz/newgate/lib/i18n"
)

// serveEntry 是守护进程本体：`newgate __serve`。
//
// **它是一个入口申报（component/entry），不是一条界面命令**（2026-09-20 改）。
//
// 为什么：`__serve` 回答的是「这次进程调用归谁」，那正是入口账本的问题；而挂成
// cli 命令时，**关掉界面就等于关掉了守护进程本体**——`disable: ["cli"]` 的装配
// 编得出来，却起不了 daemon（实测：`newgate: no entry claimed this call (asked:
// wrapper did not claim)`）。纯 dashboard 的发行版（没有终端界面，只有一个网页
// 控制台）因此根本不成立，而它是**合法**的产品形状：那时候守护进程仍然要有人
// 起，网页才有东西可连。wrapper 报「argv0 是被接管的 client」走的也是这条道。
//
// 顺带少一样声明：它当命令时得报 Unstyled（输出不是版式），当入口就不必了——
// 入口的输出去哪儿是入口自己的事。
//
// 它只带 filters（数据面策略账本）进去，不带任何具体策略：账本在 Bind 期就存在
// （gateway 的 New 里建），贡献者在各自 Start 期往里写，数据面在 Serve 期读。
type serveEntry struct {
	filters *policy.Registry
	// hub 是共享端口那张表（porthub 没装就是 nil）。
	//
	// 它只在这一处出现：**守护进程入口**是这个进程里唯一知道「端口上还有别人」
	// 的地方。数据面（forward）拿到的是一个 handler，不知道它从哪来；porthub
	// 拿到的是一个 handler，不知道它是谁——两边都不认识对方。
	hub porthubapi.Service
	// listeners 是「这个进程开始服务了」那本账（serving 没装就是 nil）。
	//
	// 它与 hub 出现在同一处、理由也一样：**守护进程入口是这个进程里唯一说得出口
	// 「我在服务」的地方**。数据面拿到的是一个已经装好的进程，界面拿到的是一个
	// 「可以起来了」的通知——两边都不认识对方，也不认识本模块的另一半。
	listeners servingapi.Service

	// headless 记的是「这个装配里没有终端界面」，由 Start 按装了什么算出来
	// （见 module.go），决定**无参数时这个进程是什么**。
	//
	// 有界面时：`newgate` 无参数是给界面回答的（帮助 / 状态），守护进程只在
	// 显式 `__serve` 时认领。没有界面时（纯 dashboard 的发行版就是这种形状）：
	// 这个进程**就是**守护进程——那时没有第二个答案可给，而 `newgate start`
	// 也随界面一起没了（它是 runtime 注册进界面的命令），不给兜底的话这种构建
	// 编得出来却永远起不来（`no entry claimed this call`）。
	headless bool
}

var _ entryapi.Handler = (*serveEntry)(nil)

// Name 进日志：组合根那句 `[entry] resolve: … → <Name>`。
func (serveEntry) Name() string { return "__serve" }

// Claims 认两件事：显式的 `newgate __serve …`，以及**没有界面时的「只有本入口
// 自己的 flag」**（`newgate --port 8907`）。
//
// 第一条逐字相等，不做拼写容错：`__serve` 是内部入口（daemon.Spawn 拼出来的），
// 不是给人敲的，猜错一个近似拼写只会让真正的错误更难看出来。
//
// 第二条只在不装界面时成立（见 headless）：那时没有子命令可派，flag 只可能是
// 守护进程自己的，所以「起服务」是唯一说得通的行为。**带了别的东西就放过去**
// ——`newgate frobnicate` 该得到「没人认领这次调用」这句人话，而不是悄悄起一个
// 守护进程（那种错要等端口被占或者根本连不上才会被发现）。
//
// 只读 Process、没有副作用——入口账本会问好几个申报者，问的顺序不该改变结果。
func (e serveEntry) Claims(p entryapi.Process) bool {
	if len(p.Args) > 0 && p.Args[0] == "__serve" {
		return true
	}
	return e.headless && onlyOwnFlags(p.Args)
}

// onlyOwnFlags 报告这串参数**只**由本入口自己的 flag 组成。
//
// 认得的就是 `--port`：它是守护进程的参数，而「哪些参数属于我」这件事只有
// 守护进程自己知道。将来它多一个 flag，这里跟着加一行——这是诚实的代价，
// 比「凡是横杠开头的都算我的」安全得多（那会把 `--help` 也吞进来，
// 于是在一个没有界面的构建里敲 `newgate --help` 会起一个守护进程）。
func onlyOwnFlags(args []string) bool {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--port":
			i++ // 吃掉它的值；值缺了 intFlag 会退回默认端口
		case strings.HasPrefix(a, "--port="):
		default:
			return false
		}
	}
	return true
}

func (c serveEntry) Handle(p entryapi.Process) int {
	return Serve(c.filters, c.hub, c.listeners, intFlag(p.Args, "--port", 0))
}

// intFlag 取 `--名字 N` 或 `--名字=N` 里的整数，没有给默认值。
func intFlag(args []string, name string, def int) int {
	for i, a := range args {
		v := ""
		switch {
		case a == name && i+1 < len(args):
			v = args[i+1]
		case strings.HasPrefix(a, name+"="):
			v = strings.TrimPrefix(a, name+"=")
		default:
			continue
		}
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

// Serve 是守护进程的主循环。
//
// 热更新在这里落地（服务稳定性的地基）：watcher 后台监测配置目录，
// 变了就原子换页。已经在跑的 agent **完全不受影响**——改 claude 的
// profile 不会打断正在跑的 opencode，因为：
//   - 每个 agent 读自己的绑定（per-agent profile）
//   - 换页是原子指针替换，不存在「读到一半配置变了」的中间态
//   - 新配置加载失败时**保留旧快照**，正在跑的一切继续工作
//
// 依赖方向：数据面只认识**策略账本**（policy.Registry），不认识任何一位策略。
// 账本由 gateway 的 New 建好（Bind 期），策略在各自 Start 期注册进来。
func Serve(filters *policy.Registry, hub porthubapi.Service, listeners servingapi.Service, port int) int {
	rot, rerr := logx.New(paths.LogFile(), 16<<20, 3) // 16MB × 4 份
	var lg *log.Logger
	if rerr != nil {
		lg = log.New(os.Stdout, "", log.LstdFlags)
		lg.Printf("%s", i18n.T("Log rotation init failed, falling back to stdout: {err}", i18n.A{"err": rerr}))
	} else {
		defer rot.Close()
		lg = log.New(rot, "", log.LstdFlags)
	}

	// 优雅交接进来的进程：pid/lock 已由父进程改写到自己名下（AdoptRuntime），
	// 此时抢锁会读到自己的 pid 而误判「已在运行」。父进程就是担保人，跳过。
	inherited := os.Getenv("NEWGATE_LISTENER_FD") != ""
	if !inherited {
		if err := daemon.AcquireLock(); err != nil {
			lg.Printf("%s", i18n.T("Failed to acquire the lock: {err}", i18n.A{"err": err}))
			return 69
		}
		defer daemon.RemoveLock()
		// 抢到锁之后**自己**写 pidfile：只有持有监听权的进程才该出现在那里。
		// 父进程代写会被这一步之后的「陈旧锁清理」删掉（AcquireLock 里那句
		// RemovePid），而再没有人补回来——2026-09-18 两次现场的完整链条见
		// daemon.WriteOwn。写不出去只警告：代理照常服务，只是 status/metrics
		// 会看不到它（那时候 doctor 的「守护进程」一项会说出来）。
		if err := daemon.WriteOwn(port); err != nil {
			lg.Printf("%s", i18n.T("Cannot write the pidfile (status/metrics will not see this process): {err}", i18n.A{"err": err}))
		}
	} else {
		lg.Printf("%s", i18n.T("Graceful handover: taking over the listening socket from the parent process", nil))
	}

	// 控制令牌必须先于 watcher 落盘：别的用户 `newgate stop` 发不出信号，
	// 只能走 /__newgate/stop，daemon 一启动就得能验它。这里是最后兜底
	// （cmdStart / launch.Launch 在 Spawn 前已经各兜一次，正常早就有）。
	if st, err := store.EnsureControlToken(); err != nil {
		// daemon 照常起：没有令牌只是跨用户停机不可用，不该拦下整个代理。
		lg.Printf("%s", i18n.T("Cannot write the control token (cross-user stop unavailable): {err}", i18n.A{"err": err}))
	} else if st.ControlToken != "" {
		lg.Printf("%s", i18n.T("Control endpoint /__newgate/stop is ready (cross-user stop available)", nil))
	}

	watcher, err := store.NewWatcher(time.Second)
	if err != nil {
		lg.Printf("%s", i18n.T("Failed to load the configuration: {err}", i18n.A{"err": err}))
		return 65
	}
	watcher.OnChange(func(old, nw *store.Snapshot) {
		changes := store.DiffSummary(old, nw)
		if len(changes) == 0 {
			lg.Printf("[config] %s", i18n.T("Configuration reloaded (generation {gen}), no structural change",
				i18n.A{"gen": watcher.Generation()}))
			return
		}
		lg.Printf("[config] %s", i18n.T("Configuration hot-reloaded (generation {gen}): {changes} — running sessions are unaffected",
			i18n.A{"gen": watcher.Generation(), "changes": changes}))
	})
	watcher.OnError(func(e error) {
		// 关键：坏配置不替换快照。宁可用旧配置继续跑，也不能因为一次
		// 手误让所有 agent 同时失效。
		lg.Printf("[config] %s", i18n.T("New configuration failed to load, still using the last usable configuration: {err}",
			i18n.A{"err": e}))
	})
	watcher.Start()
	defer watcher.Close()

	if port <= 0 {
		port = watcher.Current().State.Port
	}
	// thinkcache 落盘冷层：daemon 重启后找回上一进程的推理内容。失败就降级
	// 纯内存（重启前那几轮的原文找不回来了），落盘永远不反过来搞挂代理。
	if err := thinkcache.AttachDisk(paths.ThinkCacheFile(), 100<<20); err != nil {
		lg.Printf("%s", i18n.T("thinkcache disk layer disabled (memory-only): {err}", i18n.A{"err": err}))
	}
	// 冷层跑起来之后的失败（压实丢记录、刷盘失败、冷层停用）必须说出来：
	// 用户能观察到的现象是「重启后推理找不回来了」，而那正是最难反推原因的一类。
	thinkcache.SetErrorHandler(func(err error) {
		lg.Printf("[thinkcache] %v", err)
	})
	srv := forward.New(watcher.Current().State.BindHost(), port, lg, watcher, filters)

	// 这个端口的根 handler：装了 porthub 就交给它（挂载表优先，没人认领的落回
	// 数据面），没装就是数据面自己——也就是这个机制出现之前的样子。
	//
	// 为什么合成放在这里而不是数据面里：**这是组合，不是数据面的逻辑**。数据面
	// 是「转发一次模型请求」这条链，它不该知道这个进程的端口上还住着谁；而守护
	// 进程入口本来就是「把这个进程装起来」的地方（日志、锁、信号、排空都在这儿）。
	// 依赖本身是声明的（module.go 里的 Optional），所以在依赖图上看得见。
	if hub != nil {
		root, err := hub.Root("gateway", srv.Handler())
		if err != nil {
			lg.Printf("%s", i18n.T("Cannot share the port with other services, the data plane will serve it alone: {err}",
				i18n.A{"err": err}))
		} else {
			srv.SetRoot(root)
			lg.Printf("%s", i18n.T("This port is shared: services mounted on it take their paths first, everything else is the data plane", nil))
		}
	}

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	// requested 记录「这次退出是有人要求的」，只影响退出码。
	//
	// 停机路径**只有一个出口**：下面的 goroutine 只负责 `srv.Shutdown()`
	// 让 Serve() 返回，pid/lock 的清理和退出码一律由 srv.Start() 之后那段
	// 统一做。曾经三个出口各自 `RemoveLock + RemovePid + os.Exit(0)`，和主
	// 路径的 `return 70` 抢跑：实测（2026-09-17，mock/e2e_claude.sh 第 11 章）
	// 主路径先跑完，`RemoveLock` 有 defer 兜住、`RemovePid` 没有，于是
	// .newgate.pid 留在磁盘上——下次 start/restart 就会读到一个死 pid。
	var requested int32

	go func() {
		for s := range sig {
			if s == syscall.SIGHUP {
				// SIGHUP 是「重读配置」的传统语义
				lg.Printf("%s", i18n.T("Received SIGHUP, forcing a configuration reload", nil))
				watcher.Reload(true)
				continue
			}
			if srv.Draining() {
				// 排空期被信号打断：pid/lock 已是新进程的，清不得
				lg.Printf("%s", i18n.T("Received {sig} while draining, exiting now (pid/lock untouched)",
					i18n.A{"sig": s}))
				os.Exit(0)
			}
			lg.Printf("%s", i18n.T("Received {sig}, exiting", i18n.A{"sig": s}))
			atomic.StoreInt32(&requested, 1)
			srv.Shutdown()
			return
		}
	}()

	// 控制端点停机（别的用户 `newgate stop`）：发不出信号的用户靠它停机。
	// 同样只关 listener，清理交给下面统一做。
	go func() {
		<-srv.StopRequested()
		if srv.Draining() {
			// 排空期收到停机：同样不许动新进程的 pid/lock
			lg.Printf("%s", i18n.T("Received a control stop while draining, exiting now (pid/lock untouched)", nil))
			os.Exit(0)
		}
		lg.Printf("%s", i18n.T("Control stop (token verified), exiting", nil))
		atomic.StoreInt32(&requested, 1)
		srv.Shutdown()
	}()

	lg.Printf("%s", i18n.T("newgate {version} (built {built}) started, default profile={profile}, config hot-reload enabled",
		i18n.A{"version": buildinfo.Version(), "built": buildinfo.BuildTimeDisplay(),
			"profile": watcher.Current().State.DefaultProfile}))
	// 「这个进程开始服务了」：想搭车的模块（装了 porthub 就挂共享端口、没装就自己
	// 监听一个端口的 web 界面）在这一刻起来。
	//
	// **为什么不能放在模块的 Start 里**：Start 每一条 `newgate …` 命令都会跑一遍，
	// 在那里起监听等于敲一次 `newgate status` 就开一台服务器。而这里是这件事唯一
	// 说得出口的地方——这个进程真的要把端口交给内核了。
	//
	// 位置在 Start 之前（而不是 accept 之后）：它说的是「我要开始了」。差一拍的
	// 代价是「绑定失败时已经起来的监听」——那由下面这个 stop 收掉，而且那种情况下
	// 进程立刻就走（见下面的失败分支）。
	if listeners != nil {
		stop, err := listeners.Notify()
		if err != nil {
			// fail-open：一个界面起不来不该拖垮数据面（日志里点名是谁）。
			lg.Printf("%s", i18n.T("Some services did not start: {err}", i18n.A{"err": err}))
		}
		defer stop()
	}

	if err := srv.Start(); err != nil {
		// 优雅交接的排空：listener 已移交新进程，Serve 因此返回——但这
		// 不是退出的时候。等在途请求流完（Drained），再直接退（os.Exit
		// 跳过 defer 的 RemoveLock：pid/lock 已是 新进程的）。
		if srv.Draining() {
			lg.Printf("%s", i18n.T("Listening socket handed over to the new process, waiting for in-flight requests to drain (up to 10 minutes)", nil))
			<-srv.Drained()
			lg.Printf("%s", i18n.T("Drain complete, the old process is stepping down", nil))
			os.Exit(0)
		}
		lg.Printf("%s", i18n.T("Proxy exiting: {err}", i18n.A{"err": err}))
		// 唯一出口：进程要走了，pid/lock 是它自己的，必须一起带走。
		// defer 的 RemoveLock 只管 lock，pid 得在这里补上。
		daemon.RemoveLock()
		daemon.RemovePid()
		if atomic.LoadInt32(&requested) == 1 {
			return 0
		}
		return 70
	}
	return 0
}
