package resolve

import (
	"encoding/json"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
)

// TestRefSplicesInPlace a reference (`@other key`) expands in place
// into that chain -- the documented "a slot can be bound to some
// tier chain, and can itself be a chain" rests on this one semantic.
//
// The expansion order is still "profile is the main order, list the
// within order": a reference inserts the whole referenced chain at its own
// position, without changing the relative order of the other candidates.
func TestRefSplicesInPlace(t *testing.T) {
	profiles := []*domain.Profile{
		{Name: "head", Priority: prio(10), Roles: map[string]domain.Candidates{
			"normal": one("p1", "opus"),
			// The slot is itself a chain: follow normal first, and
			// only use its own fallback once normal is entirely down
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

// TestRefWholeKeyIsAlias when the whole key is one reference, the result is word
// for word identical to "this key has an alias to there" -- the two paths (alias / reference
// expansion) are the same thing in product semantics, and the alias is cheaper to implement.
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

// TestRefCycleDoesNotHang a reference cycle must stop and explain, not expand forever.
func TestRefCycleDoesNotHang(t *testing.T) {
	profiles := []*domain.Profile{{Name: "head", Priority: prio(10), Roles: map[string]domain.Candidates{
		"a": append(list("@b"), domain.Binding{Provider: "p1", Model: "a-own"}),
		"b": append(list("@a"), domain.Binding{Provider: "p1", Model: "b-own"}),
	}}}
	steps, skips := BuildChain("a", profiles, mkProvs("p1"), Opts{Active: "head"})

	// a's candidates are [@b, a-own]: the reference is first, so b is expanded entirely
	// (b-own) first, then back to a's own. That is what "expand in place" means.
	eq(t, names(steps), "head:p1/b-own", "head:p1/a-own")
	found := false
	for _, s := range skips {
		if s.Target == "@a" && s.Reason != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a cycle must record a skip explanation, actual %+v", skips)
	}
}

// TestRefSharesDedup when a reference-expanded candidate duplicates
// an outside one, the whole chain still has it only once.
func TestRefSharesDedup(t *testing.T) {
	profiles := []*domain.Profile{{Name: "head", Priority: prio(10), Roles: map[string]domain.Candidates{
		"normal": one("p1", "opus"),
		"slot":   append(list("@normal"), domain.Binding{Provider: "p1", Model: "opus"}),
	}}}
	steps, _ := BuildChain("slot", profiles, mkProvs("p1"), Opts{Active: "head"})
	eq(t, names(steps), "head:p1/opus")
}

// TestRefParsing the parsing and validation of the three spellings.
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

	// Mixing a reference and a concrete binding = syntax error (cannot tell which it is)
	for _, bad := range []string{
		`{"name":"x","roles":{"s":{"ref":"normal","provider":"p","model":"m"}}}`,
		`{"name":"x","roles":{"s":"@normal/p1"}}`,
	} {
		var q domain.Profile
		if err := json.Unmarshal([]byte(bad), &q); err == nil {
			t.Errorf("should error but passed: %s", bad)
		}
	}
}
