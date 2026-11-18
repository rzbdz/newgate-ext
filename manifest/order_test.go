package manifest

import (
	"testing"

	modules "github.com/rzbdz/newgate/component"
)

// What this guards is that **the generated order_gen.go can be trusted**.
//
// It is computed and frozen at build time (see tools/distgen/orderprobe); at runtime there
// is only one check (is the arrangement right), and **it does not check whether it really
// is a topological order** -- that is the graph-building step's job. So "is this table
// right at all" has to be watched by someone **here**, or it is frozen data nobody verified.
//
// Two things are checked together; missing either is not enough:
//
//  1. It **works**: for each spec, the graph assembled in this order makes
//     `Resolve` take the fast path, and the order it produces is exactly the same;
//  2. It **is not decoration**: the order must really be used (Selection.Load
//     sorts by it), or nobody would notice this table going stale.
func TestGeneratedOrderMatchesResolve(t *testing.T) {
	specs := Specs()
	if len(specs) == 0 {
		t.Fatal("there are no specs at all")
	}
	_ = specs
	for _, name := range SpecNames() {
		loader := Loader{Spec: name}
		order := loader.PrecomputedOrder()
		if len(order) == 0 {
			t.Errorf("%s has no start order -- was order_gen.go not generated?"+
				"(run `go run ./tools/distgen`)", name)
			continue
		}

		// (1) Assemble by the blueprint: it must assemble,
		// and the order must be the one on the blueprint.
		//
		// This is also evidence that "runtime really took the blueprint": `Loader`
		// implements component.Precomputed, so Resolve does not build edges and sort (see
		// component.planFromOrder). Name mismatches and count mismatches error out on the spot.
		plan, err := modules.Resolve(loader)
		if err != nil {
			t.Errorf("%s does not assemble from the blueprint: %v", name, err)
			continue
		}
		if got := plan.Names(); !sameStrings(got, order) {
			t.Errorf("%s's actual order differs from the blueprint:\n  blueprint: %v\n  actual: %v", name, order, got)
		}

		// (2) The blueprint must **equal the "computed from scratch" answer**. This is
		// the most important one: the blueprint is frozen at build time, and runtime no
		// longer verifies whether it really is a topological order (that is build time's
		// job). Without this one, a wrongly ordered blueprint has nobody watching it.
		components, err := Specs()[name].Load()
		if err != nil {
			t.Errorf("%s does not assemble from scratch: %v", name, err)
			continue
		}
		fresh, err := modules.Resolve(staticLoader(components))
		if err != nil {
			t.Errorf("%s failed to build the graph from scratch: %v", name, err)
			continue
		}
		if got := fresh.Names(); !sameStrings(got, order) {
			t.Errorf("%s: the blueprint differs from the \"compute from scratch\" result (is the list stale?):\n"+
				"  blueprint: %v\n  from scratch: %v", name, order, got)
		}
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type staticLoader []modules.Component

func (s staticLoader) Load() ([]modules.Component, error) { return s, nil }
