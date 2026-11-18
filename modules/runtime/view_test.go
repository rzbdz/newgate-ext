package runtime

import (
	"testing"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/runtime/takeover"
)

// TestTakeoverPhaseIsFourStates 钉住四态判据。
//
// 它**不是**布尔值，而这一点是这张表存在的理由：「接管没接管」听起来像二选一，
// 实际上意愿与现实各有真假，两种**不一致**各自对应一个真实故障现场：
//
//	要求接管却没装上 = 危险（工具静默直连，请求根本不经过 newgate）
//	关掉过却还装着   = 意外（用户以为直连了，其实还在走网关）
//
// 两者都是「任何一处单看都正常」的那种错，所以只能在这里钉住。CLI 那行汇总与
// web 这张表都从 phaseOf 出发，判据错了是两个界面一起错。
func TestTakeoverPhaseIsFourStates(t *testing.T) {
	// 下面的案子说的都是**工具在这台机器上**时的四种状态，所以每一条都带
	// Installed: true。没装那一种是单独的一态，见这个函数的末尾。
	cases := []struct {
		name string
		s    takeover.Status
		want takeoverPhase
	}{
		{"磁盘装了 = 生效", takeover.Status{Installed: true, Wanted: true, Active: true}, phaseActive},
		{"要求了但没装上 = 危险的那种", takeover.Status{Installed: true, Wanted: true, Active: false}, phasePending},
		{"没要求 = 直连（正常）", takeover.Status{Installed: true, Wanted: false, Active: false}, phaseDirect},

		// 用户明确关过它、磁盘上却没放开（释放失败：多用户下常见的权限坑，
		// 见 CLAUDE.md §3.1）。CLI 那行汇总的注释一直把这一种称作「两个对称
		// 故障」之一，可代码从没为它亮过灯——2026-09-20 补上（那时它被当成
		// 「生效中」，于是屏幕上一切正常）。
		//
		// 它**不会**被「没表过态」误触：Wanted 的缺省是 true（见 domain.State.
		// TakeoverWanted），所以从老版本升上来的机器（那时的接管没记进 state.json）
		// 拿到的是 wanted=true，落进 phaseActive。
		{"明确关过但磁盘还装着 = 释放没生效", takeover.Status{Installed: true, Wanted: false, Active: true}, phaseStale},

		// 第五态：这台机器上**没有这个工具**。它压过上面四种——没有工具的时候，
		// 「接管了没有」这个问题不成立。
		//
		// 2026-09-21 实测到的现场：`which opencode` 什么都没有，而 status 报
		// 「opencode ✓」。openocde 走 config 机制，那一格判的是我们改过的
		// ~/.config/opencode/*.json 在不在——**我们做过什么**，不是**用户有没有
		// 这个工具**。四个案子都要试一遍：不管意愿与现实怎么组合，没装就是没装。
		{"没装（意愿+现实都在）", takeover.Status{Installed: false, Wanted: true, Active: true}, phaseAbsent},
		{"没装（只有意愿）", takeover.Status{Installed: false, Wanted: true, Active: false}, phaseAbsent},
		{"没装（只有现实）", takeover.Status{Installed: false, Wanted: false, Active: true}, phaseAbsent},
		{"没装（都没有）", takeover.Status{Installed: false, Wanted: false, Active: false}, phaseAbsent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := phaseOf(c.s); got != c.want {
				t.Errorf("phaseOf(%+v) = %v，期望 %v", c.s, got, c.want)
			}
		})
	}
}

// TestDangerousPhaseIsRed 是上一条的可见面：三种状态在界面上必须**长得不一样**，
// 而中间那种必须是红的。
//
// 灰的或者没颜色的「声明了但没生效」在屏幕上是看不见的——而它恰恰是那个「你
// 以为 claude 在走 newgate，其实它直连」的现场。
func TestDangerousPhaseIsRed(t *testing.T) {
	pending := takeover.Status{Agent: "claude", Installed: true, Wanted: true, Active: false}
	_, tone := stateCell(pending)
	if tone != view.ToneBad {
		t.Fatalf("「声明了却没生效」该是 %q（唯一的危险故障），实际 %q", view.ToneBad, tone)
	}
	if _, tone := stateCell(takeover.Status{Agent: "claude", Installed: true, Wanted: true, Active: true}); tone != view.ToneOK {
		t.Errorf("生效中该是 %q，实际 %q", view.ToneOK, tone)
	}
	// 反过来的那一半（关过、却没放开）是 **warn，不是 bad**：流量仍然经过网关，
	// 那是安全的一侧；bad 留给「静默直连」那一种。两种都红的话，红就指不出重点。
	stale := takeover.Status{Agent: "claude", Installed: true, Wanted: false, Active: true}
	if text, tone := stateCell(stale); tone != view.ToneWarn {
		t.Errorf("「关过但还接着」该是 %q，实际 %q（文案 %q）", view.ToneWarn, tone, text)
	}
	// 直连是正常态：**不着色**。给它上色会让一张全是「直连」的表看起来像故障。
	if text, tone := stateCell(takeover.Status{Agent: "opencode", Installed: true}); tone != "" {
		t.Errorf("直连不该着色（那是正常态），实际 tone=%q text=%q", tone, text)
	}
}

// TestTakeoverTableShape 是这张表的形状棘轮（与 breaker/plugin-manager 那两张
// 同一条）：格子按列 ID 索引，键名写错不会报错——前端取不到就是空白。
func TestTakeoverTableShape(t *testing.T) {
	table := takeoverTable([]takeover.Status{
		{Agent: "claude", Mechanism: takeover.MechShim, Wanted: true, Active: true, Detail: "/x/bin/claude → newgate"},
		{Agent: "opencode", Mechanism: takeover.MechConfig},
	})

	if len(table.Columns) == 0 {
		t.Fatal("没有列——表头没了，前端画不出东西")
	}
	columns := map[string]bool{}
	for _, col := range table.Columns {
		if col.ID == "" || col.Label == "" {
			t.Errorf("列缺 ID 或 Label: %+v（ID 是格子的索引键，Label 才是给人看的）", col)
		}
		columns[col.ID] = true
	}
	if len(table.Rows) != 2 {
		t.Fatalf("两个 agent 该有两行，实际 %d", len(table.Rows))
	}
	for _, row := range table.Rows {
		for id, cell := range row.Cells {
			if !columns[id] {
				t.Errorf("格子 %q 没有对应的列——前端取不到，这一格是空白", id)
			}
			switch cell.Tone {
			case "", view.ToneOK, view.ToneWarn, view.ToneBad:
			default:
				t.Errorf("格子 %q 的 tone=%q 不是已知语义色（前端会当没给）", id, cell.Tone)
			}
			if cell.Text == "" {
				t.Errorf("格子 %q 是空的——该显式写一个占位符（见 dashIfEmpty）", id)
			}
		}
	}
}

// TestTakeoverTableKeepsTheHeaderWhenEmpty：一个 agent 都没接管时表头照在。
//
// 空表不是错误——「谁都没走 newgate」与「这张表坏了」在界面上必须是两种样子。
func TestTakeoverTableKeepsTheHeaderWhenEmpty(t *testing.T) {
	table := takeoverTable(nil)
	if len(table.Rows) != 0 {
		t.Fatalf("空表该没有行，实际 %d", len(table.Rows))
	}
	if len(table.Columns) == 0 {
		t.Fatal("空表也该有表头，否则前端连「这张表在说哪几件事」都显示不出来")
	}
}
