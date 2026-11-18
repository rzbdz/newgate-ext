package extension

import "testing"

// 槽位规则的守门人。这套规则是**约定**，没有类型系统兜底，所以只能靠测试钉住
// 「通用键优先、自定义先到先得、抢不到进 others、顺序稳定」这四条。

func TestGeneralSectionsKeepTheirPublishedOrder(t *testing.T) {
	// 故意打乱声明顺序：通用节的显示顺序由 PlanSections 决定，与声明顺序无关
	// ——否则一个模块调换自己的注册时机就能把整节搬走。
	plan := PlanSections([]Section{SectionMaintenance, SectionTakeover, SectionObserve})
	if got := plan.Slot(SectionMaintenance); got != SectionMaintenance {
		t.Fatalf("维护 落到了 %q", got)
	}
	want := []Section{SectionTakeover, SectionRunOnce, SectionRouting,
		SectionObserve, SectionMaintenance, SectionModules, SectionUI, SectionOthers}
	got := plan.Order()
	if len(got) != len(want) {
		t.Fatalf("Order() = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Order()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCustomSectionsAreFirstComeAndBounded(t *testing.T) {
	declared := []Section{"甲", "乙", "丙", "丁", "戊", "己"}
	plan := PlanSections(declared)
	for _, name := range []Section{"甲", "乙", "丙", "丁"} {
		if got := plan.Slot(name); got != name {
			t.Fatalf("%q 本该拿到自己的节，落到了 %q", name, got)
		}
	}
	// 超出 MaxCustomSections 的两个抢不到名额。
	for _, name := range []Section{"戊", "己"} {
		if got := plan.Slot(name); got != SectionOthers {
			t.Fatalf("%q 本该进 others，落到了 %q", name, got)
		}
	}
	order := plan.Order()
	// 自定义节按**声明顺序**排在通用节之后、others 之前。这里只看尾巴：
	// 最后三个是前两个自定义节 + 兜底节。
	tail := order[len(order)-3:]
	if tail[0] != "丙" || tail[1] != "丁" || tail[2] != SectionOthers {
		t.Fatalf("自定义节没有按先到先得排在 others 之前：%v", tail)
	}
	// 通用节一个不少地排在自定义节前面。
	if order[len(generalSections)] != "甲" {
		t.Fatalf("自定义节没有紧跟通用节：%v", order)
	}
}

func TestEmptyAndUnknownSectionsFallBack(t *testing.T) {
	plan := PlanSections([]Section{"", "没人认识我"})
	if got := plan.Slot(""); got != SectionOthers {
		t.Fatalf("空节名落到了 %q", got)
	}
	// 没声明过的名字也该给一个安全的答案（渲染路径上不该出现问号）。
	if got := plan.Slot("从没出现过"); got != SectionOthers {
		t.Fatalf("未声明节名落到了 %q", got)
	}
	if got := PlanSections(nil).Order(); len(got) == 0 {
		t.Fatal("没有命令时 Order() 也该是可渲染的（至少含 others）")
	}
}

func TestSlotReturnsACopyOfTheOrder(t *testing.T) {
	plan := PlanSections(nil)
	order := plan.Order()
	order[0] = "被改坏了"
	if plan.Order()[0] == "被改坏了" {
		t.Fatal("Order() 返回了内部切片，调用方能改坏槽位表")
	}
}
