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

// TestReportUndeclaredImports 找的是**没人声明的依赖**：一个模块 import 了另一个
// 模块的**根包**，装配图上却没有对应的边。
//
// 判据是 CLAUDE.md 里那句：「两张图对不上就是线索：import 了却没有任何依赖边，
// 说明有一条没人声明的依赖（编译过、测试过、review 看不出来，改动时才发现拆不
// 动）」。import 是**编译器事实**，Requires/Optional 是**声明**——声明少写一条，
// 编译照样过，测试照样绿。
//
// # 为什么不用 archmap 那张 import 图（第一版就是那么写的）
//
// 那张图的点是**模块粒度**的：`nodeOf` 把 `modules/cli/extension` 与 `modules/cli`
// 折成同一个点（见 imports.go 的 groupDirs）。这个粒度对「画给人看」是对的，对
// 这个探针是致命的——它分不出这两件事：
//
//	import "…/modules/gateway"          ← 绑到了它的**契约**，这是一条真依赖
//	import "…/modules/config/domain"    ← 绑到了**共享叶子**，按规矩不算依赖边
//
// 于是 `breaker → config` 这种（其实 import 的是 `config/paths`）会和真依赖长得
// 一模一样。第一版 55 对里绝大多数是这种噪音，而「全是噪音」的报告没人会看第二次。
//
// 所以这里换一把尺子：直接问 `go list`「这个包 import 的字符串里，有没有哪一条
// **正好是**某个模块的根包」。根包 = `…/modules/<名字>`，后面不再有斜杠。
//
// # 为什么今天是棘轮（期望 0 条）
//
// 2026-09-21 第一次跑出来是 **1 条真问题**：gateway 的 Start 里
// `MustGet(confighook.ConfigHooksCapability)`，而 gateway → confighook 一条边都
// 没声明（同一批还查出 runtime 的同一处，那条靠 app/declared_test.go 的
// capability 粒度棘轮守着——它跨模块的边是有的，只是拿的不是声明过的那个）。
//
// 修完之后这份名单是空的，那就别再让它只是个「报告」：**空集是可以断言的**，
// 而断言了的空集才拦得住下一条。真的出现一条正当的编译期边时，走
// allowedRootImports 显式加一行并写清理由——那条路要动代码，比「看一眼报告然后
// 忘掉」贵，正是我们要的价。
func TestNoUndeclaredRootImports(t *testing.T) {
	root := repoRoot(t)
	report, err := archmap.Build(manifest.Specs(), root)
	if err != nil {
		t.Fatalf("生成架构图失败: %v", err)
	}

	// 声明边：装配图上「谁依赖谁」。多份规格书取并集——某一份关掉了某个模块，
	// 不代表那个模块与别人之间没有声明。
	declared := map[[2]string]bool{}
	for _, spec := range report.Specs {
		for _, e := range spec.Edges {
			declared[[2]string{norm(e.From), norm(e.To)}] = true
		}
	}
	if len(declared) == 0 {
		t.Fatal("装配图一条边都没有——声明那份数据没读到，下面的报告是空转的")
	}

	// 组件名那一套（抹掉 - 和 _），用来判断「这个模块到底是不是组件」。
	componentNames := map[string]bool{}
	for _, spec := range report.Specs {
		for _, n := range spec.Nodes {
			if !strings.HasPrefix(n.ID, "?") {
				componentNames[norm(n.ID)] = true
			}
		}
	}
	if len(componentNames) == 0 {
		t.Fatal("装配图一个组件都没有——声明那份数据没读到，下面的报告是空转的")
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
				// 两端都必须是**组件**：还没做成组件的模块（见下面那段）没有
				// Requires 可比，把它们算进来只会得到一条没法处理的报告。
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

	// 还没做成组件的：`modules/<x>` 底下的包是真的，但**没有任何一份规格书装它**
	// （今天的例子是 configshare：`module.go` 还只有一段包注释，没有 `New()`）。
	// 它没有 Requires 可比，所以跳过——但要说出来，不然「探针漏了一个模块」和
	// 「那个模块本来就不在盘上」长得一模一样。
	var notComponents []string
	for m := range seenModules {
		if !componentNames[m] {
			notComponents = append(notComponents, m)
		}
	}
	sort.Strings(notComponents)
	if len(notComponents) > 0 {
		t.Logf("（跳过 %d 个还不是组件的模块：%v）", len(notComponents), notComponents)
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
		t.Errorf("%s 的代码 import 了模块 %s 的**根包**，但装配图上没有这条依赖边。\n"+
			"  编译过、测试过、review 看不出来，改动时才发现拆不动——要么在 Requires "+
			"里声明它，要么把它降级成共享叶子（import 子包）。\n"+
			"  确实是有意为之的话，把 (%q, %q) 加进 allowedRootImports 并写清理由。",
			p[0], p[1], p[0], p[1])
	}
}

// allowedRootImports 是**豁免名单**，今天是空的。
//
// 加一条就要写一句「为什么这条不需要声明」。空着不是摆设：它把「加一条豁免」
// 这件事变成一次要动代码、要写理由的编辑，而不是看一眼报告然后忘掉。
var allowedRootImports = map[[2]string]bool{}

type listed struct {
	path    string
	imports []string
}

// listPackages 跑一次 `go list`，拿到这个 module 里每个包 import 了什么。
//
// 用 `-e` 让编不过的包不至于把整份报告弄没（同 imports.go 的取舍）；只看**非测试**
// 源码的 import（`go list` 的 `.Imports` 本来就不含 `_test.go`）——测试怎么拉线不
// 是产品依赖。
func listPackages(t *testing.T, dir string) []listed {
	t.Helper()
	cmd := exec.Command("go", "list", "-e", "-f",
		`{{.ImportPath}}{{"\t"}}{{join .Imports " "}}`, "./...")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list 在 %s 里失败: %v", dir, err)
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

// 两个仓库的模块前缀。跨仓库那条边也在这里比对：replace 只是让它编得过，
// 「谁依赖谁」照样该声明。
var modulePrefixes = []string{
	"github.com/rzbdz/newgate/modules/",
	"github.com/rzbdz/newgate-ext/modules/",
}

// moduleOf 说这个包属于哪个模块（用**目录名**那套写法），不是模块里的包就返回 false。
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

// rootModuleOf 说这条 import 是不是**某个模块的根包**，是的话是哪个模块。
//
// 「根包」是判据的核心：`…/modules/gateway` 后面不再有斜杠。多一段就是叶子
// （`…/modules/config/domain`），而叶子谁都能直接 import、不算依赖边
// （CLAUDE.md 的「共享叶子」那条）。
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

// norm 抹掉 `-` 与 `_`：目录名让 Go 舒服、组件名让人舒服，两套写法靠这个对上
// （`modules/confighook` 的组件叫 `config-hook`，`modules/opencodeomo` 的叫
// `opencode-omo`）。
func norm(s string) string {
	return strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(s))
}
