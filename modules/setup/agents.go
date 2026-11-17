package setup

// 本文件是向导的第一幕：**把选中的客户端装上，并让它们走 newgate**。
//
// 它只做两件事，两件都是「这一台机器此刻的样子」，不问任何问题：
//
//	install  客户端没装 → 跑它自己的安装命令（见 confighook.AgentInstaller）
//	takeover 让这台客户端的流量走我们（见 kernel 的 runtime/takeover）
//
// # 为什么它不去问「装哪个包」
//
// 装法归客户端模块（`npm i -g @openai/codex`、hermes 的 curl|bash…），那是
// confighook.AgentInstaller 这条端口存在的全部理由（见内核 install.go 的注释）。
// 这里只负责**按人选的顺序跑一遍**，并把每一步的结局如实报出来。
//
// # 为什么「装不上」不是向导的失败
//
// 用户可能没有 npm、没有 curl、公司网络不通、没有 sudo。那是他这台机器的现实，
// 不是配置错了。所以：一条失败就记下来、继续下一条，最后在收尾里逐条念出来
// ——「没装成却看着像装成了」是这个环节唯一不可接受的结局。

import (
	"fmt"
	"io"
	"os"

	"github.com/rzbdz/newgate/lib/i18n"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
	runtimeapi "github.com/rzbdz/newgate/modules/runtime"
)

// Step 是一条「装 + 接管」的结局。
type Step struct {
	Agent string
	// Installed 报告跑完之后它在不在这台机器上。
	Installed bool
	// DidInstall = 这一次真的跑了安装命令。
	DidInstall bool
	// Takeover 是接管那一幕的结局（空 = 没走到：还不在机器上）。
	Takeover runtimeapi.Result
	// TakenOver 报告「此刻磁盘上真的被我们改过了吗」。
	//
	// 与 len(Takeover.Lines)>0 不是一回事：配置型客户端（codex / hermes）的
	// 配置文件要它自己先跑一次才会出现，那一次接管会**跳过**并记一行说明。
	// 把那一行当成「接管成功」正是这个仓库最不能接受的那种话
	// （「没装成却看着像装成了」）——用户在 codex 里发请求，走的是直连。
	TakenOver bool
	// Err 是安装那一步的错误（接管自己的错误在 Takeover.Err 里）。
	Err error
}

// EnsureAgents 对选中的每一个 agent 依次：没装就装、装上了就接管。
//
// out 收安装命令自己的输出（best-effort 的「尽力」要看得见，见内核 install.go）。
func EnsureAgents(agents agentapi.AgentCatalog, rt runtimeapi.Control, ids []string, port int, out io.Writer) []Step {
	status := map[string]bool{}
	for _, st := range rt.TakeoverStatus() {
		status[st.Agent] = st.Active
	}
	var steps []Step
	for _, id := range ids {
		st := Step{Agent: id}
		if _, ok := agents.Get(id); !ok {
			st.Err = i18n.E("unknown agent {agent}", i18n.A{"agent": id})
			steps = append(steps, st)
			continue
		}
		if !agents.Installed(id) {
			inst := agents.Installer(id)
			if inst == nil {
				st.Err = i18n.E("{agent} is not installed, and this build knows no way to install it; install it yourself, then run this again",
					i18n.A{"agent": id})
				steps = append(steps, st)
				continue
			}
			fmt.Fprintf(out, "newgate: %s\n", i18n.T("running: {cmd}",
				i18n.A{"cmd": joinArgv(inst.Command())}))
			st.DidInstall = true
			if err := inst.Install(out); err != nil {
				st.Err = err
				steps = append(steps, st)
				continue
			}
			// 装是装完了，但这个进程的 PATH 里可能还是找不到它（包管理器把 bin
			// 放到了别处）。不假装成功——Installed 再说一次真话。
			st.Installed = agents.Installed(id)
			if !st.Installed {
				st.Err = i18n.E("{agent} was installed but is still not on PATH — open a new shell and run newgate config again",
					i18n.A{"agent": id})
				steps = append(steps, st)
				continue
			}
		} else {
			st.Installed = true
		}
		// 装上之后立刻接管：接管写的是**这台机器上的配置文件**，没装的话那些
		// 文件还不存在，接管只会报「没这个文件」。
		st.Takeover = rt.TakeOver(id, port)
		st.TakenOver = st.Takeover.Err == nil && takenOver(agents, id, status)
		if !st.TakenOver && st.Takeover.Err == nil {
			// 配置型客户端（codex / hermes）的文件要它自己先跑一次才会出现。
			// 那是一个**要告诉用户的下一步**，不是失败——但绝不能写成「已接管」。
			if msg := firstStartHint(agents, id); msg != "" {
				st.Takeover.Warn = append(st.Takeover.Warn, msg)
			}
		}
		steps = append(steps, st)
	}
	return steps
}

// takenOver 问磁盘：这台客户端此刻**真的**被我们指着本机代理吗。
//
// 配置型走它自己的 IsTakenOver（那是它的判据，每家有细微差别：codex 看那段表头、
// hermes 看 providers.newgate 那一行）；shim 型看内核的接管状态（链接在不在）。
// 两者都由拥有那件事的模块回答，这里不猜——猜出来的「已接管」是这个环节最坏的
// 一种错。
func takenOver(agents agentapi.AgentCatalog, id string, status map[string]bool) bool {
	a, ok := agents.Get(id)
	if !ok {
		return false
	}
	if a.Config == nil {
		return status[id]
	}
	for _, t := range a.Config.Targets() {
		if _, err := os.Stat(t); err != nil {
			continue
		}
		if a.Config.IsTakenOver(t) {
			return true
		}
	}
	return false
}

// firstStartHint 是「装好了，但它的配置文件还不存在」时该说的那句话。
//
// 判据用描述符的机制（有 Config = 配置型），而不是一张 agent 名单——名单会在
// 加一家客户端时忘记更新，而那种漏掉的表现恰好是「用户以为接管了」。
func firstStartHint(agents agentapi.AgentCatalog, id string) string {
	a, ok := agents.Get(id)
	if !ok || a.Config == nil {
		return ""
	}
	return i18n.T("{agent} writes its config file on first run; start it once ({cmd}), then run newgate on {agent}",
		i18n.A{"agent": id, "cmd": firstBin(a)})
}

func firstBin(a *agentapi.Agent) string {
	if len(a.Bin) > 0 {
		return a.Bin[0]
	}
	return a.ID
}

// joinArgv 把 argv 拼成一行给人看（不做 shell 转义：它是提示文本）。
func joinArgv(argv []string) string {
	s := ""
	for i, a := range argv {
		if i > 0 {
			s += " "
		}
		s += a
	}
	if s == "" {
		return i18n.T("(no command declared)", nil)
	}
	return s
}

// Report 把一次「装 + 接管」写成给人看的几行。w 通常是 stdout。
func Report(w io.Writer, steps []Step) {
	for _, s := range steps {
		switch {
		case s.Err != nil:
			fmt.Fprintln(w, i18n.T("  ✗ {agent}: {err}", i18n.A{"agent": s.Agent, "err": s.Err.Error()}))
		case s.Takeover.Err != nil:
			fmt.Fprintln(w, i18n.T("  ✗ {agent}: installed, but taking it over failed: {err}",
				i18n.A{"agent": s.Agent, "err": s.Takeover.Err.Error()}))
		default:
			what := i18n.T("already installed", nil)
			if s.DidInstall {
				what = i18n.T("installed", nil)
			}
			line := i18n.T("  ✓ {agent}: {what}",
				i18n.A{"agent": s.Agent, "what": what})
			switch {
			case s.TakenOver && len(s.Takeover.Lines) > 0:
				line += i18n.T(", taken over ({first})", i18n.A{"first": s.Takeover.Lines[0]})
			case !s.TakenOver:
				line += i18n.T(", not taken over yet", nil)
			}
			fmt.Fprintln(w, line)
		}
		for _, warn := range s.Takeover.Warn {
			fmt.Fprintln(w, "    "+i18n.T("note: {warn}", i18n.A{"warn": warn}))
		}
	}
}

// StepsFailed 报告有没有哪一步真的失败了（用于退出码）。
func StepsFailed(steps []Step) bool {
	for _, s := range steps {
		if s.Err != nil || s.Takeover.Err != nil {
			return true
		}
	}
	return false
}

// stdout 是向导的默认输出口（测试里换成 buffer）。
var stdout io.Writer = os.Stdout
