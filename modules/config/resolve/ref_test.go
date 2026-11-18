package resolve

import (
	"encoding/json"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
)

// TestRefSplicesInPlace 引用（`@别键`）就地展开成那条链——文档说的「一个
// 槽位可以绑到某条档位链上，也可以自己是一条链」，靠的就是这一条语义。
//
// 展开顺序仍是「profile 为主序、list 为次序」：引用在自己的位置上把被引用
// 的链整条插进来，不改变其余候选的相对次序。
func TestRefSplicesInPlace(t *testing.T) {
	profiles := []*domain.Profile{
		{Name: "head", Priority: prio(10), Roles: map[string]domain.Candidates{
			"normal": one("p1", "opus"),
			// 槽位自己是一条链：先跟 normal 走，normal 全挂了再用自己的兜底
			"omo-sisyphus": append(list("@normal"), domain.Binding{Provider: "p9", Model: "fallback"}),
		}},
		{Name: "b", Priority: prio(20), Roles: map[string]domain.Candidates{
			"normal": one("p2", "sonnet"),
		}},
	}
	provs := mkProvs("p1", "p2", "p9")

	steps, _ := BuildChain("omo-sisyphus", profiles, provs, Opts{Active: "head"})
	eq(t, names(steps),
		"head:p1/opus", // @normal 展开：链头 profile 的 normal
		"b:p2/sonnet",  // @normal 展开：下一个 profile 的 normal
		"head:p9/fallback",
	)
}

// TestRefWholeKeyIsAlias 整个键就是一条引用时，结果与「这个键有别名到那里」
// 逐字相同——两条路（别名 / 引用展开）在产品语义上是一回事，实现上别名更便宜。
func TestRefWholeKeyIsAlias(t *testing.T) {
	ref := []*domain.Profile{{Name: "head", Priority: prio(10), Roles: map[string]domain.Candidates{
		"normal":        one("p1", "opus"),
		"omo-librarian": list("@normal"),
	}}}
	alias := []*domain.Profile{{Name: "head", Priority: prio(10), Roles: map[string]domain.Candidates{
		"normal": one("p1", "opus"),
	}}}
	domain.SetExtraRoles([]domain.ExtraRole{
		{Key: "omo-librarian", Source: "test", Default: "normal"}})
	defer domain.SetExtraRoles(nil)

	provs := mkProvs("p1")
	a, _ := BuildChain("omo-librarian", ref, provs, Opts{Active: "head"})
	b, _ := BuildChain("omo-librarian", alias, provs, Opts{Active: "head"})
	eq(t, names(a), "head:p1/opus")
	eq(t, names(b), "head:p1/opus")
}

// TestRefCycleDoesNotHang 引用成环要停下来并说明，而不是无限展开。
func TestRefCycleDoesNotHang(t *testing.T) {
	profiles := []*domain.Profile{{Name: "head", Priority: prio(10), Roles: map[string]domain.Candidates{
		"a": append(list("@b"), domain.Binding{Provider: "p1", Model: "a-own"}),
		"b": append(list("@a"), domain.Binding{Provider: "p1", Model: "b-own"}),
	}}}
	steps, skips := BuildChain("a", profiles, mkProvs("p1"), Opts{Active: "head"})

	// a 的候选是 [@b, a-own]：引用排在第一位，所以先把 b 整条展开（b-own），
	// 再回到 a 自己的那条。这就是「就地展开」的意思。
	eq(t, names(steps), "head:p1/b-own", "head:p1/a-own")
	found := false
	for _, s := range skips {
		if s.Target == "@a" && s.Reason != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("成环要记一条 skip 说明，实际 %+v", skips)
	}
}

// TestRefSharesDedup 引用展开出来的候选与外面重复时，全链仍然只出现一次。
func TestRefSharesDedup(t *testing.T) {
	profiles := []*domain.Profile{{Name: "head", Priority: prio(10), Roles: map[string]domain.Candidates{
		"normal": one("p1", "opus"),
		"slot":   append(list("@normal"), domain.Binding{Provider: "p1", Model: "opus"}),
	}}}
	steps, _ := BuildChain("slot", profiles, mkProvs("p1"), Opts{Active: "head"})
	eq(t, names(steps), "head:p1/opus")
}

// TestRefParsing 三种写法的解析与校验。
func TestRefParsing(t *testing.T) {
	var p domain.Profile
	src := `{"name":"x","roles":{
	  "s1": "@normal",
	  "s2": {"ref":"normal"},
	  "s3": ["@normal", "p1/m1"]
	}}`
	if err := json.Unmarshal([]byte(src), &p); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string][]string{
		"s1": {"@normal"},
		"s2": {"@normal"},
		"s3": {"@normal", "p1/m1"},
	} {
		var got []string
		for _, b := range p.Roles[key] {
			got = append(got, b.String())
		}
		eq(t, got, want...)
	}

	// 引用与具体绑定混写 = 语法错误（说不清是哪一个）
	for _, bad := range []string{
		`{"name":"x","roles":{"s":{"ref":"normal","provider":"p","model":"m"}}}`,
		`{"name":"x","roles":{"s":"@normal/p1"}}`,
	} {
		var q domain.Profile
		if err := json.Unmarshal([]byte(bad), &q); err == nil {
			t.Errorf("该报错却通过了: %s", bad)
		}
	}
}
