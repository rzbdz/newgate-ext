// Package upgrade 是模块「升级自己那份动态数据」的登记处与命令。
//
// # 它解决的那件事
//
// 有些模块依赖一份**不进二进制**的东西：指纹识别要的模型参考库、将来可能有的
// 远端配置快照、某个插件自己的数据库文件。它们不该跟 newgate 的版本绑死——参考
// 库更新一个新模型，不该要求所有人重装二进制；插件换一份数据，也不该等发行版。
// 但它们又都是**模块自己的知识**：只有拥有者知道去哪儿拿、怎么校验、坏了怎么
// 回滚。所以这里只提供一条缝：模块在 Start 里登记一个带 ID 的升级动作，
// `newgate upgrade` 把它们全跑一遍。
//
// # 二进制也是一个动作（2026-10-29）
//
// 第一版只做动态数据，并且明说「不换二进制、不拉 git 仓库」。换二进制现在由
// `modules/self-update` 登记成**同一张表里的一个动作**（ID `binary`）：本模块仍然
// 不知道怎么下 Release、怎么编、怎么交接 daemon——那些是那个模块的知识；这里只多
// 了两样所有动作都能用的东西：
//
//   - **目标版本**（`newgate upgrade@v0.9.0`）。只有声明了 Versioned 的动作收它；
//     数据动作没有「版本」这个概念，点了版本时它们被跳过并说一声。
//   - **只看不动**（`--check`）与**强制**（`--force`）。它们装在 Request 里交给每个
//     动作，含义由动作自己落实（见 Request 的字段注释）。
//
// 它为什么住在发行版：哪些东西允许被升级、默认装哪些动作，是产品决定；内核
// 只提供模块框架。装不装本模块，verify 之类的功能照常工作（只是没有手动刷新
// 入口，参考库仍按自己的 TTL 更新）。
package upgrade

import (
	"context"

	modules "github.com/rzbdz/newgate/component"
)

// Name 是这个模块在装配表与 `newgate plugin` 里的名字（= 目录名）。
const Name = "upgrade"

// Action 是一个模块登记上来的升级动作。
//
// Title/Describe 是**闭包**而不是字符串：登记发生在各自的 Start 里，那时语言层
// 不一定装好（与 view.Title 同一条理由）。求值发生在 `newgate upgrade`
// 真的列出/执行它的时候。
type Action struct {
	// ID 是机器标记（命令可以点名一个动作、输出也可以按它排序）。不翻译，
	// 只允许小写字母、数字、`.`、`_`、`-`。
	ID string
	// Title 是给人看的一句话名字。
	Title func() string
	// Describe 说明这一下会动什么、代价是什么（要下载吗、会花 token 吗）。
	Describe func() string
	// Run 执行升级。Result 是**给人看的结果**（新版本号、下载了什么）以及「是不是
	// 根本没什么可做」；error 表示这一下失败了（命令会继续跑其余动作）。
	Run func(ctx context.Context, req Request) (Result, error)
	// Versioned 声明这个动作认识 Request.Version。没声明的动作在用户点了版本时
	// 被跳过：把 `@v0.9.0` 交给一个只会「拿最新」的动作，它要么忽略（静默），
	// 要么各自报一种错——两种都不如在这里统一说一句。
	Versioned bool
	// Kind 决定它和谁一起被问（见 Kind 的注释）。零值是 KindData。
	Kind Kind
}

// Kind 是动作的类别。`newgate upgrade` 不静默执行：它先只看一遍、再**按类别各问
// 一次** y/N——换 newgate 本身与刷新模块的数据是两种分量的事（前者会重启 daemon，
// 后者只是下一份文件），合在一句「要升级吗」里，用户没法只要其中一半。
type Kind int

const (
	// KindData：模块自己的动态数据/配置（指纹参考库之类）。这一类合起来问一次。
	KindData Kind = iota
	// KindProgram：换 newgate 自己（self-update 的 binary）。单独问一次。
	KindProgram
)

// Result 是一个动作这一次的结论。
type Result struct {
	// Message 由命令原样打印。
	Message string
	// UpToDate 说「已经是最新/没什么可做」。只看那一遍里报了它的动作不会被追问，
	// 也不会再执行——问一句「要升级吗」然后什么都不做，是在浪费用户的确认。
	UpToDate bool
}

// Done 与 Current 是两个常用的构造：做了（或只看时「会做」）什么 / 已经是最新。
func Done(msg string) Result    { return Result{Message: msg} }
func Current(msg string) Result { return Result{Message: msg, UpToDate: true} }

// Request 是这一次 `newgate upgrade` 的参数。
type Request struct {
	// Version 是 `upgrade@<值>` 里的那个值：tag、分支或提交，原样交给动作解释。
	// 空 = 「最新」。
	Version string
	// Check 只看不动：报出「会做什么 / 有没有新的」，**不许改动任何东西**
	// （不下载到最终位置、不替换、不重启）。动作做不到只看的时候就直说做不到。
	Check bool
	// Force 允许重装同一个版本、允许倒退到更旧的版本（`-f` 同时也免确认）。
	Force bool
}

// Service 是升级动作的登记处。
//
// 形状与 web-dashboard 的扩展端点、cli 的命令登记同一条：交进去、拿回撤销，
// Stop 时还回来；重名当场报错（两个模块抢同一个 ID 时，命令只会跑到其中一个，
// 而用户在输出里看不出少了一个）。
type Service interface {
	Register(a Action) (modules.Release, error)
	Actions() []Action
}

// Capability 是它在装配图上的名字。
var Capability = modules.NewCapability[Service]("upgrade")
