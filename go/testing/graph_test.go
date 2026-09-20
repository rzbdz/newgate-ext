package disttesting

import (
	"context"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/go/manifest"
	app "github.com/rzbdz/newgate/go/app"
	modules "github.com/rzbdz/newgate/go/component"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	"github.com/rzbdz/newgate/go/testing/testkit"
)

// 这一组锁的是**发行版自己那张图**的不变式：每份规格书能不能装起来、规格书点名与
// 关掉的东西是不是真的生效、自己的模块是不是真的可摘。
//
// 它与内核那条摘除矩阵（core 仓库的 app/matrix_test.go）是**互补**的：那条管内核
// 自带的十三个模块，这条管本发行版装的那几个 + 规格书本身的语义。两边的判据是同一
// 句：**装不起来可以，但得是因为有人硬依赖它，而且报错要点名缺哪个端口。**

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
			for _, e := range app.CoreModules() {
				if !contains(sel.Disable, e.Dir) {
					continue
				}
				if got[e.Component.Name] {
					t.Errorf("规格书 disable 点了 %s（目录 %s），可它还在图里——"+
						"「我以为关掉了，它还在跑」是这一层最坏的失效", e.Component.Name, e.Dir)
				}
			}
		})
	}
}

// TestEverySpecHasExactlyOneUI 每份规格书里**界面只能有一个**。
//
// 为什么值得单独一条（内核侧有一条同样的 TestExactlyOneUI）：端口不声明基数，而
// cli 的 capability 恰恰是按设计唯一的——装第二个 ui 的后果是**完全静默**的：装配
// 成功、不报错，而它拿到的命令/状态行/体检项全是零个（注入只进声明顺序里的第一个，
// 而顺序是**目录名字母序**）。`dist-simple-cli.json` 这份规格书存在的全部意义就是
// 把内核的 cli 换成 simple-cli；这里就是那个「换」字有没有做对的判据。
func TestEverySpecHasExactlyOneUI(t *testing.T) {
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

			if uis := modules.GetAll(manager.Context(), cliapi.Capability); len(uis) != 1 {
				t.Fatalf("装了 %d 个 ui，应当正好 1 个——多于一个时只有目录名排最前的那个生效，"+
					"其余完全静默；图 = %v", len(uis), manager.ComponentNames())
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
