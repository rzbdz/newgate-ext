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
// # 边界（第一版）
//
// **只做动态数据/配置**。不换二进制、不拉 git 仓库、不编译、不重启 daemon——
// 「绑定一个 git remote 自动 fetch + build + install」是明确**没有做**的那件事
// （见命令里的说明）。静态升级已经有更合适的那条路：换二进制 + 优雅交接
// (`newgate restart`)，它需要处理运行中的进程，和这里「拿一份文件」是两种问题。
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
	// Run 执行升级。返回的字符串是**给人看的结果**（新版本号、下载了什么），
	// 由命令原样打印；error 表示这一下失败了（命令会继续跑其余动作）。
	Run func(ctx context.Context) (string, error)
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
