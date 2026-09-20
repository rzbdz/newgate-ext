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

	// 分层前先收缩强连通分量：**聚合到目录层会造出环**（两个目录因为共享叶子
	// 互相可达，见 Edge.Cycle 的说明）。收缩之后剩下的一定是 DAG，环里的点落在
	// 同一条带上、带内的边标出来——review 的人该看见它们，而不是让分层算法在
	// 一个有环的图上给出一个看起来正常、其实随机的答案。
	comp := sccOf(deps)
	edges := g.Edges
	g.Edges = nil
	for _, e := range edges {
		from, okFrom := idx[e.From]
		to, okTo := idx[e.To]
		if okFrom && okTo && from != to && comp[from] == comp[to] {
			e.Cycle = true
		}
		g.Edges = append(g.Edges, e)
	}
	compDeps := condense(deps, comp)
	compUsers := invert(compDeps)
	compLayer := assignLayers(compDeps)
	if g.Compact {
		// 收紧：把点往邻居那边挪，缩短边的总跨度。**层号在这里没有物理意义**
		// （它只是个分组），所以按图论的目标函数优化它——与最长路径分层不同，
		// 这一步会牺牲「每一层都尽量靠上」去换「线更短、图更紧凑」。
		refineLayers(compDeps, compUsers, compLayer)
		// 收紧可能腾空一些层号，重新压紧，免得画出几条空带。
		compLayer = repack(compLayer)
	}
	layers := make([]int, len(deps))
	for i := range layers {
		layers[i] = compLayer[comp[i]]
	}

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
	transpose(byLayer, deps, users)

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
// 用 Kahn 的拓扑序一次算完（先算依赖、再算依赖者）。它跑在**收缩之后**的图上
// （见 Layout），所以输入一定是 DAG——组件框架拒绝成环的依赖，Go 不允许 import
// 环，而聚合出来的环已经被强连通分量收掉了。
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

// invert 把邻接表翻个方向：out[v] 是谁依赖 v。
func invert(deps [][]int) [][]int {
	out := make([][]int, len(deps))
	for v, ds := range deps {
		for _, w := range ds {
			out[w] = append(out[w], v)
		}
	}
	return out
}

// refineLayers 是 Sugiyama 分层之后的**坐标收紧**：在不违反任意一条边
// `from > to` 的前提下，把每个节点移到它的前驱/后继允许的区间里、尽量靠近
// 邻居层号的中位数。
//
// 为什么 import 图要做而装配图不做：前者的层号只是「方便看」的分组；最长路径
// 初值会把很多点拉到很高的层，图变成又长又稀。后者的深度本身就是架构叙事
// （叶子 → 机制 → 组合根），不能为了少几根长线把那个阶梯压扁。
//
// 每轮按「向下 → 向上」各扫一次：前一半把节点往依赖靠，后一半把节点往使用者
// 靠。中位数而不是均值，避免一个远邻把整个节点群拖歪。固定轮数保证稳定。
func refineLayers(deps, users [][]int, layer []int) {
	const passes = 12
	for pass := 0; pass < passes; pass++ {
		for v := range layer {
			moveLayer(v, deps, users, layer)
		}
		for v := len(layer) - 1; v >= 0; v-- {
			moveLayer(v, deps, users, layer)
		}
	}
}

func moveLayer(v int, deps, users [][]int, layer []int) {
	lo, hi := 0, maxInt
	var wanted []int
	for _, d := range deps[v] {
		// v 依赖 d：v 必须在 d 的**上一层**或更高。
		if layer[d]+1 > lo {
			lo = layer[d] + 1
		}
		wanted = append(wanted, layer[d]+1)
	}
	for _, u := range users[v] {
		// u 依赖 v：v 必须在 u 的**下一层**或更低。
		if layer[u]-1 < hi {
			hi = layer[u] - 1
		}
		wanted = append(wanted, layer[u]-1)
	}
	if lo > hi || len(wanted) == 0 {
		return
	}
	sort.Ints(wanted)
	want := wanted[len(wanted)/2]
	if want < lo {
		want = lo
	}
	if want > hi {
		want = hi
	}
	layer[v] = want
}

const maxInt = int(^uint(0) >> 1)

// repack 去掉空层号，保留顺序。收紧后图会天然出现空层（比如所有叶子都被拉近了
// 一层），继续画空带只会把图重新拉长。
func repack(layer []int) []int {
	seen := map[int]bool{}
	for _, l := range layer {
		seen[l] = true
	}
	levels := make([]int, 0, len(seen))
	for l := range seen {
		levels = append(levels, l)
	}
	sort.Ints(levels)
	pos := map[int]int{}
	for i, l := range levels {
		pos[l] = i
	}
	out := make([]int, len(layer))
	for i, l := range layer {
		out[i] = pos[l]
	}
	return out
}

// transpose 是 Sugiyama 的第二个经典步骤：中位数排序之后，逐对尝试交换层内
// 相邻节点；只有**严格减少交叉数**才留下。它比「再扫几轮」更慢一点，但图就几十
// 个点，换来的是线少绕、视觉上更能顺着读。
func transpose(byLayer [][]int, deps, users [][]int) {
	const passes = 8
	for pass := 0; pass < passes; pass++ {
		changed := false
		for l := range byLayer {
			for i := 0; i+1 < len(byLayer[l]); i++ {
				before := localCrossings(byLayer, deps, users, l, i)
				byLayer[l][i], byLayer[l][i+1] = byLayer[l][i+1], byLayer[l][i]
				after := localCrossings(byLayer, deps, users, l, i)
				if after < before {
					changed = true
					continue
				}
				byLayer[l][i], byLayer[l][i+1] = byLayer[l][i+1], byLayer[l][i]
			}
		}
		if !changed {
			return
		}
	}
}

// localCrossings 数相邻两层之间的交叉；交换某一层相邻的两点只可能影响它上下
// 两条带，所以不必每次把整张图都数一遍。
func localCrossings(byLayer [][]int, deps, users [][]int, layer, pos int) int {
	total := 0
	if layer > 0 {
		total += crossingsBetween(byLayer[layer], byLayer[layer-1], users)
	}
	if layer+1 < len(byLayer) {
		total += crossingsBetween(byLayer[layer+1], byLayer[layer], users)
	}
	_ = deps // 保留签名里的对称性：调用方拥有两份邻接表，读起来能看出它是层间判据。
	_ = pos  // 当前实现算整条相邻带，图小且结果稳定；无需做难读的局部数学。
	return total
}

// crossingsBetween 只数**确实连接这两层**的边对。边从 upper 指向 lower（deps
// 的方向），两条边端点顺序相反就是一次交叉。
func crossingsBetween(upper, lower []int, users [][]int) int {
	upperPos := map[int]int{}
	lowerPos := map[int]int{}
	for i, v := range upper {
		upperPos[v] = i
	}
	for i, v := range lower {
		lowerPos[v] = i
	}
	type edge struct{ a, b int }
	var edges []edge
	for lo, us := range users {
		b, ok := lowerPos[lo]
		if !ok {
			continue
		}
		for _, up := range us {
			if a, ok := upperPos[up]; ok {
				edges = append(edges, edge{a, b})
			}
		}
	}
	count := 0
	for i := 0; i < len(edges); i++ {
		for j := i + 1; j < len(edges); j++ {
			if (edges[i].a-edges[j].a)*(edges[i].b-edges[j].b) < 0 {
				count++
			}
		}
	}
	return count
}

// sccOf 是 Tarjan 强连通分量：返回每个点所属的分量号（0..k-1）。
//
// 迭代深度等于图的规模（几十个点），递归写法够用；换成显式栈只会让这段更难读。
func sccOf(deps [][]int) []int {
	n := len(deps)
	index := make([]int, n)
	low := make([]int, n)
	onStack := make([]bool, n)
	comp := make([]int, n)
	for i := range index {
		index[i], comp[i] = -1, -1
	}
	var stack []int
	counter, comps := 0, 0
	var strong func(v int)
	strong = func(v int) {
		index[v], low[v] = counter, counter
		counter++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range deps[v] {
			switch {
			case index[w] == -1:
				strong(w)
				if low[w] < low[v] {
					low[v] = low[w]
				}
			case onStack[w] && index[w] < low[v]:
				low[v] = index[w]
			}
		}
		if low[v] == index[v] {
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				comp[w] = comps
				if w == v {
					break
				}
			}
			comps++
		}
	}
	for v := 0; v < n; v++ {
		if index[v] == -1 {
			strong(v)
		}
	}
	return comp
}

// condense 把分量之间的边收成一张 DAG（分量内部的边不进这张表——它们就是要被
// 标出来的那些环）。
func condense(deps [][]int, comp []int) [][]int {
	n := 0
	for _, c := range comp {
		if c+1 > n {
			n = c + 1
		}
	}
	out := make([][]int, n)
	seen := map[[2]int]bool{}
	for v, ds := range deps {
		for _, w := range ds {
			a, b := comp[v], comp[w]
			if a == b || seen[[2]int{a, b}] {
				continue
			}
			seen[[2]int{a, b}] = true
			out[a] = append(out[a], b)
		}
	}
	return out
}
