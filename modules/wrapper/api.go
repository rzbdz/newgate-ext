package wrapper

import (
	modules "github.com/rzbdz/newgate/component"
)

// Capability 标识进程内唯一的 shim 接管实现。
var Capability = modules.NewCapability[Wrapper]("wrapper")

// Wrapper 是「PATH shim」这条接管通路里**装/摘链接**那一半的所有权：
// Install/Uninstall/Installed/Foreign 四个动作。
//
// 另一半——链接被敲响时的那次进程调用——**不在这个接口上**（2026-09-20 改）：
// 它是**进程入口**的一种，归 component/entry 那本账（本模块在自己的 Start 里
// 用 entry.RankShim 申报：argv0 是某个被接管的 client 时认领）。两半仍然同属
// 本模块，只是各自走各自该走的端口——否则「谁是入口」这件事又要靠组合根
// 点名某个模块，而那正是要拆掉的东西。
//
// 底层字节操作（建符号链接、改 shell rc）仍是 runtime/injection 这个库；
// 本模块拥有的是**策略**——哪些名字算 client、什么条件才认领一次调用。
type Wrapper interface {
	// Install 为某个 client 建 shim 链接，返回链接路径。
	Install(agentID string) (string, error)

	// Uninstall 摘掉一个 shim。名字必须是已知 client，且链接必须是我们装的
	// ——同名的真实可执行文件、用户自己的包装脚本一律不动。
	Uninstall(agentID string) error

	// Installed 列出我们自己装的 shim 名。
	Installed() []string

	// Foreign 列出 shim 目录里不是我们装的条目。只展示，不删。
	Foreign() []string
}
