package manifest

import (
	"testing"

	modules "github.com/rzbdz/newgate/component"
)

// 这条守的是**生成物 order_gen.go 可信**这件事。
//
// 它是构建期算好冻下来的（见 tools/distgen/orderprobe），运行期只做一次核对
// （排列对不对），**不核对它是不是真的拓扑序**——那是构图期的事。所以「这份表
// 到底对不对」必须有人在**这里**看着，否则它是一份没人验过的冻结数据。
//
// 两件事一起验，缺一条都不够：
//
//  1. 它**有效**：每份规格书按这个顺序装出来的图，`Resolve` 走的是快路径，
//     而且产出的顺序与它一模一样；
//  2. 它**不是装饰**：顺序必须真的被用上（Selection.Load 排过），否则这份表
//     过期了也没人知道。
func TestGeneratedOrderMatchesResolve(t *testing.T) {
	specs := Specs()
	if len(specs) == 0 {
		t.Fatal("一份规格书都没有")
	}
	_ = specs
	for _, name := range SpecNames() {
		loader := Loader{Spec: name}
		order := loader.PrecomputedOrder()
		if len(order) == 0 {
			t.Errorf("%s 没有启动顺序——order_gen.go 没生成？"+
				"（跑 `go run ./tools/distgen`）", name)
			continue
		}

		// ① 按图纸装：必须装得起来，而且顺序就是图纸上那个。
		//
		// 这一条同时是「运行期真的走了图纸」的证据：`Loader` 实现了
		// component.Precomputed，所以 Resolve 不会去建边排序（见
		// component.planFromOrder）。名字对不上、数量对不上都当场报错。
		plan, err := modules.Resolve(loader)
		if err != nil {
			t.Errorf("%s 按图纸装不起来: %v", name, err)
			continue
		}
		if got := plan.Names(); !sameStrings(got, order) {
			t.Errorf("%s 的实际顺序与图纸不一致：\n  图纸: %v\n  实际: %v", name, order, got)
		}

		// ② 图纸必须**等于「从头算」的答案**。这一条是最要紧的：图纸是构建期
		// 冻下来的，而运行期不再校验它是不是真的拓扑序（那是构建期的事）。
		// 少了这一条，一张排错了的图纸没有任何人看着。
		components, err := Specs()[name].Load()
		if err != nil {
			t.Errorf("%s 从头装不起来: %v", name, err)
			continue
		}
		fresh, err := modules.Resolve(staticLoader(components))
		if err != nil {
			t.Errorf("%s 从头构图失败: %v", name, err)
			continue
		}
		if got := fresh.Names(); !sameStrings(got, order) {
			t.Errorf("%s 的图纸与「从头算」的结果不一致（清单过期了？）：\n"+
				"  图纸: %v\n  从头: %v", name, order, got)
		}
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type staticLoader []modules.Component

func (s staticLoader) Load() ([]modules.Component, error) { return s, nil }
