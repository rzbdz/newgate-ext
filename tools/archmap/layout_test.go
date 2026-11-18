package archmap

import (
	"sort"
	"testing"
)

// Layout is the map's only **algorithm** part, and its wrong output looks "a bit ugly" -
// nobody suspects the tool. So these assertions are not typographic fuss: they give
// "ugly" a floor - a topological violation, an empty layer, a floating coordinate is
// a red light each.
// Assert on **data**, not HTML text: HTML is rendering, data is the conclusion.

func node(id string) Node { return Node{ID: id, Label: id} }

func edge(from, to string) Edge { return Edge{From: from, To: to, Style: "solid"} }

// TestShortcutEdgeStillPointsUp: a "shortcut" edge (a->d, skipping two layers) must not
// flatten the layering - layering is **longest path**, not "a point may sit wherever".
func TestShortcutEdgeStillPointsUp(t *testing.T) {
	g := Graph{Nodes: []Node{node("a"), node("b"), node("c"), node("d")},
		Edges: []Edge{edge("a", "b"), edge("b", "c"), edge("c", "d"), edge("a", "d")}}
	Layout(&g)
	layer := layersOf(g)
	if layer["d"] != 0 || layer["a"] != 3 {
		t.Errorf("layering by longest path should stretch the chain to the full length, actual a=L%d d=L%d", layer["a"], layer["d"])
	}
	assertEveryEdgePointsUp(t, g)
}

// TestCompactKeepsTheOrdering: tightening (refineLayers) moves only **layer numbers**,
// never into an illegal relation - moving a dependency above its user reverses the arrow.
func TestCompactKeepsTheOrdering(t *testing.T) {
	// Four chains on one leaf: untightened, longest path puts all on one layer - long, sparse graph.
	g := Graph{Compact: true}
	g.Nodes = append(g.Nodes, node("leaf"))
	for _, id := range []string{"a", "b", "c", "d"} {
		g.Nodes = append(g.Nodes, node(id), node(id+"2"))
		g.Edges = append(g.Edges, edge(id, "leaf"), edge(id+"2", id))
	}
	Layout(&g)
	assertEveryEdgePointsUp(t, g)
	assertNoEmptyLayer(t, g)
}

// TestSCCStaysOnOneBand: an aggregated cycle (two directories reachable from each
// other) must land on one band with its edges marked. Left to the layering algorithm,
// a cyclic map gets a plausible but random answer - the worst thing this map can have.
func TestSCCStaysOnOneBand(t *testing.T) {
	g := Graph{Nodes: []Node{node("a"), node("b"), node("c")},
		Edges: []Edge{edge("a", "b"), edge("b", "a"), edge("a", "c")}}
	Layout(&g)
	layer := layersOf(g)
	if layer["a"] != layer["b"] {
		t.Errorf("two nodes that depend on each other should be on the same band, actual a=L%d b=L%d", layer["a"], layer["b"])
	}
	if layer["c"] >= layer["a"] {
		t.Errorf("c is depended on by a, so it should be below: a=L%d c=L%d", layer["a"], layer["c"])
	}
	marked := 0
	for _, e := range g.Edges {
		if e.Cycle {
			marked++
			if layer[e.From] != layer[e.To] {
				t.Errorf("edge %s->%s marked as a cycle crosses layers", e.From, e.To)
			}
		}
		if layer[e.From] <= layer[e.To] && !e.Cycle {
			t.Errorf("edge %s->%s does not go bottom-up", e.From, e.To)
		}
	}
	if marked != 2 {
		t.Errorf("both edges in the cycle should be marked, actually marked %d", marked)
	}
}

// TestTransposeRemovesAnObviousCrossing: the swap step after median ordering should
// untangle these two lines. Built plainly: upper points a, b; a depends on q on the
// right, b on p on the left, and label order happens to start with a on the left.
//
// Deliberately **not** the complete bipartite graph (a/b each to p/q): that crosses
// no matter what, so it would be unfair evidence of "the algorithm did nothing".
func TestTransposeRemovesAnObviousCrossing(t *testing.T) {
	g := Graph{Nodes: []Node{node("a"), node("b"), node("p"), node("q")},
		Edges: []Edge{edge("a", "q"), edge("b", "p")}}
	Layout(&g)
	if n := totalCrossings(g); n != 0 {
		t.Errorf("these two edges can be drawn without a single crossing, actual crossings %d", n)
	}
}

// TestLayoutIsStable: run the same map twice, points must land in the same place.
// Open it twice in review and they differ, nothing can be compared - the reason it exists.
func TestLayoutIsStable(t *testing.T) {
	build := func() Graph {
		g := Graph{Compact: true}
		for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
			g.Nodes = append(g.Nodes, node(id))
		}
		g.Edges = []Edge{edge("a", "b"), edge("a", "c"), edge("d", "b"), edge("d", "e"),
			edge("f", "c"), edge("f", "e"), edge("b", "c")}
		return g
	}
	first, second := build(), build()
	Layout(&first)
	Layout(&second)
	for i := range first.Nodes {
		if first.Nodes[i] != second.Nodes[i] {
			t.Fatalf("two layouts of the same input differ:\n  %+v\n  %+v", first.Nodes[i], second.Nodes[i])
		}
	}
}

// ---------- assertion helpers ----------

func layersOf(g Graph) map[string]int {
	out := map[string]int{}
	for _, n := range g.Nodes {
		out[n.ID] = n.Layer
	}
	return out
}

func assertEveryEdgePointsUp(t *testing.T, g Graph) {
	t.Helper()
	layer := layersOf(g)
	for _, e := range g.Edges {
		if e.Cycle {
			continue
		}
		if layer[e.From] <= layer[e.To] {
			t.Errorf("edge %s(L%d) -> %s(L%d) violates \"dependencies are below\"",
				e.From, layer[e.From], e.To, layer[e.To])
		}
	}
}

// assertNoEmptyLayer: an empty layer is what repack should have removed after tightening.
// Keeping it only makes the map longer, and a long map loses the layer's grouping meaning.
func assertNoEmptyLayer(t *testing.T, g Graph) {
	t.Helper()
	seen := map[int]bool{}
	for _, n := range g.Nodes {
		seen[n.Layer] = true
	}
	for l := 0; l < g.Layers; l++ {
		if !seen[l] {
			t.Errorf("layer %d is empty (out of %d layers only %d are used)", l, g.Layers, len(seen))
		}
	}
}

// totalCrossings counts crossings between all adjacent layers using the **final order**.
func totalCrossings(g Graph) int {
	max := 0
	for _, n := range g.Nodes {
		if n.Layer > max {
			max = n.Layer
		}
	}
	byLayer := make([][]int, max+1)
	index := map[string]int{}
	for i, n := range g.Nodes {
		index[n.ID] = i
	}
	order := make([]int, len(g.Nodes))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return g.Nodes[order[a]].Order < g.Nodes[order[b]].Order
	})
	for _, i := range order {
		byLayer[g.Nodes[i].Layer] = append(byLayer[g.Nodes[i].Layer], i)
	}
	deps := make([][]int, len(g.Nodes))
	users := make([][]int, len(g.Nodes))
	for _, e := range g.Edges {
		from, okFrom := index[e.From]
		to, okTo := index[e.To]
		if !okFrom || !okTo || from == to {
			continue
		}
		deps[from] = append(deps[from], to)
		users[to] = append(users[to], from)
	}
	total := 0
	for l := 1; l < len(byLayer); l++ {
		total += crossingsBetween(byLayer[l], byLayer[l-1], users)
	}
	return total
}
