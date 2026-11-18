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
	// infoPkgDir/infoFileName is **the third** artifact: each spec's identity (its name,
	// which GitHub repo it ships from). A separate leaf package because **modules**
	// read it (self-update needs to know which artifact to download), and manifest
	// imports every module - module -> manifest is a compile cycle (arch-diagram hit it).
	infoPkgDir   = "manifest/distinfo"
	infoFileName = "specs_gen.go"
	// orderProbeDir is the probe program that computes each spec's start order; it runs in a
	// **new process** so it reads the manifest this run just wrote (see its package doc).
	orderProbeDir = "tools/distgen/orderprobe"
	// specGlob is the spec file pattern (dist.json / dist-simple-cli.json).
	specGlob = "dist*.json"
	// defaultSpec is the spec a build falls back to when nothing was injected.
	defaultSpec = "dist.json"
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

	target := filepath.Join(root, genPkgDir, genFileName)
	infoTarget := filepath.Join(root, infoPkgDir, infoFileName)

	// The file as committed, kept so -check can report on it and put it back.
	committed, readErr := os.ReadFile(target)

	// Pass 1: the component table **in spec order**, written to disk so the probe in pass 2 can
	// read it.
	//
	// A real write even under -check, because the probe is a new process and can only see what is
	// on disk -- and the answer must be computed from the table *this* run just derived from the
	// specs, not from the previous generation. -check therefore compares against **the bytes it
	// found** and restores them before returning, so a failing check leaves the tree untouched
	// (a check that dirties the file it is checking is a check people stop running).
	table, err := source(render(mods, specs, names, nil))
	if err != nil {
		fail(err)
	}
	if err := write(target, table); err != nil {
		fail(err)
	}
	infoContent, err := source(renderInfo(specs, names))
	if err != nil {
		fail(err)
	}

	// Pass 2: **a new process** computes each spec's start order from the table just written.
	//
	// Why a new process: this one's manifest is **compiled in** -- the previous generation. A
	// just-edited spec would get a silently wrong order from the old table, and "wrong order" is
	// miles from "stale manifest". A new process necessarily reads the file on disk.
	order, err := probeOrder(root, names)
	if *check {
		restore := committed
		if readErr != nil {
			restore = nil
		}
		if restore != nil {
			defer func() { _ = write(target, restore) }()
		}
	}
	if err != nil {
		fail(err)
	}

	// Pass 3: the same file again, each spec's components in start order.
	table, err = source(render(mods, specs, names, order))
	if err != nil {
		fail(err)
	}
	outputs := []struct {
		path string
		data []byte
	}{{target, table}, {infoTarget, infoContent}}

	if *check {
		for _, f := range outputs {
			var current []byte
			if f.path == target {
				current = committed
			} else {
				current, _ = os.ReadFile(f.path)
			}
			if readErr != nil && f.path == target {
				current = nil
			}
			if !bytes.Equal(current, f.data) {
				fail(fmt.Errorf("%s is stale: run `go run ./tools/distgen` and commit again", f.path))
			}
		}
		fmt.Printf("distgen: %d specs x %d own modules, all generated files are fresh\n",
			len(names), len(mods))
		return
	}
	for _, f := range outputs {
		if err := write(f.path, f.data); err != nil {
			fail(err)
		}
	}
	fmt.Printf("distgen: %d specs x %d own modules -> %s, %s\n",
		len(names), len(mods), target, infoTarget)
}

// source gofmts generated text; a formatting error means the generator itself is broken.
func source(raw []byte) ([]byte, error) {
	out, err := format.Source(raw)
	if err != nil {
		return nil, fmt.Errorf("the generated source is invalid (this is a generator bug): %w", err)
	}
	return out, nil
}

// write puts one generated file in place.
func write(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// probeOrder runs tools/distgen/orderprobe and returns, per spec, the **permutation** of its
// declared component list that is the start order.
func probeOrder(root string, names []string) (map[string][]int, error) {
	cmd := exec.Command("go", "run", "./"+orderProbeDir)
	cmd.Dir = root
	cmd.Stderr = os.Stderr
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("running %s failed (does a spec not assemble?): %w", orderProbeDir, err)
	}
	var order map[string][]int
	if err := json.Unmarshal(raw, &order); err != nil {
		return nil, fmt.Errorf("cannot parse the output of %s: %w", orderProbeDir, err)
	}
	for _, name := range names {
		if len(order[name]) == 0 {
			return nil, fmt.Errorf("the probe gave no order for %s -- was it skipped there?", name)
		}
	}
	return order, nil
}

// render generates the assembly manifest source. It must be **gofmt-clean itself**
// (check-fmt runs separately; a misaligned import block is steps from the cause).
//
// order is spec name -> component names in start order, or nil on the first pass. The whole
// point of the second pass is that **the emitted table IS the start order**: a Loader's slice
// is the order (see component.Loader), so there is no separate order table to fall out of sync
// with the component table.
func render(mods []module, specs map[string]spec, names []string, order map[string][]int) []byte {
	byDir := make(map[string]module, len(mods))
	for _, m := range mods {
		byDir[m.dir] = m
	}

	var b bytes.Buffer
	b.WriteString("// Code generated by tools/distgen. DO NOT EDIT.\n")
	b.WriteString("//\n")
	b.WriteString("// Compiled from the specs at the repository root (dist*.json): one component table per\n")
	b.WriteString("// spec, **in start order** (a provider before every consumer that Needs or Optionals\n")
	b.WriteString("// it). The order is computed here, at build time, by tools/distgen/orderprobe -- see\n")
	b.WriteString("// component.Loader: the slice a loader hands over is the order, so runtime never sorts.\n")
	b.WriteString("//\n")
	b.WriteString("// A build picks one spec with -ldflags -X distinfo.spec=<file name>; DefaultSpec is\n")
	b.WriteString("// the fallback when nothing was injected.\n")
	b.WriteString("\npackage " + genPkgDir + "\n\n")
	b.WriteString("import (\n")
	b.WriteString("\t\"fmt\"\n")
	b.WriteString("\t\"strings\"\n\n")
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

	b.WriteString("// Specs is each spec's component table, in start order.\n")
	b.WriteString("func Specs() map[string][]modules.Component {\n")
	b.WriteString("\treturn map[string][]modules.Component{\n")
	for _, name := range names {
		s := specs[name]
		want := s.Modules
		if order != nil {
			positions, ok := order[name]
			if !ok || len(positions) != len(s.Modules) {
				fail(fmt.Errorf("the probe gave %d positions for %s, the spec installs %d -- "+
					"the table it read is not the one this run wrote", len(positions), name, len(s.Modules)))
			}
			want = make([]string, 0, len(positions))
			seen := make([]bool, len(s.Modules))
			for _, i := range positions {
				if i < 0 || i >= len(s.Modules) || seen[i] {
					fail(fmt.Errorf("the probe returned %v for %s, which is not a permutation of its %d modules",
						positions, name, len(s.Modules)))
				}
				seen[i] = true
				want = append(want, s.Modules[i])
			}
		}
		fmt.Fprintf(&b, "\t\t%q: {\n", name)
		for _, dir := range want {
			m := byDir[dir]
			fmt.Fprintf(&b, "\t\t\t%s.New(),\n", m.alias)
		}
		b.WriteString("\t\t},\n")
	}
	b.WriteString("\t}\n}\n\n")

	b.WriteString("// SpecNames lists every spec name, sorted by file name.\n")
	fmt.Fprintf(&b, "func SpecNames() []string { return []string{%s} }\n\n", quoteList(names))
	b.WriteString("// Loader installs the graph named by a spec. An unknown name **errors**; it does not\n")
	b.WriteString("// fall back to the default -- \"I built the simple one, why does the UI show two\"\n")
	b.WriteString("// deserves an answer in the first second of startup, not a manual binary diff.\n")
	b.WriteString("type Loader struct{ Spec string }\n\n")
	b.WriteString("// Load returns the spec's components **already in start order** (see component.Loader).\n")
	b.WriteString("func (l Loader) Load() ([]modules.Component, error) {\n")
	b.WriteString("\tname := l.Spec\n")
	b.WriteString("\tif name == \"\" {\n")
	b.WriteString("\t\tname = DefaultSpec\n")
	b.WriteString("\t}\n")
	b.WriteString("\tcomps, ok := Specs()[name]\n")
	b.WriteString("\tif !ok {\n")
	b.WriteString("\t\treturn nil, fmt.Errorf(\"no spec named %q (available: %s)\", name, strings.Join(SpecNames(), \", \"))\n")
	b.WriteString("\t}\n")
	b.WriteString("\treturn comps, nil\n")
	b.WriteString("}\n")
	return b.Bytes()
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

// validate matches two lists that **know nothing of each other**, failing loudly on both.
//
// Both errors disguise themselves, so fail here:
//
//   - modules names a directory not under modules/ => a typo, or a forgotten copy. The symptom
//     would be "a package missing at compile", which looks like a generator bug.
//   - a modules/ directory no spec names => **no error** (that is normal: simple-cli exists
//     only for dist-simple-cli.json), but make it visible.
func validate(specs map[string]spec, mods []module) error {
	own := make(map[string]bool, len(mods))
	for _, m := range mods {
		own[m.dir] = true
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
