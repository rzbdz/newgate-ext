package breaker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/lib/view"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// TestHealthTableCellsMatchColumns 是这张表的形状棘轮。
//
// 格子是按**列 ID** 索引的（见 view.Table 的注释：行是 map，不是数组），于是
// 键名写错不会报错——前端按列去取，取不到就是空白。一张少了半列的表看起来
// 完全正常，只有对着终端里的 `newgate breaker` 逐格比才会发现。
//
// 同一个理由适用 tone：它不是 view.Tone* 里的词时，前端一律当没给（不着色），
// 于是「这条摘了」在网页上是一片灰。两种错都静默，所以在这里拦。
func TestHealthTableCellsMatchColumns(t *testing.T) {
	b, _ := clocked()
	trip(t, b, "relay", "slow")                            // 摘牌：State=open
	b.Report("relay", "fresh", Input{Kind: KindConnError}) // 只计数：Fails=1

	concepts, err := healthConcepts(b, nil)
	if err != nil {
		t.Fatalf("贡献健康表出错: %v", err)
	}
	if len(concepts) != 1 {
		t.Fatalf("该恰好贡献一张表，实际 %d 个概念", len(concepts))
	}
	c := concepts[0]
	if c.Kind != view.KindTable {
		t.Fatalf("Kind 该是 %q，实际 %q", view.KindTable, c.Kind)
	}
	if c.Apply != nil {
		t.Fatal("这张表是可写的——它报的是 daemon 内存里的状态，没有「写回去」这回事")
	}

	table, ok := c.Data.(view.Table)
	if !ok {
		t.Fatalf("Data 该是 view.Table，实际 %T", c.Data)
	}
	if len(table.Columns) == 0 {
		t.Fatal("一列都没有——表头没了，前端画不出东西")
	}

	columns := map[string]bool{}
	for _, col := range table.Columns {
		if col.ID == "" || col.Label == "" {
			t.Errorf("列缺 ID 或 Label: %+v（ID 是格子的索引键，Label 才是给人看的）", col)
		}
		columns[col.ID] = true
	}

	if len(table.Rows) != 2 {
		t.Fatalf("两个 binding 该有两行，实际 %d 行", len(table.Rows))
	}
	for _, row := range table.Rows {
		for id, cell := range row.Cells {
			if !columns[id] {
				t.Errorf("格子 %q 没有对应的列——前端取不到，这一格是空白", id)
			}
			switch cell.Tone {
			case "", view.ToneOK, view.ToneWarn, view.ToneBad:
			default:
				t.Errorf("格子 %q 的 tone=%q 不是已知语义色，前端会当没给（静默不着色）", id, cell.Tone)
			}
			if cell.Text == "" {
				t.Errorf("格子 %q 是空的——该显示「没有」时请显式写一个占位符", id)
			}
		}
	}
}

// TestHealthTableReportsHealthyBindingsToo 锁住「报全部，不只报出事的」。
//
// CLI 的 `newgate breaker` 刻意只报出问题的（终端里没问题的行是噪音），网页
// 这张表刻意全报：一张能停在屏幕上的表，好坏是相对的——一条能用的 binding
// 卡顿到什么程度，只有跟旁边那些比才看得出来。
func TestHealthTableReportsHealthyBindingsToo(t *testing.T) {
	b, _ := clocked()
	trip(t, b, "relay", "slow")

	concepts, _ := healthConcepts(b, nil)
	table := concepts[0].Data.(view.Table)
	if len(table.Rows) != 1 {
		t.Fatalf("只报出事的那些了？实际 %d 行", len(table.Rows))
	}
	if got := table.Rows[0].Cells["binding"].Text; got != "relay/slow" {
		t.Fatalf("binding 那一格该是 provider/model，实际 %q", got)
	}
	// 摘了就该是红的（bad），而不是「有值但没颜色」。
	if tone := table.Rows[0].Cells["state"].Tone; tone != view.ToneBad {
		t.Fatalf("摘牌的 state 该是 %q，实际 %q", view.ToneBad, tone)
	}
}

// TestHealthTableIsEmptyWhenNothingIsKnown 是空白装配的那条：一条 binding 都
// 没记过时，表头照在。空表不是错误——「什么都没出问题」与「这张表坏了」在
// 界面上必须是两种样子。
func TestHealthTableIsEmptyWhenNothingIsKnown(t *testing.T) {
	b, _ := clocked()
	concepts, err := healthConcepts(b, nil)
	if err != nil {
		t.Fatalf("没有 binding 时不该出错: %v", err)
	}
	table := concepts[0].Data.(view.Table)
	if len(table.Rows) != 0 {
		t.Fatalf("空表该没有行，实际 %d", len(table.Rows))
	}
	if len(table.Columns) == 0 {
		t.Fatal("空表也该有表头，否则前端连「这张表在说哪几件事」都显示不出来")
	}
	// 空表要序列化成 `[]`，不是 `null`：nil 切片在 JSON 里是 null，而这一格的
	// 契约是「一个列表」。前端靠 `?? []` 兜住了，但协议不该指望每个消费者都
	// 这么小心。
	raw, err := json.Marshal(table)
	if err != nil {
		t.Fatalf("表序列化失败: %v", err)
	}
	if !strings.Contains(string(raw), `"rows":[]`) {
		t.Fatalf("空表的 rows 该是 []，实际: %s", raw)
	}
}

// TestHealthCardIsLive：这张表声明了 `Live`，界面才会每隔几秒重问一次。
//
// 掉掉它**不会有任何东西报错**：页面安静地停在打开那一刻，而这张表说的全是
// 「现在」——谁在失败、还要等多久。`untilText` 那几个相对时间尤其如此：一个
// 不动的页面上，「还要等 42s」过一分钟就成了假话。
func TestHealthCardIsLive(t *testing.T) {
	b, _ := clocked()
	concepts, err := healthConcepts(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(concepts) == 0 {
		t.Fatal("一张卡都没有——这条断言等于没跑")
	}
	for _, c := range concepts {
		if !c.Live {
			t.Errorf("%s 没有声明 Live：它的内容由流量决定，而读一次只是内存里一把快照", c.ID)
		}
	}
}

// TestPollingTheCardDoesNotAdvanceTheBreaker 是「把这张卡设成 Live」的安全性
// 那一半：**每隔几秒被问一次的那条路，绝不能推进状态机**。
//
// 熔断器有一条会改状态的读路径——`Available`：冷却期满时它顺手把 binding 推进
// 半开、发放本轮的试探名额（见 state.go 的注释）。那扇门是**数据面**走的：
// 谁要发请求，谁去领名额。如果这张卡改成调 `Available`，症状会是「有人把界面
// 开着」就让冷却中的 binding 一轮轮地拿到试探名额——准入决定被一次页面浏览改掉，
// 而且没有任何东西会红。所以这里把两半都钉住：轮询**不动**它，`Available` 才动。
func TestPollingTheCardDoesNotAdvanceTheBreaker(t *testing.T) {
	b, adv := clocked()
	trip(t, b, "relay", "slow") // 摘牌（trip 内部走的是 Available，那是写路径）

	state := func() view.Cell {
		t.Helper()
		concepts, err := healthConcepts(b, nil)
		if err != nil {
			t.Fatal(err)
		}
		return concepts[0].Data.(view.Table).Rows[0].Cells["state"]
	}

	// 冷却期满。此刻表里显示的是**由时钟推导**出来的半开（state(now) 那条判据），
	// 谁都没写任何东西——要紧的是**名额还在不在**：那才是 Available 会动的东西。
	adv(24 * time.Hour)

	awaiting := view.Cell{Text: i18n.T("half-open · awaiting trial", nil), Tone: view.ToneWarn}
	if got := state(); got != awaiting {
		t.Fatalf("前提不成立：冷却期满该显示「等着试探」，实际 %q/%q", got.Text, got.Tone)
	}
	// 界面那一侧：问几次都不许变，而且**一次名额都不许领走**。
	for i := 0; i < 3; i++ {
		if got := state(); got != awaiting {
			t.Fatalf("第 %d 次轮询之后状态变了：%q → %q —— 看一眼界面就推进了状态机",
				i+1, awaiting.Text, got.Text)
		}
	}
	if !b.Available("relay", "slow") {
		t.Fatal("轮询把这一轮的试探名额领走了——数据面的准入决定被一次页面浏览改掉")
	}

	// 对照：真正会动它的是 Available（数据面领名额那条路）——领走之后表里看得出来。
	if got := state(); got.Text != i18n.T("half-open · trial in flight", nil) {
		t.Fatalf("Available 领走名额之后该显示「试探在飞」，实际 %q", got.Text)
	}
}

// trip 把一条 binding 连续打失败到开闸（阈值见策略默认值）。
func trip(t *testing.T, b Breaker, provider, model string) {
	t.Helper()
	for i := 0; i < 2; i++ {
		b.Report(provider, model, Input{Kind: KindConnError})
	}
	if b.Available(provider, model) {
		t.Fatalf("%s/%s 打了两发连接失败还没被摘", provider, model)
	}
}
