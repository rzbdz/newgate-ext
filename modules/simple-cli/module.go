// Package simplecli 是一个**极简界面**：只渲染 status，其余注入点收下但不显示。
//
// # 它是用来证明什么的
//
// newgate 里界面是**可替换的壳**：模块往「界面」注入自己的命令与状态行，注入是一
// 条弱依赖（modules.Optional(cli)）——界面在就挂进去，不在就跳过。这个模块就是那句
// 话的反证实验：在 modules-ext.json 里关掉 `cli`、启用 `simple-cli`，**其余模块一行
// 不改**，系统必须照常起来：
//
//   - 它照样 Provide `cliapi.Capability`，所以各模块的 Optional(cli) 照常命中，
//     命令与状态行落进它的账本（换壳对注入者是不可见的）；
//   - 它只**渲染** status 与命令清单，其余六类注入（诊断、状态块、诊断素材、
//     术语、详细模式标记）收下但不显示——**注册期打一行说明**，不静默吞掉；
//   - 数据面、接管、熔断、配置解析全不受影响，它们本来就不依赖界面。
//
// # 为什么它不实现 modules/cli 的那套渲染
//
// 那套渲染（help 分节、表格、CJK 宽度对齐）是**一个具体壳的排版**，不是界面的
// 契约。契约只有两个方向：模块往里注入（cli/extension 的七个 RegisterXxx），
// 进程入口从它那儿出去（entry.Handler）。这个模块两边都实现了，只是故意做得
// 最小——这正是「界面可以小到什么程度」的答案。
package simplecli

import (
	"context"
	"fmt"
	"sort"
	"sync"

	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/component/entry"
	"github.com/rzbdz/newgate/go/lib/buildinfo"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
)

type service struct {
	self modules.Release

	mu       sync.Mutex
	commands []cliapi.Command
	statuses []cliapi.StatusProvider
	// 收下但不渲染的那几类。计数留着，status 里报出来——「注入点被接受了但没有
	// 显示」这件事必须看得见，否则某一个模块的体检项突然不见了会被当成它的 bug。
	accepted map[string]int
}

var (
	_ cliapi.CLI    = (*service)(nil)
	_ entry.Handler = (*service)(nil)
)

// New 声明极简界面组件。
//
// 它 Provide 的是**界面那个端口本身**（cliapi.Capability），不是自己的私有端口：
// 换壳要能被注入者无感，否则每次换壳都要改所有模块——那正是这套注入要避免的事。
// 「同时装两个壳」的后果由既有测试拦（app 的 TestExactlyOneUI 断言提供者恰好一个），
// 而声明里关掉一个就不会有两个。
func New() modules.Component {
	s := &service{accepted: map[string]int{}}
	return modules.Component{
		Name: "simple-cli",
		Type: "cli",
		// 依赖是**空的**：界面不依赖任何模块（与 modules/cli 同一条规矩）。
		Provides: []modules.Provision{
			modules.Provide(cliapi.Capability, cliapi.CLI(s)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			// 申报自己为**显式优先**的入口（entry.RankPreferred）：它比默认界面
			// 先被问。一个发行版里通常只装一个壳，这个 rank 的用处是**两个壳都在
			// 时**也能说清谁赢——由产品显式选定，而不是靠目录名字母序碰运气。
			release, err := modules.MustGet(ctx, entry.Capability).Register(s, entry.RankPreferred)
			if err != nil {
				return err
			}
			s.self = release
			return nil
		},
		Stop: func(context.Context) error {
			if s.self == nil {
				return nil
			}
			return s.self()
		},
	}
}

// Name 进日志：组合根那句 `[entry] resolve: … → simple-cli`。
func (s *service) Name() string { return "simple-cli" }

// Claims 永远认领——本壳是产品选定的入口，申报 rank 小于默认界面。
func (s *service) Claims(entry.Process) bool { return true }

// Handle 执行一次调用：认识 status 与（无参数时的）自我说明。
func (s *service) Handle(p entry.Process) int {
	if len(p.Args) == 0 {
		fmt.Println("newgate（simple-cli）——极简界面。可用：status")
		return 0
	}
	if p.Args[0] == "status" {
		return s.status()
	}
	// 命令清单里的命令照样能跑：模块注入的 Command 是它自己的分派器
	// （与 modules/cli 拿到的那个是同一个接口），这里只是把调用转过去。
	if cmd, ok := s.lookup(p.Args[0]); ok {
		return cmd.Run(simpleHost{}, p.Args[1:])
	}
	fmt.Printf("simple-cli: 不认识 %q（newgate status 看清单）\n", p.Args[0])
	return 64
}

func (s *service) lookup(name string) (cliapi.Command, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.commands {
		for _, n := range c.Names() {
			if n == name {
				return c, true
			}
		}
	}
	return nil, false
}

func (s *service) status() int {
	fmt.Println("newgate " + buildinfo.Version() + "  simple-cli")
	fmt.Println("  界面        simple-cli（渲染 status 与命令，其余注入点收下不显示）")

	s.mu.Lock()
	commands := append([]cliapi.Command(nil), s.commands...)
	statuses := append([]cliapi.StatusProvider(nil), s.statuses...)
	accepted := map[string]int{}
	for k, v := range s.accepted {
		accepted[k] = v
	}
	s.mu.Unlock()

	lines := []cliapi.StatusLine{}
	for _, p := range statuses {
		lines = append(lines, p.Status()...)
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Rank < lines[j].Rank })
	for _, l := range lines {
		fmt.Printf("  %-10s %s\n", l.Label, l.Value)
	}

	names := []string{}
	for _, c := range commands {
		names = append(names, c.Names()...)
	}
	sort.Strings(names)
	fmt.Printf("  命令（%d 条）\n", len(names))
	for _, n := range names {
		fmt.Println("             · " + n)
	}
	if len(accepted) > 0 {
		kinds := []string{}
		for k := range accepted {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		fmt.Printf("  收下但未渲染 ")
		for i, k := range kinds {
			if i > 0 {
				fmt.Print("、")
			}
			fmt.Printf("%s×%d", k, accepted[k])
		}
		fmt.Println()
	}
	return 0
}

// ---- cli/extension 的注入点 ----

// Run 只用于满足接口形状：入口走的是 Handle（见上）。留一个明确的行为而不是
// panic——真有人按接口调它时，得到的结果与入口那条路一致。
func (s *service) Run(p entry.Process) int { return s.Handle(p) }

// RegisterCommand 记账并返回撤销句柄（与 modules/cli 同语义：谁注入谁撤销）。
func (s *service) RegisterCommand(c cliapi.Command) (modules.Release, error) {
	if c == nil {
		return nil, fmt.Errorf("simple-cli: 命令不能为 nil")
	}
	s.mu.Lock()
	s.commands = append(s.commands, c)
	s.mu.Unlock()
	return func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, existing := range s.commands {
			if existing == c {
				s.commands = append(s.commands[:i], s.commands[i+1:]...)
				return nil
			}
		}
		return nil
	}, nil
}

func (s *service) RegisterStatus(p cliapi.StatusProvider) (modules.Release, error) {
	if p == nil {
		return nil, fmt.Errorf("simple-cli: 状态提供者不能为 nil")
	}
	s.mu.Lock()
	s.statuses = append(s.statuses, p)
	s.mu.Unlock()
	return func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, existing := range s.statuses {
			if existing == p {
				s.statuses = append(s.statuses[:i], s.statuses[i+1:]...)
				return nil
			}
		}
		return nil
	}, nil
}

// 其余五类：**收下、记账、打一行说明**。
//
// 为什么不返回错误：返回错误会让注入者（gateway / config / runtime…）的 Start 当场
// 失败，整张图装配不起来——而「换个壳」不该把数据面也换掉。也不静默吞掉：注册期
// 那一行日志 + status 里那一行计数，让「我注入的东西没显示」有据可查（现实里
// 这正是最容易误判成模块 bug 的一类现象）。
func (s *service) RegisterDiagnostics(cliapi.DiagnosticProvider) (modules.Release, error) {
	return s.accept("诊断"), nil
}
func (s *service) RegisterStatusBlocks(cliapi.BlockProvider) (modules.Release, error) {
	return s.accept("状态块"), nil
}
func (s *service) RegisterDump(cliapi.Dumper) (modules.Release, error) {
	return s.accept("诊断素材"), nil
}
func (s *service) RegisterGlossary(cliapi.Glossarist) (modules.Release, error) {
	return s.accept("术语"), nil
}
func (s *service) RegisterVerbose(cliapi.Verbose) (modules.Release, error) {
	return s.accept("详细模式"), nil
}

func (s *service) accept(kind string) modules.Release {
	s.mu.Lock()
	s.accepted[kind]++
	n := s.accepted[kind]
	s.mu.Unlock()
	fmt.Printf("simple-cli: 收下 %s 注入（本壳不渲染，见 status 的计数）\n", kind)
	return func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.accepted[kind] > 0 {
			s.accepted[kind]--
		}
		return nil
	}
}

// simpleHost 是 Command.Run 要的宿主。极简壳只提供最小能力：退出码与提示。
// **其余能力（NotifyProxy / DaemonRunning）刻意不做**——它们是「界面拥有进程
// 生命周期」才做得到的事，而这个壳没有那部分知识（它连守护进程都不认识）。
type simpleHost struct{}

var _ cliapi.Host = simpleHost{}

func (simpleHost) Die(code int, message string) int {
	if message != "" {
		fmt.Println(message)
	}
	return code
}
func (simpleHost) Verb() string        { return "" }
func (simpleHost) NotifyProxy()        {}
func (simpleHost) DaemonRunning() bool { return false }
