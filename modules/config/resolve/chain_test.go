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
		t.Errorf("chain is wrong\n got:  %v\n want: %v", got, want)
	}
}

// ---------- the three binding spellings ----------

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
			t.Fatalf("%s: length %d should be %d", role, len(got), len(want))
		}
		for i := range want {
			if got[i].String() != want[i] {
				t.Errorf("%s[%d] = %s should be %s", role, i, got[i], want[i])
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
		t.Errorf("got %+v", b)
	}
}

func TestBadBindingString(t *testing.T) {
	for _, bad := range []string{"noslash", "/leading", "trailing/"} {
		if _, err := domain.ParseBindingString(bad); err == nil {
			t.Errorf("%q should error", bad)
		}
	}
}

// ---------- chain order ----------

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
	// Finish everything inside cheap first, then drop to rich
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
	// heavy: the sparse level takes effect
	steps, _ := BuildChain("heavy", ps, provs, Opts{Active: "full"})
	eq(t, names(steps), "full:pB/opus", "onlyheavy:pA/fable")
	// light: the sparse level is skipped entirely
	steps, skips := BuildChain("light", ps, provs, Opts{Active: "full"})
	eq(t, names(steps), "full:pB/haiku")
	found := false
	for _, sk := range skips {
		// Assert the **category** (machine marker) rather than the
		// sentence: the wording is for humans and changes with language.
		if sk.Profile == "onlyheavy" && sk.Kind == SkipUndefined {
			found = true
		}
	}
	if !found {
		t.Error("a sparse layer should record the reason \"tier not defined\"")
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
		t.Fatalf("normal->mid inside a profile should not expand the whole chain repeatedly, got %+v", skips)
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
	// pinned still walks its own internal list, but does not drop to other profiles
	eq(t, names(steps), "strict:pA/m1", "strict:pA/m2")
}

func TestPinnedOnlyAppliesWhenItIsHead(t *testing.T) {
	ps := []*domain.Profile{
		{Name: "strict", Priority: prio(10), Pinned: true,
			Roles: map[string]domain.Candidates{"h": one("pA", "m1")}},
		{Name: "head", Priority: prio(20), Roles: map[string]domain.Candidates{"h": one("pB", "m2")}},
	}
	steps, _ := BuildChain("h", ps, mkProvs("pA", "pB"), Opts{Active: "head"})
	// strict is not the chain head, so its pinned does not take effect and it participates normally
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

	// when explicitly selected, excluded does not block the chain head
	steps, _ = BuildChain("h", ps, provs, Opts{Active: "pricey"})
	eq(t, names(steps), "pricey:pB/m2", "normal:pA/m1")
}

// ---------- filters ----------

func TestUnavailableFilterFiltersStep(t *testing.T) {
	ps := []*domain.Profile{{Name: "a", Roles: map[string]domain.Candidates{"h": list("pA/m1", "pB/m2")}}}
	steps, skips := BuildChain("h", ps, mkProvs("pA", "pB"), Opts{
		Active: "a",
		Available: func(p, _ string) (bool, string) {
			if p == "pA" {
				// The wording of the reason belongs to whoever provides the
				// criterion: today it is this sentence from controlplane.
				return false, "circuit open"
			}
			return true, ""
		},
	})
	eq(t, names(steps), "a:pB/m2")
	// The category is resolve's (this gate rejected it); the reason is
	// passed through as is (it belongs to whoever provides the criterion
	// -- today that sentence is controlplane's i18n.T("circuit open")).
	if len(skips) == 0 || skips[0].Kind != SkipUnavailable ||
		!strings.Contains(skips[0].Reason, "circuit open") {
		t.Errorf("should record the \"unavailable\" skip and its reason, got %+v", skips)
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
		t.Errorf("should carry the disabled reason, got %+v", skips)
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
		t.Errorf("there should be 2 skip records, got %+v", skips)
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
		t.Errorf("should say it was truncated because of maxAttempts, got %+v", last)
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
		t.Errorf("with no priority written it should be %d, got %d", domain.DefaultPriority, p.Prio())
	}
}

// ---------- compatibility: existing formats still read ----------

func TestBackwardCompatOldProfileFormat(t *testing.T) {
	old := `{"name":"ds","description":"d","roles":{
	  "heavy":{"provider":"upstream-a","model":"model-large"}}}`
	var p domain.Profile
	if err := json.Unmarshal([]byte(old), &p); err != nil {
		t.Fatal(err)
	}
	b, ok := p.Resolve("heavy")
	if !ok || b.String() != "upstream-a/model-large" {
		t.Errorf("cannot read the old format: %+v %v", b, ok)
	}
	if p.Prio() != domain.DefaultPriority {
		t.Error("the old format has no priority, it should take the default")
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
		t.Error("the override should take effect")
	}
	if len(skips) != 0 {
		t.Errorf("there should be no skips, got %+v", skips)
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
		t.Error("the override should not take effect when the provider is undefined")
	}
	found := false
	for _, sk := range skips {
		if sk.Profile == "(classifier_override)" && sk.Kind == SkipUndefined {
			found = true
		}
	}
	if !found {
		t.Errorf("should record why the override did not take effect, got %+v", skips)
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
		t.Error("the override should not take effect when the provider has no key")
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
		t.Error("the override should not take effect when the breaker is open")
	}
}

func TestOverrideChainDedupsWhenOverrideAlreadyInChain(t *testing.T) {
	ps := []*domain.Profile{{Name: "a", Roles: map[string]domain.Candidates{
		"light": list("pA/m1", "pB/m2")}}}
	steps, _, applied := OverrideChain("classifier_override",
		domain.Binding{Provider: "pA", Model: "m1"},
		"light", ps, mkProvs("pA", "pB"), Opts{Active: "a"})
	// pA/m1 was originally on the light chain; after the override it appears once and first
	eq(t, names(steps), "(classifier_override):pA/m1", "a:pB/m2")
	if !applied {
		t.Error("the override should also take effect when the binding is already in the chain (moved to the front)")
	}
}
