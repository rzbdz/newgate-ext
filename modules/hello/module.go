// Package hello 是**发行版模块**的最小样例，也是骨架发行版（`dist-hello.json`）
// 的全部内容：整个 newgate 框架起来之后，`newgate` 这个入口就跑一句 hello world。
//
// 它做两件事，正好是「一个模块能对进程做的两件事」：
//
//   - Start 时往装配日志打一行 hello —— 证明它被装进来了；
//   - 往**入口账本**申报自己（见 component/entry）—— 没有别的入口认领这次调用时
//     归它，于是「框架 + hello」这个发行版跑起来就是一句 hello world。
//
// 第二件事才是这个样例的价值所在：入口不是组合根写死的分发（`app` 不认识任何
// 模块），而是模块自己申报的。`newgate plugin` 会把它列在 `example` 那一类里，
// 而 `newgate`（没有别的入口时）会走到它这里。
package hello

import (
	"context"
	"fmt"
	"log"

	modules "github.com/rzbdz/newgate/component"
	entryapi "github.com/rzbdz/newgate/component/entry"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// TypeExample 是分类标签。词汇表归产品层（见 component.Type 的注释），
// 这里给出取值：`newgate plugin` 会按它分组显示，装不认识的值也不会报错。
const TypeExample = "example"

// 这个样例只有两句话，但两句话都**走目录表**——因为翻译这件事最贵的不是工具，
// 是习惯：一个模块第一次写文案时抄了哪一行，决定它以后长什么样。骨架发行版
// （`dist-hello.json`）跑的就是这个模块，所以它也是「一句话该怎么写」的样板。
//
// 规矩（内核 `docs/13-i18n.md` 是权威原文）：源码里写**英文**（源语言），
// `i18n.T("那条英文", i18n.A{...})`，中文活在 `modules/i18n/catalogs/zh-Hans.json`。
// 键就是那句英文本身，所以没有「键写错」这回事；代价是改措辞会让旧译文变孤儿，
// `tools/i18n check` 会报出来。
//
// **不能把消息放进常量再 `i18n.T(常量)`**：扫描器只认调用点上的字符串字面量，
// 引用常量不算——那条规矩是为了让「这句话长什么样」在调用点一眼可见。

// New 声明这个组件：启动时登记一行日志 + 申报一个兜底的进程入口。
//
// 依赖是**硬依赖**入口账本（`Need`）：没有账本就没人可申报，这个模块的意义也就
// 没了。声明出来（而不是在 Start 里悄悄 MustGet）才能让「摘掉 entry」在构图期
// 就失败并点名缺的那个端口。
func New() modules.Component {
	// 申报句柄：Start 拿到的贡献必须在 Stop 里撤销（见 component.Release 的注释）。
	// 拿它做闭包变量而不是包级变量——同一个模块被装两次（测试里很常见）时，
	// 包级变量会让第二张图撤掉第一张图的登记。
	var release modules.Release

	return modules.Component{
		Name:     "hello",
		Type:     TypeExample,
		Requires: []modules.Requirement{modules.Need(entryapi.Capability)},
		Start: func(_ context.Context, ctx modules.Context) error {
			log.Printf("[hello] %s", i18n.T("hello from newgate-ext", nil))
			registry := modules.MustGet(ctx, entryapi.Capability)
			rel, err := registry.Register(entry{}, entryapi.DefaultRank)
			if err != nil {
				return err
			}
			release = rel
			return nil
		},
		Stop: func(context.Context) error {
			if release == nil {
				return nil
			}
			rel := release
			release = nil
			return rel()
		},
	}
}

// entry 是这次入口申报：**兜底**（entry.DefaultRank）——它永远认领。
//
// 同一个 rank 上只能有一个申报者，而内核的 `cli` 也在这一档（它也是兜底：谁都没
// 认领就是它）。两者**不该装进同一张图**：谁被 Resolve 选中会退化成「目录名字母序
// 决定进程行为」（cli 排在 hello 前面，于是 cli 赢），而那正是这套机制想避免的
// 那类隐式判据。发行版的规格书级测试守着这条（testing/graph_test.go）。
type entry struct{}

func (entry) Name() string { return "hello" }

// Claims 永远为真：兜底入口的条件就是「没有人认领」。
func (entry) Claims(entryapi.Process) bool { return true }

// Handle 是骨架发行版的全部行为。带上 argv0 与版本号：调试「我跑的是哪一份产物」
// 时，这两样是最先要看的东西。
func (entry) Handle(p entryapi.Process) int {
	fmt.Println(i18n.T("hello world — {argv0} ({version})",
		i18n.A{"argv0": p.Argv0, "version": p.Version}))
	return 0
}
