package runtime

import (
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	modules "github.com/rzbdz/newgate/component"
)

// Runtime 封装被接管客户端的启动边界，使 CLI 不需要知道 shim、环境注入和 argv 细节。
type Runtime interface {
	// Launch 注入 env 并 exec 真实客户端。facts 由调用方从目录端口取来（见
	// agentapi.AgentFacts）——「这个客户端此刻怎么样」是客户端的知识，本模块只转交。
	Launch(agent *agentapi.Agent, facts agentapi.AgentFacts, args []string, profile string) int
}

// Control 是**接管与生命周期**那条端口：`start` / `on` / `off` / `status` 底下的
// 那件事，2026-11-17 从命令里升成公开端口（理由见 control.go 的文件头）。
//
// # 为什么它与 Runtime 分成两个口子
//
// 两个原因，一个原则性的、一个很实际的：
//
//   - **谁需要哪一半**是不同的。包装启动（wrapper、`newgate claude`）只需要
//     Launch——它绝不修改磁盘上的接管状态；而发行版的安装向导只需要这四件，
//     它从不启动客户端。合成一个宽接口，等于让每个消费者都依赖另一半
//     （以及那一半将来还会长出来的东西）。
//   - 合并会让**已经存在的测试桩**全部编译不过：wrapper 的测试桩只实现 Launch，
//     它装在 Runtime 上就必须补四个自己永远不调的方法。宽接口把「不关我事」变成
//     「我得知道」。实测（2026-11-17）：先加进 Runtime 时 wrapper_test 当场红。
//
// 两个口子由**同一个组件**提供（modules/runtime 的 service 两个都实现），
// 所以「接管与启动是同一个模块」这一点没有变——变的只是消费者能声明自己只要
// 哪一半。
type Control interface {
	// ---- 接管 ----
	//
	// 这四条是 `start` / `on` / `off` / `status` 底下的那件事，2026-11-17 升成
	// 端口（理由见 control.go 的文件头）。返回值与命令打印的是同一份类型——
	// 「一次接管改了什么」只有一种形状。

	// TakeOver 接管一个 agent 并记下「用户要接管它」。
	TakeOver(agent string, port int) Result
	// Release 释放一个 agent 并记下「用户不要它被接管」。
	Release(agent string) Result
	// TakeOverAll 接管所有没被用户显式关掉的 agent。
	TakeOverAll(port int) []Result
	// TakeoverStatus 列出每个已知 agent 的接管状态。
	TakeoverStatus() []Status

	// ---- 生命周期 ----

	// Start 起代理并全面接管（`newgate start` 的语义）。命令与向导走同一份实现：
	// 同一个令牌先落盘、同一道「没 key 就别接管」的闸、同一次全面接管。
	Start(opts StartOptions) (StartReport, error)
}

// Capability 标识唯一的客户端运行时实现。
var Capability = modules.NewCapability[Runtime]("runtime")

// ControlCapability 标识唯一的接管/生命周期实现。
var ControlCapability = modules.NewCapability[Control]("runtime-control")
