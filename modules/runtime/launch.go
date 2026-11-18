package runtime

// 本文件是**包装启动**：`newgate claude …` / `newgate run claude …` /
// `newgate --profile ds claude`。2026-09-18 从 modules/cli/launch.go 整体搬来。
//
// 为什么搬：「把某个客户端拉起来，并把它需要的 env/argv 组装好」就是本模块的
// 定义（见 package runtime 的说明）。原处的注释写着「runtime 只负责找到被 shim
// 遮住的真实二进制、构造最小环境覆盖并启动它」——而这段代码正是那条规则的调用
// 方，它留在界面里只因为界面是当初敲命令的地方。
//
// 界面因此不再需要知道「有哪些 agent」才能启动一个：客户端 id 是本模块与
// config-hook 的词汇。它把 `newgate claude …` 这一行交给账本里名字叫 claude 的
// 命令——那条命令就是下面的 launchCommand，名字由它自己声明（各 agent 一个）。

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate/lib/i18n"
)

// launchCommand 包装启动一个 agent：`newgate claude …` / `newgate run claude …` /
// `newgate --profile ds claude`。
//
// **一条命令，动词现查**（2026-09-18 重写）。上一版是「每个已知 agent 一个实例、
// 各自记着自己的 id」，那样注册期就必须知道全部动词；而动词是**运行期长出来的**
// ——客户端模块在自己的 Start 里注册 agent。于是 launchCommands 只能在 Start 里
// 枚举目录，而那一刻别人可能还没注册：实测（`Optional(cli)` 这条排序边把 cli 提到
// 最前之后）runtime 排在 claudecode 之前，枚举出来是空表，`newgate claude` 报
// 「未知命令」。70 条 e2e 断言就是这么红的。
//
// 根因不是顺序，是**在 Start 里读了别人写的东西**——依赖图表达不了「所有人都写完
// 了」（见 docs/02-component-framework.md 的三段法则）。所以动词表在**分派那一刻**
// 现查，注册期只交一条命令。
//
// 缺的那块信息由宿主补回来：分派器交给命令的 args **不含动词本身**（契约如此），
// `newgate claude --profile=ds` 分派到这条命令时收到的是 `["--profile=ds"]`。宿主
// 知道它是按哪个名字找到我们的（cliapi.Host.Verb），把 id 补回参数首位，后面走
// 同一条 splitLaunch（它也负责校验 id 真的存在）。
type launchCommand struct {
	rt     Runtime
	agents confighookapi.AgentCatalog
}

var (
	_ cliapi.Command    = (*launchCommand)(nil)
	_ cliapi.Documented = (*launchCommand)(nil)
	_ cliapi.Handoff    = (*launchCommand)(nil)
)

// Names 现查目录：每个已知 agent 一个动词，外加显式的 `run`。
//
// **每次分派都现查**（不是缓存）：目录在装配之后还会长，而且新 agent 注册进来
// 就该立刻可分派——不需要重启，也不需要赌自己排在客户端模块后面。
func (c launchCommand) Names() []string {
	names := append([]string(nil), c.agents.Names()...)
	return append(names, "run")
}

// Help 只声明 `run <agent> [args…]` 一行。
//
// **故意不逐个 agent 列一行**：agent 名是各客户端模块的键，help 里列出它们等于
// 界面又认识了一遍客户端。用户敲 `newgate claude` 从来不是从 help 里学来的。
//
// 上一版靠「每个 agent 一个实例、实例按 agentID 返回空 Usage」来做这件事，代价是
// 注册期就得知道全部 agent（见上面的说明）。一条命令之后，一行就是一行。
func (c launchCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionRunOnce, Rank: 20,
		Usage: "run <agent> [args…]", Summary: i18n.T("Run once with a given profile", nil)}
}

func (c launchCommand) Run(host cliapi.Host, args []string) int {
	// 动词位被分派器剥掉了：名字就是 agent 的那种调用要把 id 补回参数首位，
	// 让 splitLaunch 走同一条路径（它也负责校验 id 是不是真的存在）。
	if verb := host.Verb(); verb != "" && verb != "run" {
		args = append([]string{verb}, args...)
	}
	return runLaunch(c.rt, c.agents, args)
}

// launchCommands 只有一条：动词表在分派期现查（见 launchCommand 的说明）。
func launchCommands(rt Runtime, agents confighookapi.AgentCatalog) []cliapi.Command {
	return []cliapi.Command{launchCommand{rt: rt, agents: agents}}
}

// HandsOff：这条命令接着会把控制权交给被启动的客户端，newgate 的版式审计不该
// 去看那个进程的输出（见 cliapi.Handoff）。
func (launchCommand) HandsOff() {}

// cmdLaunch 包装启动一个 agent：解析 newgate 自己的选项，透传其余参数。
//
// 支持（docs/08-operations.md）：
//
//	newgate claude
//	newgate claude --profile=ds
//	newgate --profile ds claude
//	newgate run claude --profile=ds
//	newgate claude --resume abc          # --resume abc 透传给 claude
func runLaunch(rt Runtime, agents confighookapi.AgentCatalog, args []string) int {
	agentName, profile, passthrough, err := splitLaunch(agents, args)
	if err != nil {
		return style.Die(64, err.Error())
	}
	a, _ := agents.Get(agentName)

	// 没装的工具：这次调用归**我们**（见 offerInstall 与 takeYes）。装在的照旧，
	// 一个字都不碰它的命令行。
	if !agents.Installed(agentName) {
		if handled, rest := offerInstall(agents, a, passthrough); handled {
			passthrough = rest
		}
	}

	// profile 不存在立刻报错，绝不静默回落到默认——那正是「切了没生效」的来源。
	if profile != "" {
		if _, err := store.LoadProfile(profile); err != nil {
			avail, _ := store.ListProfiles()
			return style.Die(65, i18n.T("No such profile {profile} (available: {list})",
				i18n.A{"profile": profile, "list": strings.Join(avail, ", ")}))
		}
	}

	return rt.Launch(a, agents.Facts(a.ID), passthrough, profile)
}

// splitLaunch 把启动 argv 切成 (agent, profile, 透传参数)。
//
// 规则（docs/08-operations.md 的落地，唯一放宽：`--profile`/`--preset` 是 newgate 自己的
// 选项，出现在 agent 之后也照样被我们消费——否则 `newgate claude --profile=ds`
// 里的 profile 会被透传给 claude）：
//
//  1. 从左往右扫；
//  2. `--profile`/`--preset` 按 arity 消费值（`--profile ds` 或 `--profile=ds`）；
//  3. 第一个既不是 newgate 选项、也不是其值的 token 是 agent；
//  4. agent 之后、且不是 newgate 选项的 token 一律原样透传；
//  5. agent 确定之前的未知 `-` 选项按拼错处理（报错）。
func splitLaunch(agents confighookapi.AgentCatalog, args []string) (agent, profile string, passthrough []string, err error) {
	if len(args) > 0 && args[0] == "run" {
		args = args[1:]
	}

	var rest []string
	agentKnown := false

	for i := 0; i < len(args); i++ {
		a := args[i]

		// newgate 自己的 profile 选项（`--preset` 是历史别名，同一件事）。
		if a == "--profile" || a == "--preset" || strings.HasPrefix(a, "--profile=") || strings.HasPrefix(a, "--preset=") {
			v := ""
			if j := strings.Index(a, "="); j >= 0 {
				v = a[j+1:]
			} else if i+1 < len(args) {
				v = args[i+1]
				i++
			}
			if v == "" {
				return "", "", nil, i18n.E("{option} needs a profile name", i18n.A{"option": a})
			}
			if profile != "" && profile != v {
				return "", "", nil, i18n.E("Both profile {first} and {second} were given, and they disagree",
					i18n.A{"first": profile, "second": v})
			}
			profile = v
			continue
		}

		if strings.HasPrefix(a, "-") {
			if !agentKnown {
				return "", "", nil, i18n.E(
					"Unknown option {option} (an unknown option before the tool counts as a typo, docs/08-operations.md rule 5)",
					i18n.A{"option": a})
			}
			rest = append(rest, a)
			continue
		}

		if !agentKnown {
			agent = a
			agentKnown = true
		} else {
			rest = append(rest, a)
		}
	}

	if agent == "" {
		return "", "", nil, i18n.E("Which agent to launch? e.g. newgate claude or newgate run claude", nil)
	}
	if _, ok := agents.Get(agent); !ok {
		return "", "", nil, i18n.E("Unknown agent {agent} (known: {list})",
			i18n.A{"agent": agent, "list": strings.Join(agents.Names(), ", ")})
	}
	return agent, profile, rest, nil
}

// takeYes 从透传参数里摘掉 `-y` / `--yes`。
//
// **只在「这个工具没装」时调用**（见 runLaunch）：命令根本不存在，所以它后面的参数
// 不可能是给它的——`newgate opencode -y` 里的 `-y` 只可能是「确认安装」。反过来，
// 装着的工具一律原样透传：那时 `-y` 是**它自己的**参数，摘走等于改用户的命令行。
// 判据就一条：装没装。
func takeYes(args []string) (yes bool, rest []string) {
	for _, a := range args {
		if a == "-y" || a == "--yes" {
			yes = true
			continue
		}
		rest = append(rest, a)
	}
	return yes, rest
}

// confirm 问一句 y/N。读不到输入（没有终端、管道）一律当「不」——
// 一次没人回答的提问不该被当成同意，那是「静默地跑了用户没批准的事」。
func confirm(prompt string) bool {
	fmt.Fprint(os.Stderr, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(os.Stderr)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// offerInstall 在「这个工具没装」时接手这次调用：问一句、装、然后**照常启动**。
//
// 为什么装完接着启动而不是让用户再敲一遍：他敲的那条命令本来就是「跑它」，装上只是
// 途中的一步。转头让用户重来一次，是把我们自己的实现细节变成了他的操作步骤。
//
// 返回 (是否已经处理掉这次调用, 剩下的透传参数)。没有安装器时返回 false——那种
// 客户端不支持自助安装，落回原来那条路（FindReal 会报一句「PATH 上找不到」）。
func offerInstall(agents confighookapi.AgentCatalog, a *confighookapi.Agent, passthrough []string) (bool, []string) {
	inst := agents.Installer(a.ID)
	if inst == nil {
		return false, passthrough
	}
	yes, rest := takeYes(passthrough)
	cmdline := joinArgv(inst.Command())
	if !yes {
		if !confirm(i18n.T("{agent} is not installed. Run `{cmd}` to install it? [y/N] ",
			i18n.A{"agent": a.ID, "cmd": cmdline})) {
			fmt.Fprintln(os.Stderr, i18n.T("Not installing. Do it yourself with: {cmd}",
				i18n.A{"cmd": cmdline}))
			os.Exit(0)
		}
	}
	fmt.Fprintf(os.Stderr, "newgate: %s\n", i18n.T("running: {cmd}", i18n.A{"cmd": cmdline}))
	if err := inst.Install(os.Stderr); err != nil {
		// best-effort：装不上不是故障（没有 npm、网络不通、没有权限都正常），
		// 但**必须说清楚**——「没装成却看着像装成了」会让人在一个不存在的工具上
		// 排查半天。原样带上命令与它的报错。
		fmt.Fprintf(os.Stderr, "newgate: %s\n", i18n.T("install failed: {err}", i18n.A{"err": err}))
		os.Exit(69)
	}
	if !agents.Installed(a.ID) {
		// 装是装完了，但这个进程的 PATH 里还是找不到它（包管理器的 bin 目录不在这
		// 一次的 PATH 上，或者装到了别处）。**不假装成功**：说清楚，让用户开个新
		// shell 再跑——那条路一定对，而继续往下走只会在启动时炸得更难懂。
		fmt.Fprintf(os.Stderr, "newgate: %s\n", i18n.T(
			"{agent} was installed but is still not on PATH — open a new shell and try again",
			i18n.A{"agent": a.ID}))
		os.Exit(69)
	}
	return true, rest
}

// joinArgv 把 argv 拼成一行给人看（不做 shell 转义：它是提示文本，不是给 shell 执行的东西）。
func joinArgv(argv []string) string { return strings.Join(argv, " ") }
