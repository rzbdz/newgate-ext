package resolve

import (
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
)

// ---------- PrimaryBinding ----------

func TestPrimaryBindingReturnsChainHead(t *testing.T) {
	ps := []*domain.Profile{
		{Name: "cheap", Priority: prio(10), Roles: map[string]domain.Candidates{
			"heavy": list("ds/deepseek-chat", "glm/glm-4-plus")}},
	}
	b, ok := PrimaryBinding("heavy", ps, mkProvs("ds", "glm"), "cheap")
	if !ok || b.String() != "ds/deepseek-chat" {
		t.Errorf("PrimaryBinding = %v, %v", b, ok)
	}
}

func TestPrimaryBindingSparseFallsThrough(t *testing.T) {
	// 链头没定义 heavy，链落到下一个 profile，显示名应是那个候选。
	ps := []*domain.Profile{
		{Name: "sparse", Priority: prio(5), Roles: map[string]domain.Candidates{}},
		{Name: "full", Priority: prio(10), Roles: map[string]domain.Candidates{
			"heavy": one("glm", "glm-4-plus")}},
	}
	b, ok := PrimaryBinding("heavy", ps, mkProvs("glm"), "sparse")
	if !ok || b.String() != "glm/glm-4-plus" {
		t.Errorf("PrimaryBinding = %v, %v", b, ok)
	}
}

func TestPrimaryBindingNoCandidate(t *testing.T) {
	ps := []*domain.Profile{{Name: "x", Roles: map[string]domain.Candidates{}}}
	if _, ok := PrimaryBinding("heavy", ps, mkProvs(), "x"); ok {
		t.Error("没有任何候选时应 ok=false")
	}
}

// ---------- ResolveRequest ----------

func TestResolveRequest_TierName(t *testing.T) {
	ps := []*domain.Profile{{Name: "a", Roles: map[string]domain.Candidates{
		"heavy": one("ds", "deepseek-chat")}}}
	steps, _, tier := ResolveRequest("heavy", "a", ps, mkProvs("ds"), Opts{Active: "a"})
	if tier != "heavy" {
		t.Errorf("tier = %q", tier)
	}
	eq(t, names(steps), "a:ds/deepseek-chat")
}

func TestResolveRequest_ConcreteModelRoutesToTier(t *testing.T) {
	ps := []*domain.Profile{
		{Name: "ds", Roles: map[string]domain.Candidates{
			"heavy": one("ds", "deepseek-chat"),
			"light": one("ds", "deepseek-flash")}},
	}
	steps, _, tier := ResolveRequest("deepseek-chat", "ds", ps, mkProvs("ds"), Opts{Active: "ds"})
	if tier != "heavy" {
		t.Errorf("tier = %q，应为 heavy", tier)
	}
	eq(t, names(steps), "ds:ds/deepseek-chat")
}

func TestResolveRequest_ConcreteModelMovedToFront(t *testing.T) {
	// heavy 链是 [deepseek-chat, glm-4-plus]；客户端点名 glm-4-plus，
	// 它应该被挪到最前，deepseek-chat 跟在后面。
	ps := []*domain.Profile{
		{Name: "a", Roles: map[string]domain.Candidates{
			"heavy": list("ds/deepseek-chat", "glm/glm-4-plus")}},
	}
	steps, _, tier := ResolveRequest("glm-4-plus", "a", ps, mkProvs("ds", "glm"), Opts{Active: "a"})
	if tier != "heavy" {
		t.Errorf("tier = %q", tier)
	}
	eq(t, names(steps), "a:glm/glm-4-plus", "a:ds/deepseek-chat")
}

func TestResolveRequest_UnknownModel(t *testing.T) {
	ps := []*domain.Profile{{Name: "a", Roles: map[string]domain.Candidates{
		"heavy": one("ds", "deepseek-chat")}}}
	steps, _, tier := ResolveRequest("no-such-model", "a", ps, mkProvs("ds"), Opts{Active: "a"})
	if tier != "" || len(steps) != 0 {
		t.Errorf("未知模型应返回空链和空 tier，得到 tier=%q steps=%v", tier, names(steps))
	}
}

// TestResolveRequest_KnownModelUnavailableFallsBackToTier 锁 2026-11-04 现场：
//
//	model deepseek-flash 既不是已知档位（…），也不在任何 profile 的绑定里
//
// ——而它明明绑在 ds 里。触发条件是唯一绑定了它的那条 binding 被熔断：
// 反查原先在**带准入过滤的链**上做，模型从链上被摘掉，于是「不可用」被
// 误报成「不认识」。认出来的档位链还有别的可用候选时，就该按那条链走
// （fallback 本来干这个），并把被摘掉的原因留在 skips 里（不静默）。
func TestResolveRequest_KnownModelUnavailableFallsBackToTier(t *testing.T) {
	ps := []*domain.Profile{
		{Name: "a", Roles: map[string]domain.Candidates{
			"mid": one("a", "a-model")}},
		{Name: "ds", Priority: prio(19), Roles: map[string]domain.Candidates{
			"mid": one("ds", "deepseek-flash")}},
	}
	deny := func(provider, model string) (bool, string) {
		if model == "deepseek-flash" {
			return false, "circuit open"
		}
		return true, ""
	}
	steps, skips, tier := ResolveRequest("deepseek-flash", "a", ps,
		mkProvs("a", "ds"), Opts{Active: "a", Available: deny})
	if tier != "mid" {
		t.Fatalf("模型绑在 mid 上，tier 应为 mid（不可用不等于不认识），得到 %q", tier)
	}
	eq(t, names(steps), "a:a/a-model")
	found := false
	for _, sk := range skips {
		if sk.Kind == SkipUnavailable && strings.Contains(sk.Reason, "circuit open") {
			found = true
		}
	}
	if !found {
		t.Errorf("被摘掉的点名 binding 应留下跳过原因（circuit open），得到 %+v", skips)
	}
}

// TestResolveRequest_KnownModelUnavailableIsNotUnknown：点名 profile 自己
// （探活/点名的调用方会把 profile 集合收敛到这一份）唯一候选被摘掉时，tier
// 仍然要认出来——链为空是「现在没有可用候选」，不是「这个名字不认识」。
func TestResolveRequest_KnownModelUnavailableIsNotUnknown(t *testing.T) {
	ps := []*domain.Profile{{Name: "ds", Roles: map[string]domain.Candidates{
		"mid": one("ds", "deepseek-flash")}}}
	deny := func(provider, model string) (bool, string) { return false, "circuit open" }
	steps, _, tier := ResolveRequest("deepseek-flash", "ds", ps,
		mkProvs("ds"), Opts{Active: "ds", Available: deny})
	if tier != "mid" || len(steps) != 0 {
		t.Errorf("应得到 tier=mid、空链（现在不可用），得到 tier=%q steps=%v", tier, names(steps))
	}
}

// TestResolveRequest_KnownModelPastMaxSteps：反查不能先在截断后的链上做。
// 点名模型排在 maxAttempts 之后时，它仍然是客户端**点名要的**那个，必须
// 挪到链头；截断只决定后面还能跟几个 fallback。
func TestResolveRequest_KnownModelPastMaxSteps(t *testing.T) {
	ps := []*domain.Profile{{Name: "a", Roles: map[string]domain.Candidates{
		"mid": list("pA/m1", "pB/m2", "pC/deepseek-flash")}}}
	steps, _, tier := ResolveRequest("deepseek-flash", "a", ps,
		mkProvs("pA", "pB", "pC"), Opts{Active: "a", MaxSteps: 2})
	if tier != "mid" {
		t.Fatalf("tier = %q，应为 mid", tier)
	}
	eq(t, names(steps), "a:pC/deepseek-flash", "a:pA/m1")
}

// TestResolveRequest_AmbiguousModelPrefersMid 一对多反查的优先级（2026-09-09
// 实抓回归）：glm-5.3 同时绑 heavy 和 mid（常见配置），客户端发来的名字
// 大多来自 sonnet 槽（主循环/分类器/总结），必须解析成 mid——按 Roles
// 原序命中 heavy 会把 mid 请求硬说成 heavy，下游按档位做决定的插件全部
// 失真（现场：分类器被 claude-bg 跳过，27 秒一条）。
func TestResolveRequest_AmbiguousModelPrefersMid(t *testing.T) {
	ps := []*domain.Profile{{Name: "glm", Priority: prio(20), Roles: map[string]domain.Candidates{
		"heavy": list("glm/glm-5.3", "ds/claude-opus"),
		"mid":   list("glm/glm-5.3", "ds/deepseek-chat"),
	}}}
	steps, _, tier := ResolveRequest("glm-5.3", "glm", ps, mkProvs("glm", "ds"), Opts{Active: "glm"})
	if tier != "mid" {
		t.Fatalf("同名填 heavy+mid 应解析成 mid，实际 %q", tier)
	}
	// 链也用 mid 的：点名模型在头，后面跟 mid 的候选（deepseek-chat），
	// 不是 heavy 的 claude-opus。
	eq(t, names(steps), "glm:glm/glm-5.3", "glm:ds/deepseek-chat")
}

// TestRealNameRoleOrderIsPermutation 优先序必须是 Roles 的重排——漏一个
// 档位会让那个档位独占的模型永远解析不出 tier。
func TestRealNameRoleOrderIsPermutation(t *testing.T) {
	if len(realNameRoleOrder) != len(domain.Roles) {
		t.Fatalf("realNameRoleOrder 与 Roles 数量不一致: %v vs %v", realNameRoleOrder, domain.Roles)
	}
	seen := map[string]bool{}
	for _, r := range realNameRoleOrder {
		if seen[r] {
			t.Errorf("重复档位: %s", r)
		}
		seen[r] = true
		if !domain.IsRole(r) {
			t.Errorf("未知档位: %s", r)
		}
	}
	for _, r := range domain.Roles {
		if !seen[r] {
			t.Errorf("漏了档位: %s", r)
		}
	}
}

func TestResolveRequest_IsRoleIsExact(t *testing.T) {
	// 一个恰好叫 "light" 的模型名会被当成档位名——这是有意的约定。
	if !domain.IsRole("light") {
		t.Fatal("light 应是档位名")
	}
	if domain.IsRole("deepseek-chat") {
		t.Fatal("deepseek-chat 不是档位名")
	}
}
