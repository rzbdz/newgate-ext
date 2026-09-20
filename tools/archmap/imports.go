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
// （github.com/rzbdz/newgate-ext/…）。标准库与第三方一律不进图，否则一张图几百
// 个点，什么都看不出来。
var pkgPrefixes = []string{
	"github.com/rzbdz/newgate-ext/",
	"github.com/rzbdz/newgate/",
}

// groupDirs 是**聚合到哪一层**的判据。
//
// 图的元素是 **import 包（目录）**，不是每个 Go 文件包：`modules/gateway/forward`
// 与 `modules/gateway/rewrite` 之间的边画出来没有信息量——它们本来就属于同一个
// 模块，那个模块才是「一个东西」。所以同一目录下的包合成一个圆角矩形，边只在
// 目录之间画。
//
// 这一层也是 review 时唯一问得出口的问题所在的层：「gateway 依赖谁」「谁依赖
// config」，而不是「forward 的 rewrite 用不用 i18n」。
var groupDirs = map[string]bool{
	"modules": true, "lib": true, "tools": true, "testing": true, "cmd": true,
}

type listedPkg struct {
	ImportPath string
	Name       string
	Imports    []string
}

// ImportGraph 扫一遍两个仓库里所有包的 import，聚合到目录层画成图。
//
// 为什么用 `go list` 而不是自己解析 import 语句：解析源码要自己处理构建约束、
// 生成文件、`_test.go`、以及「这个包到底属不属于这个 module」；而 `go list` 给出
// 的就是**编译器眼里的那份事实**。代价是要能跑 go 命令（开发机上有，CI 上也有
// ——这条通路本来就只在开发时用）。
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

	// 先把「哪些包属于我们」定下来，再把它们归到目录。
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

	// 边：包 → 包，聚合成 目录 → 目录；自己到自己不画。
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

// nodeOf 把一个包归到它的 import 包：`<repo>/modules/gateway/forward` →
// `core:modules/gateway`。
//
// 顶层的包（app、component、entry…）与 lib/tools/testing/cmd 下的**直接子目录**
// 保持原样——它们本来就是最小的「一个东西」。
func nodeOf(pkgPath string) string {
	repo, rest := splitRepo(pkgPath)
	segs := strings.Split(rest, "/")
	if len(segs) >= 2 && groupDirs[segs[0]] {
		return repo + ":" + segs[0] + "/" + segs[1]
	}
	return repo + ":" + segs[0]
}

// splitRepo 拆出仓库归属与去掉前缀的路径。
func splitRepo(path string) (repo, rest string) {
	if strings.HasPrefix(path, "github.com/rzbdz/newgate-ext/") {
		return "ext", strings.TrimPrefix(path, "github.com/rzbdz/newgate-ext/")
	}
	return "core", strings.TrimPrefix(path, "github.com/rzbdz/newgate/")
}

// labelOf 是图上印的名字（去掉仓库前缀：一长串 github.com 没人看）。
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
