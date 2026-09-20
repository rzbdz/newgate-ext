package archmap

import (
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
)

// pkgPrefixes 是「我们自己写的」包的两条前缀。
//
// 判据是**仓库归属**，不是路径好看：内核（github.com/rzbdz/newgate/…）与发行版
// （github.com/rzbdz/newgate-ext/…）。两张图看的就是这两个仓库之间的边——标准库
// 与第三方一律不进图，否则一张图几百个点，什么都看不出来。
var pkgPrefixes = []string{
	"github.com/rzbdz/newgate-ext/",
	"github.com/rzbdz/newgate/",
}

type listedPkg struct {
	ImportPath string
	Name       string
	Imports    []string
	Module     *struct {
		Path string
		Main bool
	}
	DepOnly bool `json:",omitempty"`
}

// ImportGraph 扫一遍两个仓库里所有包的 import，画成图。
//
// 为什么用 `go list` 而不是自己解析 import 语句：解析源码要自己处理构建约束、
// 生成文件、`_test.go`、以及「这个包到底属不属于这个 module」；而 `go list` 给出
// 的就是**编译器眼里的那份事实**，还带模块归属。代价是要能跑 go 命令（开发机
// 上有，CI 上也有——这条通路本来就只在开发时用）。
//
// 两个 module 分别列：`./...` 只列当前 module 的包，内核是另一个 module（虽然
// 被 replace 到了 ./core），得按它的 import 前缀点名列。
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

	ours := map[string]bool{}
	for _, p := range pkgs {
		if isOurs(p.ImportPath) && !p.DepOnly {
			ours[p.ImportPath] = true
		}
	}

	g := Graph{
		ID:    "imports",
		Title: "imports (scanned)",
		Note: "who imports whom - what the compiler sees. Grey edges stay inside one repo; " +
			"the pink ones cross the replace boundary between the kernel and the distribution.",
	}
	names := make([]string, 0, len(ours))
	for p := range ours {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		g.Nodes = append(g.Nodes, Node{
			ID: p, Label: shortPath(p), Group: repoOf(p), Note: p,
		})
	}
	seen := map[string]bool{}
	for _, p := range pkgs {
		if !ours[p.ImportPath] {
			continue
		}
		for _, imp := range p.Imports {
			if !ours[imp] || imp == p.ImportPath {
				continue
			}
			key := p.ImportPath + "→" + imp
			if seen[key] {
				continue
			}
			seen[key] = true
			style := "solid"
			if repoOf(p.ImportPath) != repoOf(imp) {
				style = "cross" // 跨仓库：内核 ←→ 发行版之间那道 replace
			}
			g.Edges = append(g.Edges, Edge{From: p.ImportPath, To: imp, Style: style})
		}
	}
	Layout(&g)
	return g, nil
}

// goList 跑一次 `go list -json`，返回解析出来的包。
//
// -e 让「有包编不过」不至于把整张图弄没：拿得到的照样画（坏掉的那个包会在
// 图上少几条边，而少的那几条正好是线索）。
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

// repoOf 归到仓库：图例按它上色。
func repoOf(path string) string {
	if strings.HasPrefix(path, "github.com/rzbdz/newgate-ext/") {
		return "ext"
	}
	return "core"
}

// shortPath 去掉仓库前缀：图上要能一眼看出 `modules/gateway/forward`，而不是
// 一长串 github.com。
func shortPath(path string) string {
	for _, p := range pkgPrefixes {
		if strings.HasPrefix(path, p) {
			return strings.TrimPrefix(path, p)
		}
	}
	return path
}
