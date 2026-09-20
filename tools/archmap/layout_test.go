package archmap

import (
	"sort"
	"testing"
)

// 布局是这张图唯一的**算法**部分，而它算错的样子是「有点难看」——没人会怀疑工具。
// 所以这几条断言不是排版洁癖，是让「难看」有个下限：违反拓扑约束的边、空掉的层、
// 飘忽不定的坐标，各自是一条红灯。
//
// 都在**数据**上断言，不在 HTML 文本上断言：HTML 是渲染，数据才是结论。

func node(id string) Node { return Node{ID: id, Label: id} }

func edge(from, to string) Edge { return Edge{From: from, To: to, Style: "solid"} }

// TestShortcutEdgeStillPointsUp：一条「抄近路」的边（a→d，跳过中间两层）不许把
// 分层压平——分层是**最长路径**，不是一个点爱在哪就在哪。
func TestShortcutEdgeStillPointsUp(t *testing.T) {
	g := Graph{Nodes: []Node{node("a"), node("b"), node("c"), node("d")},
		Edges: []Edge{edge("a", "b"), edge("b", "c"), edge("c", "d"), edge("a", "d")}}
	Layout(&g)
	layer := layersOf(g)
	if layer["d"] != 0 || layer["a"] != 3 {
		t.Errorf("最长路径分层该把链拉满，实际 a=L%d d=L%d", layer["a"], layer["d"])
	}
	assertEveryEdgePointsUp(t, g)
}

// TestCompactKeepsTheOrdering：收紧（refineLayers）只挪**层号**，不许挪出非法
// 关系——把依赖挪到使用者上面，画出来的箭头就反了，而图还是「挺好看」的。
func TestCompactKeepsTheOrdering(t *testing.T) {
	// 四条链挂在一个叶子上：不收紧的话最长路径会把它们全顶到同一层，图又长又稀。
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

// TestSCCStaysOnOneBand：聚合出来的环（两个目录互相可达）必须落在同一条带上、
// 边标出来。让分层算法在有环的图上自己决定——它会给出一个看起来正常、其实随机
// 的答案，那正是这张图最不能有的东西。
func TestSCCStaysOnOneBand(t *testing.T) {
	g := Graph{Nodes: []Node{node("a"), node("b"), node("c")},
		Edges: []Edge{edge("a", "b"), edge("b", "a"), edge("a", "c")}}
	Layout(&g)
	layer := layersOf(g)
	if layer["a"] != layer["b"] {
		t.Errorf("互为依赖的两个点该在同一条带上，实际 a=L%d b=L%d", layer["a"], layer["b"])
	}
	if layer["c"] >= layer["a"] {
		t.Errorf("c 被 a 依赖，该在下面：a=L%d c=L%d", layer["a"], layer["c"])
	}
	marked := 0
	for _, e := range g.Edges {
		if e.Cycle {
			marked++
			if layer[e.From] != layer[e.To] {
				t.Errorf("标了环的边 %s→%s 跨层了", e.From, e.To)
			}
		}
		if layer[e.From] <= layer[e.To] && !e.Cycle {
			t.Errorf("边 %s→%s 没有从下往上", e.From, e.To)
		}
	}
	if marked != 2 {
		t.Errorf("环里的两条边都该标出来，实际标了 %d 条", marked)
	}
}

// TestTransposeRemovesAnObviousCrossing：中位数排序之后那一步交换该把这两条线
// 解开。构造得很直白：a、b 两个上层点，a 依赖右边的 q、b 依赖左边的 p——而按
// 标签排序的起点恰好是 a 在左。交换一次就干净了。
//
// 刻意**不用**四条边的完全二分图（a/b 各连 p/q）：那个无论怎么排都必然交叉一次，
// 拿它当「算法没干活」的证据是冤枉。
func TestTransposeRemovesAnObviousCrossing(t *testing.T) {
	g := Graph{Nodes: []Node{node("a"), node("b"), node("p"), node("q")},
		Edges: []Edge{edge("a", "q"), edge("b", "p")}}
	Layout(&g)
	if n := totalCrossings(g); n != 0 {
		t.Errorf("这两条边可以一条不交叉地画出来，实际交叉 %d 次", n)
	}
}

// TestLayoutIsStable：同一份图跑两次，点必须落在同一个地方。review 时点开两次
// 对不上，就没法对比着看——而「对比着看」正是这个产物存在的理由。
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
			t.Fatalf("同输入两次布局不一致:\n  %+v\n  %+v", first.Nodes[i], second.Nodes[i])
		}
	}
}

// ---------- 断言用的小工具 ----------

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
			t.Errorf("边 %s(L%d) → %s(L%d) 违反了「依赖在下面」",
				e.From, layer[e.From], e.To, layer[e.To])
		}
	}
}

// assertNoEmptyLayer：空层是收紧之后该被 repack 掉的东西。留着它只会让图重新
// 变长，而图一长，层号那个「分组」的意思就没人看得出来了。
func assertNoEmptyLayer(t *testing.T, g Graph) {
	t.Helper()
	seen := map[int]bool{}
	for _, n := range g.Nodes {
		seen[n.Layer] = true
	}
	for l := 0; l < g.Layers; l++ {
		if !seen[l] {
			t.Errorf("第 %d 层是空的（%d 层里只用上了 %d 个）", l, g.Layers, len(seen))
		}
	}
}

// totalCrossings 用**画完之后的次序**数一遍全部相邻层之间的交叉。
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
