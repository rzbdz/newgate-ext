package archmap

import (
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
)

// pkgPrefixes are the two prefixes of "our own" packages.
//
// The criterion is **repo ownership**, not a nice path: the kernel (github.com/rzbdz/newgate/...)
// and the distribution (github.com/rzbdz/newgate-ext/...). Stdlib and third-party never
// enter the map, or it is hundreds of points and nothing reads.
var pkgPrefixes = []string{
	"github.com/rzbdz/newgate-ext/",
	"github.com/rzbdz/newgate/",
}

// groupDirs is the criterion for **which level to aggregate at**.
//
// Map elements are **import packages (directories)**, not each Go file package: an
// edge between `modules/gateway/forward` and `modules/gateway/rewrite` carries no
// information - they belong to the same module, and the module is the "one thing".
// So packages in one directory become one rounded rectangle; edges only between dirs.
//
// This is also the level where the only review question worth asking lives: "who
// does gateway depend on" and "who depends on config", not "does rewrite use i18n".
var groupDirs = map[string]bool{
	"modules": true, "lib": true, "tools": true, "testing": true, "cmd": true,
}

type listedPkg struct {
	ImportPath string
	Name       string
	Imports    []string
}

// ImportGraph scans every package's imports in both repos, aggregates to directory level.
//
// Why `go list` instead of parsing import statements: parsing source means handling
// build constraints, generated files, `_test.go`, and "is this package even in this
// module"; `go list` gives **the compiler's own facts**. The price is running go
// (on dev machines and CI - this path is only used in development anyway).
//
// The two modules are listed separately: `./...` lists only the current module's
// packages, and the kernel is another module (replaced to ./core), so name its prefix.
func ImportGraph(root string) (Graph, error) {
	pkgs, err := goList(root, "./...")
	if err != nil {
		return Graph{}, err
	}
	core, err := goList(root, "github.com/rzbdz/newgate/...")
	if err != nil {
		return Graph{}, err
	}
	pkgs = append(pkgs, core...)

	// First pin down "which packages are ours", then fold them into directories.
	members := map[string][]string{} // 节点 id → 目录里的包
	for _, p := range pkgs {
		if !isOurs(p.ImportPath) {
			continue
		}
		id := nodeOf(p.ImportPath)
		members[id] = append(members[id], p.ImportPath)
	}

	g := Graph{
		ID:      "imports",
		Title:   "imports (scanned, by package)",
		Compact: true,
		Note: "who imports whom - what the compiler sees. One box is one import package " +
			"(a directory), not one Go package. Grey edges stay inside a repo; the pink ones " +
			"cross the replace boundary between the kernel and the distribution.",
	}
	ids := make([]string, 0, len(members))
	for id := range members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		sort.Strings(members[id])
		g.Nodes = append(g.Nodes, Node{
			ID: id, Label: labelOf(id), Group: repoOfID(id),
			Note: id + " · " + plural(len(members[id]), "package", "packages"),
		})
	}

	// Edges: package -> package, aggregated to directory -> directory; self-edges not drawn.
	seen := map[string]bool{}
	for _, p := range pkgs {
		if !isOurs(p.ImportPath) {
			continue
		}
		from := nodeOf(p.ImportPath)
		for _, imp := range p.Imports {
			if !isOurs(imp) {
				continue
			}
			to := nodeOf(imp)
			if to == from {
				continue
			}
			key := from + "→" + to
			if seen[key] {
				continue
			}
			seen[key] = true
			style := "solid"
			if repoOfID(from) != repoOfID(to) {
				style = "cross" // 跨仓库：内核 ←→ 发行版之间那道 replace
			}
			g.Edges = append(g.Edges, Edge{From: from, To: to, Style: style})
		}
	}
	Layout(&g)
	return g, nil
}

// goList runs `go list -json` once and returns the parsed packages.
//
// -e keeps "a package does not compile" from losing the whole map: what can be obtained
// is still drawn (the broken package loses a few edges, and those are exactly the clue).
func goList(dir, pattern string) ([]listedPkg, error) {
	cmd := exec.Command("go", "list", "-e", "-json", pattern)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var pkgs []listedPkg
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var p listedPkg
		if err := dec.Decode(&p); err != nil {
			break // 半截 JSON（被 -e 放过的坏包）不值得把整张图扔掉
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

func isOurs(path string) bool {
	for _, p := range pkgPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// nodeOf folds a package into its import package: `<repo>/modules/gateway/forward` ->
// `core:modules/gateway`.
//
// Top-level packages (app, component, entry...) and the **direct subdirectories** of
// lib/tools/testing/cmd stay as they are - they are already the smallest "one thing".
func nodeOf(pkgPath string) string {
	repo, rest := splitRepo(pkgPath)
	segs := strings.Split(rest, "/")
	if len(segs) >= 2 && groupDirs[segs[0]] {
		return repo + ":" + segs[0] + "/" + segs[1]
	}
	return repo + ":" + segs[0]
}

// splitRepo splits out repo ownership and the prefix-stripped path.
func splitRepo(path string) (repo, rest string) {
	if strings.HasPrefix(path, "github.com/rzbdz/newgate-ext/") {
		return "ext", strings.TrimPrefix(path, "github.com/rzbdz/newgate-ext/")
	}
	return "core", strings.TrimPrefix(path, "github.com/rzbdz/newgate/")
}

// labelOf is the name printed on the map (repo prefix stripped: a long github.com is unreadable).
func labelOf(id string) string {
	if i := strings.Index(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}

func repoOfID(id string) string {
	if strings.HasPrefix(id, "ext:") {
		return "ext"
	}
	return "core"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoa(n) + " " + many
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
