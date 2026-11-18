package archmap_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/manifest"
	"github.com/rzbdz/newgate-ext/tools/archmap"
)

// The cycle axis: **among aggregation-made cycles, which really mean "two modules
// bound to each other's root package"**.
//
// CLAUDE.md says aggregation makes cycles, caused by shared leaves (importable
// contract packages), not a design flaw - reviewers should see them, but verify.
//
// Go refuses to compile package-level import cycles, so real cycles do not exist -
// the target is another kind: two modules **both** importing the other's **root
// package** (bound to each other's contract); looks like a leaf illusion but hurts.
//
// So the criterion is clean: for every pair (A, B) in an aggregated cycle ask
// "does a package in A import B's **root package**" and the reverse. Both yes ->
// a real cycle, reported. Asserts **zero real cycles**: empty today (2026-09-21).
//
// # How hard this assertion is (say it plainly; do not let it pose as more)
//
// Softer than it looks. Two modules **mutually** bound to root packages is nearly
// impossible in compiling Go - that is an import cycle, which Go rejects. So it
// catches little; its **real product is the classification**: of the 13 aggregated
// cycles: leaf illusion or really bound? Reviewers want that answer, not the boolean.
//
// Kept: the one place turning "cycles are an illusion" from **assertion** to **checked**.
func TestReportCycles(t *testing.T) {
	root := repoRoot(t)
	report, err := archmap.Build(manifest.Specs(), root)
	if err != nil {
		t.Fatal(err)
	}

	// which modules' **root packages** each package imports (same ruler as the undeclared probe).
	// module -> the set of modules whose root packages it imports
	rootImports := map[string]map[string]bool{}
	for _, dir := range []string{root, root + "/core"} {
		for _, p := range listPackages(t, dir) {
			from, ok := moduleOf(p.path)
			if !ok {
				continue
			}
			for _, imp := range p.imports {
				if to, ok := rootModuleOf(imp); ok && to != from {
					if rootImports[from] == nil {
						rootImports[from] = map[string]bool{}
					}
					rootImports[from][to] = true
				}
			}
		}
	}

	// Aggregated cycles: edges marked Cycle in the import map.
	pairs := map[[2]string]bool{}
	for _, e := range report.Imports.Edges {
		if !e.Cycle {
			continue
		}
		a, b := norm(labelKey(e.From)), norm(labelKey(e.To))
		if a > b {
			a, b = b, a
		}
		pairs[[2]string{a, b}] = true
	}
	keys := make([][2]string, 0, len(pairs))
	for p := range pairs {
		keys = append(keys, p)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})

	var real [][2]string
	t.Logf("cycles from aggregation: %d pairs", len(keys))
	for _, p := range keys {
		ab := rootImports[p[0]][p[1]]
		ba := rootImports[p[1]][p[0]]
		verdict := "artifact (at least one side is only a leaf)"
		if ab && ba {
			verdict = "**both sides bound to each other's root package**"
			real = append(real, p)
		}
		t.Logf("  %s <-> %s   %s (A->B root=%v B->A root=%v)", p[0], p[1], verdict, ab, ba)
	}
	if len(real) > 0 {
		t.Errorf("these modules **both** bind to each other's root package: %v\n"+
			"not every cycle from aggregation is a leaf artifact -- these few pairs are real bidirectional contract bindings.", real)
	}
}

// labelKey turns an import-map node id (with repo prefix) back into the module-name form.
func labelKey(id string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(id, "core:"), "ext:")
	return strings.TrimPrefix(s, "modules/")
}
