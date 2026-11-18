package config

import (
	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/config/roleprov"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	modules "github.com/rzbdz/newgate/component"
)

// ExtraRole 是模块可追加的语义档位；别名让端口使用者无需依赖配置实现包。
type ExtraRole = domain.ExtraRole

// 落盘 schema 的四个类型 + profile 的 .kv 解析器：给**发行版**用（配置共享模块
// 在另一台机器上生成配置文件，得先按同一把尺子校验——「用 daemon 自己那个解析器」
// 而不是另写一个差不多的是那边的原话）。
//
// 2026-09-20 之前配置共享住在本仓库里，直接用 store/domain 即可；它搬去发行版之后
// 这条依赖必须走公开面，否则发行版就在 import 内核的实现包——那种依赖在内核重构
// 时会断，而且断在另一个仓库里。别名只做转发、不是新契约：schema 的真相仍在
// domain，解析器的真相仍在 store（同上面 ExtraRole/Step 那几条的写法）。
type (
	Provider  = domain.Provider
	Providers = domain.Providers
	Profile   = domain.Profile
	Binding   = domain.Binding
)

// ParseProfileKV 解析一份 profile 的 .kv 文本（`name = value` 逐行）。
func ParseProfileKV(text string) (*Profile, error) { return store.ParseProfileKV(text) }

// Step 是解析成功后候选链中的一步。
type Step = resolve.Step

// Skip 解释某个候选为何没有进入最终链，用于可观测性而不是控制流。
type Skip = resolve.Skip

// RoleProvider 允许模块向配置层贡献动态档位，同时保留来源供冲突诊断。
// 契约本体在 roleprov（注册表实现所在包）定义，这里只做名字转发。
type RoleProvider = roleprov.RoleProvider

// RoleWatchProvider 是 RoleProvider 的可选能力，用于声明哪些文件变化后需要重载。
type RoleWatchProvider = roleprov.RoleWatchProvider

// Chains 是一次「链现状」查询的回答：某一份 profile 此刻每个键的链。
//
// 为什么一次给**全部键**而不是一个键一个键地问：读的人（`/ui` 的聚合主页、
// `newgate tier`）看的是**一屏**——「这一份档位文件此刻把每一档解析成什么」。
// 按键分开问的话，取十次快照会读到十个不同时刻的盘（中间被人改了一笔，屏幕上
// 就是一份前后不一致的配置），而这里一次快照算完，那一屏是**同一个时刻**的。
type Chains struct {
	// Profile 是这次回答针对哪一份 profile（机器取值，不翻译）。
	Profile string
	// Default 为 true 表示它就是此刻生效的那一份（链头）。
	Default bool
	// Keys 按传入次序一一对应；顺序由调用方给（那是产品取舍——`domain.Roles`
	// 是能力从高到低，界面照同一条排）。
	Keys []Chain
}

// Chain 某一档在这一份 profile 下的解析结果。
type Chain struct {
	// Key 是档位名（heavy/normal/…），也可以是模块贡献的动态角色键。
	Key string
	// Steps 整条候选链，链头在内，按实际尝试顺序排。**不截断**：maxAttempts 是
	// 单次请求的执行上限，不是 membership（见 chainsFor 里 MaxSteps: 0 那段）。
	Steps []Step
	// Skips 没进链的候选与原因。被跳过的候选常常一步都不在链上，所以它必须
	// 单独交出来，而不是只能从 Steps 里推。
	Skips []Skip
	// Actions 是**这一档能做的改动**（「把这一档的链头换成 X」），不是数据。
	//
	// 它由本模块构造，因为只有本模块知道这件事的另外几样东西：这一档写在哪一份
	// 文件里、那个文件此刻的基线是什么、并发改到了要拿什么跟用户交代（CAS 冲突）。
	// 调用方（某个界面节）只把它们原样挂到自己的行上——`view.Action` 的闭包跨包
	// 传递是安全的，它在同一个进程里等着被 RunRowAction 调。
	//
	// 空的两种情况都正常，但意思不同：**这一档没有可换的候选**（文件里只写了一个
	// 绑定、或者候选是别的 profile 顶上来的），以及**它已经是链头了**。两者都不该
	// 画一个点了不改变任何事的按钮。
	Actions []view.Action
}

// Config 是配置模块对外的最小端口。
//
// 两条路各有各的用途，别混：
//   - RegisterRoleProvider 是**写**——模块把自己那些动态档位键登记进来；
//   - Chains 是**读**——把 resolve 的结论端出来（这一刻每一档解析成什么、谁被
//     跳过、为什么）。
//
// 消费者不能绕过 store/resolve 边界直接修改配置内部状态；读也一样：调用方要
// 那份结论就到这里来拿，别自己 import store/resolve 去拼（内核一重构，那种
// 依赖会断在另一个仓库里，而且断得悄无声息）。
type Config interface {
	RegisterRoleProvider(RoleProvider) (modules.Release, error)

	// Chains 报某一档位文件此刻解析出来的全部链。
	//
	// keys 是**要问的档位名**（空 = `domain.Roles` 那一套，按能力从高到低）；
	// profile 空 = 此刻全局默认的那一份。profile 不存在时返回错误——那不是
	// 「一份空配置」，是调用方问错了名字，静默给个空链会让人以为这份档位是空的。
	//
	// 它读**盘上此刻的样子**（不读 daemon 的内存态），所以 CLI 与浏览器拿到的是
	// 同一份事实；健康表（熔断摘牌、probe 延迟）**不在这里**——那是另一本账，
	// 且会随时间变，见下面 Chain。Steps 因此是「按配置解析出来的链」，不是
	// 「此刻真的会走这条链」。
	Chains(profile string, keys ...string) (*Chains, error)

	// AllChains 报**每一份** profile 的链，生效的那一份排在最前。
	//
	// 它是「一屏看全部」那条路：想要的是「我这些链长什么样」，而那是**一屏**上的
	// 一个事实，不是十次分开查询。分开问的话，那一屏上会拼起十个时刻的配置——每张
	// 卡单看都对，合起来前后不一致，而界面上看不出任何异样。
	//
	// keys 与 Chains 同义。
	AllChains(keys ...string) ([]*Chains, error)
}

// Capability 是配置端口的唯一身份；配置实现只能有一个，避免多份状态分叉。
var Capability = modules.NewCapability[Config]("config")
