// Package pluginmanager 是「系统里有哪几块、每块能开能关什么」的账本。
//
// 它是一个**普通模块**（everything is module）：没有内核钩子、没有白名单、没有
// init() 特例，靠 tools/genmodules 扫目录进图，和其他 15 个模块同级。它的特殊性
// 只体现在「大部分模块选择依赖它」——想参与运行期开关的模块 Need 这个 capability
// 并在自己的 Start 里 RegisterSelf；不想参与的**不写就是合法状态**，图不会因此
// 报缺端口。这与 gateway.RegisterRequestHook 的自愿参与完全同构。
//
// 两类事实分得很清楚，别混：
//
//   - **静态分类**（Type）跟着组件定义走，在 component.Component 上，图一装配就
//     能枚举——不需要任何模块配合、不需要启动。`newgate plugin` 的分组用它。
//   - **运行期开关点**（Switch）静态不了：这些 Path 背后是活着的服务依赖
//     （deepseek 的要引用 thinking/breaker，claudecode 的要引用 thinkingapi.Service），
//     只能等模块 Start 时上报。
//
// **依赖方向只有一个**：modules/* → pluginmanager → component。本包不 import 任何
// 业务模块（尤其不 import cli/gateway），所以谁都 import 得起它；反过来 component
// 里没有一行知道本包存在。
//
// 热路径只用一个纯函数（query.go 的 Off/On），不经过本包的注册表——见那里的注释。
package pluginmanager

import (
	"time"

	modules "github.com/rzbdz/newgate/component"
)

// Type 是模块的分类标签，用于 `newgate plugin` 的分组展示。
//
// 它是**产品概念**，所以定义在这里而不是 component：内核只要求「每个组件都得有
// 个分类键」（Type 非空），至于有哪些分类是 newgate 的事。这条与「模块的键不
// hard-code 进 core」是同一条规矩——core 提供表，产品填内容。
type Type = modules.Type

const (
	TypeInfra   Type = "infra"   // 地基：config / confighook / breaker / plugin-manager
	TypeGateway Type = "gateway" // 数据面：gateway
	TypeCLI     Type = "cli"     // 命令行：cli
	TypeClient  Type = "client"  // 客户端家族：claudecode / opencode / opencodeomo / runtime / wrapper
	TypeModel   Type = "model"   // 模型家族：deepseek / glm / thinking
	TypeBridge  Type = "bridge"  // 客户端 × 模型 的交叉语义：claudecode_deepseek / claudecode_glm

	// TypeOthers 是所有**不在上表里的**分类的落点。
	TypeOthers Type = "others"
)

// DisplayOrder 是 `newgate plugin` 的分组顺序。
//
// 它是一张**顺序表，不是白名单**：不在表里的 Type 一律归入 TypeOthers 分组展示，
// 不报错、不拒绝。为什么不做成闭集校验——分类是产品概念，会随版本长出新成员，
// 而硬拒绝的代价是「一个新模块因为用了个新分类词，整个 newgate plugin 就崩了」，
// 可它其实只是想被分到「其它」里。所以约定俗成 + 留余量：常用的进表（拿到自己的
// 分组和顺序），少见的一律落 others；某个分类真变多了，再往表里加一行。
func DisplayOrder() []Type {
	return []Type{TypeInfra, TypeGateway, TypeCLI, TypeClient, TypeModel, TypeBridge, TypeOthers}
}

// Danger 是开关点的危险级别。它决定 CLI 的默认时限与告警颜色，**不是**装饰。
type Danger string

const (
	// DangerSafe 关掉只是少个优化，随时可关。
	DangerSafe Danger = "safe"
	// DangerQuirk 关掉会让某家上游开始报错——它本来就是来修那家上游的怪癖的。
	DangerQuirk Danger = "quirk"
	// DangerFootgun 关掉会破坏正确性或可观测性，**必须限时**（见 Switch.TTL）。
	DangerFootgun Danger = "footgun"
)

// Switch 是一个运行期开关点，由模块在自己的 Start 里上报。
//
// Path 必须以所属模块名为前缀（`<模块名>.<路径>`），全局唯一。这条前缀规则让
// 「这个开关属于谁」不需要额外查表就能看出来，也让 RegisterSelf 自己就能验证。
type Switch struct {
	Path  string // "<模块名>.<路径>"，如 "deepseek.tail-shape"
	Title string // 人话
	Why   string // 关掉会发生什么

	Danger Danger

	// Default 是出厂态。true = 出厂开着，用户能关（kill switch 语义，配 Off() 读）；
	// false = 出厂关着，用户要显式开（mode 语义，配 On() 读）。
	Default bool

	// TTL 是 CLI 打开/关闭它时的默认时限；0 = 不限时。DangerFootgun 必须 > 0，
	// 注册时强制——「不给无限期的 footgun」是结构性保证，不是纪律。
	TTL time.Duration
}

// Module 是 `newgate plugin` 渲染一行所需的事实：静态分类 + 该模块上报的开关点。
// Type 来自组件图，Switches 来自注册表——两者合并的地方就是这里。
type Module struct {
	Name string
	Type Type
	// Desc 是模块自己写的一句话（见 component.Component.Desc）；空 = 它没写。
	// 这里存的是**求过值**的字符串：本结构是给界面用的快照，而快照该在生成它的
	// 那一刻把语言定下来（与 view.Concept.Title 同一条）。
	Desc     string
	Switches []Switch
}

// Manager 是 plugin-manager 公开的控制面。
//
// RegisterSelf 是**自愿**的：模块想支持运行期开关就调它，不想就不调。不调不影响
// 装配，也不影响它出现在 `newgate plugin` 里（列表以组件图为枚举源）——那种模块
// 显示为「无法 runtime 开关（v1）」。这就是 v1 的已知局限，明说而不是留空。
type Manager interface {
	// RegisterSelf 一个模块上报自己的运行期开关点。name 必须与 Component.Name
	// 一致；switches 可以为空（= 这个模块没有可开关的点，但仍登记自己在册）。
	//
	// 重名模块、跨模块的 Path 撞车、footgun 不带时限，一律当场报错而不是先到先得。
	RegisterSelf(name string, switches []Switch) (modules.Release, error)

	// Lookup 按 Path 找一条开关点。
	Lookup(path string) (Switch, bool)

	// Modules 返回全部模块（组件图 ∪ 注册表），按启动顺序。这是 CLI 的唯一数据源。
	Modules() []Module
}

// **名单是怎么进来的**（2026-09-20 改）：本模块实现 component.CatalogAware，
// 装配完成后由内核递一次组件名单——不再由组合根点名叫 SetCatalog。
//
// 为什么改：`app.New` 里那一行 `MustGet(pluginmanagerapi.Capability).SetCatalog(…)`
// 是一条**组合根到具体模块**的依赖边，而且是它让「plugin-manager 可以摘掉」这句
// 话不成立（摘掉它，组合根编译不过）。名单本来就在内核手里（Manager.components），
// 所以现在由内核按接口找想要它的人：谁想要谁实现 CatalogAware，组合根一行不改。

// Capability 是 plugin-manager 的端口身份。
var Capability = modules.NewCapability[Manager]("plugin-manager")
