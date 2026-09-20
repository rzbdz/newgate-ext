package archmap_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/manifest"
	"github.com/rzbdz/newgate-ext/tools/archmap"
)

// 这个测试干两件事：**生成架构图**（dist/architecture.html，离线双击就能看），
// 以及**顺手把图里能机械核对的方向断言掉**。
//
// 为什么断言和产物写在一起：图是给人看的，人会看漏；而这两张图恰好承载了
// 这个仓库最要紧的几条方向（内核不认识发行版、数据面不认识策略、装配没有环）。
// 让生成产物这件事**必须**先通过那些断言，产物才有资格被拿去 review——
// 否则一张画错的图比没有图更糟，它会让人以为架构是那样。
//
// 断言都在图的数据上做，不在 HTML 文本上做：HTML 是渲染，数据才是结论。
func TestArchitectureMap(t *testing.T) {
	root := repoRoot(t)

	// 报告怎么装只有一份实现（archmap.Build）：测试与 modules/arch-diagram 那个
	// 「挂到端口上」的调用方走的是同一条路。分开写过一次，两边就会在「哪一步失败
	// 怎么降级」上长歪。
	report, err := archmap.Build(manifest.Specs(), root)
	if err != nil {
		t.Fatalf("装不出报告（%s 解析失败？）: %v", "resolve", err)
	}

	// ---- 一、装配图：每份规格书一张 ----
	if len(report.Specs) == 0 {
		t.Fatal("一份规格书都没有——生成器与规格书脱节了")
	}
	for _, g := range report.Specs {
		assertLayeringDirection(t, g.ID, g)
		assertLayered(t, g.ID, g)
	}

	// ---- 二、import 图：扫出来的事实 ----
	imports := report.Imports
	if len(imports.Nodes) == 0 {
		t.Fatalf("import 图是空的：%s", imports.Note)
	}
	assertLayeringDirection(t, "imports", imports)
	assertLayered(t, "imports", imports)
	assertKernelNeverImportsTheDistribution(t, imports)

	// ---- 三、产物 ----
	html, err := archmap.Render(report)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	out := filepath.Join(root, "dist", "architecture.html")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("架构图: %s（%d 份规格书 + import 图 %d 点/%d 边）",
		out, len(report.Specs), len(imports.Nodes), len(imports.Edges))
}

// assertLayeringDirection 锁两件事，缺一不可：
//
//   - **不在同一个强连通分量里的边，必须从下层指向上层**（依赖在下面）；
//   - **同一个分量里的边必须落在同一层**（它们就是聚合造出来的那些环，画在同一
//     条带上、标红）。
//
// 组件框架拒绝成环的依赖、Go 不允许 import 环，所以这两条本来该是白送的。留着
// 它的理由不是防那两件事，是防**我这个工具**：分层算错（比如边的方向建反、
// 或者强连通分量算错）会造出一模一样的现象——点全挤在同一层，画出来只是「有点
// 难看」，没人会想到是工具错了。
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
			t.Errorf("%s: 边 %s→%s 指向了图上没有的点", name, e.From, e.To)
			continue
		}
		if e.Cycle {
			if from != to {
				t.Errorf("%s: %s(L%d) → %s(L%d) 标了 Cycle 却跨层——"+
					"环里的点该落在同一条带上", name, e.From, from, e.To, to)
			}
			continue
		}
		if from <= to {
			t.Errorf("%s: %s(L%d) → %s(L%d) 违反了「依赖在下面」——"+
				"要么强连通分量算错了，要么分层算错了", name, e.From, from, e.To, to)
		}
	}
}

// assertLayered 要求分层**用满**了：如果一个点都没落在 L0，说明第一层算完就没
// 往上走（典型是方向反了）。
func assertLayered(t *testing.T, name string, g archmap.Graph) {
	t.Helper()
	if len(g.Nodes) == 0 {
		t.Errorf("%s: 图是空的", name)
		return
	}
	perLayer := map[int]int{}
	for _, n := range g.Nodes {
		perLayer[n.Layer]++
	}
	if perLayer[0] == 0 {
		t.Errorf("%s: 没有一个点在第 0 层——分层没走通", name)
	}
	if g.Layers < 2 && len(g.Edges) > 0 {
		t.Errorf("%s: 有 %d 条边却只有 %d 层", name, len(g.Edges), g.Layers)
	}
}

// assertKernelNeverImportsTheDistribution 是这张图上最值钱的一条：**内核不认识
// 发行版**。
//
// 它是 app/independence_test.go 那条规矩的另一半——那边查的是「有没有回来一个
// Pin 文件 / modules-ext 目录」（历史形态），这边查的是**编译期事实**：内核的包
// 里有没有人 import 发行版的包。破了这条，内核就没法单独发版、单独测试，
// 发行版也就没法 fork。
func assertKernelNeverImportsTheDistribution(t *testing.T, g archmap.Graph) {
	t.Helper()
	checked := 0
	for _, e := range g.Edges {
		// 节点 id 是 `<repo>:<import 包>`（见 imports.go 的 nodeOf）。
		if !strings.HasPrefix(e.From, "core:") {
			continue
		}
		checked++
		if strings.HasPrefix(e.To, "ext:") {
			t.Errorf("内核 import 了发行版：%s → %s\n"+
				"    内核必须是能单独发版的那个（replace 是单向的）", e.From, e.To)
		}
	}
	if checked == 0 {
		t.Fatal("一条内核内部的 import 边都没扫到——扫描坏了，这条断言等于没跑")
	}
}

// repoRoot 从本文件的位置往上找仓库根（有 go.mod 且不是 core/ 的那一层）。
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
			t.Fatal("找不到仓库根（该有一份 dist.json）")
		}
		dir = parent
	}
}
