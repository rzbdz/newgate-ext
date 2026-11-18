package runtime

// 本文件是**接管自己的**那批命令：start / stop / on / off / restart / shim。
// 2026-09-18 从 modules/cli/{lifecycle,shim}.go 整体搬来。
//
// 为什么搬：接管是 runtime 的语义——插上 shim、改写客户端配置、备份原文件、
// 释放回去，全是本模块在做的事。它们留在界面时，界面得认识 takeover / injection
// / confighook 三套东西才写得出来，而它真正该做的只有「把 argv 交给对的人」。
//
// 命令由本模块在 Start 里注册进界面：本模块对 ui 声明一条**弱依赖**
// （Optional(cli)，见 component.Optional），于是它排在界面之后——Start 跑到这里
// 时界面的账本已经就绪；没装界面就跳过，接管照常工作。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/lib/httpx"
	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlpath"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
	"github.com/rzbdz/newgate-ext/modules/runtime/daemon"
	"github.com/rzbdz/newgate-ext/modules/runtime/injection"
	"github.com/rzbdz/newgate-ext/modules/runtime/takeover"
	"github.com/rzbdz/newgate/lib/i18n"
)

// die 是统一的报错出口（版式在 lib/style）。
func die(code int, msg string) int { return style.Die(code, msg) }

// pingProxy 端口上的控制面活着吗。
func pingProxy(port int) bool { return controlplane.Ping(port) }

// cmdShim 是**底层逃生口**，不是日常命令。
//
// 日常接管请用 newgate on/off <agent>：用户不该被迫知道自己的工具是靠
// PATH shim 接管还是靠改配置文件接管——那是 runtime/takeover 的事
// （docs/08-operations.md：逃生口是一等功能，但它得摆在逃生口的位置上）。
//
// 这里只留三件 takeover 层不做的事：
//   - status：把 shim 目录的真实情况摊开，排查「装了却没生效」
//   - uninstall：连 rc 里那行 PATH 一起删干净（takeover 只摘链接，
//     留着空目录在 PATH 里是无害的，也让下次 on 不用再动 rc）
//   - install/off：老命令的别名，直接转给 on/off，免得肌肉记忆报错
func cmdShim(agents confighookapi.AgentCatalog, sub, which string) int {
	switch sub {
	case "install", "add", "on":
		if which == "" {
			return die(64, i18n.T("Usage: newgate shim install <agent> (known: {names})",
				i18n.A{"names": strings.Join(agents.Names(), ", ")}))
		}
		fmt.Println(style.Hint(i18n.T("Just use newgate on {agent}; newgate picks the mechanism itself",
			i18n.A{"agent": which})))
		fmt.Println()
		return cmdTakeover(agents, which)

	case "off", "remove", "rm":
		if which == "" {
			return die(64, i18n.T("Usage: newgate shim off <agent> (known: {names})",
				i18n.A{"names": strings.Join(agents.Names(), ", ")}))
		}
		fmt.Println(style.Hint(i18n.T("Just use newgate off {agent}", i18n.A{"agent": which})))
		fmt.Println()
		return cmdRelease(which)

	case "uninstall", "purge":
		for _, n := range injection.Installed() {
			if err := injection.Uninstall(n); err != nil {
				fmt.Fprintln(os.Stderr, style.Item(style.Warn, n+": "+err.Error()))
				continue
			}
			fmt.Println(style.Item(style.OK, i18n.T("Removed the shim {name}", i18n.A{"name": n})))
		}
		for _, rc := range injection.RCFiles() {
			changed, err := injection.RemoveFromRC(rc)
			if err != nil {
				fmt.Fprintln(os.Stderr, style.Item(style.Warn, rc+": "+err.Error()))
				continue
			}
			if changed {
				fmt.Println(style.Item(style.OK, i18n.T("Removed the PATH line from {file}", i18n.A{"file": rc})))
			}
		}
		if fg := injection.Foreign(); len(fg) > 0 {
			// 只在我们**认得这个 agent 名**时才出声：那种情况是外来同名文件
			// 会遮住我们的 shim，值得看一眼；目录里躺着 claude-bak 这种
			// 无关文件是用户自己的事，报出来只是噪音。
			var shadow []string
			for _, n := range fg {
				if _, known := agents.Get(n); known {
					shadow = append(shadow, n)
				}
			}
			if len(shadow) > 0 {
				fmt.Println(style.Hint(i18n.T("an external file is still in {dir}: {names} (it shadows newgate's shim)",
					i18n.A{"dir": injection.Dir(), "names": strings.Join(shadow, ", ")})))
			}
		}
		fmt.Println()
		fmt.Println(style.Hint(i18n.T("PATH traces are cleared; reopen the shell to take effect", nil)))
		fmt.Println(style.Hint(i18n.T("This does not change takeover intent: the next newgate start reinstalls it", nil)))
		return 0

	default:
		return shimStatus(agents)
	}
}

// shimStatus 摊开 shim 目录的真实情况，用于排查「装了却没生效」。
func shimStatus(agents confighookapi.AgentCatalog) int {
	fmt.Println(style.Title("newgate shim", injection.Dir()))
	fmt.Println(style.Rule(72))

	if injection.InPath() {
		fmt.Println(style.Field(i18n.T("In PATH", nil), style.Green(i18n.T("yes", nil))))
	} else {
		fmt.Println(style.Field(i18n.T("In PATH", nil), style.Yellow(i18n.T("no", nil))+
			style.Dim("   "+i18n.T("reopen the shell or exec $SHELL -l", nil))))
	}

	inst := injection.Installed()
	if len(inst) == 0 {
		fmt.Println(style.Field(i18n.T("Installed", nil), style.Dim(i18n.T("none", nil))))
	} else {
		t := style.NewTable("shim", i18n.T("points to", nil))
		for _, n := range inst {
			a, ok := agents.Get(n)
			if !ok {
				t.Row(n, style.Dim(i18n.T("unknown agent", nil)))
				continue
			}
			if real, err := a.FindReal(injection.Dir()); err == nil {
				t.Row(n, real)
			} else {
				t.Row(n, style.Red(i18n.T("the real executable was not found (forwarding will fail)", nil)))
			}
		}
		fmt.Println(style.Field(i18n.T("Installed", nil),
			i18n.N("{n} shim", "{n} shims", len(inst), i18n.A{"n": len(inst)})))
		fmt.Print(t.String())
	}

	t := style.NewTable(i18n.T("rc file", nil), i18n.T("PATH line", nil))
	for _, rc := range injection.RCFiles() {
		mark := style.Dim(i18n.T("none", nil))
		if injection.HasBlock(rc) {
			mark = style.Green(i18n.T("yes", nil))
		}
		t.Row(rc, mark)
	}
	fmt.Print(t.String())

	// 期望态 vs 现实态：只列走 shim 机制的 agent，其余与 shim 无关。
	var rows [][2]string
	for _, s := range takeover.List() {
		if s.Mechanism != takeover.MechShim {
			continue
		}
		state := style.Mark(style.OK) + " " + i18n.T("in effect", nil)
		switch {
		case s.Wanted && !s.Active:
			state = style.Mark(style.Bad) + " " + i18n.T("declared takeover but not in effect", nil)
		case !s.Wanted && s.Active:
			state = style.Mark(style.Warn) + " " + i18n.T("not declared but still installed", nil)
		case !s.Wanted:
			state = style.Mark(style.Skip) + " " + i18n.T("not taken over", nil)
		}
		rows = append(rows, [2]string{s.Agent, state})
	}
	if len(rows) > 0 {
		fmt.Print(style.Section(i18n.T("Takeover intent", nil)) + "\n")
		t := style.NewTable("agent", i18n.T("status", nil))
		for _, r := range rows {
			t.Row(r[0], r[1])
		}
		fmt.Print(t.String())
	}

	fmt.Println()
	fmt.Println(style.Hint(i18n.T("Low-level escape hatch; day to day use newgate on|off <agent> · newgate shim uninstall also clears the rc files", nil)))
	return 0
}

func cmdStart(agents confighookapi.AgentCatalog, force bool) int {
	// 实现在 control.go 的 Start 里：命令行与 `newgate config` 那个向导走**同一份**
	// 代码，因为「起代理 + 全面接管」只有一种正确行为（令牌先落盘、没 key 不接管、
	// 接管后写回 TakenOver）。这里只负责把结局翻成退出码与那句 shell 环境提醒。
	rep, err := (&service{}).Start(StartOptions{Force: force})
	if err != nil {
		switch {
		case errors.Is(err, ErrConfigUnusable):
			return 65
		case errors.Is(err, ErrProxyDidNotStart):
			return 70
		}
		return die(70, err.Error())
	}
	for _, r := range rep.Takeover {
		if r.Mechanism == takeover.MechShim && r.Err == nil && !r.Skipped && len(r.Lines) > 0 {
			warnShellEnvConflict(agents, r.Agent)
		}
	}
	return 0
}

// printResults 把接管/释放结果打成人话。
//
// 三种标记不能混：✓ 真的接管上了，· 有意跳过，✗ 出错了。agent 名补到固定
// 宽度，续行对齐到说明列——一次接管多个 agent 时才扫得动。
func printResults(rs []takeover.Result) {
	const nameW = 10
	for _, r := range rs {
		switch {
		case r.Err != nil:
			fmt.Fprintln(os.Stderr, style.Item(style.Bad, style.Pad(r.Agent, nameW)+r.Err.Error()))
		case len(r.Lines) == 0:
			continue
		default:
			mark := style.OK
			if r.Skipped {
				mark = style.Skip
			}
			fmt.Println(style.Item(mark, style.Pad(r.Agent, nameW)+r.Lines[0]))
			for _, l := range r.Lines[1:] {
				fmt.Println("    " + style.Pad("", nameW) + style.Dim(l))
			}
		}
		for _, w := range r.Warn {
			fmt.Println(style.Bullet(style.Mark(style.Warn) + " " + w))
		}
	}
}

// cmdTakeover / cmdRelease 单独接管或释放一个 agent。
//
// 用户不该关心机制：claude 靠 PATH shim、opencode 靠改配置文件，这是我们的
// 实现细节。对外只有「接管」和「释放」，机制由 runtime/takeover 挑。
func cmdTakeover(agents confighookapi.AgentCatalog, agent string) int {
	if agent == "" {
		return die(64, i18n.T("Which agent to take over? e.g. newgate on claude", nil))
	}
	st := store.LoadState()
	r := takeover.On(agent, st.Port)
	if r.Err != nil {
		return die(65, r.Err.Error())
	}
	printResults([]takeover.Result{r})
	if r.Mechanism == takeover.MechShim {
		warnShellEnvConflict(agents, agent)
	}
	if daemon.Running() == nil {
		fmt.Println(style.Hint(i18n.T("Proxy is not running · it takes effect after newgate start", nil)))
	}
	return 0
}

func cmdRelease(agent string) int {
	if agent == "" {
		return die(64, i18n.T("Which agent to release? e.g. newgate off claude", nil))
	}
	r := takeover.Off(agent)
	if r.Err != nil {
		return die(65, r.Err.Error())
	}
	if len(r.Lines) == 0 {
		fmt.Println(style.Item(style.Skip, style.Pad(agent, 10)+i18n.T("was not taken over in the first place", nil)))
	} else {
		printResults([]takeover.Result{r})
	}
	fmt.Println(style.Hint(i18n.T("{agent} is back to a direct connection; newgate start will no longer take it over (newgate on {agent} restores it)",
		i18n.A{"agent": agent})))
	if r.Mechanism == takeover.MechShim {
		fmt.Println(style.Hint(i18n.T("This shell may have cached the path: hash -r (zsh: rehash)", nil)))
	}
	return 0
}

func cmdStop() int {
	// 顺序很重要：先把 PATH shim 摘掉。否则 `claude` 这类命令仍会命中
	// ~/.config/newgate/bin/claude → newgate，而 wrapper 会**懒启动代理**，
	// stop 等于没停——这是「stop 了还在走 newgate」的直接原因。
	//
	// OffAll 不改接管意愿：下次 start 会把这些原样插回去。
	printResults(takeover.OffAll())

	pid, err := daemon.Stop()
	if err != nil {
		return die(70, err.Error())
	}
	if pid > 0 {
		fmt.Println(style.Item(style.OK, i18n.T("Proxy stopped   pid {pid}", i18n.A{"pid": pid})))
	} else {
		fmt.Println(style.Item(style.Skip, i18n.T("The proxy was not running", nil)))
	}

	st := store.LoadState()
	st.TakenOver = false
	// 同上：这一步写不出去，下次 start 会以为「用户从没放手过」。
	if err := store.SaveState(st); err != nil {
		return style.Die(70, i18n.T("Cannot write the takeover state back to state.json: {err}",
			i18n.A{"err": err.Error()}))
	}

	fmt.Println()
	fmt.Println(style.Hint(i18n.T("All tools are back to direct connections; the PATH line still points at the empty shim dir, which is harmless", nil)))
	fmt.Println(style.Hint(i18n.T("Remove it completely: newgate shim uninstall", nil)))
	return 0
}

// cmdRestart 重启代理并保留接管现场。
//
// 优先走优雅交接（nginx upgrade 语义）：旧 daemon 把监听 socket 移交给
// 新二进制，在途请求流完为止——正穿行在代理里的会话（比如正在开发
// newgate 的 Claude Code）完全不受影响，接管状态也不动。
// 运行中的是旧版 daemon（没有交接能力）时退回 stop+start：有短暂断流
// 窗口，会提示一句。
func cmdRestart(agents confighookapi.AgentCatalog, force bool) int {
	if tryHandoff() {
		return 0
	}
	cmdStop()
	fmt.Println()
	return cmdStart(agents, force)
}

// tryHandoff 让运行中的 daemon 把监听 socket 交接给磁盘上的新二进制。
// 成功（已完全就位）返回 true；不可行时打印原因并返回 false，由调用方
// 退回 stop+start。
func tryHandoff() bool {
	i := daemon.Running()
	if i == nil {
		return false // 没在跑，restart 退化为 start
	}
	st := store.LoadState()
	if st.ControlToken == "" {
		fmt.Println(style.Item(style.Skip, i18n.T("The daemon has no control token (started before the upgrade), falling back to stop+start", nil)))
		return false
	}
	port := i.Port
	if port <= 0 {
		port = st.Port
	}
	// 先探能力再动手：旧版 daemon 没注册 /__newgate/upgrade，盲发会被
	// catch-all 转发给上游。status 的 handoff 字段是无副作用的探针。
	supports, err := handoffSupported(port)
	if err != nil || !supports {
		if err != nil {
			fmt.Println(style.Item(style.Skip, i18n.T("Cannot probe the daemon handoff capability ({err}), falling back to stop+start",
				i18n.A{"err": err})))
		} else {
			fmt.Println(style.Item(style.Skip, i18n.T("The running daemon is an old version (no graceful handoff), falling back to stop+start", nil)))
		}
		return false
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d%s", port, controlpath.Upgrade), nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+st.ControlToken)
	// 旧 daemon 要等新进程 ready 才回 200，给足时间
	resp, err := httpx.LocalClient(20 * time.Second).Do(req)
	if err != nil {
		fmt.Println(style.Item(style.Skip, i18n.T("Cannot send the handoff request ({err}), falling back to stop+start",
			i18n.A{"err": err})))
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := ioutil.ReadAll(io.LimitReader(resp.Body, 512))
		fmt.Println(style.Item(style.Skip, i18n.T("Handoff refused (HTTP {code}: {body}), falling back to stop+start",
			i18n.A{"code": resp.StatusCode, "body": string(b)})))
		return false
	}
	// 200 = 新进程已接上 socket、pid/lock 已改写。这里只确认它完全就位。
	for k := 0; k < 100; k++ { // 最多 5s
		if j := daemon.Running(); j != nil && j.PID != i.PID && pingProxy(j.Port) {
			fmt.Println(style.Item(style.OK, i18n.T("Proxy gracefully restarted   pid {old} → {new} · 127.0.0.1:{port}",
				i18n.A{"old": i.PID, "new": j.PID, "port": j.Port})))
			fmt.Println(style.Hint(i18n.T("Socket handed over seamlessly: in-flight requests drain in the old process, sessions are not interrupted; takeover state is untouched", nil)))
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Printf("%s %s\n", style.Mark(style.Warn), i18n.T("Handoff sent, but the new process was not in place within 5 seconds · log {log}",
		i18n.A{"log": paths.LogFile()}))
	return false
}

// handoffSupported 问 daemon 的 status 端点：支持优雅交接吗。
func handoffSupported(port int) (bool, error) {
	resp, err := httpx.LocalClient(3 * time.Second).
		Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, controlpath.Status))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var out struct {
		Handoff bool `json:"handoff"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.Handoff, nil
}

// activeProblems 只检查**实际会被用到**的 provider，别因为某个用不到的
// profile 缺 key 就挡住启动。
func activeProblems(st *domain.State) []string {
	snap, err := store.Load()
	if err != nil {
		return []string{err.Error()}
	}
	used := map[string]bool{}
	check := func(profile string) {
		for _, p := range snap.Profiles {
			if p.Name != profile {
				continue
			}
			for _, cands := range p.Roles {
				for _, b := range cands {
					used[b.Provider] = true
				}
			}
		}
	}
	check(st.DefaultProfile)
	for _, p := range st.Active {
		check(p)
	}
	var out []string
	for name := range used {
		prov, ok := snap.Providers.Providers[name]
		if !ok {
			out = append(out, i18n.T("Provider {name} is not defined", i18n.A{"name": name}))
			continue
		}
		if prov.Key() == "" {
			hint := i18n.T("fill in api_key in providers.json", nil)
			if prov.APIKeyEnv != "" {
				hint = i18n.T("set the environment variable {env} or fill in api_key", i18n.A{"env": prov.APIKeyEnv})
			}
			out = append(out, i18n.T("Provider {name} has no key → {hint}", i18n.A{"name": name, "hint": hint}))
		}
	}
	return out
}

func warnShellEnvConflict(agents confighookapi.AgentCatalog, toolID string) {
	a, ok := agents.Get(toolID)
	if !ok {
		return
	}
	var conflict []string
	for _, k := range append([]string{a.BaseURLEnv, a.AuthEnv}, a.UnsetEnv...) {
		if os.Getenv(k) != "" {
			conflict = append(conflict, k)
		}
	}
	if len(conflict) == 0 {
		return
	}
	fmt.Println(style.Hint(i18n.T("Already exported in the shell: {names}", i18n.A{"names": strings.Join(conflict, ", ")})))
	fmt.Println(style.Hint(i18n.T("Takeover overrides it inside the child process, without affecting newgate; it applies again after off (i.e. back to a direct connection)", nil)))
}

// ---------- 命令声明 ----------
//
// 每一条都只做「解析 argv → 调上面的实现」。帮助行、槽位、位置是给界面看的
// 元数据，由拥有这条命令的模块声明（见 cliapi.Documented）。

// 接管那一节的位置：与原来写在界面里时一致，用户的肌肉记忆不会变。
const rankTakeover = 10

type startCommand struct{ agents confighookapi.AgentCatalog }

var (
	_ cliapi.Command    = (*startCommand)(nil)
	_ cliapi.Documented = (*startCommand)(nil)
)

func (startCommand) Names() []string { return []string{"start"} }

func (startCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionTakeover, Rank: rankTakeover,
		Usage: "start", Summary: i18n.T("Start the proxy + take over every agent", nil)}
}

func (c startCommand) Run(_ cliapi.Host, args []string) int {
	if a := cliapi.Positional(args, 0); a != "" {
		return cmdTakeover(c.agents, a)
	}
	return cmdStart(c.agents, cliapi.Flag(args, "--force"))
}

type stopCommand struct{ agents confighookapi.AgentCatalog }

var (
	_ cliapi.Command    = (*stopCommand)(nil)
	_ cliapi.Documented = (*stopCommand)(nil)
)

func (stopCommand) Names() []string { return []string{"stop"} }

func (stopCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionTakeover, Rank: rankTakeover,
		Usage: "stop", Summary: i18n.T("Stop the proxy + every agent back to a direct connection", nil)}
}

func (stopCommand) Run(_ cliapi.Host, args []string) int {
	if a := cliapi.Positional(args, 0); a != "" {
		return cmdRelease(a)
	}
	return cmdStop()
}

type takeoverCommand struct{ agents confighookapi.AgentCatalog }

var (
	_ cliapi.Command    = (*takeoverCommand)(nil)
	_ cliapi.Documented = (*takeoverCommand)(nil)
)

func (takeoverCommand) Names() []string { return []string{"on", "takeover", "take"} }

func (takeoverCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionTakeover, Rank: rankTakeover,
		Usage: "on <agent>", Summary: i18n.T("Take over just one", nil)}
}

func (c takeoverCommand) Run(_ cliapi.Host, args []string) int {
	return cmdTakeover(c.agents, cliapi.Positional(args, 0))
}

type releaseCommand struct{}

var (
	_ cliapi.Command    = (*releaseCommand)(nil)
	_ cliapi.Documented = (*releaseCommand)(nil)
)

func (releaseCommand) Names() []string { return []string{"off", "release", "free"} }

func (releaseCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionTakeover, Rank: rankTakeover,
		Usage: "off <agent>", Summary: i18n.T("Release just one; later start runs will leave it alone", nil)}
}

func (releaseCommand) Run(_ cliapi.Host, args []string) int {
	if a := cliapi.Positional(args, 0); a != "" {
		return cmdRelease(a)
	}
	return cmdStop()
}

type restartCommand struct{ agents confighookapi.AgentCatalog }

var (
	_ cliapi.Command    = (*restartCommand)(nil)
	_ cliapi.Documented = (*restartCommand)(nil)
)

func (restartCommand) Names() []string { return []string{"restart"} }

func (restartCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionTakeover, Rank: rankTakeover,
		Usage: "restart", Summary: i18n.T("Restart the proxy, keeping the takeover scene as it is", nil)}
}

func (c restartCommand) Run(_ cliapi.Host, args []string) int {
	return cmdRestart(c.agents, cliapi.Flag(args, "--force"))
}

type shimCommand struct{ agents confighookapi.AgentCatalog }

var (
	_ cliapi.Command    = (*shimCommand)(nil)
	_ cliapi.Documented = (*shimCommand)(nil)
)

func (shimCommand) Names() []string { return []string{"shim"} }

func (shimCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionMaintenance, Rank: 50,
		Usage: "shim …", Summary: i18n.T("Low-level escape hatch; on/off is enough day to day", nil)}
}

func (c shimCommand) Run(_ cliapi.Host, args []string) int {
	// 不给默认 agent：客户端 id 是**各客户端模块自己的键**，界面不该知道任何一个。
	return cmdShim(c.agents, cliapi.Positional(args, 0), cliapi.Positional(args, 1))
}

// commands 是本模块注入界面的全部命令。
func commands(agents confighookapi.AgentCatalog) []cliapi.Command {
	return []cliapi.Command{
		startCommand{agents}, stopCommand{agents}, takeoverCommand{agents},
		releaseCommand{}, restartCommand{agents}, shimCommand{agents},
	}
}
