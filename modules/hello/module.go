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
)

// TypeExample 是分类标签。词汇表归产品层（见 component.Type 的注释），
// 这里给出取值：`newgate plugin` 会按它分组显示，装不认识的值也不会报错。
const TypeExample = "example"

// Greeting 是那一行日志的内容。导出是为了让测试能引用同一份字面量——
// 断言写死字符串的话，改文案要改两处，而它们是同一个事实。
const Greeting = "hello from newgate-ext"

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
			log.Printf("[hello] %s", Greeting)
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
	fmt.Printf("hello world — %s (%s)\n", p.Argv0, p.Version)
	return 0
}
