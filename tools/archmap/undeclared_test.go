package archmap_test

import (
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/manifest"
	"github.com/rzbdz/newgate-ext/tools/archmap"
)

// TestReportUndeclaredImports looks for **dependencies nobody declared**: a module
// imports another module's **root package**, with no matching edge on the assembly map.
//
// The criterion is CLAUDE.md's: "where the maps disagree is the clue: imported but no
// dependency edge means a dependency nobody declared (compiles, tests, review misses it,
// and only a change shows it cannot be split)". Imports are **compiler facts**,
// Requires/Optional are **declarations** - miss one, it still compiles and tests green.
//
// # Why not the archmap import map (the first version did)
//
// That map's points are **module-granular**: `nodeOf` folds `modules/cli/extension`
// into `modules/cli` (see imports.go's groupDirs). Right for "a map for people",
// fatal for this probe - it cannot tell these two apart:
//
//	import ".../modules/gateway"          <- bound to its **contract**, a real dependency
//	import ".../modules/config/domain"    <- bound to a **shared leaf**, not a dependency
//
// So `breaker -> config` (really importing `config/paths`) looks exactly like a real
// dependency. The first version had mostly such noise in its 55 pairs; a report of
// nothing but noise never gets a second look.
//
// So the ruler changes: ask `go list` "is any imported string **exactly** a module's root
// package". Root package = `.../modules/<name>`, no further slash.
//
// # Why it is a ratchet today (expected 0)
//
// The first run on 2026-09-21 found **1 real problem**: gateway's Start calls
// `MustGet(confighook.ConfigHooksCapability)` with no declared gateway -> confighook
// edge (a sibling of the runtime spot app/declared_test.go's capability ratchet guards).
//
// After the fix the list is empty, so stop letting it be just a "report": **an empty
// set can be asserted**, and only an asserted empty set stops the next one. When a
// legitimate compile-time edge appears, add a line to allowedRootImports with a
// reason - that road costs code, more than "look and forget", which is the price we want.
func TestNoUndeclaredRootImports(t *testing.T) {
	root := repoRoot(t)
	report, err := archmap.Build(manifest.Specs(), root)
	if err != nil {
		t.Fatalf("generating the architecture map failed: %v", err)
	}

	// Declared edges: "who depends on whom" on the assembly maps. Union over specs - a
	// spec turning a module off does not mean no declaration exists between modules.
	declared := map[[2]string]bool{}
	for _, spec := range report.Specs {
		for _, e := range spec.Edges {
			declared[[2]string{norm(e.From), norm(e.To)}] = true
		}
	}
	if len(declared) == 0 {
		t.Fatal("the assembly map has no edges at all -- the declarations were not read, so the report below is spinning empty")
	}

	// Component-name form (strip - and _), to tell "is this module a component at all".
	componentNames := map[string]bool{}
	for _, spec := range report.Specs {
		for _, n := range spec.Nodes {
			if !strings.HasPrefix(n.ID, "?") {
				componentNames[norm(n.ID)] = true
			}
		}
	}
	if len(componentNames) == 0 {
		t.Fatal("the assembly map has no components at all -- the declarations were not read, so the report below is spinning empty")
	}

	found := map[[2]string]bool{}
	seenModules := map[string]bool{}
	for _, dir := range []string{root, filepath.Join(root, "core")} {
		for _, p := range listPackages(t, dir) {
			from, ok := moduleOf(p.path)
			if !ok {
				continue
			}
			seenModules[from] = true
			for _, imp := range p.imports {
				to, ok := rootModuleOf(imp)
				// Both ends must be **components**: a module not yet made a component (see below)
				// has no Requires to compare; counting it in gives an unactionable report.
				if !ok || to == from || !componentNames[from] || !componentNames[to] {
					continue
				}
				if declared[[2]string{from, to}] {
					continue
				}
				found[[2]string{from, to}] = true
			}
		}
	}

	// Not a component yet: packages under `modules/<x>` are real, but **no spec installs
	// it** (today's example is configshare: `module.go` has only a package doc, no
	// `New()`). No Requires to compare, so skip - but say so, or "the probe missed a
	// module" and "that module was never on disk" look identical.
	var notComponents []string
	for m := range seenModules {
		if !componentNames[m] {
			notComponents = append(notComponents, m)
		}
	}
	sort.Strings(notComponents)
	if len(notComponents) > 0 {
		t.Logf("(skipping %d modules that are not components yet: %v)", len(notComponents), notComponents)
	}

	for p := range allowedRootImports {
		delete(found, p)
	}

	pairs := make([][2]string, 0, len(found))
	for p := range found {
		pairs = append(pairs, p)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
	for _, p := range pairs {
		t.Errorf("the code of %s imports the **root package** of module %s, but there is no such dependency edge on the assembly map.\n"+
			"  It compiled, it was tested, review could not see it, and only when changing something does it turn out it cannot be split -- either declare it in Requires "+
			"or demote it to a shared leaf (import a subpackage).\n"+
			"  If this really is intentional, add (%q, %q) to allowedRootImports and write down why.",
			p[0], p[1], p[0], p[1])
	}
}

// allowedRootImports is the **exemption list**, today empty.
//
// Adding one requires writing why it needs no declaration. Empty is not decoration: it
// turns "add an exemption" into a code edit with a reason, not "read a report and forget".
var allowedRootImports = map[[2]string]bool{}

type listed struct {
	path    string
	imports []string
}

// listPackages runs `go list` once, getting what each package in this module imports.
//
// `-e` keeps an unbuildable package from losing the whole report (same trade as
// imports.go); only **non-test** imports (`go list`'s .Imports excludes `_test.go`) -
// how tests draw lines is not a product dependency.
func listPackages(t *testing.T, dir string) []listed {
	t.Helper()
	cmd := exec.Command("go", "list", "-e", "-f",
		`{{.ImportPath}}{{"\t"}}{{join .Imports " "}}`, "./...")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list failed in %s: %v", dir, err)
	}
	var pkgs []listed
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		path, rest, _ := strings.Cut(line, "\t")
		pkgs = append(pkgs, listed{path: path, imports: strings.Fields(rest)})
	}
	return pkgs
}

// Module prefixes of the two repos. The cross-repo edge is compared here too: replace
// only makes it compile; "who depends on whom" still has to be declared.
var modulePrefixes = []string{
	"github.com/rzbdz/newgate/modules/",
	"github.com/rzbdz/newgate-ext/modules/",
}

// moduleOf says which module a package belongs to (by **directory name**), false if not.
func moduleOf(pkg string) (string, bool) {
	for _, p := range modulePrefixes {
		rest, ok := strings.CutPrefix(pkg, p)
		if !ok || rest == "" {
			continue
		}
		if i := strings.Index(rest, "/"); i >= 0 {
			rest = rest[:i]
		}
		return norm(rest), true
	}
	return "", false
}

// rootModuleOf says whether this import is **some module's root package**, and which.
//
// "Root package" is the core of the criterion: `.../modules/gateway` has no further
// slash. One more segment is a leaf (`.../modules/config/domain`), and leaves may be
// imported by anyone, not counted as dependency edges (CLAUDE.md's "shared leaf").
func rootModuleOf(imp string) (string, bool) {
	for _, p := range modulePrefixes {
		rest, ok := strings.CutPrefix(imp, p)
		if !ok || rest == "" {
			continue
		}
		if strings.Contains(rest, "/") {
			return "", false // 叶子：不是根包
		}
		return norm(rest), true
	}
	return "", false
}

// norm strips `-` and `_`: directory names suit Go, component names suit people, this
// aligns the two (`modules/confighook` is component `config-hook`, `modules/opencodeomo`
// is `opencode-omo`).
func norm(s string) string {
	return strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(s))
}
