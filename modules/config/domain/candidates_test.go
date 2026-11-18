package domain

import "testing"

// TestCandidatesForNormalFallsBackToMid is the backward compatibility
// of the four-tier split (2026-09-16): normal is the newly added **main
// tier**, old configs do not have it, and mid fills in when it is missing.
//
// Why not "skip the whole level": skipping would leave configs without normal with no candidate
// on the main tier (the chain idles, claude's main loop 404s outright), while before the four-tier
// split the main loop used exactly mid's resources -- semantically it should be the same as mid.
//
// This compatibility relation must stay inside the profile: if it returned @mid, BuildChain would
// expand the whole cross-profile mid chain again on every profile, reporting normal candidates as
// dozens of "duplicates". An explicit @mid and module slot references still expand across profiles.
func TestCandidatesForNormalFallsBackToMid(t *testing.T) {
	mid := Candidates{{Provider: "p", Model: "sonnet-class"}}
	heavy := Candidates{{Provider: "p", Model: "fable-class"}}
	normal := Candidates{{Provider: "p", Model: "opus-class"}}

	old := &Profile{Roles: map[string]Candidates{"heavy": heavy, "mid": mid}}
	if got := old.CandidatesFor("normal"); len(got) != 1 || got[0].Model != "sonnet-class" {
		t.Errorf("when normal is not written it should reuse this profile's mid, actual %v", got)
	}
	// A reference run to the end is what the place that really does the work wants
	if b, ok := old.Resolve("normal"); !ok || b.Model != "sonnet-class" {
		t.Errorf("normal should resolve to the candidates of mid, actual %v %v", b, ok)
	}

	// If normal is written, use it, regardless of mid
	withNormal := &Profile{Roles: map[string]Candidates{
		"heavy": heavy, "normal": normal, "mid": mid}}
	if got := withNormal.CandidatesFor("normal"); got[0].Model != "opus-class" {
		t.Errorf("when normal is written it should use its own, actual %v", got)
	}
	// The other tiers are unaffected by this rule
	if got := withNormal.CandidatesFor("heavy"); got[0].Model != "fable-class" {
		t.Errorf("heavy was dragged along by normal's rule: %v", got)
	}
	if got := withNormal.CandidatesFor("mid"); got[0].Model != "sonnet-class" {
		t.Errorf("mid was changed: %v", got)
	}

	// Not even mid: fall back to the old wildcard / fallback path, rather than idling out of nothing
	bare := &Profile{Roles: map[string]Candidates{"heavy": heavy},
		Fallback: &Binding{Provider: "p", Model: "fallback"}}
	if b, ok := bare.Resolve("normal"); !ok || b.Model != "fallback" {
		t.Errorf("with no mid it should use the fallback, actual %v %v", b, ok)
	}
}

// TestRolesOrder: the tier order is capability from high to low -- status / doctor
// / TUI all sort by it, and this also pins the result of the four-tier split.
func TestRolesOrder(t *testing.T) {
	want := []string{"heavy", "normal", "mid", "light", "vision"}
	if len(Roles) != len(want) {
		t.Fatalf("Roles = %v, want %v", Roles, want)
	}
	for i := range want {
		if Roles[i] != want[i] {
			t.Fatalf("Roles = %v, want %v", Roles, want)
		}
	}
}

// TestReaddedEmptyTierInheritsParent: deleting a tier and adding
// it back empty must not overwrite the parent's candidates.
//
// The scene (2026-09-20): deleting vision in the UI and then adding an empty
// tier, applyProfileRoles wrote `vision: []` into the file; MergeFrom used that
// empty array to **overwrite** the parent profile's vision, so that tier resolved
// to nothing -- the user thought the binding logic was broken. This pins it:
// an empty tier = not written, the parent's candidates are inherited as usual.
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
		t.Fatalf("an empty tier should inherit the parent's candidates, actual %v", got)
	}
}

// TestExplicitCandidatesStillOverrideParent: the other way round -- if the child writes real
// candidates, the parent's are overwritten. This guards that the direction of merging cannot
// be reversed (inheritance is "borrow only when missing", not "add noise even when present").
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
		t.Fatalf("explicit candidates must be respected, the parent must not override them: %v", got)
	}
}
