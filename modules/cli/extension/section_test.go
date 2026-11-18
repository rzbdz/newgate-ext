package extension

import "testing"

// Gatekeeper of the slot rules. These rules are **conventions** with no type
// system as a backstop, so only tests can pin the four: "generic keys first,
// custom first come first served, the losers go to others, stable order".

func TestGeneralSectionsKeepTheirPublishedOrder(t *testing.T) {
	// Deliberately shuffle the declaration order: the display order of generic
	// sections is decided by PlanSections, independent of declaration order --
	// otherwise a module could move a whole section by changing when it registers.
	plan := PlanSections([]Section{SectionMaintenance, SectionTakeover, SectionObserve})
	if got := plan.Slot(SectionMaintenance); got != SectionMaintenance {
		t.Fatalf("Maintenance landed in %q", got)
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
	declared := []Section{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"}
	plan := PlanSections(declared)
	for _, name := range []Section{"alpha", "beta", "gamma", "delta"} {
		if got := plan.Slot(name); got != name {
			t.Fatalf("%q should get its own section, landed in %q", name, got)
		}
	}
	// The one that exceeds MaxCustomSections loses its quota.
	for _, name := range []Section{"epsilon", "zeta"} {
		if got := plan.Slot(name); got != SectionOthers {
			t.Fatalf("%q should go into others, landed in %q", name, got)
		}
	}
	order := plan.Order()
	// Custom sections come after generic sections and before others,
	// in **declaration order**. Only the tail is checked here: the last
	// three are the first two custom sections + the fallback section.
	tail := order[len(order)-3:]
	if tail[0] != "gamma" || tail[1] != "delta" || tail[2] != SectionOthers {
		t.Fatalf("custom sections are not ordered before others by first-come order: %v", tail)
	}
	// Not one generic section missing, all before the custom sections.
	if order[len(generalSections)] != "alpha" {
		t.Fatalf("a custom section does not come right after the general one: %v", order)
	}
}

func TestEmptyAndUnknownSectionsFallBack(t *testing.T) {
	plan := PlanSections([]Section{"", "nobody knows me"})
	if got := plan.Slot(""); got != SectionOthers {
		t.Fatalf("an empty section name landed in %q", got)
	}
	// An undeclared name should also get a safe answer (a
	// question mark should not appear on the render path).
	if got := plan.Slot("never appeared"); got != SectionOthers {
		t.Fatalf("an unclaimed section name landed in %q", got)
	}
	if got := PlanSections(nil).Order(); len(got) == 0 {
		t.Fatal("with no commands Order() should still be renderable (at least containing others)")
	}
}

func TestSlotReturnsACopyOfTheOrder(t *testing.T) {
	plan := PlanSections(nil)
	order := plan.Order()
	order[0] = "was corrupted"
	if plan.Order()[0] == "was corrupted" {
		t.Fatal("Order() returned the internal slice, the caller can corrupt the slot table")
	}
}
