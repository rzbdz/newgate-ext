package disttesting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"testing"
)

// siteDefaultLang is the language **mounted at the root path** of the public site (see
// tools/sitegen's defaultLang). Written here, not imported: that one lives in `package
// main`, and this test must read **the directory name on disk** - both written once; a
// language change makes this fail to read the file (wanted: silently skipping is worse).
const siteDefaultLang = "zh-Hans"

// This pins a **cross-spec** relation: the dev one must be exactly the flagship one
// **plus** arch-diagram.
//
// # Why a test (it caught a real drift)
//
// CLAUDE.md defines dev as "default modules + arch-diagram", which is not a description but
// a **requirement**: dev exists so "I can see **the product assembly's** internals". It
// serves `/arch`, and that graph shows **what this process installed** - one module missing
// from dev and the graph is no longer the product's, the only thing reviewed.
//
// How the drift happened (measured 2026-09-20 -> 09-21): `a7b7fde` built `dist-dev.json`
// copying **the then** default table + arch-diagram; the next day codex / codex_deepseek
// joined `dist.json` (`20aa52e` / `969ae33`), and nothing reminded "a second spec copies
// that table". So dev silently lost two blocks, unreported: dev built, CI was green, the
// graph simply **lacked two modules** - on an architecture graph, missing looks like absent.
//
// This is the second instance of "a full list is a copy of that table" (the first is the
// kernel module table, see CLAUDE.md section 3 on `"disable": ["*"]`). The difference: this
// one **has no other spelling** - dev = default + the difference - so the copy must exist,
// and a test pins it.
//
// The criterion compares **sets**, not arrays: spec order does not affect assembly (start
// order is computed by `tools/distgen` via a real Resolve, see manifest/order_gen.go), and
// comparing order would redden a harmless reordering.
func TestDevSpecIsTheDefaultPlusTheMap(t *testing.T) {
	root := repoRoot(t)
	def := readSpecModules(t, filepath.Join(root, "dist.json"))
	dev := readSpecModules(t, filepath.Join(root, "dist-dev.json"))

	// Expectation: every default module is in dev, plus arch-diagram; and the reverse - what
	// is extra is **the difference itself**; one more or less must be reported.
	want := map[string]bool{archDiagram: true}
	for _, m := range def {
		want[m] = true
	}

	have := map[string]bool{}
	for _, m := range dev {
		have[m] = true
	}

	var missing, extra []string
	for m := range want {
		if !have[m] {
			missing = append(missing, m)
		}
	}
	for m := range have {
		if !want[m] {
			extra = append(extra, m)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) > 0 {
		t.Errorf("dist-dev.json is missing %v -- dev is \"the default modules + %s\", and a module that is missing looks"+
			"exactly like \"it does not exist\" on the /arch map (see the comment on this test). Add it to dist-dev.json then "+
			"`go run ./tools/distgen`", missing, archDiagram)
	}
	if len(extra) > 0 {
		t.Errorf("dist-dev.json has extra %v -- it should only have one more than the default spec, %s; the extras make /arch "+
			"draw an assembly that **does not exist in the product**, which is worse than drawing too little (it looks right)",
			extra, archDiagram)
	}
}

// siteModuleCountRe grabs the site's "N modules" sentence.
//
// A regex rather than whole-sentence equality: this is **prose for humans**, wording may
// change (that must not redden a criterion); the criterion is **the number in it**.
var siteModuleCountRe = regexp.MustCompile(`(\d+) 个模块`)

// TestTheSiteStatesTheDefaultSpecsModuleCount: the public site's "N modules" must equal
// what `dist.json` installs.
//
// # Why it drifts (measured once)
//
// `d5555a4` wrote the page when `dist.json` had 11; the next day `969ae33` added
// codex_deepseek to the default spec, and the sentence became **false** - the site still
// generated, CI still green. Same **class** of drift as TestDevSpecIsTheDefaultPlusTheMap:
// a fact copied from a spec, true when copied, source changed silently.
//
// # Why not drop the number
//
// It is the page's opening: "which assembly this site talks about" needs a concrete
// quantity. Vagueness ("a whole row of modules") deletes its use - **restoring the number**
// beats deleting it, provided it cannot rot again.
//
// The site is markdown, the number is code, no build-time seam (site output is not in
// version control - see tools/sitegen/main.go's header), so this is the only place to pin it.
func TestTheSiteStatesTheDefaultSpecsModuleCount(t *testing.T) {
	root := repoRoot(t)
	mods := readSpecModules(t, filepath.Join(root, "dist.json"))

	// This page is the "docs" landing page: it says it describes dist.json, so numbers must match.
	// Other pages carry no number; if they do, that is another decision (assert everywhere?).
	path := filepath.Join(root, "site", "src", siteDefaultLang, "docs", "index.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read the site page (%s): %v", path, err)
	}
	m := siteModuleCountRe.FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("cannot find the \"N modules\" sentence in %s --"+
			"the opening of this page says \"which assembly this site is about\"; without that sentence this check spins empty", path)
	}
	if m[1] != strconv.Itoa(len(mods)) {
		t.Errorf("the site says %s modules, but dist.json installs %d --"+
			"this is a fact copied from the spec, and whoever changes the spec will not remember to update the doc (it drifted once in practice:"+
			"11 -> 12). Fix %s", m[1], len(mods), path)
	}
}

// archDiagram is the **only** thing dev may have over default.
//
// Written here, not read from somewhere: it is this criterion's **whole content**. "Should
// dev install arch-diagram" is not derivable - it is this distribution's product decision
// (see CLAUDE.md section 5), so it can only be written down once, and a test guards it.
const archDiagram = "arch-diagram"

// readSpecModules reads a spec's modules list.
//
// **From disk, not from manifest**: manifest/modules_gen.go is these specs' *product*, and
// validating a source with its product passes when both are wrong (edited spec, no regen),
// making this test spin. The JSON on disk is the source.
func readSpecModules(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", filepath.Base(path), err)
	}
	var spec struct {
		Modules []string `json:"modules"`
	}
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatalf("%s is not valid JSON: %v", filepath.Base(path), err)
	}
	if len(spec.Modules) == 0 {
		t.Fatalf("%s has an empty modules list -- that spec installs nothing, and the check would spin green",
			filepath.Base(path))
	}
	return spec.Modules
}

// repoRoot walks up from the test's package directory to the repo root (go.mod + dist.json).
//
// Same criterion as tools/sitegen's repoRoot: go.mod alone matches `core/` too -
// it is a module, and we would stop at the kernel's level.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("locating the repo root: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "dist.json")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("cannot find the repo root (it must have both go.mod and dist.json)")
		}
		dir = parent
	}
}
