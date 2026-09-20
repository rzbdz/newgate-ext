package archmap

import (
	"sort"
	"unicode/utf8"
)

// 分层布局（Sugiyama 那一套的简化版）：分层 → 层内排序 → 落坐标。
//
// 为什么自己在 Go 里算，而不是丢给前端某个 dagre/elk：
//
//   - 产物要能**离线打开**（双击就是一个文件），所以不能依赖 CDN 上的 JS 库；
//   - 两张图的规模（几十个点）不需要工业级布局，而**布局稳定**比漂亮重要——
//     review 时点开两次，同一个模块该在同一个地方，不然没法对比着看；
//   - 算完再画，前端只管渲染：图本身（点、边、层）是**数据**，谁想拿去做别的
//     视图（比如导出 mermaid）都不用再解析一遍 SVG。
const (
	nodeH   = 32.0
	rowGap  = 72.0
	colGap  = 26.0
	marginX = 70.0
	marginY = 56.0
	charW   = 7.4 // 12px 等宽的大致字宽，用来估节点宽度
)

// Layout 就地算好每个点的层、层内次序与坐标。
//
// 方向定死：**边从下往上指**——被依赖的在下面（叶子机制），依赖者在上面。
// 所以 layer 0 画在画布最底下，层号越大越靠上。
func Layout(g *Graph) {
	if len(g.Nodes) == 0 {
		return
	}
	idx := map[string]int{}
	for i, n := range g.Nodes {
		idx[n.ID] = i
	}

	deps := make([][]int, len(g.Nodes))  // 我依赖谁（出边）
	users := make([][]int, len(g.Nodes)) // 谁依赖我（入边）
	seen := map[[2]int]bool{}
	for _, e := range g.Edges {
		from, okFrom := idx[e.From]
		to, okTo := idx[e.To]
		if !okFrom || !okTo || from == to || seen[[2]int{from, to}] {
			continue
		}
		seen[[2]int{from, to}] = true
		deps[from] = append(deps[from], to)
		users[to] = append(users[to], from)
	}

	layers := assignLayers(deps)
	maxLayer := 0
	for _, l := range layers {
		if l > maxLayer {
			maxLayer = l
		}
	}

	// 层内先按标签排一遍：这是**稳定的起点**，让交叉消减的两轮迭代从同一个
	// 地方出发——同样的输入必须得到同样的图。
	order := make([]int, len(g.Nodes))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		if layers[ia] != layers[ib] {
			return layers[ia] < layers[ib]
		}
		if g.Nodes[ia].Label != g.Nodes[ib].Label {
			return g.Nodes[ia].Label < g.Nodes[ib].Label
		}
		return g.Nodes[ia].ID < g.Nodes[ib].ID
	})

	byLayer := make([][]int, maxLayer+1)
	for _, i := range order {
		byLayer[layers[i]] = append(byLayer[layers[i]], i)
	}
	reduceCrossings(byLayer, deps, users)

	// 尺寸与坐标。
	width := 0.0
	for i := range g.Nodes {
		g.Nodes[i].H = nodeH
		g.Nodes[i].W = nodeWidth(g.Nodes[i].Label, g.Nodes[i].Missing)
	}

	layerWidth := make([]float64, len(byLayer))
	for l, ids := range byLayer {
		sum := 0.0
		for i, id := range ids {
			if i > 0 {
				sum += colGap
			}
			sum += g.Nodes[id].W
		}
		layerWidth[l] = sum
		if sum > width {
			width = sum
		}
	}
	canvasW := width + 2*marginX
	canvasH := marginY*2 + float64(maxLayer+1)*nodeH + float64(maxLayer)*rowGap

	for l, ids := range byLayer {
		x := (canvasW - layerWidth[l]) / 2
		// layer 0 在最下面：层号越大，y 越小。
		y := marginY + float64(maxLayer-l)*(nodeH+rowGap)
		for pos, i := range ids {
			g.Nodes[i].Layer = l
			g.Nodes[i].Order = pos
			g.Nodes[i].X = x
			g.Nodes[i].Y = y
			x += g.Nodes[i].W + colGap
		}
	}
	g.Width = canvasW
	g.Height = canvasH
	g.Layers = maxLayer + 1
}

// assignLayers 是最长路径分层：layer(n) = 1 + max(layer(它依赖的))。
//
// 用 Kahn 的拓扑序一次算完（先算依赖、再算依赖者）。**图必须是 DAG**——组件
// 框架本来就拒绝成环的依赖，import 图里 Go 也不允许环，所以这里不做环检测；
// 万一真出了环，剩下的点会停在 layer 0（画在最底下），一眼能看出来不对。
func assignLayers(deps [][]int) []int {
	layers := make([]int, len(deps))
	pending := make([]int, len(deps))
	for i := range deps {
		pending[i] = len(deps[i])
	}
	queue := make([]int, 0, len(deps))
	for i, n := range pending {
		if n == 0 {
			queue = append(queue, i)
		}
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, user := range usersOf(deps, n) {
			if layers[n]+1 > layers[user] {
				layers[user] = layers[n] + 1
			}
			pending[user]--
			if pending[user] == 0 {
				queue = append(queue, user)
			}
		}
	}
	return layers
}

// usersOf 是「谁依赖 n」（入边）。分层只需要方向，不用建两遍邻接表：
// 扫一遍出边表就够（点几十个，这点代价换一段更好读的代码）。
func usersOf(deps [][]int, n int) []int {
	var out []int
	for i, d := range deps {
		for _, to := range d {
			if to == n {
				out = append(out, i)
				break
			}
		}
	}
	return out
}

// reduceCrossings 反复用中位数法重排每一层，少一点交叉线。
//
// 只做几轮就停：这是个启发式，几轮之后收益就没了，而**跑得久不等于画得好**。
// 交替从下往上、从上往下扫，是为了不让某一头的局部最优锁死整张图。
func reduceCrossings(byLayer [][]int, deps, users [][]int) {
	const passes = 6
	for p := 0; p < passes; p++ {
		down := p%2 == 0
		if down {
			for l := 1; l < len(byLayer); l++ {
				sortByNeighborMedian(byLayer[l], byLayer[l-1], users)
			}
		} else {
			for l := len(byLayer) - 2; l >= 0; l-- {
				sortByNeighborMedian(byLayer[l], byLayer[l+1], deps)
			}
		}
	}
}

func sortByNeighborMedian(layer, reference []int, adj [][]int) {
	pos := map[int]int{}
	for i, id := range reference {
		pos[id] = i
	}
	median := map[int]float64{}
	for _, id := range layer {
		var found []int
		for _, nb := range adj[id] {
			if p, ok := pos[nb]; ok {
				found = append(found, p)
			}
		}
		if len(found) == 0 {
			median[id] = -1 // 没有邻居：留在原地（排序时排最前，靠稳定排序保持相对次序）
			continue
		}
		sort.Ints(found)
		median[id] = float64(found[len(found)/2])
	}
	sort.SliceStable(layer, func(a, b int) bool {
		ma, mb := median[layer[a]], median[layer[b]]
		if ma < 0 || mb < 0 {
			return ma < 0 && mb < 0
		}
		return ma < mb
	})
}

func nodeWidth(label string, missing bool) float64 {
	w := float64(utf8.RuneCountInString(label))*charW + 26
	if missing {
		w += 10
	}
	if w < 96 {
		w = 96
	}
	return w
}
