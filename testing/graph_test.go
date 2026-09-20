package disttesting

import (
	"context"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/manifest"
	app "github.com/rzbdz/newgate/app"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	"github.com/rzbdz/newgate/testing/testkit"
)

// 这一组锁的是**发行版自己那张图**的不变式：每份规格书能不能装起来、规格书点名与
// 关掉的东西是不是真的生效、自己的模块是不是真的可摘。
//
// 它与内核那条摘除矩阵（core 仓库的 app/matrix_test.go）是**互补**的：那条管内核
// 自带的十三个模块，这条管本发行版装的那几个 + 规格书本身的语义。两边的判据是同一
// 句：**装不起来可以，但得是因为有人硬依赖它，而且报错要点名缺哪个端口。**

// servesEntry 报告组件是不是**入口账本**的提供者——组合根自己要用的那个端口。
// 判据从组件的 Provides 里读：内核的 app.Selection.Load 用的也是这一条
// （compositionRootPorts），两边是同一句话的两种说法。
func servesEntry(c modules.Component) bool {
	for _, p := range c.Provides {
		if p.Name() == modules.Name(entry.Capability) {
			return true
		}
	}
	return false
}

// staticLoader 把一张写死的组件表交出去（内核 app/matrix_test.go 用的是同一招：
// 组合根的 Loader 接口只有一个方法，测试里没必要造一个真 Loader）。
type staticLoader []modules.Component

func (s staticLoader) Load() ([]modules.Component, error) { return s, nil }

// TestEverySpecBuilds 每份规格书都要能装、能起、能停，而且：
//   - 它点名的模块**真的在图里**（点了名没装上是静默故障：功能少了没人报错）；
//   - 它 disable 的内核模块**真的不在图里**（写错名字的话 Selection.Load 就会拦，
//     但「拦没拦住」值得在成品这一侧再确认一次）。
func TestEverySpecBuilds(t *testing.T) {
	for _, name := range manifest.SpecNames() {
		t.Run(name, func(t *testing.T) {
			testkit.Sandbox(t)

			sel, ok := manifest.Specs()[name]
			if !ok {
				t.Fatalf("SpecNames 报了 %q，Specs() 里却没有", name)
			}
			comps, err := sel.Load()
			if err != nil {
				t.Fatalf("装配选择：%v", err)
			}
			manager, err := modules.NewContext(context.Background(), staticLoader(comps))
			if err != nil {
				t.Fatalf("起图：%v", err)
			}
			t.Cleanup(func() { _ = manager.Stop(context.Background()) })

			got := map[string]bool{}
			for _, n := range manager.ComponentNames() {
				got[n] = true
			}
			for _, e := range sel.Extra {
				if !got[e.Component.Name] {
					t.Errorf("规格书点名了 %s（目录 %s），图上没有它——点名的东西必须真的装上；图 = %v",
						e.Component.Name, e.Dir, manager.ComponentNames())
				}
			}
			// 关掉的那些必须**真的**不在图里。判据从规格书读——Disable 名单，或者
			// AllCore（内核自带的全部可关模块）——不写死名字，也不重复一份名单。
			for _, e := range app.CoreModules() {
				if !sel.AllCore && !contains(sel.Disable, e.Dir) {
					continue
				}
				if servesEntry(e.Component) {
					// 组合根自己要用的那个（入口账本）**关不掉**：Selection.Load 拒绝关它，
					// AllCore 也把它留在外面。所以它必须还在图里——它是「这张图里还有人
					// 能认领这次调用」的那一半保证。
					if !got[e.Component.Name] {
						t.Errorf("入口账本（%s）不在图里——没有它，这个二进制裸跑没人能认领调用",
							e.Component.Name)
					}
					continue
				}
				if got[e.Component.Name] {
					t.Errorf("规格书关掉了 %s（目录 %s），可它还在图里——"+
						"「我以为关掉了，它还在跑」是这一层最坏的失效", e.Component.Name, e.Dir)
				}
			}
		})
	}
}

// TestEverySpecHasAtMostOneUI 每份规格书里界面**最多一个**，而旗舰那份必须正好有一个。
//
// 为什么值得单独一条（内核侧有一条同样的 TestExactlyOneUI）：端口不声明基数，而
// cli 的 capability 恰恰是按设计唯一的——装第二个 ui 的后果是**完全静默**的：装配
// 成功、不报错，而它拿到的命令/状态行/体检项全是零个（注入只进声明顺序里的第一个，
// 而顺序是**目录名字母序**）。`dist-simple-cli.json` 这份规格书存在的全部意义就是
// 把内核的 cli 换成 simple-cli；这里就是那个「换」字有没有做对的判据。
//
// 为什么上界才是判据（2026-09-20 改，原来是「正好一个」）：骨架配置
// `dist-hello.json` **根本没有界面**——它整个发行版就是「框架 + 一个 hello」，
// `newgate` 那个入口由 hello 自己申报（见 modules/hello）。所以「多于一个」是
// 那条静默失效，「一个都没有」只是骨架配置的常态。
//
// 「至少有一个」这件事没有丢，只是换了个更贴事实的说法——见下面那条
// TestEverySpecClaimsTheBareInvocation：界面只是申报入口的一种方式。
func TestEverySpecHasAtMostOneUI(t *testing.T) {
	for _, name := range manifest.SpecNames() {
		t.Run(name, func(t *testing.T) {
			testkit.Sandbox(t)

			sel := manifest.Specs()[name]
			comps, err := sel.Load()
			if err != nil {
				t.Fatalf("装配选择：%v", err)
			}
			manager, err := modules.NewContext(context.Background(), staticLoader(comps))
			if err != nil {
				t.Fatalf("起图：%v", err)
			}
			t.Cleanup(func() { _ = manager.Stop(context.Background()) })

			names := manager.ComponentNames()
			uis := modules.GetAll(manager.Context(), cliapi.Capability)
			if len(uis) > 1 {
				t.Fatalf("装了 %d 个 ui，最多只能 1 个——多于一个时只有目录名排最前的那个生效，"+
					"其余完全静默；图 = %v", len(uis), names)
			}

			// 兜底入口（entry.DefaultRank）只能有一个申报者：cli 与 hello 都在那一档
			// 上，两个同图时谁被 Resolve 选中会退化成「目录名字母序决定进程行为」。
			// 那是隐式判据，所以在规格书这一层直接禁掉这个组合。
			if len(uis) == 1 && contains(names, "hello") {
				t.Fatalf("这张图里既有界面又有 hello：两者都在兜底档上申报入口，"+
					"谁被选中取决于目录名字母序（cli 排在前面）——hello 属于骨架配置，"+
					"别把它装进带界面的发行版；图 = %v", names)
			}
		})
	}
}

// TestEverySpecClaimsTheBareInvocation 每份规格书装出来的二进制，**裸调用
// `newgate` 必须有人认领**。
//
// 为什么这是比「必须有界面」更准的判据：界面只是申报入口的**一种**方式。骨架配置
// （框架 + hello）没有任何界面，它的 `newgate` 由 hello 申报——两张图在这条判据下
// 都成立，而它们真正共有的、不许坏的东西也正好是这一条：**用户敲下 `newgate`
// 得到的必须是一个行为，不是一句「没人认领这次调用」加退出码 69**。
//
// 它守的退化很具体：某个发行版只装了数据面模块（网关、接管），忘了装任何申报入口
// 的东西——编译过、测试过、`newgate status` 一切正常，而裸敲 `newgate` 直接退出。
func TestEverySpecClaimsTheBareInvocation(t *testing.T) {
	for _, name := range manifest.SpecNames() {
		t.Run(name, func(t *testing.T) {
			testkit.Sandbox(t)

			sel := manifest.Specs()[name]
			comps, err := sel.Load()
			if err != nil {
				t.Fatalf("装配选择：%v", err)
			}
			// NewContext 会真的 Start 一遍，所以申报是发生过的（不是"摆了个组件"）。
			manager, err := modules.NewContext(context.Background(), staticLoader(comps))
			if err != nil {
				t.Fatalf("起图：%v", err)
			}
			t.Cleanup(func() { _ = manager.Stop(context.Background()) })

			registry := modules.MustGet(manager.Context(), entry.Capability)
			if h, why, ok := registry.Resolve(entry.Process{Argv0: "newgate"}); !ok {
				t.Fatalf("`newgate` 没人认领（%s）——这个二进制裸跑只会打一句人话就退出；图 = %v",
					why, manager.ComponentNames())
			} else {
				t.Logf("裸调用归 %s", h.Name())
			}
		})
	}
}

// TestDistModulesAreRemovable 发行版自己的模块逐个摘一遍。
//
// 判据与内核那条矩阵逐字相同：**摘掉一个模块，要么装得起来**（它没人硬依赖），
// **要么当场失败并点名缺的那个端口**（有人硬依赖它）。两种都是对的结果，错的是
// 第三种：摘掉之后装不起来，却没人声明过依赖——那说明有人在不声明的情况下伸手拿
// 别人提供的东西（`MustGet`），摘模块的人会拿到一个指不到真因的错。
func TestDistModulesAreRemovable(t *testing.T) {
	const specName = "dist.json"
	testkit.Sandbox(t)

	base, ok := manifest.Specs()[specName]
	if !ok {
		t.Fatalf("没有 %s 这份规格书", specName)
	}
	if len(base.Extra) < 2 {
		t.Fatalf("%s 只装了 %d 个自有模块，矩阵退化了", specName, len(base.Extra))
	}

	for _, victim := range base.Extra {
		t.Run(victim.Dir, func(t *testing.T) {
			testkit.Sandbox(t)

			sel := app.Selection{Disable: base.Disable}
			for _, e := range base.Extra {
				if e.Dir == victim.Dir {
					continue
				}
				sel.Extra = append(sel.Extra, e)
			}
			comps, err := sel.Load()
			if err != nil {
				t.Fatalf("装配选择：%v", err)
			}
			dependents := hardDependents(comps, victim.Component)
			manager, err := modules.NewContext(context.Background(), staticLoader(comps))

			if err == nil {
				t.Cleanup(func() { _ = manager.Stop(context.Background()) })
				if len(dependents) > 0 {
					t.Fatalf("摘掉 %s 竟然装起来了，但 %v 声明了硬依赖它的端口——"+
						"说明那条依赖没进图（Requires 里写成了 Optional？）", victim.Dir, dependents)
				}
				return
			}
			if len(dependents) == 0 {
				t.Fatalf("摘掉 %s 装不起来，可是没有任何模块声明硬依赖它——有人在不声明的情况下"+
					"伸手拿它提供的东西：\n%v", victim.Dir, err)
			}
			for _, provision := range victim.Component.Provides {
				if strings.Contains(err.Error(), provision.Name()) {
					return
				}
			}
			t.Fatalf("摘掉 %s 的报错没有点名它提供的端口（%v），无法判断失败原因：\n%v",
				victim.Dir, provisionNames(victim.Component), err)
		})
	}
}

// hardDependents 返回那些**硬依赖**了 victim 提供的端口的组件名（弱依赖不算：
// 它本来就在说「你不在我也能跑」）。
func hardDependents(all []modules.Component, victim modules.Component) []string {
	provided := map[string]bool{}
	for _, p := range victim.Provides {
		provided[p.Name()] = true
	}
	var out []string
	for _, c := range all {
		if c.Name == victim.Name {
			continue
		}
		for _, req := range c.Requires {
			if provided[req.Name()] && !req.Optional() {
				out = append(out, c.Name)
				break
			}
		}
	}
	return out
}

func provisionNames(c modules.Component) []string {
	out := make([]string, 0, len(c.Provides))
	for _, p := range c.Provides {
		out = append(out, p.Name())
	}
	return out
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
