// Package extension 是模块向界面注入东西时实现的那组接口，以及界面自己的端口身份。
//
// # 为什么契约要下沉成叶子包
//
// 界面（modules/cli）是个重包：它 import runtime（daemon/takeover/injection）、
// gateway（forward/special/probe/…）和 breaker，因为它负责进程生命周期、数据面
// 装配和一堆渲染。模块若为了拿到 `Command` 接口去 import 它，就等于把这些全拖进
// 自己的依赖里，于是：
//
//   - 谁 import 了那个模块，就再也无法被 runtime / gateway 的**测试**引用
//     —— 那两处的测试要造客户端描述符，一 import 就成环（实测：
//     `runtime/launch` 与 `runtime/takeover` 的测试当场编译不过）；
//   - gateway 注册不了自己的命令（`newgate st` 只能被迫写在界面里）。
//
// 契约下沉之后，模块只 import 这个叶子包 + component + config，箭头是
// `modules/* → cli/extension → config`，环就没有了。这与 gateway/api.go 把插件
// 契约下沉到 gateway/special 是**同一条规矩**（docs/03-architecture.md §3）。
//
// # 账本在哪
//
// **账本（三本 Registry）在 modules/cli 自己的 service 上**，由它 Provide 出
// Capability。模块拿到的是一组注册回调，界面在分派命令、渲染 status / doctor 时
// 循环调用它们拿数据——界面**不 import 任何模块**，也不认识任何人。
//
// （2026-09-18 曾把账本挪进一个独立的 modules/surface 模块，那是多余的：它唯一
// 买到的是「界面可以去依赖模块」的自由，而界面的目标恰恰是**不依赖任何人**。
// 折回之后少一个模块、少一层概念，与「界面只提供注入点」的模型一致。）
//
// # 边界
//
// 本包**不 import 任何 config 包**，只剩 component 一件：契约类型（命令、状态行、
// 诊断、术语、诊断素材）全是纯数据，没有一个字段是企业配置语义。
//
// 2026-09-18 之前这里 import 过 config/resolve（`Host.PrintChain` 收 []Step），
// 而 config 根包要 import 本包（模块注入时用这里的类型），两者互引就是环——那次是
// 靠「引 resolve 这个叶子」绕开的。后来 PrintChain 从 Host 上摘掉（模块直接调
// config 的渲染器），这条 import 也就不需要了。
package extension

import (
	"strings"

	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Diagnostic 是模块交给 CLI 展示的一组结构化状态，
// 让模块保留诊断知识，而 CLI 只负责统一编排和输出。
type Diagnostic struct {
	// Rank 决定行序（小的在前）。与 StatusLine.Rank 同理：体检项的先后是版面
	// 判断，由贡献者声明，界面不写死任何一项。
	Rank    int
	Label   string
	State   string
	Line    string
	Details []string
}

// DiagnosticProvider 允许任意模块追加 doctor 信息，不需要 CLI 反向导入该模块。
type DiagnosticProvider interface {
	Diagnostics() []Diagnostic
}

// StatusLine 是模块贡献给 `newgate status` 的一行（字段名 + 值）。
//
// 与 Diagnostic 分开是因为两者的**时机**不同：doctor 是人主动敲的体检，可以
// 展开细节；status 是每次都会看一眼的概览，只该占一行。
type StatusLine struct {
	// Rank 决定**行序**（小的在前）。同 Rank 保持注册顺序，稳定。
	//
	// 「代理排第一行」这类判断是**版面**判断，所以由贡献者声明而不是界面写死
	// ——界面一旦写死「哪一行排第一」，那一行就成了界面认识某个模块的证据。
	Rank  int
	Label string
	Value string
}

// StatusBlock 是一段**已经排好版**的状态输出：可选节标题 + 若干行。
//
// 为什么允许预渲染（而不是只报 label/value）：`status` 里有两块是表格（档位绑定、
// fallback 链），它们的排版与数据是一体的 know-how——让界面重排就得把 resolve
// 的知识搬回界面，那正是这一轮要拆掉的东西。所以块的**内容与排版**都由 owner 给，
// 界面只决定**位置**（Rank）。
//
// 它是可选端口：多数模块只需要 StatusLine。
type StatusBlock struct {
	Rank int
	// Title 空 = 不打印节标题（用于接在上一段后面的续行）。
	Title string
	// Lines 已排好版，逐行原样输出。
	Lines []string
}

// BlockProvider 允许模块给 `newgate status` 贡献成块的内容，理由见 StatusBlock。
type BlockProvider interface {
	StatusBlocks() []StatusBlock
}

// StatusProvider 允许模块给 `newgate status` 贡献一行，理由同 DiagnosticProvider：
// 模块保留自己的知识，CLI 只负责排版。
//
// 为什么需要它（而不是让 CLI 直接去读某个模块）：`status` 要显示「哪些开关点是
// 非出厂态」，而那份账本归 plugin-manager。CLI 若为了这一行去读它，那一行就会
// 把两个模块焊在一起——「谁的状态谁自己报」是解开这种耦合的唯一办法。
type StatusProvider interface {
	// Status 由**贡献者自己**装所需的配置快照（store.LoadState），界面不传。
	//
	// 为什么不把 state 传进来（2026-09-18）：那会让界面为了转手一份 state 而
	// import config/domain + config/store——「界面认识配置的内部结构」正是这一轮
	// 要拆掉的东西。谁的知识谁自己取，界面只负责循环与排版。
	Status() []StatusLine
}

// Host 是扩展命令可使用的最小 CLI 能力集合。
// 它避免 Command 获得整个 CLI 内部，并明确哪些交互仍由主 CLI 统一控制。
//
// 加一个方法要慎重：它是**所有**模块命令都能看到的面积。判断标准是「这件事
// 只有 CLI 做得成」——而不是「这样我就不用把逻辑搬过去了」。
type Host interface {
	Die(code int, message string) int

	// Verb 是**分派器这次按哪个名字找到我**。
	//
	// 为什么需要它（2026-09-18 实测补上）：`Command.Run` 收到的 args 里没有动词
	// （见 Command 的契约），于是「一条命令服务多个动词」就丢了一个信息。上一版
	// 的绕法是给每个动词注册一个实例、各自记着自己的名字——那条路要求**注册期
	// 就知道全部动词**，而动词是运行期长出来的（客户端模块在自己的 Start 里注册
	// agent），于是它要么赌装配顺序、要么在 Start 里读别人的注册表。两样都不该
	// 出现在 Start 里（见 docs/02-component-framework.md 的三段法则）。
	//
	// 把「你这次是按哪个名字被找到的」交回来，命令就可以**现查**它的动词表，
	// 注册期只交一条命令。宿主没有分派概念时（测试桩、直接调用）返回空串。
	Verb() string
	// LiveRouting 也**不在 Host 上**（2026-09-18 移走）：它给的是守护进程自报的
	// 熔断表快照，那是数据面/控制面的数据（modules/gateway/controlplane.Doc 的
	// Available / Rank），不是「只有界面做得成」的事。谁要谁直接读那个叶子。
	NotifyProxy()

	// DaemonRunning 守护进程现在在跑吗（读 pidfile）。
	//
	// 只有 CLI 做得成这件事：它拥有进程生命周期（起停、优雅交接、pidfile
	// 的读写时机）。命令想问的其实是「我这一改有人立刻读吗」——不在跑就只是
	// 落了个盘，下次起来才生效，跟用户说清楚比让他以为已经生效强。
	DaemonRunning() bool

	// 链与跳过的排版**不在 Host 上**（2026-09-18 移走）：那是配置的知识，谁要
	// 谁去调 modules/config 的 PrintChain / PrintSkips。借界面转发一圈只会让界面
	// import 配置——而界面不该认识任何模块。

	// PrintThinkCache **不在 Host 上**（2026-09-18 移走，与 LiveRouting 同一条
	// 理由）：它是 gateway 的 thinkcache 计数与排版，谁要谁自己去
	// modules/gateway/thinkcache_status.go 渲染。它曾经在 Host 上，于是这个接口
	// 里出现了一条**以某个模块命名**的方法——界面因此「认识」了那个模块。
}

// Command 是模块向 CLI 贡献的命令端口；Names 声明路由名，Run 执行命令语义。
//
// **args 里没有命令名**：`newgate plugin deepseek off` 分派到 `plugin` 这条命令时，
// Run 收到的是 `["deepseek", "off"]`——动词已被分派器剥掉。这条是 2026-09-18 用
// 真实二进制跑出来的：契约没写它，于是照 `os.Args` 的直觉按 1 起下标写，整条命令
// 错位一格（`plugin <模块>` 打成了列表、`plugin <路径> off 90s` 报「不认识的动词
// 90s」）。下标基准这种事必须写在契约上，不能靠猜。
//
// Names 里的多个名字是**同一个命令**的别名，分派到哪一个都收到同样形状的 args。
type Command interface {
	Names() []string
	Run(Host, []string) int
}

// Command 是模块注册进来的命令。args 是**去掉命令名之后**的原始参数（含选项），
// 位置参数用 Positional 取、开关用 Flag / FlagValue 取——别自己数下标。

// Section 是帮助屏上的一个**槽位名**。
//
// 它就是一个字符串，靠**约定**生效，不是闭集（2026-09-18 定）：
//
//   - 界面自己发布一批**通用槽位键**（下面那组常量）：它们是「逻辑上想得通」的
//     大类（接管 / 路由 / 观测 / 维护 …），谁都可以选用；
//   - 通用槽位之外，模块可以**自己起一个名字**——先到先得，最多
//     MaxCustomSections 个，按首次声明的顺序排在通用槽位之后；
//   - 抢不到名额（或者根本没起名字）的一律进 SectionOthers。
//
// 为什么不做成闭集枚举：分类是约定，会随装机长出新成员，硬拒绝的代价是「一个新
// 模块因为用了个新节名，整个 --help 就崩了」——而它只是想被列出来。为什么又要设
// 上限：槽位多了 help 就退化成一堆一行的节，等于没分组。这两条与 pluginmanager
// 的 Type 词汇表是同一个设计（顺序表 + others 兜底）。
//
// 上一版界面反过来维护一张「哪几节是我自己的」的名字清单（coreSection 里那个
// switch）——**界面因为别人的节名而需要被修改**，那正是这一轮要拆掉的东西。
//
// # 键是身份，不是文案（2026-09-20 本地化）
//
// 槽位名过去直接写中文（`SectionTakeover = "接管"`），于是它同时充当了两个角色：
// 分组的**键**（plan.slot 的判据）与屏幕上的**标题**。加了 i18n 之后这两个角色
// 必须分开——键是身份，不该随语言变（变了之后「同一节的中文名与英文名」就是两个
// 不同的槽位，自定义名额、排序、others 兜底全会错位），标题才是文案。
//
// 所以键一律是 ASCII 稳定标识（`"takeover"`），给人看的名字走 Display() 查目录。
// 自定义槽位是模块自己起的键，没有目录条目，Display() 原样返回它本身。
type Section string

// 通用槽位键。界面发布它们，模块选用；顺序即显示顺序（见 PlanSections）。
//
// 取值是**稳定标识**，不是文案：改这里的字面量等于改协议（自定义名额与排序都按
// 键比对），所以它们不随语言变、也不该被翻译。
const (
	SectionTakeover    Section = "takeover"
	SectionRunOnce     Section = "once"
	SectionRouting     Section = "routing"
	SectionObserve     Section = "observe"
	SectionMaintenance Section = "maintenance"
	SectionModules     Section = "modules"
	SectionUI          Section = "ui"

	// SectionOthers 是**兜底槽位**：抢不到名额的、没起名字的都落这里。
	SectionOthers Section = "others"
)

// Display 是这个槽位**给人看**的名字（键是身份，显示名随语言变）。
//
// 查不到目录（源语言、或这门语言还没翻）时 i18n.T 返回的就是这里写的那句英文，
// 所以默认路径不依赖任何数据文件。自定义槽位落进 default：它的键是模块自己起的，
// 界面不认识它，也就没有对应的显示名——原样显示那个键是**正确的降级**，不是错误
// （它与 PlanSections 把陌生节名收进 others 是同一条 fail-open）。
func (s Section) Display() string {
	switch s {
	case SectionTakeover:
		return i18n.T("Takeover", nil)
	case SectionRunOnce:
		return i18n.T("One-shot (keeps no global state)", nil)
	case SectionRouting:
		return i18n.T("Routing and configuration", nil)
	case SectionObserve:
		return i18n.T("Probing and observation", nil)
	case SectionMaintenance:
		return i18n.T("Maintenance", nil)
	case SectionModules:
		return i18n.T("Modules", nil)
	case SectionUI:
		return i18n.T("Interface", nil)
	case SectionOthers:
		return i18n.T("Other", nil)
	}
	return string(s) // 自定义槽位：模块自己起的键，显示它本身
}

// generalSections 是界面发布的通用槽位，顺序即显示顺序。SectionOthers 永远最后。
var generalSections = []Section{
	SectionTakeover, SectionRunOnce, SectionRouting,
	SectionObserve, SectionMaintenance, SectionModules, SectionUI,
}

// MaxCustomSections 是通用槽位之外还能容纳的**自定义节**个数。
//
// 有上限是因为节多了 help 就等于没分组；先到先得是因为「谁先声明谁占坑」是唯一
// 不需要界面做价值判断的规则。装到一个新模块抢不到名额时，它落进「其它」照样
// 列得出来，只是不再独占一节。
const MaxCustomSections = 4

// SectionPlan 是一批声明过的节名到**实际显示的节**的映射，以及显示顺序。
//
// 它是一次装配的产物（声明顺序 = 命令注册顺序），所以由界面在渲染时现算。
type SectionPlan struct {
	order []Section
	slot  map[Section]Section
}

// PlanSections 按上面的规则给每个声明过的节名定一个落点。
//
// declared 必须是**稳定的顺序**（命令注册顺序），因为自定义节是先到先得——顺序
// 不定的话同一套装机会渲染出不同的 help，那种不确定性最难查。
func PlanSections(declared []Section) SectionPlan {
	plan := SectionPlan{slot: map[Section]Section{}}
	var customs []Section
	for _, name := range declared {
		if _, done := plan.slot[name]; done {
			continue
		}
		switch {
		case name == "":
			// 没声明 = 没有位置主张，直接进兜底。
			plan.slot[name] = SectionOthers
		case isGeneralSection(name):
			plan.slot[name] = name
		case len(customs) < MaxCustomSections:
			customs = append(customs, name)
			plan.slot[name] = name
		default:
			plan.slot[name] = SectionOthers
		}
	}
	plan.order = append(plan.order, generalSections...)
	plan.order = append(plan.order, customs...)
	plan.order = append(plan.order, SectionOthers)
	return plan
}

// Slot 这个声明实际显示在哪一节。
func (p SectionPlan) Slot(declared Section) Section {
	if s, ok := p.slot[declared]; ok {
		return s
	}
	return SectionOthers
}

// Order 按显示顺序返回所有会出现（或可能为空）的节。
func (p SectionPlan) Order() []Section { return append([]Section(nil), p.order...) }

func isGeneralSection(name Section) bool {
	for _, known := range generalSections {
		if name == known {
			return true
		}
	}
	return false
}

// FlagValue 取 `--名字 值` 或 `--名字=值` 里的值；names 是同一选项的别名，都没有
// 就给空串。与 Arg 同属 argv 解析的公共部分：每个模块各写一遍必然有一遍写错。
func FlagValue(args []string, names ...string) string {
	for i, a := range args {
		for _, name := range names {
			if a == name && i+1 < len(args) {
				return args[i+1]
			}
			if strings.HasPrefix(a, name+"=") {
				return strings.TrimPrefix(a, name+"=")
			}
		}
	}
	return ""
}

// Positional 取第 i 个**位置参数**（跳过 `-` 开头的），越界给空串。
//
// **必须是这个函数、不能数下标**（2026-09-18 实测）：契约包里曾经还有一个纯下标
// 访问器 `Arg`，`newgate start --force` 于是把 `--force` 当成了 agent 名，报
// 「不认识的 agent "--force"」——**而 doctor 自己就在推荐这条命令**；同一类现场
// 还有 `newgate probe --json`（`--json` 被当成 profile 名）。两个名字、两种语义
// 摆在一起，下一个人必然踩中错的那个，所以 `Arg` 已经删掉，只剩这一个。
//
// 位置参数不可能是 `-` 开头的：agent 名、profile 名、子命令全在这个集合里。
// `--` 之后一律算位置参数（要给客户端透传以 `-` 开头的参数时用它），`--` 自己
// 不算一个位置；单独一个 `-` 仍算位置参数（按惯例表示 stdin）。
func Positional(args []string, i int) string {
	var seen int
	literal := false
	for _, a := range args {
		if !literal {
			if a == "--" {
				literal = true
				continue
			}
			if len(a) > 1 && a[0] == '-' {
				continue
			}
		}
		if seen == i {
			return a
		}
		seen++
	}
	return ""
}

// Flag 报告命令行里有没有这个选项（`--` 之后不再有选项）。
//
// 只做**精确**匹配：`--profile=ds` 不算 `--profile`，那种带值的用 FlagValue。
// 它替掉的是散在 config / gateway / runtime 里三份一模一样的 `hasFlag`——同一段
// 三行代码抄三遍，改动时漏掉一份就是「这条命令的开关在这一层不生效」。
func Flag(args []string, name string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == name {
			return true
		}
	}
	return false
}

// HelpLine 是命令在 `newgate --help` 里占的那一行。
//
// **节与位置都由命令自己声明，界面不认识任何一条命令**。上一版是反过来的：界面
// 里写死一张 cmd(...) 清单，于是每加一个模块就要回来改界面——命令搬回模块之后
// 界面还在硬编码它们，"搬了"就等于白搬。
type HelpLine struct {
	// Section 归到哪个槽位（见上面的槽位表）。挑不出来就留空，会落进
	// SectionOthers——那是**正确的降级**，不是错误。
	Section Section
	// Rank 决定**节内**位置：小的在前，同 Rank 按 Usage 稳定排序。节的先后由
	// SectionOrder 决定，不受 Rank 影响——否则一个模块调大自己的 Rank 就能把
	// 整节搬走，槽位的意义就没了。
	//
	// 为什么用整数而不是枚举：节的集合随装机变化，枚举必然要改框架。留出空档
	// （10/20/30…）就是留给别人插队的余量。
	Rank int
	// Usage 左列，命令写法。
	Usage string
	// Summary 右列，一句话结论。
	Summary string
}

// Documented 是 Command 的可选搭档：命令自己声明它在 --help 里长什么样。
//
// 为什么必须有它：命令从 CLI 的 switch 搬回各模块之后，`newgate --help` 不能再
// 硬编码那些行——**硬编码就等于「命令搬了、CLI 还认识它」，白搬**。help 是
// 「CLI 认识哪些模块」的另一个面，两个面得一起搬。
//
// 没实现它的命令仍然可用，只是不在 help 里占一行。那不是一个好状态（用户看不见
// 的命令约等于不存在），所以新命令都该实现它——但它是可选接口，因为它是**呈现**
// 关注点，不该挡住一条命令先能跑起来。
type Documented interface {
	Help() HelpLine
}

// CLI 既是进程入口最终调用的分派器，也是模块注入自己那一份东西的端口。
//
// 两件事共用一个接口是有意的：它们都是「界面这件事」的两面——入口把一次命令行
// 调用交给它（Run），模块把自己的一部分挂到它上面（RegisterXxx）。分成两个端口
// 只会让每个模块都要 Need 两次、而它们永远是同一个组件提供的。
//
// **它不再由组合根点名调用**（2026-09-20）：界面在自己的 Start 里往 root 的
// 入口账本申报一个 Handler（rank = entry.DefaultRank，即「谁都没认领就是我」），
// 组合根只问账本「这次调用归谁」。于是换界面（甚至换成一个 web-daemon）在组合根
// 上是零改动，`app` 里也再没有一行 import 本包。
//
// 为什么注入必须是 Register 而不是让模块 Provide 一个「命令端口」：后者没有生命
// 周期。模块认领一个命令名之后没人能撤销它，也没人查重——两个模块认领同一个名字
// 是静默先到先得（2026-09-17 实测）。走 Register 则每次注入都拿到一个 Release，
// 由注入方自己在 Stop 时释放。
//
// **读侧不在这里**：命令账本、诊断、状态行都归界面自己，它直接读自己的 service，
// 不需要经过接口。
type CLI interface {
	// Run 执行一次命令行调用。p 是**这次进程调用**的全部事实（argv0 / args /
	// env / 版本），由组合根装好递进来——界面自己去读 os.Args 就又把「进程是
	// 怎么被调起来的」抄了一遍，而那正是 entry 包存在的理由。
	//
	// argv0 分发（`newgate-<preset>` ≡ `newgate --preset <preset>`）在界面内部完成，
	// 因为那是**命令行语法**，属于界面；「这次调用归不归界面管」才是 entry 的事。
	Run(p entry.Process) int

	// RegisterCommand 注入一条命令。命令名（Names）是查重的逻辑键，撞名当场
	// 报错而不是先到先得。
	RegisterCommand(Command) (modules.Release, error)
	// RegisterDiagnostics 注入一组 doctor 输出。诊断可叠加，没有键命名空间，
	// 因此不查重，只记 token。
	RegisterDiagnostics(DiagnosticProvider) (modules.Release, error)
	// RegisterStatus 注入 `newgate status` 里的若干行，理由同 RegisterDiagnostics。
	RegisterStatus(StatusProvider) (modules.Release, error)
	// RegisterStatusBlocks 注入 `newgate status` 里的成块内容（表格等），
	// 理由见 StatusBlock。
	RegisterStatusBlocks(BlockProvider) (modules.Release, error)
	// RegisterDump 注入诊断包（`newgate alllogs`）里的原始素材，理由见 Dumper。
	RegisterDump(Dumper) (modules.Release, error)
	// RegisterGlossary 注入帮助屏术语表里属于自己的一行，理由见 Glossarist。
	RegisterGlossary(Glossarist) (modules.Release, error)
	// RegisterVerbose 上报「我的详细模式开着」，理由见 Verbose。
	RegisterVerbose(Verbose) (modules.Release, error)
}

// Verbose 报告「现在处于需要看细节的模式」——某个模块开了自己的详细开关。
//
// 界面拿它做一个判断：要不要对自己的输出做版式自检（见 modules/cli/layout_audit）。
// 谁的模式谁自己报——界面原来直接读 gateway 的 state 段问「debug 开了吗」，
// 那是界面认识别人的配置结构。
type Verbose interface {
	Verbose() bool
}

// GlossaryLine 是帮助屏末尾术语表里的一行（词 + 一句话解释）。
type GlossaryLine struct {
	Rank int
	Term string
	// Definition 一句话解释；里面带的数据（agent 名、槽位键）由贡献者自己取。
	Definition string
}

// Glossarist 让模块贡献术语表里属于自己的一行。
//
// **为什么需要它**（2026-09-18）：术语表里有两行是别人的词——「agent」是
// config-hook 的（哪些客户端被接管），「槽位键」是 config 的（模块贡献的动态角色）。
// 界面为了它们得认识 AgentCatalog 与 domain.ExtraRoles，那正是「界面认识模块」
// 的又一个面。现在各交各的，界面只排版。
//
// 界面自己那几行（profile 这类**纯界面词汇**）仍然写在界面里：它们不归任何模块。
type Glossarist interface {
	Glossary() []GlossaryLine
}

// DumpSection 是诊断包里的一段**原始素材**：标题 + 原文行。
//
// 它和 Diagnostic / StatusLine 的分工：那两者是**给人扫的一句话结论**，这一段是
// 「出了事要拿出去的原文」（配置、日志、被改写的文件）。诊断包（`newgate alllogs`）
// 就是把这些原文按顺序拼起来。
type DumpSection struct {
	Rank  int
	Title string
	// Lines 原文，逐行原样输出（不做版式处理——它可能要贴进 issue）。
	Lines []string
}

// Dumper 让模块把自己的原始素材交进诊断包。
//
// **为什么需要它**（2026-09-18）：诊断包原来整个长在界面里，于是界面为了拼出
// 它得知道 providers.json 的结构、profile 的档位表、接管改了哪些文件、日志在哪、
// 证据文件叫什么。全是各模块自己的知识。现在各交各的原文，界面只负责排序与拼接
// ——与 status / doctor 是同一条规矩的第三次应用。
//
// 它是可选端口：没有原始素材要交的模块不实现。
type Dumper interface {
	Dump() []DumpSection
}

// Handoff 标记这条命令会把控制权交给**别的进程**（包装启动一个客户端）。
//
// 界面拿它做一个判断：这类命令的输出来自那个进程，不该按 newgate 的版式去审计
// 对齐。以前那个判断是界面自己 `detectLaunch` 猜出来的（「第一个 token 是不是
// 已知 agent」）——那需要界面认识 agent 表；现在由命令自己声明，界面只看标记。
type Handoff interface {
	// HandsOff 这条命令是不是把控制权交给别的进程。
	HandsOff()
}

// Unstyled 报告这条命令**这一次调用**的输出不是 newgate 的版式：原始配置文本
// （`profile kv`）、逐字节日志（`logs`）、全屏界面（`tui`）、JSON（`probe --json`）。
// 版式审计跳过它。
//
// **为什么由命令声明而不是界面列名单**（2026-09-18）：界面原来硬编码一张
// `switch args[0]` 的名字表（"__serve", "logs", "alllogs", "tui", "run", "probe",
// "profile"）——那正是「界面认识别人的命令」的又一个面，每加一个命令都要回来改
// 界面。现在命令自己说「我这次不是版式」，界面只问一句。
//
// 它是**动态**的（收 args）：同一条命令可能只有某种用法不排版（`probe --json`，
// 或 `profile kv` 这个子命令）。
type Unstyled interface {
	Unstyled(args []string) bool
}

// **BuildInfo 已删（2026-09-20）**：链接期版本信息现在是**进程事实**的一部分
// （entry.Process 的 Version/BuildTime/CommitTime），由组合根装一次、经
// `entry.MakeProcess` 落进 buildinfo 叶子，界面与别的模块一样**读**它。
//
// 为什么不再从 Run 的参数hand 递：那个参数是「这次命令行调用」的形状，而版本号
// 是「这个二进制」的事实——两者混在一个类型里，等于让「换一个界面实现」也要重新
// 定义版本怎么传。删掉之后 Run 收的就是纯粹的调用现场（见 CLI.Run）。

// Capability 是界面的端口身份。模块用它注入自己的命令与状态行；进程组合根用它
// 把这次调用交给界面。
var Capability = modules.NewCapability[CLI]("cli")
