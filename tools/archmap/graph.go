// Package archmap 把这张架构的**两份来源**画成一张可以翻的 HTML。
//
// # 两份来源为什么要放在一起看
//
//   - **装配图（resolve）**：真跑一遍组件框架的解析，得到「谁需要谁、谁提供了
//     什么」。它是**声明**：写在 Requires/Provides 里的东西。
//   - **import 图（scan）**：扫一遍所有包的 import。它是**事实**：编译器眼里
//     的东西。
//
// 两张图对不上就是问题所在——声明了 Need 却没 import（那多半是走 capability
// 拿的，正常），或者 import 了却没有任何依赖边（**那是一条没人声明的依赖**，
// 藏得最深的那种：编译过、测试过、review 时看不出来，改动时才发现拆不动）。
// 分开画、并排看，这件事才有个抓手。
//
// # 方向
//
// 两张图都按「**依赖在下，依赖者在上面**」摆：最底下是叶子机制（没人依赖它、
// 它也不依赖别人），往上是被它们撑起来的东西，最上面是组合根。边从下往上指。
package archmap

import (
	"sort"

	modules "github.com/rzbdz/newgate/component"
)

// Node 是图上的一个点。
type Node struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Group 决定配色与图例（模块类型 / 仓库归属）。
	Group string `json:"group"`
	// Note 是悬停时那行小字（谁提供的、装在哪）。
	Note string `json:"note"`
	// Missing 标记「被需要、但这个装配里没有」——Optional 依赖的常见下场，
	// 画出来比省掉有用：它正是「关掉一个模块会怎样」的答案。
	Missing bool `json:"missing,omitempty"`

	Layer int     `json:"layer"`
	Order int     `json:"order"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	W     float64 `json:"w"`
	H     float64 `json:"h"`
}

// Edge 从 From（依赖者、画在上面）指向 To（被依赖者、画在下面）。
type Edge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Style string `json:"style"` // solid = Need，dashed = Optional
	Label string `json:"label"` // 走的是哪个 capability
}

// Graph 是一张画好的图（坐标已经算完，前端只画）。
type Graph struct {
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	Note   string  `json:"note"`
	Nodes  []Node  `json:"nodes"`
	Edges  []Edge  `json:"edges"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Layers int     `json:"layers"`
}

// Report 是整个产物：几张装配图（每份规格书一张）+ 一张 import 图。
type Report struct {
	GeneratedAt string   `json:"generatedAt"`
	Specs       []Graph  `json:"specs"`
	Imports     Graph    `json:"imports"`
	Notes       []string `json:"notes"`
}

// ---------- 装配图 ----------

// ResolveGraph 真跑一遍组件解析，把结果画成图。
//
// 走的是框架的 `component.Resolve`（**图纸**，不是 `New`）：校验与拓扑排序一步
// 不少，但一个 Start 都不跑——不起端口、不写盘、不改全局注册表。所以同一个进程
// 里可以连问几张图（每份规格书一张），而这是**框架给的能力**，不是这里自己解析
// 一遍凑出来的。
func ResolveGraph(id, title, note string, loaders ...modules.Loader) (Graph, error) {
	plan, err := modules.Resolve(loaders...)
	if err != nil {
		return Graph{}, err
	}
	comps := plan.Components()
	deps := plan.Dependencies()

	g := Graph{ID: id, Title: title, Note: note}
	names := make([]string, 0, len(comps))
	for _, c := range comps {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	for _, name := range names {
		g.Nodes = append(g.Nodes, Node{
			ID: name, Label: name, Group: groupOf(comps, name), Note: noteOf(comps, name),
		})
	}

	seen := map[string]bool{}
	for _, c := range comps {
		for _, d := range deps[c.Name] {
			to := d.ProvidedBy
			if to == "" {
				// 没人提供：Optional 的常见下场（那一版的发行版关掉了它）。
				// 画一个虚点，让「这一版少装了什么」看得见——review 装配时
				// 这条边和实边一样有用。
				to = "?" + d.Capability
				if !seen[to] {
					seen[to] = true
					g.Nodes = append(g.Nodes, Node{
						ID: to, Label: d.Capability, Group: "missing", Missing: true,
						Note: "needed by " + c.Name + ", not installed in this distribution",
					})
				}
			}
			style := "solid"
			if d.Optional {
				style = "dashed"
			}
			g.Edges = append(g.Edges, Edge{From: c.Name, To: to, Style: style, Label: d.Capability})
		}
	}
	Layout(&g)
	return g, nil
}

func groupOf(comps []modules.Component, name string) string {
	for _, c := range comps {
		if c.Name == name {
			if c.Type == "" {
				return "module"
			}
			return string(c.Type)
		}
	}
	return "module"
}

func noteOf(comps []modules.Component, name string) string {
	for _, c := range comps {
		if c.Name != name {
			continue
		}
		var provided []string
		for _, p := range c.Provides {
			provided = append(provided, p.Name())
		}
		sort.Strings(provided)
		note := "type: " + string(c.Type)
		if len(provided) > 0 {
			note += " · provides: " + join(provided, ", ")
		}
		return note
	}
	return ""
}

func join(items []string, sep string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
