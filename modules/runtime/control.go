package runtime

// 本文件是**生命周期与接管的公开端口**。
//
// # 为什么需要它（2026-11-17）
//
// 发行版的 `newgate config` 要在一个向导里做完「装上 → 起代理 → 接管」。前两件事
// 各有各的端口（confighook 的 AgentInstaller、daemon.Spawn），而**接管**此前只有
// 命令：`start` / `on <agent>` 的实现直接调 `takeover` 那个内部包，模块拿不到。
//
// 发行版于是只有两条路：抄一遍接管逻辑（= 它在 import 内核的内部实现，内核重构时
// 断在另一个仓库里），或者 shell 出去跑 `newgate start`（= 一个向导里起一个子进程
// 再解析它的输出，退出码与措辞一改就静默失效）。两条都不该是答案。
//
// 所以这里把「接管」升成 Runtime 端口的一部分——**它是这个模块的定义**（package
// runtime 的第一句话就是「把一个 Agent 描述符变成真实客户端进程」），而命令只是它
// 的一个门面。门面底下的东西本来就该能被别的模块问。
//
// 返回值与命令用的是同一份类型（takeover.Result/Status），所以「一次接管改了什么」
// 只有一种形状，向导打印的与 `newgate on` 打印的是同一批事实。

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/rzbdz/newgate-ext/lib/httpx"
	"github.com/rzbdz/newgate-ext/lib/style"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
	"github.com/rzbdz/newgate-ext/modules/runtime/daemon"
	"github.com/rzbdz/newgate-ext/modules/runtime/takeover"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Status 是一个 agent 的接管状态（与 `newgate status` 那一行同一份事实）。
type Status = takeover.Status

// Result 是一次接管/释放的结局。
type Result = takeover.Result

// TakeOver 接管一个 agent 并记下「用户要接管它」——`newgate on <agent>` 的那件事。
func (*service) TakeOver(agent string, port int) Result { return takeover.On(agent, port) }

// Release 释放一个 agent 并记下「用户不要它被接管」——`newgate off <agent>`。
func (*service) Release(agent string) Result { return takeover.Off(agent) }

// TakeOverAll 接管所有没被用户显式关掉的 agent——`start` 收尾那一步。
func (*service) TakeOverAll(port int) []Result { return takeover.OnAll(port) }

// TakeoverStatus 列出每个已知 agent 的接管状态。
func (*service) TakeoverStatus() []Status { return takeover.List() }

// StartOptions 是 Start 的开关。
type StartOptions struct {
	// Force 跳过「配置不可用就别接管」那道闸（`newgate start --force`）。
	//
	// 向导用不到它：向导刚刚亲手写了一组可用的 provider。留着是因为端口要与
	// 命令一一对应——少一个开关，下一个人就得为它开一条旁路。
	Force bool
	// SkipTakeover 只把代理起起来，**不**全面接管。
	//
	// 向导用它：用户在多选那一屏已经点名了要哪几家，而我们绝不能把没选的那些
	// 也接过来——那正是那一屏写着「skip 它们就不变」的承诺。`newgate start`
	// 不传它，所以命令行的语义（全面接管）一个字节都没变。
	SkipTakeover bool
	// Quiet 不打印任何东西。
	//
	// 向导用它：它自己正在画一屏，内核那几句「代理已启动」插进去会与版式打架。
	// 事实仍然通过返回值交出去（StartReport），所以 quiet 不是「少报」。
	Quiet bool
}

// StartReport 是 Start 的结局。
type StartReport struct {
	// Running 报告结束时代理在不在跑。
	Running bool
	// AlreadyRunning = 这次调用没有起新进程（代理本来就在跑）。
	AlreadyRunning bool
	// PID / Port 是跑着的那个进程。
	PID  int
	Port int
	// Profile 是它此刻的全局默认链头。
	Profile string
	// Takeover 是本次全面接管的结果（AlreadyRunning 时为空：那时接管是上一次的事）。
	Takeover []Result
	// Problems 是「配置不可用」的逐条原因（非空 = 拒绝接管，且 Force 为假）。
	Problems []string
}

// Start 起代理并全面接管——`newgate start` 的语义，只是把结局交回给调用方。
//
// 它与命令走**同一份代码**（cmdStart 调它），所以向导起出来的代理、`newgate start`
// 起出来的代理，行为一模一样：同一个令牌先落盘、同一道「没 key 就别接管」的闸、
// 同一次 OnAll、同一份 TakenOver 写回。
func (s *service) Start(opts StartOptions) (StartReport, error) {
	var rep StartReport
	say := func(format string, a ...any) {
		if !opts.Quiet {
			fmt.Printf(format, a...)
		}
	}
	sayErr := func(format string, a ...any) {
		if !opts.Quiet {
			fmt.Fprintf(os.Stderr, format, a...)
		}
	}

	if _, err := os.Stat(paths.ProvidersFile()); os.IsNotExist(err) {
		say("%s\n", style.Dim(i18n.T("First run, initializing the config", nil)))
		if _, err := store.Init(false); err != nil {
			return rep, err
		}
	}
	if i := daemon.Running(); i != nil {
		rep = StartReport{Running: true, AlreadyRunning: true, PID: i.PID, Port: i.Port,
			Profile: store.LoadState().DefaultProfile}
		say("%s\n", style.Item(style.OK, i18n.T("Proxy already running   pid {pid} · 127.0.0.1:{port}",
			i18n.A{"pid": i.PID, "port": i.Port})))
		return rep, nil
	}

	// 令牌先于 Spawn 落盘：daemon 一起来就要能验 /__newgate/stop。
	// 也是为了防竞态——下面 Spawn 之后 st 还会被 SaveState 写回，
	// 先确保 st 里带着令牌，写回就不会把 daemon 已生成的令牌冲掉。
	st, err := store.EnsureControlToken()
	if err != nil {
		// 不拦：用户要的是把代理起起来，为一行令牌拒绝启动更糟。但后果明说。
		sayErr("%s %s\n", style.Mark(style.Warn), i18n.T(
			"Cannot write the control token (a cross-user newgate stop will not work): {err}",
			i18n.A{"err": err.Error()}))
	}
	// 没 key 就别接管——接管了每个请求都是错误，而用户的配置已经被改了
	if probs := activeProblems(st); len(probs) > 0 && !opts.Force {
		rep.Problems = probs
		sayErr("newgate: %s\n", i18n.T("Config is unusable, refusing to take over", nil))
		for _, p := range probs {
			sayErr("  %s %s\n", style.Mark(style.Bad), p)
		}
		sayErr("%s\n", style.Hint(i18n.T("Put a key in {file}, or set the matching environment variable",
			i18n.A{"file": paths.ProvidersFile()})))
		sayErr("%s\n", style.Hint(i18n.T("Check: newgate doctor    Force takeover: newgate start --force", nil)))
		return rep, ErrConfigUnusable
	}

	info, err := daemon.Spawn(st.Port)
	if err != nil {
		return rep, i18n.Ef(err, "Cannot start the daemon: {err}", nil)
	}
	ok := false
	for k := 0; k < 60; k++ {
		if controlplane.Ping(st.Port) {
			ok = true
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if !ok {
		// 区分「进程没起来」和「起来了但我们连不上」——后者绝不能杀进程
		if httpx.TCPAlive("127.0.0.1", st.Port, time.Second) {
			sayErr("newgate: %s\n", i18n.T(
				"Port {port} is listening but the HTTP probe failed; the proxy process is kept",
				i18n.A{"port": st.Port}))
			sayErr("  %s\n", i18n.T(
				"Most likely cause: an outbound proxy is hijacking loopback requests. Run newgate doctor", nil))
		} else {
			sayErr("newgate: %s\n", i18n.T(
				"The proxy did not come up (nothing is listening on port {port}); see the log {log}",
				i18n.A{"port": st.Port, "log": paths.LogFile()}))
			_, _ = daemon.Stop()
			return rep, ErrProxyDidNotStart
		}
	}
	rep = StartReport{Running: true, PID: info.PID, Port: st.Port, Profile: st.DefaultProfile}
	say("%s\n", style.Item(style.OK, i18n.T("Proxy started   pid {pid} · 127.0.0.1:{port} · profile {profile}",
		i18n.A{"pid": info.PID, "port": st.Port, "profile": st.DefaultProfile})))

	// 全面接管：所有没被用户显式 off 掉的 agent，各按自己的机制插上。
	if !opts.SkipTakeover {
		say("%s\n", "")
		rep.Takeover = s.TakeOverAll(st.Port)
		if !opts.Quiet {
			printResults(rep.Takeover)
		}
	}

	st.TakenOver = true
	// **不能吞这个错**：上面刚打印了「代理已启动」、下面还要打印「配置改动 1 秒内
	// 自动热更新」。写盘失败（CLAUDE.md §3.1 记的那个 umask 权限坑就是现场）时
	// TakenOver 没落盘，下次 start 的行为与用户以为的正相反，而屏幕上全是绿的。
	if err := store.SaveState(st); err != nil {
		return rep, i18n.Ef(err, "Cannot write the takeover state back to state.json: {err}", nil)
	}
	say("%s\n", "")
	say("%s\n", style.Hint(i18n.T("Config changes hot-reload within 1 second; no restart needed", nil)))
	say("%s\n", style.Hint(i18n.T("Original config backup: {dir}/original/", i18n.A{"dir": paths.BackupDir()})))
	return rep, nil
}

// 两个可由调用方分辨的失败原因（命令据此给 65 / 70 的退出码，向导据此决定下一句）。
var (
	// ErrConfigUnusable = 配置不可用，拒绝接管（`start` 退出码 65）。
	ErrConfigUnusable = errors.New("config is unusable")
	// ErrProxyDidNotStart = 代理没起来（`start` 退出码 70）。
	ErrProxyDidNotStart = errors.New("the proxy did not come up")
)

// 编译期确认端口形状。
var (
	_ Runtime = (*service)(nil)
	_ Control = (*service)(nil)
)
