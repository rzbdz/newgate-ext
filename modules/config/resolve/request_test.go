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
	// The chain head does not define heavy, the chain drops to the
	// next profile, and the displayed name should be that candidate.
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
		t.Error("with no candidates it should be ok=false")
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
		t.Errorf("tier = %q, should be heavy", tier)
	}
	eq(t, names(steps), "ds:ds/deepseek-chat")
}

func TestResolveRequest_ConcreteModelMovedToFront(t *testing.T) {
	// heavy's chain is [deepseek-chat, glm-4-plus]; the client names glm-4-plus,
	// which should be moved to the front, with deepseek-chat after it.
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
		t.Errorf("an unknown model should return an empty chain and empty tier, got tier=%q steps=%v", tier, names(steps))
	}
}

// TestResolveRequest_KnownModelUnavailableFallsBackToTier pins the scene of 2026-11-04:
//
//	model deepseek-flash is neither a known tier (...) nor in any profile's bindings
//
// -- while it plainly was bound in ds. The trigger was that the one binding that had
// it was broken: the reverse lookup used to run on **the chain with admission filtering**, the
// model was taken off the chain, and so "unavailable" was misreported as "unknown". When the
// recognized tier's chain still has other usable candidates, it should follow that chain (that
// is what fallback is for), and leave the reason it was taken off in skips (do not be silent).
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
		t.Fatalf("the model is bound to mid, so tier should be mid (unavailable is not the same as unknown), got %q", tier)
	}
	eq(t, names(steps), "a:a/a-model")
	found := false
	for _, sk := range skips {
		if sk.Kind == SkipUnavailable && strings.Contains(sk.Reason, "circuit open") {
			found = true
		}
	}
	if !found {
		t.Errorf("a named binding that was removed should leave the skip reason (circuit open), got %+v", skips)
	}
}

// TestResolveRequest_KnownModelUnavailableIsNotUnknown: when the profile named
// itself (the callers that probe / name a profile narrow the profile set to this
// one) and its only candidate was taken off, the tier must still be recognized --
// an empty chain is "no usable candidate right now", not "this name is unknown".
func TestResolveRequest_KnownModelUnavailableIsNotUnknown(t *testing.T) {
	ps := []*domain.Profile{{Name: "ds", Roles: map[string]domain.Candidates{
		"mid": one("ds", "deepseek-flash")}}}
	deny := func(provider, model string) (bool, string) { return false, "circuit open" }
	steps, _, tier := ResolveRequest("deepseek-flash", "ds", ps,
		mkProvs("ds"), Opts{Active: "ds", Available: deny})
	if tier != "mid" || len(steps) != 0 {
		t.Errorf("should get tier=mid and an empty chain (currently unavailable), got tier=%q steps=%v", tier, names(steps))
	}
}

// TestResolveRequest_KnownModelPastMaxSteps: the reverse lookup must not run on the truncated chain
// first. When the named model comes after maxAttempts, it is still the one the client **named**,
// and must be moved to the chain head; truncation only decides how many fallbacks can follow.
func TestResolveRequest_KnownModelPastMaxSteps(t *testing.T) {
	ps := []*domain.Profile{{Name: "a", Roles: map[string]domain.Candidates{
		"mid": list("pA/m1", "pB/m2", "pC/deepseek-flash")}}}
	steps, _, tier := ResolveRequest("deepseek-flash", "a", ps,
		mkProvs("pA", "pB", "pC"), Opts{Active: "a", MaxSteps: 2})
	if tier != "mid" {
		t.Fatalf("tier = %q, should be mid", tier)
	}
	eq(t, names(steps), "a:pC/deepseek-flash", "a:pA/m1")
}

// TestResolveRequest_AmbiguousModelPrefersMid the priority of a one-to-many reverse lookup
// (a regression caught in measurement on 2026-09-09): glm-5.3 is bound to both heavy and mid
// (a common config), and the names clients send mostly come from the sonnet slot (main loop /
// classifier / summaries), so it must resolve to mid -- matching heavy by Roles' original order
// would force mid requests to claim heavy, and every downstream plugin that decides by tier
// would be distorted (the scene: the classifier skipped by claude-bg, 27 seconds a request).
func TestResolveRequest_AmbiguousModelPrefersMid(t *testing.T) {
	ps := []*domain.Profile{{Name: "glm", Priority: prio(20), Roles: map[string]domain.Candidates{
		"heavy": list("glm/glm-5.3", "ds/claude-opus"),
		"mid":   list("glm/glm-5.3", "ds/deepseek-chat"),
	}}}
	steps, _, tier := ResolveRequest("glm-5.3", "glm", ps, mkProvs("glm", "ds"), Opts{Active: "glm"})
	if tier != "mid" {
		t.Fatalf("the same name filling heavy+mid should resolve to mid, actual %q", tier)
	}
	// The chain is mid's too: the named model at the head, followed
	// by mid's candidates (deepseek-chat), not heavy's claude-opus.
	eq(t, names(steps), "glm:glm/glm-5.3", "glm:ds/deepseek-chat")
}

// TestRealNameRoleOrderIsPermutation the priority order must be a permutation of Roles
// -- missing one tier would make a model exclusive to that tier never resolve to a tier.
func TestRealNameRoleOrderIsPermutation(t *testing.T) {
	if len(realNameRoleOrder) != len(domain.Roles) {
		t.Fatalf("realNameRoleOrder and Roles counts disagree: %v vs %v", realNameRoleOrder, domain.Roles)
	}
	seen := map[string]bool{}
	for _, r := range realNameRoleOrder {
		if seen[r] {
			t.Errorf("duplicate tier: %s", r)
		}
		seen[r] = true
		if !domain.IsRole(r) {
			t.Errorf("unknown tier: %s", r)
		}
	}
	for _, r := range domain.Roles {
		if !seen[r] {
			t.Errorf("missing tier: %s", r)
		}
	}
}

func TestResolveRequest_IsRoleIsExact(t *testing.T) {
	// A model name that happens to be "light" is treated
	// as a tier name -- this is an intentional convention.
	if !domain.IsRole("light") {
		t.Fatal("light should be a tier name")
	}
	if domain.IsRole("deepseek-chat") {
		t.Fatal("deepseek-chat is not a tier name")
	}
}
