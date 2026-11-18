// Package cli 是人和组件图之间的命令行适配层。
//
// CLI 自己只拥有参数分派、输出和进程退出码。具体模块通过 RegisterCommand /
// RegisterDiagnostics 贡献命令与诊断，因此增加 OMO 等功能不需要修改一个中央
// 命令注册表。Host 则反向限定扩展命令可以调用的 CLI 能力。
//
// service 在 Start 时取得 AgentCatalog 与 Runtime，在 Stop 时清空引用；扩展
// 贡献由 Registry 账本管，各自的 Release 归贡献者自己保管。main 最终只从 App
// 取出这一项 CLI capability 并调用 Run。
package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
	"github.com/rzbdz/newgate/lib/i18n"
)

// service 是**界面**：分派与排版。
//
// 它拥有下面那七本账（命令、诊断、状态行、状态块、诊断素材、术语、详细模式标记），
// 各模块在自己的 Start 里往上挂（它们对 ui 声明一条弱依赖 Optional(cli)，所以
// 一定排在界面之后——见 component.Optional）。账本长在界面上是**对的**——它是
// 「界面认得哪些东西」这件事本身；2026-09-18 曾把它下沉成一个独立的 modules/surface
// 模块，那是多余的（见 cli/extension 的包注释）。真正要解开的那个环不在账本的位置，
// 而在**依赖方向**：界面不许有出边。
type service struct {

	// 七本账：模块通过 RegisterXxx 把自己的东西挂进来，界面在分派命令、渲染
	// status / doctor / alllogs 时循环调用它们。**界面不 import 任何模块**，所以
	// 它不认识任何人——别人的东西是别人注入进来的回调。
	commands    modules.Registry[Command]
	diagnostics modules.Registry[DiagnosticProvider]
	statuses    modules.Registry[StatusProvider]
	blocks      modules.Registry[BlockProvider]
	dumps       modules.Registry[Dumper]
	glossary    modules.Registry[Glossarist]
	verboses    modules.Registry[Verbose]
}

var _ CLI = (*service)(nil)

// RegisterCommand 注入一条命令；命令名撞车当场报错。
//
// 报错文案是中文：这条是**面向插件作者**的业务冲突（「我的命令名被谁占了」），
// 不是框架级装配错误，跟用户可见的文案保持一致。查重在 Registry.Register 的写锁
// 内跑，所以并发注入同一个名字也只会有一个成功——先到先得是这个功能最不该有的
// 行为（那样「谁占了这个名字」在清单里看不出来）。
func (s *service) RegisterCommand(command Command) (modules.Release, error) {
	if command == nil {
		return nil, errCLI(i18n.E("a command cannot be nil", nil))
	}
	names := command.Names()
	if len(names) == 0 {
		return nil, errCLI(i18n.E("a command must declare at least one name (Names)", nil))
	}
	return s.commands.Register(command, func(existing []Command) error {
		for _, other := range existing {
			for _, have := range other.Names() {
				for _, want := range names {
					if have == want {
						return errCLI(i18n.E("command name {name} is already taken",
							i18n.A{"name": want}))
					}
				}
			}
		}
		return nil
	})
}

// errCLI 给装配期错误加上 `cli:` 前缀。
//
// 前缀是**机器标记**（组件名），按本地化的规矩留在消息外面：能翻译的是后面那句
// 陈述。用 %w 而不是字符串拼接，是为了让 i18n.ID 仍能从这条错误里取出消息身份
// ——测试断言的是身份（语言无关），不是渲染出来的那句话。
func errCLI(err error) error { return fmt.Errorf("cli: %w", err) }

// RegisterDiagnostics 注入一组 doctor 输出。诊断可叠加，不查重。
func (s *service) RegisterDiagnostics(provider DiagnosticProvider) (modules.Release, error) {
	if provider == nil {
		return nil, errCLI(i18n.E("a diagnostic provider cannot be nil", nil))
	}
	return s.diagnostics.Register(provider, nil)
}

// RegisterStatus 注入 `newgate status` 里的若干行。与诊断同理：可叠加、不查重。
//
// 这条端口的存在是为了**让界面不必认识模块**：`status` 要显示各模块自己的开关
// 状态，而那份状态归各自模块。谁的状态谁自己报——界面只负责循环调用与排版。
func (s *service) RegisterStatus(provider StatusProvider) (modules.Release, error) {
	if provider == nil {
		return nil, errCLI(i18n.E("a status provider cannot be nil", nil))
	}
	return s.statuses.Register(provider, nil)
}

// RegisterStatusBlocks 注入 `newgate status` 里的成块内容（表格等）。
func (s *service) RegisterStatusBlocks(provider BlockProvider) (modules.Release, error) {
	if provider == nil {
		return nil, errCLI(i18n.E("a status block provider cannot be nil", nil))
	}
	return s.blocks.Register(provider, nil)
}

// RegisterDump 注入诊断包里的原始素材。理由同 RegisterDiagnostics。
func (s *service) RegisterDump(dumper Dumper) (modules.Release, error) {
	if dumper == nil {
		return nil, errCLI(i18n.E("a dump provider cannot be nil", nil))
	}
	return s.dumps.Register(dumper, nil)
}

// RegisterGlossary 注入帮助屏术语表里属于自己的一行。理由同 RegisterDiagnostics。
func (s *service) RegisterGlossary(g Glossarist) (modules.Release, error) {
	if g == nil {
		return nil, errCLI(i18n.E("a glossary provider cannot be nil", nil))
	}
	return s.glossary.Register(g, nil)
}

// RegisterVerbose 上报「我的详细模式开着」。理由同 RegisterDiagnostics。
func (s *service) RegisterVerbose(v Verbose) (modules.Release, error) {
	if v == nil {
		return nil, errCLI(i18n.E("a verbose provider cannot be nil", nil))
	}
	return s.verboses.Register(v, nil)
}

// verboseOn 有任何一个模块说自己在详细模式吗。
func (s *service) verboseOn() bool {
	if s == nil {
		return false
	}
	for _, v := range s.verboses.All() {
		if v.Verbose() {
			return true
		}
	}
	return false
}

// Run 进入统一命令分派；模块命令从账本里现取（不是启动时拍快照）——注入方可能
// 比界面晚一步才注册，现取才不会漏。
//
// **argv0 的语法归界面**（`newgate-<preset>` ≡ `newgate --preset <preset>`，见
// docs/08-operations.md）：preset 名从 basename 第一个 `-` 之后取，不切分（preset
// 名可含 `-`）。「这次调用归不归界面管」才是 entry 的事，不在这里。
func (s *service) Run(p entry.Process) int {
	args := p.Args
	if strings.HasPrefix(p.Argv0, "newgate-") {
		args = append([]string{"--preset", strings.TrimPrefix(p.Argv0, "newgate-")}, args...)
	}
	return runCLI(s, args)
}

// Claims 是入口申报的判据。
//
// 界面**永远认领**（返回 true）：它是「谁都不要就是它」的那一个，所以申报时用
// entry.DefaultRank——排在所有有条件的入口之后被问。这也是为什么 argv0 分发不
// 在这里判断：`newgate` 与 `newgate-ds` 都归界面，判断属于界面内部的语法。
func (s *service) Claims(entry.Process) bool { return true }

// Handle 是入口调用的落点，等价于旧的「组合根拿到 CLI 再调 Run」。
func (s *service) Handle(p entry.Process) int { return s.Run(p) }

// Name 进日志与诊断（组合根那句 `[entry] resolve: …`）。
func (s *service) Name() string { return "cli" }

// moduleCommand 按名字找一条注入进来的命令。Names 里的每个别名都是分派键。
func (s *service) moduleCommand(name string) (Command, bool) {
	for _, command := range s.commands.All() {
		for _, candidate := range command.Names() {
			if candidate == name {
				return command, true
			}
		}
	}
	return nil, false
}

// statusLines 汇总所有模块注入的 status 行，按 Rank 排序（同 Rank 保持注册顺序）。
func (s *service) statusLines() []StatusLine {
	var out []StatusLine
	for _, provider := range s.statuses.All() {
		out = append(out, provider.Status()...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}

// glossaryLines 汇总模块贡献的术语行，按 Rank 排序。
func (s *service) glossaryLines() []GlossaryLine {
	var out []GlossaryLine
	for _, g := range s.glossary.All() {
		out = append(out, g.Glossary()...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}

// dumpSections 汇总诊断包的原始素材，按 Rank 排序。
func (s *service) dumpSections() []DumpSection {
	var out []DumpSection
	for _, dumper := range s.dumps.All() {
		out = append(out, dumper.Dump()...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}

// statusBlocks 汇总所有模块注入的状态块，按 Rank 排序。
func (s *service) statusBlocks() []StatusBlock {
	var out []StatusBlock
	for _, provider := range s.blocks.All() {
		out = append(out, provider.StatusBlocks()...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}

// moduleDiagnostics 汇总所有模块注入的体检项，按 Rank 排序（同 Rank 保持注册顺序）。
func (s *service) moduleDiagnostics() []Diagnostic {
	var out []Diagnostic
	for _, provider := range s.diagnostics.All() {
		out = append(out, provider.Diagnostics()...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}

// New 声明最终 CLI 入口，并向其他模块开放那七本账。
//
// # 停止顺序：由依赖图给出，注入者先停、界面后停
//
// 依赖方向是 **扩展模块 → ui**，而且是一条**排序边**（Optional(cli)，见
// component.Optional）。所以拓扑序把 cli 排在所有注入者前面，逆序停止时
// 注入者的 Stop 一定先跑完——它们撤掉注册时界面还在。2026-09-18 实测（全图 17 个
// 组件）：
//
//	start  [cli(1), breaker(2), config(3), …]        cli 第 1 个起
//	stop   [wrapper(17), …, breaker(2), cli(1)]      注入者全停完，cli 最后停
//
// 这里曾经写过「停止顺序没有保证」——那是 Inject（不参与排序）时代的实情，
// 当时实测 breaker 在 cli **之后**停。重新走排序边之后这条保证回来了：**界面
// 的 Stop 不必再假设有人会晚到**。
//
// 但 Stop 仍然保持空实现，理由是另一条、与顺序无关的：界面的七本账里装的都是
// 别人的回调，撤它们是各自模块 Stop 的事（各自留着自己的 Release）。界面自己
// 没有要释放的资源——它不是一个 server。
// 本模块是入口申报者之一（默认入口），编译期把两个接口都钉住。
var (
	_ CLI           = (*service)(nil)
	_ entry.Handler = (*service)(nil)
)

func New() modules.Component {
	service := &service{}
	var self modules.Release
	// 界面自己的命令也走同一个账本（见 commands.go）：查重、分派、help 组装
	// 只有一条路，run() 于是只剩编排。
	if err := service.registerOwnCommands(); err != nil {
		panic("cli: " + i18n.T("own command registration failed (assembly error): {err}",
			i18n.A{"err": err.Error()}))
	}
	return modules.Component{
		Name: "cli",
		Desc: func() string {
			return i18n.T("the terminal interface: dispatch, layout and injection points, with no business knowledge", nil)
		},
		Type: "cli",
		// **没有 Requires**：界面不依赖任何模块（见 app/default_test.go 的
		// TestCLIDependenciesOnlyShrink，它断言这条边集为空）。
		//
		// 它的全部能力都来自注入：命令、status 行、体检项、术语、诊断素材，七本账
		// 在 Start 时建好，各模块在自己的 Start 里往上挂（它们 Optional(cli)，所以
		// 一定排在界面之后）。一条出边都不留是有意的——留一条「反正用得到」的边，
		// 对方的注入就会成环，那正是这一轮之前 config/runtime/config-hook 的命令
		// 被迫留在界面里的原因。
		//
		// 它仍然 Provides：进程组合根靠它把这次调用交给界面。
		Provides: []modules.Provision{
			modules.Provide(Capability, CLI(service)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			// 申报自己为**默认入口**：组装这一次调用的是 root 的账本（built-in），
			// 组合根在 main 里只问账本「归谁」——它不认识本模块，删掉本模块它也
			// 照样编译（那时没有任何入口认领，进程说一句人话退出）。
			//
			// 这里刻意用 Get 而不是 MustGet：MustGet 的契约是「这个端口已经由 Need
			// 保证存在」，而界面**一条出边都不许有**（上面的 Requires 注释）。
			// 账本缺席只有一种情况——这张图里没有 root（root 是唯一 built-in，
			// 正常构建里它一定在，见 app/matrix_test.go）。那时按 fail-open 处理：
			// 界面本身照常可用，只是没有任何入口认领这次调用；那是**进程级**的事实，
			// 由 main 说一句人话退出，不该变成某个模块的启动错误。
			//
			// 硬依赖由 wrapper 那一侧声明（它是业务模块，没有 ui 那条限制）。
			registry, ok := modules.Get(ctx, entry.Capability)
			if !ok {
				return nil
			}
			release, err := registry.Register(service, entry.DefaultRank)
			if err != nil {
				return err
			}
			self = release
			return nil
		},
		Stop: func(context.Context) error {
			if self == nil {
				return nil
			}
			return self()
		},
	}
}
