package resolve

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
)

func prio(n int) *int { return &n }

func one(p, m string) domain.Candidates { return domain.Candidates{{Provider: p, Model: m}} }

func list(pairs ...string) domain.Candidates {
	out := make(domain.Candidates, 0, len(pairs))
	for _, s := range pairs {
		if b, err := domain.ParseBindingString(s); err == nil {
			out = append(out, b)
		}
	}
	return out
}

func mkProvs(names ...string) *domain.Providers {
	m := map[string]domain.Provider{}
	for _, n := range names {
		m[n] = domain.Provider{BaseURL: "http://x/v1", APIKey: "sk-" + n}
	}
	return &domain.Providers{Providers: m}
}

func names(steps []Step) []string {
	var out []string
	for _, s := range steps {
		out = append(out, s.Profile+":"+s.Binding.String())
	}
	return out
}

func eq(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Errorf("链不对\n got:  %v\n want: %v", got, want)
	}
}

// ---------- 三种绑定写法 ----------

func TestCandidatesAcceptsThreeForms(t *testing.T) {
	var p domain.Profile
	src := `{"name":"x","roles":{
	  "a": {"provider":"p1","model":"m1"},
	  "b": "p2/m2",
	  "c": ["p3/m3", {"provider":"p4","model":"m4"}]
	}}`
	if err := json.Unmarshal([]byte(src), &p); err != nil {
		t.Fatal(err)
	}
	for role, want := range map[string][]string{
		"a": {"p1/m1"}, "b": {"p2/m2"}, "c": {"p3/m3", "p4/m4"},
	} {
		got := p.CandidatesFor(role)
		if len(got) != len(want) {
			t.Fatalf("%s: 长度 %d 应为 %d", role, len(got), len(want))
		}
		for i := range want {
			if got[i].String() != want[i] {
				t.Errorf("%s[%d] = %s 应为 %s", role, i, got[i], want[i])
			}
		}
	}
}

func TestModelNameCanContainSlash(t *testing.T) {
	b, err := domain.ParseBindingString("prov/org/model-1")
	if err != nil {
		t.Fatal(err)
	}
	if b.Provider != "prov" || b.Model != "org/model-1" {
		t.Errorf("得到 %+v", b)
	}
}

func TestBadBindingString(t *testing.T) {
	for _, bad := range []string{"noslash", "/leading", "trailing/"} {
		if _, err := domain.ParseBindingString(bad); err == nil {
			t.Errorf("%q 应该报错", bad)
		}
	}
}

// ---------- 链的顺序 ----------

func TestChainOrder_ActiveFirstThenPriority(t *testing.T) {
	ps := []*domain.Profile{
		{Name: "slow", Priority: prio(60), Roles: map[string]domain.Candidates{"h": one("pC", "mC")}},
		{Name: "fast", Priority: prio(10), Roles: map[string]domain.Candidates{"h": one("pA", "mA")}},
		{Name: "mid", Priority: prio(30), Roles: map[string]domain.Candidates{"h": one("pB", "mB")}},
	}
	steps, _ := BuildChain("h", ps, mkProvs("pA", "pB", "pC"), Opts{Active: "mid"})
	eq(t, names(steps), "mid:pB/mB", "fast:pA/mA", "slow:pC/mC")
}

func TestChainOrder_ProfileMajorListMinor(t *testing.T) {
	ps := []*domain.Profile{
		{Name: "cheap", Priority: prio(10), Roles: map[string]domain.Candidates{
			"h": list("pA/m1", "pB/m2")}},
		{Name: "rich", Priority: prio(20), Roles: map[string]domain.Candidates{
			"h": list("pC/m3", "pD/m4")}},
	}
	steps, _ := BuildChain("h", ps, mkProvs("pA", "pB", "pC", "pD"), Opts{Active: "cheap"})
	// 先把 cheap 内部走完，才降到 rich
	eq(t, names(steps), "cheap:pA/m1", "cheap:pB/m2", "rich:pC/m3", "rich:pD/m4")
}

func TestProbeRankReordersFallbackButNotHead(t *testing.T) {
	ps := []*domain.Profile{
		{Name: "head", Roles: map[string]domain.Candidates{"h": one("pA", "m1")}},
		{Name: "first", Roles: map[string]domain.Candidates{"h": one("pB", "usable")}},
		{Name: "second", Roles: map[string]domain.Candidates{"h": one("pC", "fluent")}},
		{Name: "third", Roles: map[string]domain.Candidates{"h": one("pD", "unknown")}},
	}
	ranks := map[string]int{"pA/m1": 2, "pB/usable": 2, "pC/fluent": 0, "pD/unknown": 1}
	steps, _ := BuildChain("h", ps, mkProvs("pA", "pB", "pC", "pD"), Opts{
		Active: "head",
		Rank: func(provider, model string) int {
			return ranks[provider+"/"+model]
		},
	})
	eq(t, names(steps),
		"head:pA/m1",
		"second:pC/fluent",
		"third:pD/unknown",
		"first:pB/usable",
	)
}

func TestChainSparseProfileIsSkipped(t *testing.T) {
	ps := []*domain.Profile{
		{Name: "onlyheavy", Priority: prio(5), Roles: map[string]domain.Candidates{
			"heavy": one("pA", "fable")}},
		{Name: "full", Priority: prio(10), Roles: map[string]domain.Candidates{
			"heavy": one("pB", "opus"), "light": one("pB", "haiku")}},
	}
	provs := mkProvs("pA", "pB")
	// heavy：稀疏层生效
	steps, _ := BuildChain("heavy", ps, provs, Opts{Active: "full"})
	eq(t, names(steps), "full:pB/opus", "onlyheavy:pA/fable")
	// light：稀疏层整层跳过
	steps, skips := BuildChain("light", ps, provs, Opts{Active: "full"})
	eq(t, names(steps), "full:pB/haiku")
	found := false
	for _, sk := range skips {
		// 断言**类目**（机器标记）而不是那句话：文案是给人看的，随语言变。
		if sk.Profile == "onlyheavy" && sk.Kind == SkipUndefined {
			found = true
		}
	}
	if !found {
		t.Error("稀疏层应记录「未定义该档位」的跳过原因")
	}
}

func TestNormalFallbackToMidStaysWithinEachProfile(t *testing.T) {
	ps := []*domain.Profile{
		{Name: "head", Roles: map[string]domain.Candidates{
			"mid": one("pA", "sonnet-a")}},
		{Name: "next", Roles: map[string]domain.Candidates{
			"mid": one("pB", "sonnet-b")}},
		{Name: "heavy-only", Roles: map[string]domain.Candidates{
			"heavy": one("pC", "fable")}},
	}
	steps, skips := BuildChain("normal", ps, mkProvs("pA", "pB", "pC"),
		Opts{Active: "head"})
	eq(t, names(steps), "head:pA/sonnet-a", "next:pB/sonnet-b")

	var duplicateSkips int
	for _, skip := range skips {
		if skip.Kind == SkipDuplicate {
			duplicateSkips++
		}
	}
	if duplicateSkips != 0 {
		t.Fatalf("profile 内 normal→mid 不应反复展开全链，得到 %+v", skips)
	}
}

func TestChainDedup(t *testing.T) {
	ps := []*domain.Profile{
		{Name: "a", Priority: prio(10), Roles: map[string]domain.Candidates{"h": list("pA/m", "pB/m")}},
		{Name: "b", Priority: prio(20), Roles: map[string]domain.Candidates{"h": list("pA/m", "pC/m")}},
	}
	steps, _ := BuildChain("h", ps, mkProvs("pA", "pB", "pC"), Opts{Active: "a"})
	eq(t, names(steps), "a:pA/m", "a:pB/m", "b:pC/m") // b:pA/m 被去重
}

// ---------- pinned / excluded ----------

func TestPinnedStopsChain(t *testing.T) {
	ps := []*domain.Profile{
		{Name: "strict", Priority: prio(10), Pinned: true,
			Roles: map[string]domain.Candidates{"h": list("pA/m1", "pA/m2")}},
		{Name: "other", Priority: prio(20), Roles: map[string]domain.Candidates{"h": one("pB", "m3")}},
	}
	steps, _ := BuildChain("h", ps, mkProvs("pA", "pB"), Opts{Active: "strict"})
	// pinned 仍然走完自己内部的 list，但不降到别的 profile
	eq(t, names(steps), "strict:pA/m1", "strict:pA/m2")
}

func TestPinnedOnlyAppliesWhenItIsHead(t *testing.T) {
	ps := []*domain.Profile{
		{Name: "strict", Priority: prio(10), Pinned: true,
			Roles: map[string]domain.Candidates{"h": one("pA", "m1")}},
		{Name: "head", Priority: prio(20), Roles: map[string]domain.Candidates{"h": one("pB", "m2")}},
	}
	steps, _ := BuildChain("h", ps, mkProvs("pA", "pB"), Opts{Active: "head"})
	// strict 不是链头，它的 pinned 不生效，正常参与
	eq(t, names(steps), "head:pB/m2", "strict:pA/m1")
}

func TestExcludedNotAutoSelected(t *testing.T) {
	ps := []*domain.Profile{
		{Name: "normal", Priority: prio(20), Roles: map[string]domain.Candidates{"h": one("pA", "m1")}},
		{Name: "pricey", Priority: prio(5), Excluded: true,
			Roles: map[string]domain.Candidates{"h": one("pB", "m2")}},
	}
	provs := mkProvs("pA", "pB")
	steps, _ := BuildChain("h", ps, provs, Opts{Active: "normal"})
	eq(t, names(steps), "normal:pA/m1") // pricey 优先级更高但被 excluded

	// 显式选中时 excluded 不挡链头
	steps, _ = BuildChain("h", ps, provs, Opts{Active: "pricey"})
	eq(t, names(steps), "pricey:pB/m2", "normal:pA/m1")
}

// ---------- 过滤器 ----------

func TestUnavailableFilterFiltersStep(t *testing.T) {
	ps := []*domain.Profile{{Name: "a", Roles: map[string]domain.Candidates{"h": list("pA/m1", "pB/m2")}}}
	steps, skips := BuildChain("h", ps, mkProvs("pA", "pB"), Opts{
		Active: "a",
		Available: func(p, _ string) (bool, string) {
			if p == "pA" {
				// 理由的措辞属于提供判据的人：今天 controlplane 给的就是这句。
				return false, "circuit open"
			}
			return true, ""
		},
	})
	eq(t, names(steps), "a:pB/m2")
	// 类目归 resolve（这一关拒绝了它），理由原样透传（它属于提供判据的人，
	// 今天就是 controlplane 的那句 i18n.T("circuit open")）。
	if len(skips) == 0 || skips[0].Kind != SkipUnavailable ||
		!strings.Contains(skips[0].Reason, "circuit open") {
		t.Errorf("应记录「不可用」的跳过与它给的理由，得到 %+v", skips)
	}
}

func TestDisabledFiltersStep(t *testing.T) {
	ps := []*domain.Profile{{Name: "a", Roles: map[string]domain.Candidates{"h": list("pA/m1", "pB/m2")}}}
	steps, skips := BuildChain("h", ps, mkProvs("pA", "pB"), Opts{
		Active: "a",
		Disabled: func(role, target string) (bool, string) {
			return target == "pA/m1", "you disabled it 2h ago"
		},
	})
	eq(t, names(steps), "a:pB/m2")
	if len(skips) == 0 || skips[0].Kind != SkipDisabled ||
		!strings.Contains(skips[0].Reason, "2h ago") {
		t.Errorf("应带上禁用原因，得到 %+v", skips)
	}
}

func TestMissingProviderOrKeyIsSkippedNotFatal(t *testing.T) {
	ps := []*domain.Profile{{Name: "a", Roles: map[string]domain.Candidates{
		"h": list("ghost/m0", "nokey/m1", "good/m2")}}}
	provs := &domain.Providers{Providers: map[string]domain.Provider{
		"nokey": {BaseURL: "http://x"}, // 没 key
		"good":  {BaseURL: "http://x", APIKey: "sk-ok"},
	}}
	steps, skips := BuildChain("h", ps, provs, Opts{Active: "a"})
	eq(t, names(steps), "a:good/m2")
	if len(skips) != 2 {
		t.Errorf("应有 2 条跳过记录，得到 %+v", skips)
	}
}

func TestMaxStepsTruncates(t *testing.T) {
	ps := []*domain.Profile{{Name: "a", Roles: map[string]domain.Candidates{
		"h": list("pA/m1", "pB/m2", "pC/m3")}}}
	steps, skips := BuildChain("h", ps, mkProvs("pA", "pB", "pC"),
		Opts{Active: "a", MaxSteps: 2})
	eq(t, names(steps), "a:pA/m1", "a:pB/m2")
	last := skips[len(skips)-1]
	if !strings.Contains(last.Reason, "maxAttempts") {
		t.Errorf("应说明因 maxAttempts 被截断，得到 %+v", last)
	}
}

func TestTiebreakByNameIsDeterministic(t *testing.T) {
	ps := []*domain.Profile{
		{Name: "zeta", Roles: map[string]domain.Candidates{"h": one("pZ", "m")}},
		{Name: "alpha", Roles: map[string]domain.Candidates{"h": one("pA", "m")}},
	}
	for i := 0; i < 20; i++ {
		steps, _ := BuildChain("h", ps, mkProvs("pA", "pZ"), Opts{Active: "none"})
		eq(t, names(steps), "alpha:pA/m", "zeta:pZ/m")
	}
}

func TestDefaultPriorityWhenAbsent(t *testing.T) {
	p := &domain.Profile{Name: "x"}
	if p.Prio() != domain.DefaultPriority {
		t.Errorf("没写 priority 应为 %d，得到 %d", domain.DefaultPriority, p.Prio())
	}
}

// ---------- 兼容：现有格式仍能读 ----------

func TestBackwardCompatOldProfileFormat(t *testing.T) {
	old := `{"name":"ds","description":"d","roles":{
	  "heavy":{"provider":"upstream-a","model":"model-large"}}}`
	var p domain.Profile
	if err := json.Unmarshal([]byte(old), &p); err != nil {
		t.Fatal(err)
	}
	b, ok := p.Resolve("heavy")
	if !ok || b.String() != "upstream-a/model-large" {
		t.Errorf("旧格式读不出来: %+v %v", b, ok)
	}
	if p.Prio() != domain.DefaultPriority {
		t.Error("旧格式没有 priority，应取默认值")
	}
}

// ---------- classifier_override ----------

func TestOverrideChainHeadsChain(t *testing.T) {
	ps := []*domain.Profile{{Name: "a", Roles: map[string]domain.Candidates{
		"light": list("pA/m1", "pB/m2")}}}
	steps, skips, applied := OverrideChain("classifier_override",
		domain.Binding{Provider: "pC", Model: "m3"},
		"light", ps, mkProvs("pA", "pB", "pC"), Opts{Active: "a"})
	eq(t, names(steps), "(classifier_override):pC/m3", "a:pA/m1", "a:pB/m2")
	if !applied {
		t.Error("覆盖应生效")
	}
	if len(skips) != 0 {
		t.Errorf("不该有跳过，得到 %+v", skips)
	}
}

func TestOverrideChainFallsBackOnUnknownProvider(t *testing.T) {
	ps := []*domain.Profile{{Name: "a", Roles: map[string]domain.Candidates{
		"light": list("pA/m1")}}}
	steps, skips, applied := OverrideChain("classifier_override",
		domain.Binding{Provider: "ghost", Model: "m"},
		"light", ps, mkProvs("pA"), Opts{Active: "a"})
	eq(t, names(steps), "a:pA/m1")
	if applied {
		t.Error("provider 未定义时覆盖不应生效")
	}
	found := false
	for _, sk := range skips {
		if sk.Profile == "(classifier_override)" && sk.Kind == SkipUndefined {
			found = true
		}
	}
	if !found {
		t.Errorf("应记录覆盖失效原因，得到 %+v", skips)
	}
}

func TestOverrideChainFallsBackOnMissingKey(t *testing.T) {
	ps := []*domain.Profile{{Name: "a", Roles: map[string]domain.Candidates{"light": one("pA", "m1")}}}
	provs := &domain.Providers{Providers: map[string]domain.Provider{
		"pA":    {BaseURL: "http://x", APIKey: "sk-a"},
		"nokey": {BaseURL: "http://x"},
	}}
	steps, _, applied := OverrideChain("classifier_override",
		domain.Binding{Provider: "nokey", Model: "m"},
		"light", ps, provs, Opts{Active: "a"})
	eq(t, names(steps), "a:pA/m1")
	if applied {
		t.Error("provider 无 key 时覆盖不应生效")
	}
}

func TestOverrideChainFallsBackWhenUnavailable(t *testing.T) {
	ps := []*domain.Profile{{Name: "a", Roles: map[string]domain.Candidates{"light": one("pA", "m1")}}}
	steps, _, applied := OverrideChain("classifier_override",
		domain.Binding{Provider: "pC", Model: "m3"},
		"light", ps, mkProvs("pA", "pC"), Opts{
			Active: "a",
			Available: func(p, _ string) (bool, string) {
				if p == "pC" {
					return false, "circuit open"
				}
				return true, ""
			},
		})
	eq(t, names(steps), "a:pA/m1")
	if applied {
		t.Error("熔断时覆盖不应生效")
	}
}

func TestOverrideChainDedupsWhenOverrideAlreadyInChain(t *testing.T) {
	ps := []*domain.Profile{{Name: "a", Roles: map[string]domain.Candidates{
		"light": list("pA/m1", "pB/m2")}}}
	steps, _, applied := OverrideChain("classifier_override",
		domain.Binding{Provider: "pA", Model: "m1"},
		"light", ps, mkProvs("pA", "pB"), Opts{Active: "a"})
	// pA/m1 原本在 light 链里，覆盖后只出现一次且在最前
	eq(t, names(steps), "(classifier_override):pA/m1", "a:pB/m2")
	if !applied {
		t.Error("覆盖 binding 已在链里时也应生效（提到最前）")
	}
}
