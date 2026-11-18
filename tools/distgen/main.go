// Command distgen compiles the repo-root specs (dist*.json) into the assembly manifest.
//
// It is the **distribution half** of the kernel's tools/genmodules: the kernel's
// generator scans its own modules/ for the module list (app.CoreModules()); this tool
// compiles the spec-named own modules + the kernel modules to disable into a
// map[spec name]app.Selection.
//
// Why this step (rather than main hand-writing app.Entry lines)
//
// Same reason as the kernel: "which modules are installed" is readable from the
// directory structure; a hand copy has no information and three places to sync.
// Install a module = copy the dir into modules/, write its name into the spec, rebuild.
//
// The criterion shares one ruler with the kernel (tools/genmodules/scan) - "which
// dirs count as components" and "how a dir name twists into an import alias" have one
// implementation, or one dir name gets two readings in two repos.
//
// # One artifact holds every spec
//
// Each spec gets an app.Selection, **all** generated into manifest/modules_gen.go; the
// build picks one with -ldflags -X distinfo.spec=<file>, so -check validates all
// variants at once: a broken dist-simple-cli.json reddens the default build, not later.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	app "github.com/rzbdz/newgate/app"
	modscan "github.com/rzbdz/newgate/tools/genmodules/scan"
)

const (
	// importPath is this module's path: the generated manifest imports its own modules with it.
	importPath = "github.com/rzbdz/newgate-ext"
	// coreImportPath is the kernel module's path (the output imports app and component).
	coreImportPath = "github.com/rzbdz/newgate"
	// modulesRelDir is the own modules directory (relative to module root = repo root).
	modulesRelDir = "modules"
	// genPkgDir/genFileName is where the output goes. Deliberately **not** dist/: that is
	// build/build.sh's binary output, and two things of one name make people guess at .gitignore.
	genPkgDir   = "manifest"
	genFileName = "modules_gen.go"
	// orderGenFileName is **the second** artifact: start order (see renderOrder).
	orderGenFileName = "order_gen.go"
	// infoPkgDir/infoFileName is **the third** artifact: each spec's identity (its name,
	// which GitHub repo it ships from). A separate leaf package because **modules**
	// read it (self-update needs to know which artifact to download), and manifest
	// imports every module - module -> manifest is a compile cycle (arch-diagram hit it).
	infoPkgDir   = "manifest/distinfo"
	infoFileName = "specs_gen.go"
	// orderProbeDir is the probe program that computes order (see its package doc).
	orderProbeDir = "tools/distgen/orderprobe"
	// specGlob is the spec file pattern (dist.json / dist-simple-cli.json).
	specGlob = "dist*.json"
	// specGlob is the spec file pattern (dist.json / dist-simple-cli.json).
	defaultSpec = "dist.json"
	// disableAllToken is the spec spelling for "all of the kernel's off", `["*"]`.
	//
	// Why: listing directory names carries **a copy that rots** - a module added to
	// CoreModules() does not grow that list, so a distribution meant to be off
	// silently gains one module, with no error. `*` compiles to app.Selection.AllCore.
	//
	// It lives only at the **spec** level (product data): the kernel field AllCore is
	// a bool and needs to know no string token.
	disableAllToken = "*"
)

// githubRepo is the shape of `owner/repo`. Loose as GitHub itself allows, strict enough to
// reject the two commonest mistakes: a full URL, and a clone address with `.git`.
var githubRepo = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+$`)

// spec is a spec (Spec): which modules this distribution consists of, which kernel ones are off.
//
// DisallowUnknownFields is deliberate: a misspelled key ("module" / "disabled") silently
// ignored gives "I configured it, why no effect" - only visible at runtime as a missing feature.
type spec struct {
	Distribution string   `json:"distribution"`
	Modules      []string `json:"modules"`
	Disable      []string `json:"disable"`
	// GitHub is **where this distribution ships** (`owner/repo`): self-update downloads
	// artifacts from this repo's Release and builds a tag from its source. Optional -
	// without it there is no "upgrade from Release", only a local build (see modules/self-update).
	//
	// It is in the spec, not the module, because "where it ships" is a **distribution**
	// fact: forking changes this line to point the upgrade channel at your repo, no Go code.
	GitHub string `json:"github,omitempty"`
}

// module is one own module directory to assemble.
type module struct {
	dir   string // the directory name under modules/
	path  string // import path suffix relative to the module root
	alias string // the generated import alias
}

func main() {
	check := flag.Bool("check", false,
		"only check whether the generated output matches the spec, do not write files (for CI/tests)")
	flag.Parse()

	// Flattened (2026-09-20): the module root is the repo root - Go code and specs live there.
	root, err := moduleRoot()
	repoRoot := root
	if err != nil {
		fail(err)
	}

	specs, names, err := loadSpecs(repoRoot)
	if err != nil {
		fail(err)
	}
	mods, err := scan(filepath.Join(root, modulesRelDir))
	if err != nil {
		fail(err)
	}
	if err := validate(specs, mods); err != nil {
		fail(err)
	}

	content, err := format.Source(render(mods, specs, names))
	if err != nil {
		fail(fmt.Errorf("the generated source is invalid (this is a generator bug): %w", err))
	}
	target := filepath.Join(root, genPkgDir, genFileName)
	orderTarget := filepath.Join(root, genPkgDir, orderGenFileName)
	infoTarget := filepath.Join(root, infoPkgDir, infoFileName)
	infoContent, err := format.Source(renderInfo(specs, names))
	if err != nil {
		fail(fmt.Errorf("the generated source is invalid (this is a generator bug): %w", err))
	}

	// **Ordering problem**: the listing above references startOrder from order_gen.go
	// (pasting build-time order back into each spec). On the first run it does not
	// exist, the package does not compile, the probe cannot run - so an empty
	// placeholder first. Empty is legal: no order = sort at runtime.
	if _, err := os.Stat(orderTarget); err != nil {
		if !*check {
			if err := os.MkdirAll(filepath.Dir(orderTarget), 0o755); err != nil {
				fail(err)
			}
			if err := os.WriteFile(orderTarget, formatSeed(), 0o644); err != nil {
				fail(err)
			}
		}
	}

	if *check {
		for _, f := range []struct {
			path string
			want []byte
		}{{target, content}, {infoTarget, infoContent}} {
			current, err := os.ReadFile(f.path)
			if err != nil || !bytes.Equal(current, f.want) {
				fail(fmt.Errorf("%s is stale: run `go run ./tools/distgen` and commit again", f.path))
			}
		}
	}

	if !*check {
		for _, f := range []struct {
			path string
			data []byte
		}{{target, content}, {infoTarget, infoContent}} {
			if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
				fail(err)
			}
			if err := os.WriteFile(f.path, f.data, 0o644); err != nil {
				fail(err)
			}
		}
	}

	// Second pass: **start a new process** running the real assembly resolution for order.
	//
	// Why a new process: this process's manifest is **compiled in** - the previous
	// generation. A just-edited spec gets silently wrong order from the old manifest,
	// and "wrong order" is miles from "stale manifest".
	orderContent, err := renderOrder(root, names)
	if err != nil {
		fail(err)
	}
	if *check {
		current, err := os.ReadFile(orderTarget)
		if err != nil || !bytes.Equal(current, orderContent) {
			fail(fmt.Errorf("%s is stale: run `go run ./tools/distgen` and commit again", orderTarget))
		}
		fmt.Printf("distgen: %d specs x %d own modules, all three generated files are fresh\n",
			len(names), len(mods))
		return
	}
	if err := os.WriteFile(orderTarget, orderContent, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("distgen: %d specs x %d own modules -> %s, %s, %s\n",
		len(names), len(mods), target, orderTarget, infoTarget)
}

// renderOrder runs tools/distgen/orderprobe and compiles its printed order into order_gen.go.
func renderOrder(root string, names []string) ([]byte, error) {
	cmd := exec.Command("go", "run", "./"+orderProbeDir)
	cmd.Dir = root
	cmd.Stderr = os.Stderr
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("running %s failed (does the spec not assemble?): %w", orderProbeDir, err)
	}
	var order map[string][]string
	if err := json.Unmarshal(raw, &order); err != nil {
		return nil, fmt.Errorf("cannot parse the output of %s: %w", orderProbeDir, err)
	}

	var b bytes.Buffer
	b.WriteString("// Code generated by tools/distgen. DO NOT EDIT.\n")
	b.WriteString("//\n")
	b.WriteString("// Each spec's **start order**, computed at build time. It is produced by running\n")
	b.WriteString("// `component.Resolve` once (see tools/distgen/orderprobe) -- the **same implementation**\n")
	b.WriteString("// is computed once and then frozen, not reimplemented here, so there is no\n")
	b.WriteString("// \"two implementations drift apart\" failure mode.\n")
	b.WriteString("//\n")
	b.WriteString("// Why it exists: the start order depends only on which modules are installed, and\n")
	b.WriteString("// at this layer that is a compile-time fact. Recomputing it on every process\n")
	b.WriteString("// invocation is wasted work that **is paid every time** (every CLI command, every\n")
	b.WriteString("// web backend start). See app.Selection.Order and component.resolve's fast path.\n")
	b.WriteString("package manifest\n\n")
	b.WriteString("// startOrder is spec name -> component names in start order.\n")
	b.WriteString("var startOrder = map[string][]string{\n")
	for _, name := range names {
		comps, ok := order[name]
		if !ok {
			return nil, fmt.Errorf("the probe gave no order for %s -- was it skipped there?", name)
		}
		fmt.Fprintf(&b, "\t%q: {\n", name)
		for _, c := range comps {
			fmt.Fprintf(&b, "\t\t%q,\n", c)
		}
		b.WriteString("\t},\n")
	}
	b.WriteString("}\n")
	return format.Source(b.Bytes())
}

// formatSeed is the placeholder when order_gen.go does not exist yet: an empty table.
func formatSeed() []byte {
	return []byte("// Code generated by tools/distgen. DO NOT EDIT.\n" +
		"//\n" +
		"// Placeholder: the real order is written by the second pass of `go run ./tools/distgen`.\n" +
		"package manifest\n\n" +
		"var startOrder = map[string][]string{}\n")
}

// moduleRoot walks up from cwd to the directory holding go.mod (`go run ./tools/distgen`
// runs at module root, tests may run elsewhere; locate via go.mod, not relative paths).
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("cannot find go.mod upward (starting from %s)", dir)
		}
		dir = parent
	}
}

// loadSpecs reads every spec at the repo root, sorted by file name.
//
// Sorting keeps output stable: random map order makes every generated diff jitter.
func loadSpecs(repoRoot string) (map[string]spec, []string, error) {
	paths, err := filepath.Glob(filepath.Join(repoRoot, specGlob))
	if err != nil {
		return nil, nil, err
	}
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("no specs under %s (%s) -- a distribution must have at least `distribution` and `modules`", repoRoot, specGlob)
	}
	out := make(map[string]spec, len(paths))
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, err
		}
		var s spec
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			return nil, nil, fmt.Errorf("cannot decode %s: %w", filepath.Base(p), err)
		}
		name := filepath.Base(p)
		if s.Distribution == "" {
			return nil, nil, fmt.Errorf("%s has no distribution field", name)
		}
		if len(s.Modules) == 0 {
			return nil, nil, fmt.Errorf("%s has an empty modules list -- a distribution that installs no modules is pointless", name)
		}
		if s.GitHub != "" && !githubRepo.MatchString(s.GitHub) {
			return nil, nil, fmt.Errorf("%s has github %q -- write it as `owner/repo` (no https://, no .git)", name, s.GitHub)
		}
		out[name] = s
		names = append(names, name)
	}
	sort.Strings(names)
	return out, names, nil
}

// scan lists every "is a component" directory under modules/, alphabetically.
func scan(componentsDir string) ([]module, error) {
	dirs, err := modscan.Dirs(componentsDir)
	if err != nil {
		return nil, err
	}
	out := make([]module, 0, len(dirs))
	for _, dir := range dirs {
		rel := path.Join(modulesRelDir, dir)
		out = append(out, module{dir: dir, path: rel, alias: modscan.Ident("ext_", dir)})
	}
	return out, nil
}

// validate matches two lists that **know nothing of each other**, failing loudly on all.
//
// All three errors disguise themselves, so fail here:
//
//   - modules names a directory not under modules/ => a typo, or a forgotten copy.
//     The symptom would be "a package missing at compile", which looks like a generator bug.
//   - a modules/ directory no spec names => **no error** (that is normal: simple-cli exists
//     only for dist-simple-cli.json), but make it visible.
//   - disable names a module the kernel lacks => the worst: the user thinks it is off,
//     but it runs (Selection.Load also catches it, this is the earlier build-time catch).
func validate(specs map[string]spec, mods []module) error {
	own := make(map[string]bool, len(mods))
	for _, m := range mods {
		own[m.dir] = true
	}
	core := make(map[string]bool)
	for _, e := range app.CoreModules() {
		core[e.Dir] = true
	}

	named := map[string]bool{}
	for _, name := range sortedKeys(specs) {
		s := specs[name]
		for _, want := range s.Modules {
			if !own[want] {
				return fmt.Errorf("%s names %q, but modules/%s is not a component (missing module.go or no exported New())",
					name, want, want)
			}
			named[want] = true
		}
		for i, off := range s.Disable {
			if off == disableAllToken {
				if len(s.Disable) > 1 {
					return fmt.Errorf("%s has both %q and concrete names in disable (%v) --"+
						"%q already covers them. To disable everything, keep only %q; to disable only a few, do not write it",
						name, disableAllToken, s.Disable[i+1:], disableAllToken, disableAllToken)
				}
				continue
			}
			if !core[off] {
				return fmt.Errorf("%s disable names %q, but the kernel has no such module"+
					"(did you write the **directory name**? e.g. claudecode_deepseek rather than claudecode-deepseek."+
					"To disable everything the kernel ships, write %q)", name, off, disableAllToken)
			}
		}
	}
	for _, m := range mods {
		if !named[m.dir] {
			fmt.Printf("distgen: note: modules/%s is not named by any spec (it is not installed)\n", m.dir)
		}
	}
	return nil
}

func sortedKeys(m map[string]spec) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// render generates the assembly manifest source. It must be **gofmt-clean itself**
// (check-fmt runs separately; a misaligned import block is steps from the cause).
func render(mods []module, specs map[string]spec, names []string) []byte {
	byDir := make(map[string]module, len(mods))
	for _, m := range mods {
		byDir[m.dir] = m
	}

	var b bytes.Buffer
	b.WriteString("// Code generated by tools/distgen. DO NOT EDIT.\n")
	b.WriteString("//\n")
	b.WriteString("// Compiled from the specs at the repository root (dist*.json): each app.Selection is\n")
	b.WriteString("// everything one spec installs -- its own modules plus the kernel modules it turns\n")
	b.WriteString("// off. A build picks one with -ldflags -X distinfo.spec=<file name>; DefaultSpec is\n")
	b.WriteString("// the fallback when nothing was injected.\n")
	b.WriteString("//\n")
	b.WriteString("// `\"disable\": [\"*\"]` in a spec compiles to app.Selection.AllCore: none of the\n")
	b.WriteString("// kernel's own modules, only the ones that cannot be turned off (the ports the\n")
	b.WriteString("// composition root itself uses).\n")
	b.WriteString("\npackage " + genPkgDir + "\n\n")
	b.WriteString("import (\n")
	b.WriteString("\t\"fmt\"\n")
	b.WriteString("\t\"strings\"\n\n")
	b.WriteString("\tapp \"" + coreImportPath + "/app\"\n")
	b.WriteString("\tmodules \"" + coreImportPath + "/component\"\n")
	for _, m := range mods {
		fmt.Fprintf(&b, "\t%s \"%s/%s\"\n", m.alias, importPath, m.path)
	}
	b.WriteString(")\n\n")

	b.WriteString("// DefaultSpec is the spec used when -X distinfo.spec was not injected.\n")
	b.WriteString("//\n")
	b.WriteString("// It lets `go run ./cmd/newgate` and `go test ./...` run without building first --\n")
	b.WriteString("// the default is the repository's main spec, the same one the real binary uses.\n")
	fmt.Fprintf(&b, "const DefaultSpec = %q\n\n", defaultSpec)

	b.WriteString("// Specs is each spec's assembly selection.\n")
	b.WriteString("func Specs() map[string]app.Selection {\n")
	b.WriteString("\treturn map[string]app.Selection{\n")
	for _, name := range names {
		s := specs[name]
		fmt.Fprintf(&b, "\t\t%q: {\n", name)
		if len(s.Disable) == 1 && s.Disable[0] == disableAllToken {
			// One word, not a dozen directory names: AllCore grows with the kernel's module table.
			b.WriteString("\t\t\tAllCore: true,\n")
		} else if len(s.Disable) > 0 {
			fmt.Fprintf(&b, "\t\t\tDisable: []string{%s},\n", quoteList(s.Disable))
		}
		b.WriteString("\t\t\tExtra: []app.Entry{\n")
		for _, want := range s.Modules {
			m := byDir[want]
			fmt.Fprintf(&b, "\t\t\t\t{Dir: %q, Component: %s.New()},\n", m.dir, m.alias)
		}
		b.WriteString("\t\t\t},\n")
		b.WriteString("\t\t},\n")
	}
	b.WriteString("\t}\n}\n\n")

	b.WriteString("// SpecNames lists every spec name, sorted by file name.\n")
	fmt.Fprintf(&b, "func SpecNames() []string { return []string{%s} }\n\n", quoteList(names))

	b.WriteString("// Loader installs the graph named by a spec. An unknown name **errors**; it does not\n")
	b.WriteString("// fall back to the default -- \"I built the simple one, why does the UI show two\"\n")
	b.WriteString("// deserves an answer in the first second of startup, not a manual binary diff.\n")
	b.WriteString("type Loader struct{ Spec string }\n\n")
	// The blueprint (see component.Precomputed): installed modules are fixed at **build**
	// time; order is a function of that set, so runtime should not redo it - a per-CLI cost.
	b.WriteString("// PrecomputedOrder hands over the start order the distribution computed at build time.\n")
	b.WriteString("//\n")
	b.WriteString("// With it, the runtime does not build a port table, build dependency edges, topo-sort\n")
	b.WriteString("// or look for cycles -- all that happened at build time (tools/distgen runs a real\n")
	b.WriteString("// Resolve; see order_gen.go). A manifest that disagrees with the selection errors on\n")
	b.WriteString("// the spot instead of staying quiet.\n")
	b.WriteString("func (l Loader) PrecomputedOrder() []string {\n")
	b.WriteString("\tname := l.Spec\n")
	b.WriteString("\tif name == \"\" {\n")
	b.WriteString("\t\tname = DefaultSpec\n")
	b.WriteString("\t}\n")
	b.WriteString("\treturn startOrder[name]\n")
	b.WriteString("}\n\n")
	b.WriteString("func (l Loader) Load() ([]modules.Component, error) {\n")
	b.WriteString("\tname := l.Spec\n")
	b.WriteString("\tif name == \"\" {\n")
	b.WriteString("\t\tname = DefaultSpec\n")
	b.WriteString("\t}\n")
	b.WriteString("\tsel, ok := Specs()[name]\n")
	b.WriteString("\tif !ok {\n")
	b.WriteString("\t\treturn nil, fmt.Errorf(\"no spec named %q (available: %s)\", name, strings.Join(SpecNames(), \", \"))\n")
	b.WriteString("\t}\n")
	b.WriteString("\treturn sel.Load()\n")
	b.WriteString("}\n")
	return b.Bytes()
}

// renderInfo generates the spec identity table in manifest/distinfo.
//
// Only "this spec's name and where it ships" - **not the module table**: this package
// is imported by modules, and the module table imports modules (see infoPkgDir).
func renderInfo(specs map[string]spec, names []string) []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated by tools/distgen. DO NOT EDIT.\n")
	b.WriteString("//\n")
	b.WriteString("// Each spec's identity: distribution (artifact name and version prefix) and github\n")
	b.WriteString("// (the release repository). Same source as manifest/modules_gen.go (the dist*.json\n")
	b.WriteString("// at the repository root); kept apart so modules can read it without a cycle -- see\n")
	b.WriteString("// tools/distgen's infoPkgDir.\n")
	b.WriteString("\npackage distinfo\n\n")
	b.WriteString("// DefaultSpec is the spec used when -X was not injected (same value as manifest.DefaultSpec).\n")
	fmt.Fprintf(&b, "const DefaultSpec = %q\n\n", defaultSpec)
	b.WriteString("// specs is spec file name -> identity.\n")
	b.WriteString("var specs = map[string]Info{\n")
	for _, name := range names {
		s := specs[name]
		fmt.Fprintf(&b, "\t%q: {Spec: %q, Distribution: %q", name, name, s.Distribution)
		if s.GitHub != "" {
			fmt.Fprintf(&b, ", GitHub: %q", s.GitHub)
		}
		b.WriteString("},\n")
	}
	b.WriteString("}\n")
	return b.Bytes()
}

// quoteList renders names into a Go literal list (`"a", "b"`).
func quoteList(xs []string) string {
	parts := make([]string, 0, len(xs))
	for _, x := range xs {
		parts = append(parts, fmt.Sprintf("%q", x))
	}
	return strings.Join(parts, ", ")
}

// fail is the generator's exit: one sentence on what is wrong, exit code 1.
func fail(err error) {
	fmt.Fprintln(os.Stderr, "distgen:", err)
	os.Exit(1)
}
