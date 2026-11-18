package kernel

import (
	"context"
	app "github.com/rzbdz/newgate/app"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// TestGraphCanBeRebuiltAfterStop 是整套重构里最便宜、覆盖面最广的一条不变量：
// **Stop 必须完整撤销 Start 做过的注册**。
//
// 为什么值得单独立一条
//
// 每个模块的 Start 都往别处注册东西（gateway 挂插件、confighook 注册 Agent、
// config 注册角色提供者、cli 收命令）。这些注册是**进程级全局状态**，而 Stop
// 里漏掉一个 Release 的症状极其隐蔽：
//
//   - 单独跑任何一条测试都是绿的（一个进程里只装一次图）；
//   - 只有在同一个进程里装第二次时才炸，或者更糟——**不炸**，而是第二张图
//     拿到第一张图残留的注册，行为取决于上一张图装了什么。
//
// 所以这里装两次：第一次的残留会让第二次的 Start 撞上重复注册或拿到脏数据。
// 这条测试在 2026-09-17 的重构里当场抓出过角色提供者的残留（opencodeomo
// 的 omo 槽位在第二次装配时被判定为「键被 omo 和 omo 同时注册」）。
func TestGraphCanBeRebuiltAfterStop(t *testing.T) {
	testkit.Sandbox(t)

	first, err := app.New(context.Background(), staticLoader(kernelGraph(t)))
	if err != nil {
		t.Fatalf("第一次装配失败: %v", err)
	}
	firstNames := first.ComponentNames()
	if err := first.Stop(context.Background()); err != nil {
		t.Fatalf("第一次停止失败: %v", err)
	}

	second, err := app.New(context.Background(), staticLoader(kernelGraph(t)))
	if err != nil {
		t.Fatalf("第二次装配失败（说明第一次的 Stop 没撤干净）: %v", err)
	}
	t.Cleanup(func() { _ = second.Stop(context.Background()) })

	if got := second.ComponentNames(); len(got) != len(firstNames) {
		t.Fatalf("两次装配的组件数不同：%v vs %v", firstNames, got)
	}
	// 进程级注册也必须干净。2026-09-20 之前这里数的是客户端目录（claude/opencode
	// 由客户端模块注册）——那些模块搬去发行版了，内核这一侧剩下的是**请求插件注册表**：
	// 第二次装配之后它仍应只有内核自己那些插件，一个都不许翻倍。
	plugins := special.Plugins()
	seen := map[string]int{}
	for _, plugin := range plugins {
		seen[plugin.Name()]++
	}
	for name, n := range seen {
		if n != 1 {
			t.Fatalf("插件 %s 注册了 %d 次（两次装配之间没撤干净）：%v", name, n, seen)
		}
	}
	if len(plugins) == 0 {
		t.Fatal("内核一个请求插件都没有——这条判据退化了（它本来靠插件数当残留的探针）")
	}
}

// TestGraphCoversEveryModule 把「生成器扫到的组件」和「图里真启动的组件」
// 对上账。
//
// 生成器只看静态文件（有 module.go + 导出 New()），图是运行期真装配的结果。
// 两者不一致意味着某个模块被扫进来了却在图上缺席——那要么是它没提供任何
// 东西（无害但可疑），要么是接口对不上（危险）。这里用名字集合对齐。
//
// **这条等式没有例外**（2026-09-20 起）：生成的清单是**唯一**的组件来源，组合根
// 不再额外装入任何东西。2026-09-20 之前有一个例外——入口账本的拥有者住在
// modules/ 之外、由组合根显式装入，于是等式写成「图 = 扫描清单 + built-in」，
// 而这一行代码就是「没有第二个模块绕过扫描被偷偷装进来」的守卫；现在等式本身
// 就是那个守卫。
func TestGraphCoversEveryModule(t *testing.T) {
	testkit.Sandbox(t)

	built, err := app.New(context.Background(), staticLoader(kernelGraph(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })

	names := map[string]bool{}
	for _, name := range built.ComponentNames() {
		if names[name] {
			t.Fatalf("组件名重复：%s（实际 %v）", name, built.ComponentNames())
		}
		names[name] = true
	}
	want := len(kernelGraph(t))
	if got := len(names); got != want {
		t.Fatalf("图里 %d 个组件，扫描清单 %d 个（两者必须逐个对上：装模块只有扫描这一条路）",
			got, want)
	}
	if !names["entry"] {
		t.Fatalf("entry（唯一摘不掉的模块）必须总在图里（实际 %v）", built.ComponentNames())
	}

	// 地基与两条接管通路是产品定义的一部分，缺一个都跑不起来。
	for _, must := range []string{"config", "gateway", "config-hook", "runtime", "cli", "wrapper"} {
		if !names[must] {
			t.Fatalf("缺少必需组件 %s（实际 %v）", must, built.ComponentNames())
		}
	}
}

// TestUIStaysOutOfTheDependencyGraph 锁住「ui 不是依赖」这条不变量。
//
// 2026-09-18 之前这里断言的是「cli 排在扩展模块前面」——那时候注入是一条排序边
// （模块 Optional(cli)，要在自己的 Start 里拿到界面再注册）。那条边恰恰是死结的
// 来源：界面自己也要依赖那些模块（渲染要靠它们报数据），两边互指就成环，于是
// config / runtime / config-hook 三个模块永远注入不进来，命令只能被迫留在界面里。
//
// 当时绕开那个环的办法另有其人：component.Inject（一条**不参与排序**的注入边）
// 加 Component.Attach（全图 Start 之后再跑一遍的注入阶段）。那套机制在
// 2026-09-18 晚些时候删掉了——环的真正解法是把 **ui 的出边砍干净**
// （cli.Requires 现在为空），出边没了以后 Optional(cli) 这条**入边**不可能成环，
// 于是注入可以回到 Start 里、回到一条正常的排序边上（见 component.Optional）。
//
// 所以这条不变量现在的写法是：**没有任何组件对界面声明一条非 Optional 的边**。
// 换句话说，对 ui 只允许弱依赖——装着就注册，不装就跳过；不允许 Need，那等于
// 宣称「没有界面我就活不了」，也就把界面重新拖回了依赖图里。
// uiCapabilities 是「界面」这一类端口。**一条规矩要认全部成员**：2026-09-20 之前
// 这里只写死了 "cli"，而那时 view 已经有七个消费者了——一个新模块写
// `Need(viewapi.Capability)`（等于宣称「没有 dashboard 我就活不了」）不会红，
// 而这条测试的注释正说着不许这么干。
//
// 判据是「**界面层**：别人往它里面注册自己那一面」，不是「注册型端口」——
// porthub 与 serving 也是「别人往里注册、自己不依赖别人」的形状，但它们是
// **机制**不是界面：一个模块只从共享端口进出、没有独立退路时，`Need(porthub)`
// 是正当的（它不组成这条测试要防的那个环，porthub 自己没有出边）。别照着形状
// 往这张表里加成员，加之前先问「这是不是一层界面」。
var uiCapabilities = map[string]bool{
	"cli":  true, // 终端界面：命令、状态行、诊断项往里注册
	"view": true, // web 界面的概念账本：各模块往里注册自己那一面
}

func TestUIStaysOutOfTheDependencyGraph(t *testing.T) {
	testkit.Sandbox(t)

	built, err := app.New(context.Background(), staticLoader(kernelGraph(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })

	for _, component := range built.Components() {
		for _, requirement := range component.Requires {
			if !uiCapabilities[requirement.Name()] {
				continue
			}
			if !requirement.Optional() {
				t.Fatalf("组件 %s 对 ui 声明了硬依赖（%s）——"+
					"界面依赖别人渲染、别人依赖界面注入，这就是那个环；"+
					"对 ui 请用 modules.Optional（弱依赖）", component.Name, requirement.Name())
			}
		}
	}
}

// TestInjectorsStartAfterTheUI 锁住「注入别人的人要等被注入的人」这条顺序。
//
// 这是用户 2026-09-18 的原话落到测试上：**注入别人的模块，一定要等那个被注入的
// 人加载完；被注入的人不该等注入他的人**。落到本仓库就是：往界面里注册命令与
// 状态行的模块，必须排在 `cli` 后面。
//
// # 为什么值得一条自己的测试
//
// 它以前**没有**保证。Inject（不参与排序）时代，9 个注入者的顺序纯粹是稳定拓扑
// 排序碰出来的——实测恰好都在 cli 之后，靠的是目录名字母序。任一次目录改名、或
// 加一个排在 cli 前面的注入者，顺序就反过来，而症状是「界面先起、命令后到」：
// 谁先谁后只在这一瞬间有差别，跑完就看不见了。
//
// 现在这条顺序由 Optional(cli) 这条**排序边**给出（见 component.Optional），
// 也就是由机制保证。这条测试守的是「机制真的还在」——有人把 Optional(cli) 换回
// Need(cli) 之外的写法、或者干脆删掉那条 Requires，这里就红。
//
// 「cli 先起」不等于「cli 早于一切」：cli 自己不依赖任何模块（TestCLIDependenciesOnlyShrink），
// 所以它就是拓扑序里最前面那几个之一，先于它意味着基本没什么要在它前面。
//
// # 为什么这条只管 cli，不管 view（尽管上面那条规矩两个都管）
//
// 两条界面端口的**注册时机不同**，所以对顺序的要求也不同：
//
//   - `cli` 的命令/状态行账本是在它自己的 `Start` 里建的——所以往里面注册的模块
//     必须排在它**之后**，否则 Start 跑的时候账本还不存在，那条命令就丢了。
//     这是顺序上的**硬要求**，也就是这条测试存在的原因。
//   - `view` 的概念账本在 web-dashboard 的 `New()` 里就建好了（见
//     modules/web-dashboard/module.go 的注释：必须早于任何 Start，否则比它先
//     Start 的模块注册不进来）。于是注册发生在谁的 Start 里都接得住——
//     顺序对 view 真的无所谓，多一条断言只是把一个不存在的约束写死。
func TestInjectorsStartAfterTheUI(t *testing.T) {
	testkit.Sandbox(t)

	built, err := app.New(context.Background(), staticLoader(kernelGraph(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })

	names := built.ComponentNames()
	at := map[string]int{}
	for i, name := range names {
		at[name] = i
	}
	ui, ok := at["cli"]
	if !ok {
		t.Fatalf("component order = %v; 图里没有 cli", names)
	}

	// 注入者名单从**声明**里读（谁 Optional(cli)），不是写死的清单：新模块加一条
	// 弱依赖就自动进这条断言，不需要回来改测试。
	injectors := 0
	for _, component := range built.Components() {
		for _, requirement := range component.Requires {
			if requirement.Name() != "cli" {
				continue
			}
			injectors++
			if at[component.Name] < ui {
				t.Fatalf("component order = %v; %s(%d) 排在 cli(%d) 之前 —— "+
					"它就是往界面里注册命令/状态行的那个模块，"+
					"排前面意味着它的 Start 跑的时候界面可能还没就绪",
					names, component.Name, at[component.Name], ui)
			}
		}
	}
	if injectors == 0 {
		t.Fatal("没有任何模块声明 Optional(cli) —— 界面的七本账全是空的，" +
			"要么是注入点被拆了，要么是这条依赖被误删了")
	}
}
