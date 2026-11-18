// Package testkit 是模块单测的公共地基：隔离环境 + 组件图 + 共享假实现。
//
// 为什么需要它
//
// 在这之前每个包各写各的：`isolate()` 在 opencodeomo / takeover / forward 各一
// 份，`testCatalog` 在 cli / takeover 各一份（逐字重复），`sandboxStore` 又一份。
// 更关键的是没有「只装我关心的几个组件 + 假实现」的入口——每个包都得自己写
// Loader、自己管 Stop，于是大多数包干脆不起图，直接调内部函数，**组件之间的
// 接线（谁在 Start 里注册了什么、Stop 有没有撤销）就没有测试覆盖**。
//
// 四层粒度（docs/10-testing-security.md）
//
//	单元   只测一个纯函数/一个 struct            —— 不需要本包
//	模块   本包：装出「我 + 我的依赖链」的图      —— testkit.Start
//	系统   testing/system：整张真图 + 真转发服务  —— 只测「合起来还对」
//	端到端 mock/*.sh：真二进制、真进程、真接管    —— 只有它算 e2e
//
// 本包服务**模块**那层：把一个模块和它的依赖装成一张真图跑起来，断言接口的
// 通过性、注册/撤销的对称性，以及模块自己那些「只有它清楚」的特殊行为（动态
// 更新的键、上游补丁的判据）。整张图的联动不在这里——那属于 testing/system。
//
// 一条约束：本包 import 了 confighook 和 agentstate，所以**这两个包自己的内部
// 测试不能 import 本包**（会构成 test-only import cycle）。它们各自内联需要的
// 那几行即可。
package testkit

import (
	"context"
	"github.com/rzbdz/newgate/lib/i18n"
	"os"
	"path/filepath"
	"sort"
	"testing"

	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/runtime/agentstate"
	modules "github.com/rzbdz/newgate/component"
)

// Sandbox 把一次测试要碰的所有全局状态圈进临时目录，并在 Cleanup 时还原。
//
// 圈三样东西，缺一样就会互相污染：
//
//	NEWGATE_HOME        newgate 自己的配置/日志/pid/thinkcache（paths 包读它）
//	NEWGATE_TARGET_DIR  客户端配置接管的落点（不受 NEWGATE_HOME 影响）
//	HOME                shell rc、~/.claude 之类（discovery 走它）
//
// 还要**清掉**一批会从父进程漏进来的变量，否则测试结果取决于跑测试的那个
// shell 是什么状态——最典型的是 CLAUDE_CODE_MAX_CONTEXT_TOKENS 和
// NEWGATE_DEPTH（在一个被接管的会话里跑测试，不 clean 就会得到假失败）。
func Sandbox(t *testing.T) *Env {
	t.Helper()

	root := t.TempDir()
	env := &Env{
		Root:    root,
		Home:    filepath.Join(root, "ng"),
		Targets: filepath.Join(root, "cfg"),
	}
	for _, dir := range []string{env.Home, env.Targets, filepath.Join(root, "home")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("testkit: 建沙箱目录 %s: %v", dir, err)
		}
	}

	restore := setEnv(map[string]string{
		"NEWGATE_HOME":       env.Home,
		"NEWGATE_TARGET_DIR": env.Targets,
		"HOME":               filepath.Join(root, "home"),
		// 语言**钉住**而不是只清掉：测试会断言渲染出来的文本，钉住「源语言」
		// （英文）才有稳定的期望值。要测译文的用例自己 t.Setenv 成 zh-Hans。
		"NEWGATE_LANG": i18n.SourceLang,
	})
	unset := clearEnv(
		// 父会话可能带着这些；不清就会改变被测行为。
		"NEWGATE_DEPTH", "NEWGATE_DISABLE", "NEWGATE_PROFILE", "NEWGATE_PRESET",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS", "CLAUDE_CODE_AUTO_COMPACT_WINDOW",
		// 优雅交接的 fd 契约：继承到测试进程里会被当成真 socket 用。
		"NEWGATE_LISTENER_FD", "NEWGATE_READY_FD",
		// 跑测试的 shell 多半带着 LANG=zh_CN.UTF-8（开发机）或 C（runner）：
		// 不清的话「默认语言 = 源语言」这类断言会在一个环境绿、另一个红。
		"LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE",
	)
	t.Cleanup(func() {
		unset()
		restore()
	})
	return env
}

// Env 是一次沙箱的路径集合。
type Env struct {
	Root    string // 临时根，所有东西都在它下面
	Home    string // NEWGATE_HOME
	Targets string // NEWGATE_TARGET_DIR
}

// Path 拼一个沙箱内路径，避免测试里满地 filepath.Join。
func (e *Env) Path(parts ...string) string {
	return filepath.Join(append([]string{e.Root}, parts...)...)
}

// Graph 是已经启动的组件图。所有断言入口都由它提供。
type Graph struct {
	t       *testing.T
	manager *modules.Manager
}

// Start 装出并启动一张组件图。
//
// 只传你真正关心的组件：真实模块（`cli.New()`）和手写的桩混在一起都可以，
// 依赖由 capability 声明推导，不需要手工排序。启动失败即 t.Fatal——模块测试
// 里「图装不起来」是测试写错，不是被测行为。
//
// 图在 Cleanup 时逆序停止。**Stop 也在这条路径上被测**：如果某个组件的
// Stop 没有撤销它在 Start 里的注册，下一次 Start 会撞上重复注册而失败，
// 这条测试就会先替你发现。
func Start(t *testing.T, components ...modules.Component) *Graph {
	t.Helper()
	manager, err := modules.New(loader(components))
	if err != nil {
		t.Fatalf("testkit: 装配组件图失败: %v", err)
	}
	t.Cleanup(func() {
		if err := manager.Stop(context.Background()); err != nil {
			t.Errorf("testkit: 停止组件图失败: %v", err)
		}
	})
	return &Graph{t: t, manager: manager}
}

// Get 读一个端口。缺失即 Fatal——模块测试里声明了 Need 却拿不到，是装配错了。
func Get[T any](g *Graph, capability modules.Capability[T]) T {
	g.t.Helper()
	value, ok := modules.Get(g.manager.Context(), capability)
	if !ok {
		g.t.Fatalf("testkit: 端口 %s 在图上不存在", modules.Name(capability))
	}
	return value
}

// Maybe 读一个可选端口。
func Maybe[T any](g *Graph, capability modules.Capability[T]) (T, bool) {
	return modules.Get(g.manager.Context(), capability)
}

// All 读一个端口的全部贡献。
func All[T any](g *Graph, capability modules.Capability[T]) []T {
	return modules.GetAll(g.manager.Context(), capability)
}

// Context 暴露只读端口表，给需要自己查端口的测试用。
func (g *Graph) Context() modules.Context { return g.manager.Context() }

// Names 按实际启动顺序返回组件名，用于断言依赖顺序。
func (g *Graph) Names() []string { return g.manager.ComponentNames() }

// Before 断言 a 在启动顺序里排在 b 之前（即 a 是被依赖方）。
// 依赖顺序错了的症状往往很隐蔽——消费者拿到的是零值而不是报错——所以
// 值得显式锁一条。
func (g *Graph) Before(a, b string) {
	g.t.Helper()
	ia, ib := -1, -1
	for i, name := range g.Names() {
		switch name {
		case a:
			ia = i
		case b:
			ib = i
		}
	}
	if ia < 0 {
		g.t.Fatalf("testkit: 组件 %s 不在图上（实际：%v）", a, g.Names())
	}
	if ib < 0 {
		g.t.Fatalf("testkit: 组件 %s 不在图上（实际：%v）", b, g.Names())
	}
	if ia >= ib {
		g.t.Fatalf("testkit: 期望 %s 早于 %s，实际顺序 %v", a, b, g.Names())
	}
}

// Stop 立刻停止这张图（不必等 Cleanup）。重复调用无害。
func (g *Graph) Stop() {
	if err := g.manager.Stop(context.Background()); err != nil {
		g.t.Errorf("testkit: 停止组件图失败: %v", err)
	}
}

// ---- 共享假实现 ----

// FakeCatalog 是 AgentCatalog 的最小实现：一张 map。
// 用 Set 一次装完，之后 Get/Names 就是它。
type FakeCatalog struct {
	agents     map[string]*agentapi.Agent
	facts      map[string]agentapi.AgentFacts
	installers map[string]agentapi.AgentInstaller
}

// NewCatalog 建一个空目录。
func NewCatalog() *FakeCatalog {
	return &FakeCatalog{agents: map[string]*agentapi.Agent{}}
}

// Add 注册一个 Agent，返回目录本身以便链式书写。
func (c *FakeCatalog) Add(agents ...*agentapi.Agent) *FakeCatalog {
	for _, a := range agents {
		c.agents[a.ID] = a
	}
	return c
}

// Get / Names / Facts / Installed 是 confighook.AgentCatalog。
func (c *FakeCatalog) Get(id string) (*agentapi.Agent, bool) {
	a, ok := c.agents[id]
	return a, ok
}

// Installer 说「这个 agent 交了安装方式没有」。默认没有；要验安装那条路用
// WithInstaller 塞一个假的（真的去跑 npm 会把测试变成联网测试）。
func (c *FakeCatalog) Installer(string) agentapi.AgentInstaller { return nil }

// WithInstaller 给某个 agent 塞一份假安装器。
func (c *FakeCatalog) WithInstaller(id string, in agentapi.AgentInstaller) *FakeCatalog {
	if c.installers == nil {
		c.installers = map[string]agentapi.AgentInstaller{}
	}
	c.installers[id] = in
	return c
}

// Facts 说「这个 agent 交了运行时事实没有」。假目录里一律没有——测试要验的是
// 「没交事实时走缺省」，那是**唯一**一份实现（confighook.TierOf），这里复刻一遍
// 等于把判据抄成两份。要验有事实的那条路，用 WithFacts。
func (c *FakeCatalog) Facts(string) agentapi.AgentFacts { return nil }

// WithFacts 给某个 agent 塞一份假事实（见 confighook.AgentFacts）。
func (c *FakeCatalog) WithFacts(id string, f agentapi.AgentFacts) *FakeCatalog {
	if c.facts == nil {
		c.facts = map[string]agentapi.AgentFacts{}
	}
	c.facts[id] = f
	return c
}

// Installed 走真实现那条判据（confighook.InstalledDefault），不另写一份：假目录与
// 真目录对「装没装」给出不同答案是这类判据最坏的失效方式。
func (c *FakeCatalog) Installed(id string) bool {
	return agentapi.InstalledDefault(c.agents[id], c.facts[id])
}

// Names 按字母序返回，与真实目录一致（真实实现排过序，测试不该依赖注册顺序）。
func (c *FakeCatalog) Names() []string {
	out := make([]string, 0, len(c.agents))
	for name := range c.agents {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// AsCatalog 返回接口形态，喂给 agentstate 或组件时用。
func (c *FakeCatalog) AsCatalog() agentapi.AgentCatalog { return c }

// InstallCatalog 把假目录接到 agentstate 兼容桥上，返回还原函数。
//
// agentstate 是历史遗留的全局桥（launch / injection 还走它读目录），
// 组件图里由 runtime 模块安装。测单个模块而不起 runtime 时用这个接上。
func InstallCatalog(t *testing.T, catalog agentapi.AgentCatalog) {
	t.Helper()
	restore := agentstate.Set(catalog)
	t.Cleanup(restore)
}

// ---- 内部 ----

type loader []modules.Component

func (l loader) Load() ([]modules.Component, error) { return l, nil }

func setEnv(vals map[string]string) (restore func()) {
	type prior struct {
		value string
		had   bool
	}
	saved := make(map[string]prior, len(vals))
	for k, v := range vals {
		old, had := os.LookupEnv(k)
		saved[k] = prior{old, had}
		_ = os.Setenv(k, v)
	}
	return func() {
		for k, p := range saved {
			if p.had {
				_ = os.Setenv(k, p.value)
			} else {
				_ = os.Unsetenv(k)
			}
		}
	}
}

func clearEnv(keys ...string) (restore func()) {
	type prior struct {
		value string
		had   bool
	}
	saved := make(map[string]prior, len(keys))
	for _, k := range keys {
		old, had := os.LookupEnv(k)
		saved[k] = prior{old, had}
		_ = os.Unsetenv(k)
	}
	return func() {
		for k, p := range saved {
			if p.had {
				_ = os.Setenv(k, p.value)
			}
		}
	}
}
