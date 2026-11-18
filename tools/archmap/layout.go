package archmap

import (
	"sort"
	"unicode/utf8"
)

// Layered layout (a simplified Sugiyama): layer -> order within layer -> coordinates.
//
// Why compute it in Go rather than hand it to a front-end dagre/elk:
//
//   - the artifact must **open offline** (double-click one file), so no CDN JS lib;
//   - the maps' size (dozens of points) needs no industrial layout, and **stable
//     layout** matters more than pretty - open it twice, a module must stay put;
//   - compute then paint: the map (points, edges, layers) is **data**, so other views
//     (say mermaid) need no SVG re-parsing.
const (
	nodeH   = 32.0
	rowGap  = 72.0
	colGap  = 26.0
	marginX = 70.0
	marginY = 56.0
	charW   = 7.4 // 12px 等宽的大致字宽，用来估节点宽度
)

// Layout computes each point's layer, order within the layer and coordinates in place.
//
// Direction is fixed: **edges point upward** - the depended-on below (leaf mechanisms),
// dependents above. So layer 0 is at the bottom of the canvas, higher numbers higher up.
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

	// Condense strongly connected components before layering: **aggregating to directories
	// makes cycles** (two directories reachable through shared leaves, see Edge.Cycle).
	// What remains after condensing is a DAG; points in a cycle land on one band with
	// in-band edges marked - reviewers should see them, not get a plausible random answer.
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
		// Tighten: move points toward their neighbors, shortening total edge span. **Layer
		// numbers have no physical meaning here** (just grouping), so optimize by the graph-
		// theoretic objective - trading "each layer as high as possible" for "shorter, denser".
		refineLayers(compDeps, compUsers, compLayer)
		// Tightening may vacate layer numbers; repack so no empty bands are drawn.
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

	// Order within the layer by label first: this is **the stable starting point**, so
	// the two crossing-reduction rounds start from the same place - same input, same map.
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

	// Sizes and coordinates.
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
		// layer 0 is at the bottom: the higher the layer number, the smaller y.
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

// assignLayers is longest-path layering: layer(n) = 1 + max(layer(what it depends on)).
//
// Computed in one Kahn topological pass (dependencies first, dependents after). It runs
// on the **condensed** graph (see Layout), so the input is a DAG - the framework refuses
// cyclic dependencies, Go forbids import cycles, and aggregation cycles were condensed.
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

// usersOf is "who depends on n" (in-edges). Layering needs direction only, no second
// adjacency table: one pass over the out-edge table does it (dozens of points).
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

// reduceCrossings repeatedly reorders each layer by the median, cutting crossing lines.
//
// Only a few rounds: this is a heuristic, gains vanish after a few, and **running long
// is not drawing well**. Alternating bottom-up and top-down keeps one end from locking in.
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

// invert flips an adjacency table: out[v] is who depends on v.
func invert(deps [][]int) [][]int {
	out := make([][]int, len(deps))
	for v, ds := range deps {
		for _, w := range ds {
			out[w] = append(out[w], v)
		}
	}
	return out
}

// refineLayers is the **coordinate tightening** after Sugiyama layering: without breaking
// any edge's `from > to`, move each node into the interval its predecessors/successors
// allow, as close as possible to the neighbor layers' median.
//
// Why the import map does it and the assembly map does not: the former's layer numbers
// are only a grouping; longest-path initial values push many points very high and the map
// becomes long and sparse. The latter's depth is the architecture story (leaf -> mechanism
// -> composition root), which must not be flattened to save a few lines.
// Each round sweeps down then up. Median, not mean, so a distant neighbor cannot skew;
// fixed rounds keep it stable.
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
		// v depends on d: v must be **one layer above** d or higher.
		if layer[d]+1 > lo {
			lo = layer[d] + 1
		}
		wanted = append(wanted, layer[d]+1)
	}
	for _, u := range users[v] {
		// u depends on v: v must be **one layer below** u or lower.
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

// repack removes empty layer numbers, preserving order. After tightening the map has
// empty layers naturally (say all leaves pulled one layer closer); more empty bands stretch it.
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

// transpose is Sugiyama's second classic step: after median ordering, try swapping
// adjacent nodes within a layer; keep only if it **strictly reduces crossings**. A bit
// slower than "a few more rounds", but the maps are dozens of points; lines wiggle less.
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

// localCrossings counts crossings between adjacent layers; swapping two adjacent points
// in one layer can only affect the two bands above and below it, so no full recount.
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

// crossingsBetween counts only pairs of edges **really connecting these two layers**.
// Edges point from upper to lower (deps direction); opposite endpoint order = one crossing.
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

// sccOf is Tarjan's strongly connected components: returns each point's component id (0..k-1).
//
// Depth equals the map's size (dozens), so recursion is fine; an explicit stack only hurts.
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

// condense folds inter-component edges into a DAG (in-component edges are left out -
// they are exactly the cycles to mark).
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
