// Package simplecli 是一个**极简界面**：只渲染 status，其余注入点收下但不显示。
//
// # 它是用来证明什么的
//
// newgate 里界面是**可替换的壳**：模块往「界面」注入自己的命令与状态行，注入是一
// 条弱依赖（modules.Optional(cli)）——界面在就挂进去，不在就跳过。这个模块就是那句
// 话的反证实验：在 `dist-simple-cli.json` 里关掉内核的 `cli`、启用 `simple-cli`，
// **其余模块一行不改**，系统必须照常起来：
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

	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
	"github.com/rzbdz/newgate/lib/buildinfo"
	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
)

// statusLabelW 是 status 里标签列的宽度。
//
// 它是**版式数据**，所以写成一处常量：中文标签（`界面`）与英文标签（`Interface`）
// 的**显示**宽度与**字节**长度不是一回事，按字节补空格会在换语言的那一刻歪掉。
// 宽度本身两门语言共用，不搬进目录（见内核 docs/13-i18n.md §3 的 meta.widths）。
const statusLabelW = 10

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
		fmt.Println(i18n.T("newgate (simple-cli) — a minimal UI. Available: status", nil))
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
	// 前缀 `simple-cli: ` 是**机器标记**（排查时 grep 它），留在消息外面——
	// 同内核 cli 那条 `errCLI`（见 docs/13-i18n.md §7）。
	//
	// 引号写在消息里而不是拿 `%q` 格式化实参：占位符只负责把值填进去，引号是
	// 版式的一部分（内核那几条 `plugin: unknown verb "{verb}"` 也是这么写的）。
	fmt.Printf("simple-cli: %s\n", i18n.T("unknown command \"{cmd}\" (see the list with newgate status)",
		i18n.A{"cmd": p.Args[0]}))
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
	// 版本号是机器标记（`newgate dev  simple-cli` 那行）不进消息；这一行整句进目录
	// ——壳的名字夹在句子中间，拆出去英文会粘成一个词（`simple-cli(renders`），
	// 而中文的「（」自带分隔感，译文里照原样抄一遍就行。
	fmt.Printf("  %s %s\n", style.Pad(i18n.T("Interface", nil), statusLabelW),
		i18n.T("simple-cli (renders status and the commands; the other injection points are accepted but not shown)", nil))

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
		fmt.Printf("  %s %s\n", style.Pad(l.Label, statusLabelW), l.Value)
	}

	names := []string{}
	for _, c := range commands {
		names = append(names, c.Names()...)
	}
	sort.Strings(names)
	fmt.Printf("  %s\n", i18n.N("{n} command", "{n} commands", len(names), i18n.A{"n": len(names)}))
	for _, n := range names {
		fmt.Println("             · " + n)
	}
	if len(accepted) > 0 {
		kinds := []string{}
		for k := range accepted {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		fmt.Printf("  %s ", i18n.T("accepted but not rendered", nil))
		for i, k := range kinds {
			if i > 0 {
				// 列表分隔符也是文案：中文是「、」，英文是「, 」——它是给人看
				// 的标点，不是格式串（`×` 那种符号留在外面）。
				fmt.Print(i18n.T(", ", nil))
			}
			fmt.Printf("%s×%d", kindLabel(k), accepted[k])
		}
		fmt.Println()
	}
	return 0
}

// ---- cli/extension 的注入点 ----

// Run 只用于满足接口形状：入口走的是 Handle（见上）。留一个明确的行为而不是
// panic——真有人按接口调它时，得到的结果与入口那条路一致。
func (s *service) Run(p entry.Process) int { return s.Handle(p) }

// errShell 给本壳的装配错误打上模块前缀。
//
// 前缀是**机器标记**（排查时 grep `simple-cli:` 就捞出这个壳说的话），所以留在
// 消息外面——与内核 cli 的 errCLI 同一条规矩（见 docs/13-i18n.md §7）。
func errShell(err error) error { return fmt.Errorf("simple-cli: %w", err) }

// RegisterCommand 记账并返回撤销句柄（与 modules/cli 同语义：谁注入谁撤销）。
func (s *service) RegisterCommand(c cliapi.Command) (modules.Release, error) {
	if c == nil {
		return nil, errShell(i18n.E("a command cannot be nil", nil))
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
		return nil, errShell(i18n.E("a status provider cannot be nil", nil))
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
	return s.accept(kindDiagnostics), nil
}
func (s *service) RegisterStatusBlocks(cliapi.BlockProvider) (modules.Release, error) {
	return s.accept(kindBlocks), nil
}
func (s *service) RegisterDump(cliapi.Dumper) (modules.Release, error) {
	return s.accept(kindDump), nil
}
func (s *service) RegisterGlossary(cliapi.Glossarist) (modules.Release, error) {
	return s.accept(kindGlossary), nil
}
func (s *service) RegisterVerbose(cliapi.Verbose) (modules.Release, error) {
	return s.accept(kindVerbose), nil
}

// 注入点的**身份**：ASCII、稳定，当记账表的键，也是排查时 grep 的锚点。
//
// 它们是身份不是文案——所以不再直接拿中文当键（见 CLAUDE.md「别拿渲染出来的文本
// 当判据」）：改一句译文不该动到计数表的键。给人看的那句话由 kindLabel 现算。
const (
	kindDiagnostics = "diagnostics"
	kindBlocks      = "status-blocks"
	kindDump        = "dump"
	kindGlossary    = "glossary"
	kindVerbose     = "verbose"
)

// kindLabel 把身份翻成给人看的那句话。
//
// 现算而不是注册时算好存下来：装语言发生在 Start 期间，而各模块的注入也发生在
// 各自的 Start 里——谁先谁后由依赖图决定，存下来就可能存到装语言之前的那一份
// （那正是「包级变量里不写 i18n.T」要躲的同一件事）。
func kindLabel(kind string) string {
	switch kind {
	case kindDiagnostics:
		return i18n.T("diagnostics", nil)
	case kindBlocks:
		return i18n.T("status blocks", nil)
	case kindDump:
		return i18n.T("dump material", nil)
	case kindGlossary:
		return i18n.T("glossary", nil)
	case kindVerbose:
		return i18n.T("verbose mode", nil)
	}
	return kind // 认不出的身份原样打出来：那是新增注入点忘了登记，看得见比看不见强
}

func (s *service) accept(kind string) modules.Release {
	s.mu.Lock()
	s.accepted[kind]++
	s.mu.Unlock()
	// `{kind}` 是那五类的名字：「诊断」这类词中文不分单复数，英文那几个
	// （diagnostics / status blocks / …）却是复数形状，所以句子避开冠词，
	// 也让译文的语序自由一点。
	fmt.Printf("simple-cli: %s\n", i18n.T("{kind} injection accepted (this shell does not render it; the counts are in status)",
		i18n.A{"kind": kindLabel(kind)}))
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
