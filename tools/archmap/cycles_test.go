package archmap_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/manifest"
	"github.com/rzbdz/newgate-ext/tools/archmap"
)

// 环这一轴：**聚合出来的环里，哪些是真的「两个模块互相绑到对方的根包」**。
//
// CLAUDE.md 说聚合会造出环，那是共享叶子（谁都能直接 import 的契约包）造成的、
// 不是设计毛病，而且「review 的人该看见它们」。但那是**一句断言**，得验。
//
// 包级的 import 环 Go 根本不让编译，所以真环不存在——要查的是另一种：两个模块
// **都** import 了对方的**根包**（也就是双方都绑到了对方的契约上）。那种环在聚合
// 图上和叶子造成的假象长得一模一样，但它的含义完全不同：拆任何一边都会真的疼。
//
// 判据于是很干净：对每一对聚合环里的模块 (A, B)，分别问「A 里有包 import 了 B 的
// **根包**吗」「B 里有包 import 了 A 的根包吗」。两个都是 → 真环，报出来。
// 它断言的是**零个真环**：这个集合今天（2026-09-21）是空的。
//
// # 这条断言有多硬（说清楚，别让它冒充比实际更强的保证）
//
// 比它看起来的要软。两个模块**互相**绑到对方根包，在能编译的 Go 程序里基本不可能
// 出现——那往往就意味着包级的 import 环，而 Go 直接拒绝编译。所以这条实际抓不到
// 什么，它**真正的产出是那份分类**：13 对聚合环里每一对，是「共享叶子造成的假象」
// 还是「一边真的绑到了对方的契约上」。review 的人要的是那个答案，不是那个布尔。
//
// 留着它是因为：它是唯一一处把「环是假象」这句话从**断言**变成**查过**的地方。
// CLAUDE.md 写着「聚合会造出环…那是叶子造成的」，而在此之前没人验过。
func TestReportCycles(t *testing.T) {
	root := repoRoot(t)
	report, err := archmap.Build(manifest.Specs(), root)
	if err != nil {
		t.Fatal(err)
	}

	// 每个包 import 了哪些模块的**根包**（与 undeclared 那条同一把尺子）。
	rootImports := map[string]map[string]bool{} // 模块 → 被它 import 根包的模块集合
	for _, dir := range []string{root, root + "/core"} {
		for _, p := range listPackages(t, dir) {
			from, ok := moduleOf(p.path)
			if !ok {
				continue
			}
			for _, imp := range p.imports {
				if to, ok := rootModuleOf(imp); ok && to != from {
					if rootImports[from] == nil {
						rootImports[from] = map[string]bool{}
					}
					rootImports[from][to] = true
				}
			}
		}
	}

	// 聚合环：import 图里被标了 Cycle 的边。
	pairs := map[[2]string]bool{}
	for _, e := range report.Imports.Edges {
		if !e.Cycle {
			continue
		}
		a, b := norm(labelKey(e.From)), norm(labelKey(e.To))
		if a > b {
			a, b = b, a
		}
		pairs[[2]string{a, b}] = true
	}
	keys := make([][2]string, 0, len(pairs))
	for p := range pairs {
		keys = append(keys, p)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})

	var real [][2]string
	t.Logf("聚合出来的环：%d 对", len(keys))
	for _, p := range keys {
		ab := rootImports[p[0]][p[1]]
		ba := rootImports[p[1]][p[0]]
		verdict := "假象（至少一边只是叶子）"
		if ab && ba {
			verdict = "**双向绑到对方的根包**"
			real = append(real, p)
		}
		t.Logf("  %s ↔ %s   %s（A→B根包=%v B→A根包=%v）", p[0], p[1], verdict, ab, ba)
	}
	if len(real) > 0 {
		t.Errorf("这些模块**互相**绑到了对方的根包上：%v\n"+
			"聚合出来的环不都是叶子造成的假象——这几对是真的双向契约绑定。", real)
	}
}

// labelKey 把 import 图的点 id（带仓库前缀）还原成模块名那套写法。
func labelKey(id string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(id, "core:"), "ext:")
	return strings.TrimPrefix(s, "modules/")
}
