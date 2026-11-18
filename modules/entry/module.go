// Package entry 是**入口账本的拥有者**：内核唯一认识的那个模块，也是唯一必要的
// 那个模块。
//
// # 只有它是必要的
//
// 别的模块都是可选的——core 只是恰好 ship 了一批通用插件（cli、gateway、wrapper…），
// 发行版可以关掉其中任何一个，也可以一个都不装。**只有 entry 摘不掉**，因为它
// 提供**入口申报账本**（component/entry 里的 Registry）：谁想当进程入口就往里
// 申报（cli 申报「默认」、wrapper 申报「argv0 是某个被接管的 client」、将来的
// web-daemon 申报「配置里写着 entry=web」）。组合根只做一次 Resolve，它自己
// 不认识 cli、不认识 wrapper，也不认识将来那个 web。
//
// 没有任何入口申报时，进程要能说一句人话然后退出；而如果账本住在一个可摘的
// 模块里，用户摘三次就得到一个开不了机的二进制。**这就是它存在的唯一理由**。
//
// # 「唯一认识」是怎么表达的（2026-09-20 改）
//
// 不靠点名，也不靠位置。它在 modules/ 里，与别的模块**完全一样**：被扫描、出现
// 在装配清单里、可以被点名关掉。区别只有一条，而且是**推导出来的**：
//
//	组合根自己要用它提供的端口（app.Main 里那次 Resolve）→ 关掉它等于让进程
//	没人回答「这次调用归谁」→ app.Selection.Load 拒绝（见 app/manifest.go）。
//
// 于是内核认得的不是「一个叫 entry 的模块」，而是**一个端口**；今天恰好只有这
// 一个模块提供它。以后组合根多依赖一个端口，这条守卫自动跟着走，不需要谁记得去
// 改一张名单。
//
// 这之前不是这样：那时它叫 root、住在 modules/ 之外（go/root），由组合根显式装入、
// 不参与扫描，装配选择里还硬编码着一句 `case dir == "root"`——模块的键 hard-code
// 进了内核，违反 component.Type 注释里那条「core 提供表，产品填内容」，而且
// 「哪些模块不可摘」变成了两处各自维护的说法。
//
// # 它自己什么业务都不做
//
// Start 是空的（`return nil`）：账本是构图期静态绑定进图里的值，不是 Start 的
// 副作用，所以谁先谁后都不影响这次 Resolve。它不是「内核的总管家」——是图里的
// 一个节点，只是它的消费者是组合根自己。判据很清楚：它认得内核（component）与
// 入口契约（component/entry），一个业务模块都不认得。
//
// # 契约为什么不在这个目录里
//
// 入口契约（Process / Handler / Registry / Capability）住在 component/entry：
// 组合根必须能 import 它，而组合根**不许** import modules/ 下的任何东西
// （modules/ 里的一切都要能被摘掉还编得过，见 app/direction_test.go 的棘轮）。
// 契约是那条规矩的例外——它由这个摘不掉的模块提供，所以它本来就不可摘。
//
// 这就是同一个词在这里出现两次的原因，读作：**component/entry 是契约，
// modules/entry 是拥有它的模块**（本文件里契约那个包显式别名成 entryapi，
// 免得一个文件里两个 entry 谁也说不清是谁）。
package entry

import (
	"context"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	modules "github.com/rzbdz/newgate/component"
	entryapi "github.com/rzbdz/newgate/component/entry"
)

// TypeBuiltin 是 entry 的分类标签。词汇表归产品层（见 component.Type 的注释），
// 这里只声明取值。
//
// 它今天的**唯一**消费者是摘除矩阵：除了标着它的这一项，其余每一项都必须能被
// 摘掉、也必须能独立装载（见 app/matrix_test.go）。它不再参与「能不能关掉」的
// 判断——那条由 app.Selection.Load 从组合根自己的依赖推导（见包注释）。
const TypeBuiltin = "builtin"

// New 声明 entry 组件。
//
// Requires 是空的：entry 不依赖任何模块，任何模块也可以不依赖它——入口是自愿
// 申报的。一个没人申报入口的图是**合法**的（比如只装了数据面、由别的进程驱动，
// 或者一个只装了 hello 的骨架发行版：那时 `newgate` 谁来答，由那个发行版自己
// 决定——见 ext 仓库的 template 分支）。
//
// 反过来，别的模块可以硬依赖它（wrapper 就 Need 了入口端口），这是它摘不掉的
// 第二重证据：摘掉它，构图期会因为「缺一个端口」当场失败，并点名是谁要的。
func New() modules.Component {
	table := entryapi.NewTable()
	return modules.Component{
		Name: "entry",
		Desc: func() string { return i18n.T("the entry ledger: which module claims this process invocation", nil) },
		Type: TypeBuiltin,
		Provides: []modules.Provision{
			modules.Provide(entryapi.Capability, entryapi.Registry(table)),
		},
		Start: func(context.Context, modules.Context) error { return nil },
	}
}
