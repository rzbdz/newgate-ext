package archmap_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/manifest"
	"github.com/rzbdz/newgate-ext/tools/archmap"
)

// This test does two things: **generate the architecture map** (dist/architecture.html,
// double-click offline) and, **along the way, assert its mechanically checkable directions**.
//
// Why put the assertions with the artifact: the map is for people and people miss
// things; these maps carry the repo's key directions (kernel does not know the
// distribution, data plane not the policy, assembly has no cycles). Generation
// **must** pass them first to earn review - a wrong map is worse than none, it lies.
//
// Assertions run on the map's data, not on HTML text: HTML is rendering, data is the conclusion.
func TestArchitectureMap(t *testing.T) {
	root := repoRoot(t)

	// There is only one implementation of how the report is assembled (archmap.Build):
	// the test and modules/arch-diagram's "mount it on the port" caller take the same
	// path. Written separately once, and the two grew crooked on "which step fails, how to degrade".
	report, err := archmap.Build(manifest.Specs(), root)
	if err != nil {
		t.Fatalf("cannot produce the report (does %s fail to parse?): %v", "resolve", err)
	}

	// ---- one, assembly map: one per spec ----
	if len(report.Specs) == 0 {
		t.Fatal("no specs at all -- the generator has drifted from the specs")
	}
	for _, g := range report.Specs {
		assertLayeringDirection(t, g.ID, g)
		assertLayered(t, g.ID, g)
	}

	// ---- two, import map: the scanned facts ----
	imports := report.Imports
	if len(imports.Nodes) == 0 {
		t.Fatalf("the import graph is empty: %s", imports.Note)
	}
	assertLayeringDirection(t, "imports", imports)
	assertLayered(t, "imports", imports)
	assertKernelNeverImportsTheDistribution(t, imports)

	// ---- three, artifacts ----
	html, err := archmap.Render(report)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := filepath.Join(root, "dist", "architecture.html")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("architecture map: %s (%d specs + import graph %d nodes/%d edges)",
		out, len(report.Specs), len(imports.Nodes), len(imports.Edges))
}

// assertLayeringDirection pins two things, both required:
//
//   - **edges not in the same strongly connected component must point from the
//     lower layer to the upper one** (dependencies stay below);
//   - **edges inside one component must land on the same layer** (those are the
//     aggregation-made cycles, drawn on one band and marked red).
//
// The framework refuses cycles, Go forbids import cycles - both are free. Keeping
// this guards **this tool**: miscomputed layering (reversed edges, bad SCCs) gives
// the same symptom - all points on one layer, "a bit ugly", nobody blames the tool.
func assertLayeringDirection(t *testing.T, name string, g archmap.Graph) {
	t.Helper()
	layer := map[string]int{}
	for _, n := range g.Nodes {
		layer[n.ID] = n.Layer
	}
	for _, e := range g.Edges {
		from, okFrom := layer[e.From]
		to, okTo := layer[e.To]
		if !okFrom || !okTo {
			t.Errorf("%s: edge %s->%s points at a node that is not in the graph", name, e.From, e.To)
			continue
		}
		if e.Cycle {
			if from != to {
				t.Errorf("%s: %s(L%d) -> %s(L%d) is marked Cycle but crosses layers --"+
					"nodes in a cycle must land on the same band", name, e.From, from, e.To, to)
			}
			continue
		}
		if from <= to {
			t.Errorf("%s: %s(L%d) -> %s(L%d) violates \"dependencies are below\" --"+
				"either the strongly connected components are computed wrong or the layering is", name, e.From, from, e.To, to)
		}
	}
}

// assertLayered requires layering to be **fully used**: if no point landed on L0,
// the first layer was computed and nothing went up (typically the direction is reversed).
func assertLayered(t *testing.T, name string, g archmap.Graph) {
	t.Helper()
	if len(g.Nodes) == 0 {
		t.Errorf("%s: the graph is empty", name)
		return
	}
	perLayer := map[int]int{}
	for _, n := range g.Nodes {
		perLayer[n.Layer]++
	}
	if perLayer[0] == 0 {
		t.Errorf("%s: no node is on layer 0 -- layering did not work", name)
	}
	if g.Layers < 2 && len(g.Edges) > 0 {
		t.Errorf("%s: %d edges but only %d layers", name, len(g.Edges), g.Layers)
	}
}

// assertKernelNeverImportsTheDistribution is the most valuable check on this map:
// **the kernel does not know the distribution**.
//
// It is the other half of app/independence_test.go's rule - that one looked for "a
// Pin file / modules-ext directory coming back" (the historical shape); this one
// checks the **compile-time fact**: does any kernel package import a distribution
// package. Break it, the kernel cannot ship or test alone, and the distribution cannot fork.
func assertKernelNeverImportsTheDistribution(t *testing.T, g archmap.Graph) {
	t.Helper()
	checked := 0
	for _, e := range g.Edges {
		// node id is `<repo>:<import package>` (see imports.go's nodeOf).
		if !strings.HasPrefix(e.From, "core:") {
			continue
		}
		checked++
		if strings.HasPrefix(e.To, "ext:") {
			t.Errorf("the kernel imports the distribution: %s -> %s\n"+
				"    the kernel must be the one that can be released on its own (replace is one-way)", e.From, e.To)
		}
	}
	if checked == 0 {
		t.Fatal("not a single import edge inside the kernel was scanned -- the scan is broken and this assertion checks nothing")
	}
}

// repoRoot walks up from this file to the repo root (the level with go.mod that is not core/).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "dist.json")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("cannot find the repo root (there should be a dist.json)")
		}
		dir = parent
	}
}

// legendKeyLine grabs one entry of the legend table: the key in `infra:{c:"#5b8def",t:"…"}`.
//
// No line-start anchor: the table lays **two entries per line** (compact, looks good),
// so anchoring sees only the first of each line and the test misses gateway (2026-09-20).
var legendKeyLine = regexp.MustCompile(`([a-z][a-z0-9]*)\s*:\s*\{c:"`)

// legendKeys parses the legend table's (GROUPS) keys out of the renderer.
//
// Why read source instead of exporting it as Go: the table is **rendering**
// (colors live with the JS), moving it to Go for test visibility adds needless
// indirection; one text read, same trade as the front-end gates (dict, Kind, freshness).
func legendKeys(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("render.go")
	if err != nil {
		t.Fatalf("cannot read render.go (%v) -- if the path changed, change this test too", err)
	}
	src := string(raw)
	start := strings.Index(src, "const GROUPS = {")
	if start < 0 {
		t.Fatal("cannot find GROUPS in render.go -- if it was renamed, change this test too")
	}
	body := src[start:]
	if end := strings.Index(body, "\n};"); end >= 0 {
		body = body[:end]
	}
	keys := map[string]bool{}
	for _, m := range legendKeyLine.FindAllStringSubmatch(body, -1) {
		keys[m[1]] = true
	}
	if len(keys) == 0 {
		t.Fatal("not a single GROUPS key was parsed -- the regex and the file disagree")
	}
	return keys
}

// TestEveryModuleTypeHasALegendColour is the ratchet "the legend must not paint two things alike".
//
// Group names are the **module's declared Type** (see component.Type); the legend
// table lives in the renderer: one table aligns both, and nobody remembers it when
// a family is added. Forgetting is **silent** - those modules paint the same grey
// (groupOf's fallback), map fine, legend short. Measured 2026-09-20: client / model /
// bridge / others / builtin became one "other modules" row - found by looking, no test red.
func TestEveryModuleTypeHasALegendColour(t *testing.T) {
	keys := legendKeys(t)

	types := map[string]bool{}
	for name, sel := range manifest.Specs() {
		comps, err := sel.Load()
		if err != nil {
			t.Fatalf("cannot read the assembly selection for %s: %v", name, err)
		}
		for _, c := range comps {
			if c.Type != "" {
				types[string(c.Type)] = true
			}
		}
	}
	if len(types) == 0 {
		t.Fatal("not a single module Type was read -- this assertion checks nothing")
	}

	for typ := range types {
		if !keys[typ] {
			t.Errorf("module Type %q has no entry in the legend table (GROUPS in render.go) --"+
				"it will be drawn gray with its own name on it, mixed together with other families that have no entry", typ)
		}
	}
	// Synthetic groups: the dashed "this distribution did not install it", and the two
	// that colour the import map by repo. They are not module Types, but the renderer needs them.
	for _, k := range []string{"missing", "core", "ext", "module"} {
		if !keys[k] {
			t.Errorf("the legend table is missing the composite group %q (not a module Type, but the renderer uses it)", k)
		}
	}
}
