package domain

import "testing"

// TestCandidatesForNormalFallsBackToMid 四档化（2026-09-16）的向下兼容：
// normal 是新加的**主力档**，老配置里没有它，缺省时用 mid 顶上。
//
// 为什么不是「整层跳过」：跳过会让没写 normal 的配置在主力档上没有候选
// （链空转、claude 主循环直接 404），而四档化之前主循环用的就是 mid 那一档
// 的资源——语义上就该跟 mid 一样。
//
// 这条兼容关系必须留在 profile 内：如果返回 @mid，BuildChain 会在每个
// profile 上反复展开整条跨-profile mid 链，把正常候选报成几十条「重复」。
// 显式 @mid 和模块槽位引用仍保持跨 profile 展开。
func TestCandidatesForNormalFallsBackToMid(t *testing.T) {
	mid := Candidates{{Provider: "p", Model: "sonnet-class"}}
	heavy := Candidates{{Provider: "p", Model: "fable-class"}}
	normal := Candidates{{Provider: "p", Model: "opus-class"}}

	old := &Profile{Roles: map[string]Candidates{"heavy": heavy, "mid": mid}}
	if got := old.CandidatesFor("normal"); len(got) != 1 || got[0].Model != "sonnet-class" {
		t.Errorf("没写 normal 时应复用本 profile 的 mid，实际 %v", got)
	}
	// 引用走到底是真正干活的地方要的结果
	if b, ok := old.Resolve("normal"); !ok || b.Model != "sonnet-class" {
		t.Errorf("normal 解析出来该是 mid 的候选，实际 %v %v", b, ok)
	}

	// 写了 normal 就用自己的，跟 mid 无关
	withNormal := &Profile{Roles: map[string]Candidates{
		"heavy": heavy, "normal": normal, "mid": mid}}
	if got := withNormal.CandidatesFor("normal"); got[0].Model != "opus-class" {
		t.Errorf("写了 normal 应该用自己的，实际 %v", got)
	}
	// 其余档位不受这条规则影响
	if got := withNormal.CandidatesFor("heavy"); got[0].Model != "fable-class" {
		t.Errorf("heavy 被 normal 的规则带跑了: %v", got)
	}
	if got := withNormal.CandidatesFor("mid"); got[0].Model != "sonnet-class" {
		t.Errorf("mid 被改了: %v", got)
	}

	// 连 mid 都没有：退回通配 / fallback 的老路，而不是凭空空转
	bare := &Profile{Roles: map[string]Candidates{"heavy": heavy},
		Fallback: &Binding{Provider: "p", Model: "fallback"}}
	if b, ok := bare.Resolve("normal"); !ok || b.Model != "fallback" {
		t.Errorf("没有 mid 时应走 fallback，实际 %v %v", b, ok)
	}
}

// TestRolesOrder 档位序即能力从高到低——status / doctor / TUI 都按它排，
// 顺手钉死四档化的结果。
func TestRolesOrder(t *testing.T) {
	want := []string{"heavy", "normal", "mid", "light", "vision"}
	if len(Roles) != len(want) {
		t.Fatalf("Roles = %v，想要 %v", Roles, want)
	}
	for i := range want {
		if Roles[i] != want[i] {
			t.Fatalf("Roles = %v，想要 %v", Roles, want)
		}
	}
}

// TestReaddedEmptyTierInheritsParent：删除一个档位再空加回去，不该盖掉父的候选。
//
// 现场（2026-09-20）：界面里删掉 vision、再添加一个空档位，applyProfileRoles 把
// `vision: []` 写进文件；MergeFrom 原来用这个空数组**盖掉**父 profile 的 vision，
// 于是那一档解析不出任何东西——用户以为 binding 逻辑坏了。这里钉住：空档位 = 没写，
// 父的候选照常继承。
func TestReaddedEmptyTierInheritsParent(t *testing.T) {
	base := &Profile{Roles: map[string]Candidates{
		"heavy":  {{Provider: "p", Model: "fable"}},
		"vision": {{Provider: "p", Model: "v-class"}},
	}}
	child := &Profile{Roles: map[string]Candidates{
		"vision": {}, // 删除后又空加回来
	}}

	child.MergeFrom(base)
	got := child.CandidatesFor("vision")
	if len(got) != 1 || got[0].Model != "v-class" {
		t.Fatalf("空档位该继承父的候选，实际 %v", got)
	}
}

// TestExplicitCandidatesStillOverrideParent：反过来——子写了真候选，父的就要被盖。
// 这条守的是合并的方向不能反过来（继承是「缺了才借」，不是「有也来添乱」）。
func TestExplicitCandidatesStillOverrideParent(t *testing.T) {
	base := &Profile{Roles: map[string]Candidates{
		"normal": {{Provider: "p", Model: "opus-parent"}},
	}}
	child := &Profile{Roles: map[string]Candidates{
		"normal": {{Provider: "q", Model: "opus-child"}},
	}}
	child.MergeFrom(base)
	got := child.CandidatesFor("normal")
	if len(got) != 1 || got[0].Provider != "q" || got[0].Model != "opus-child" {
		t.Fatalf("显式候选要被尊重，父的不能盖过它: %v", got)
	}
}
