package kernel

import (
	"context"
	app "github.com/rzbdz/newgate/app"
	"testing"

	pluginmanagerapi "github.com/rzbdz/newgate-ext/modules/pluginmanager"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	modules "github.com/rzbdz/newgate/component"
)

// TestPluginMetadataIsConsistent 守两件没有编译期保护、失效又都是静默的事：
//
//  1. **模块名与组件名不一致**。RegisterSelf 传的 name 是开关点 Path 的前缀，
//     而 newgate plugin 是拿组件图去对账的。对不上时那一行永远不会有人读——
//     列表里看着一切正常，只是这个模块的开关是空的。
//  2. **组件的 Type 是空的**。resolve 只拒绝空值、不校验取值（内核不认识取值，
//     这是有意的），所以「空」是唯一能在装配期拦下的情形。
//
// 形状同 TestGeneratedModuleListIsCurrent：都是「漏登记」类问题的守门人，都靠
// 一条测试而不是靠类型系统。
//
// 刻意**不校验** Type 在不在 DisplayOrder 里：未知分类归 others 展示是正确的
// 降级，不是错误——分类是产品概念，会随版本长出新成员，硬拒绝会让一个新模块
// 因为用了个新分类词就把整个 newgate plugin 打崩。
func TestPluginMetadataIsConsistent(t *testing.T) {
	testkit.Sandbox(t)
	built, err := app.New(context.Background(), staticLoader(kernelGraph(t)))
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })

	components := built.Components()
	inGraph := map[string]bool{}
	for _, c := range components {
		if c.Type == "" {
			t.Fatalf("组件 %q 没有 Type —— resolve 本该在装配期就拦下它", c.Name)
		}
		inGraph[c.Name] = true
	}
	if len(components) == 0 {
		t.Fatal("组件图为空")
	}

	pm := modules.MustGet(built.Context(), pluginmanagerapi.Capability)
	listed := pm.Modules()
	if len(listed) < len(components) {
		t.Fatalf("newgate plugin 只列出 %d 个模块，图里有 %d 个 —— 枚举源必须是图，不是注册表",
			len(listed), len(components))
	}
	for _, m := range listed {
		if !inGraph[m.Name] {
			t.Fatalf("模块 %q 上报了自己（开关点 %d 个），但图里没有这个组件名 —— "+
				"RegisterSelf 的 name 必须与 Component.Name 一致，否则它那些开关点永远不会有人读",
				m.Name, len(m.Switches))
		}
	}
}
