package kernel

import (
	"context"
	app "github.com/rzbdz/newgate/app"
	"testing"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	modules "github.com/rzbdz/newgate/component"
)

// TestDefaultGraphLayering 锁住「owner 先于注册者」这条层级。
//
// 这是设计意图的声明，不是拓扑排序的复述：拓扑排序只会保证 Requires 成立，
// 而下面这张表说的是**我们认为谁该在谁前面**。加一条依赖把层级搞反（比如让
// config 去依赖某个客户端模块）时，这里会红。
//
// 命令/诊断/状态行三本账长在 **cli** 的 service 上，而注入是一条**排序边**：
// 模块声明 modules.Optional(cli)（弱依赖，见 component.Optional），于是它们全部排在
// 界面之后——Start 里注册时界面的账本已经就绪；没装界面就跳过，模块功能不受影响。
// ui 因此从「被依赖」的位置上退了出去：装不装 ui 只影响这些贡献有没有地方去。
//
// 表里的排序来自两种边：硬依赖 Need 与弱依赖 Optional。弱依赖那几条（`plugin-manager`
// 注册 state 字段之类）同样是排序边——owner 先起、注册者后起，只是 owner 不在时
// 注册者照样跑，只是那份贡献没有地方去。
//
// This table covers the flagship spec (manifest.DefaultSpec), which since
// 2026-11-18 is *the* graph — the kernel ships none. Per-module behaviour still
// lives with its module; this is the "who must start before whom" declaration.
func TestDefaultGraphLayering(t *testing.T) {
	built, err := app.New(context.Background(), staticLoader(kernelGraph(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })
	names := built.ComponentNames()
	index := map[string]int{}
	for i, name := range names {
		index[name] = i
	}
	at := func(name string) int {
		i, ok := index[name]
		if !ok {
			t.Fatalf("component order = %v; %s 不在图里", names, name)
		}
		return i
	}

	pairs := []struct{ owner, registrant, why string }{
		{"config", "runtime", "runtime 读配置"},
		{"config", "gateway", "gateway 读配置"},
		{"config-hook", "runtime", "runtime 用 AgentCatalog"},
		{"config-hook", "plugin-manager", "plugin-manager 注册 state 字段"},
		{"runtime", "wrapper", "wrapper 懒启动代理"},
		{"gateway", "thinking", "thinking 注册请求插件"},
	}
	for _, p := range pairs {
		if at(p.owner) > at(p.registrant) {
			t.Fatalf("component order = %v; %s(%d) 排在 %s(%d) 之后，但它%s",
				names, p.owner, at(p.owner), p.registrant, at(p.registrant), p.why)
		}
	}

	// 客户端目录：**经端口问，而不是经组合根的门面**。组合根不认识任何模块
	// （见 direction_test.go），所以它没有 Get/Names 之类的便利方法——那类方法每加
	// 一个都是一条「app → 那个模块」的依赖边，而且只为测试方便存在。
	//
	// 这里**只断言端口在**，不断言里面有谁：客户端接入（claude / opencode）是产品
	// 取舍，2026-09-20 起住在发行版里，内核这张图里一个客户端都没有——空目录是它的
	// 正常状态。谁被支持由发行版的测试断言（那边有 claude/opencode/omo）。
	if _, ok := modules.Get(built.Context(), agentapi.AgentCatalogCapability); !ok {
		t.Fatal("图里没有客户端目录端口——接管机制就没有登记处了")
	}
}

// TestCLIDependenciesOnlyShrink 锁住「界面不依赖任何模块」这个方向。
//
// 目标**不是**「没人依赖界面」——那既不可能也不必要：模块要把命令注入界面，
// 本来就得先 Need 它。目标是反过来的那一侧：**界面自己不依赖任何人**，它只是
// 一个 ui，别人的东西是别人注入进来的回调，界面循环调用它们拿数据。
//
// 现在还没到那一步：下面允许名单里剩下的几个，是仍在界面手里的那批命令用的，
// 它们要一个个搬回各自的模块（start/stop 归 runtime、tier/profiles 归 config、
// probe/metrics 归 gateway…）。
//
// 这张表**只能变短**。往里加一项 = 把某条命令永远钉在界面里，而那正是这次重构
// 要拆掉的东西；症状要等到有人真想搬那条命令时才会显形（「搬不动，成环了」），
// 在这里红比在那里红早得多。
func TestCLIDependenciesOnlyShrink(t *testing.T) {
	// 名单现在是**空的**，而且它只该变小（2026-09-18 清到底）：
	//
	// 界面曾经依赖 config（读配置）、runtime（拉客户端）、agent-catalog（按 agent
	// 分派）、breaker（构造数据面）。四条边各自对应一批「界面知道某个模块的内部」：
	// tier/profile 的渲染、包装启动、agent 名单、健康表。它们全部搬回了拥有那些知识
	// 的模块，界面改成从注入点拿别人报上来的东西（命令、status 行、体检项、术语、
	// 诊断素材）。
	//
	// 所以这条断言现在的意思是：**界面一条出边都不许有**。任何新增的边都意味着又有
	// 一坨别人的知识回到了界面里。
	allowed := map[string]bool{}

	built, err := app.New(context.Background(), staticLoader(kernelGraph(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })

	for _, c := range built.Components() {
		if c.Name != "cli" {
			continue
		}
		for _, req := range c.Requires {
			if !allowed[req.Name()] {
				t.Fatalf("界面依赖了 %q，而它不在允许名单里。界面应当**不依赖任何模块**："+
					"命令由拥有那项能力的模块注入（见 modules/cli/extension）。"+
					"要么把那条命令搬回它的模块，要么先想清楚为什么界面必须认识它。", req.Name())
			}
		}
		return
	}
	t.Fatal("图里没有 cli 组件")
}

func TestGatewayReceivesModuleHooks(t *testing.T) {
	built, err := app.New(context.Background(), staticLoader(kernelGraph(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })
	var names []string
	for _, plugin := range special.Plugins() {
		names = append(names, plugin.Name())
	}
	// 内核自己的插件：always-thinks（thinking 模块的兜底翻译器）。
	// claude-bg 以前也在这张清单里——它跟着 claudecode 搬去发行版了（客户端接入
	// 是产品取舍），发行版那边有一条同样的断言守着它。
	for _, want := range []string{
		"always-thinks",
	} {
		found := false
		for _, name := range names {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("gateway hooks = %v, missing %s", names, want)
		}
	}
}

// TestExactlyOneUI 断言「装着的 ui 只有一个」。
//
// 为什么需要它（2026-09-18 实测）：端口不声明基数（component 的设计如此），而
// cli.Capability 恰恰是**按设计唯一**的——进程组合根最终调用的入口只能有一个。
// 但 owner 没做唯一性保证，于是装第二个 ui 的后果是**完全静默**的：它装配成功、
// 不报错、拿到的命令/状态行/体检项/诊断素材/术语全是零个（注入点用
// modules.Get，多 provider 时只给声明顺序里的第一个）。
//
// 更阴的是「谁赢」取决于**目录名字母序**（generatedComponents 按目录名排序，
// Get 取第一个）：加一个目录名排在 cli 之前的 ui，真 cli 就变成被忽略的那一个，
// 而 app.CLI().Run() 会把整条命令行交给它——一次由目录命名造成的静默接管。
//
// 这条测试把静默变成红。真要做多 ui（路线里写了 tui / web），得先把注入点改成
// GetAll 并把贡献发给每一个 ui，那时这条断言要跟着改。
func TestExactlyOneUI(t *testing.T) {
	built, err := app.New(context.Background(), staticLoader(kernelGraph(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })

	uis := modules.GetAll(built.Context(), cliapi.Capability)
	if len(uis) != 1 {
		t.Fatalf("装了 %d 个 ui，应当是 1 个。"+
			"多个 ui 时注入只会进「声明顺序第一个」（依赖目录名字母序），"+
			"其余的完全静默——见这条测试的注释。", len(uis))
	}
}
