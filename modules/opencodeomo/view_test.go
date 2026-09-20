package opencodeomo

import (
	"strings"
	"testing"

	"github.com/rzbdz/newgate/lib/view"
)

// TestSlotsCardExplainsAnAbsentRegistry：还没接管过 omo 时**照常出一张卡片**，
// 写明原因与怎么生成。
//
// 让它从列表里消失的话，用户会以为这个模块不在这个构建里——而实际是「还没接管过
// 一次」（与 CLI 的 omoRegistry 同一句话，同一份理由：见 lib/view 的 Concept.Broken）。
func TestSlotsCardExplainsAnAbsentRegistry(t *testing.T) {
	isolate(t)

	concepts, err := omoConcepts()
	if err != nil {
		t.Fatalf("没有注册表不该报错（那是正常状态，不是故障）: %v", err)
	}
	if len(concepts) != 1 {
		t.Fatalf("该出一张卡片，实际 %d 张", len(concepts))
	}
	c := concepts[0]
	if c.Broken == "" {
		t.Fatal("没有注册表时必须说明原因（Broken），否则界面上是一张空卡片")
	}
	// 原因里要有两样东西：文件在哪、怎么生成。只说「读不出来」等于把人卡住。
	if !strings.Contains(c.Broken, SlotsFile()) {
		t.Errorf("原因里该点名文件（%s）: %q", SlotsFile(), c.Broken)
	}
	if !strings.Contains(c.Broken, "newgate on opencode") {
		t.Errorf("原因里该说清怎么生成一个: %q", c.Broken)
	}
}

// TestSlotsTableShape 是这张表的形状棘轮（与内核那三张同一条）：格子按列 ID
// 索引，键名写错不会报错——前端取不到就是空白。
func TestSlotsTableShape(t *testing.T) {
	isolate(t)
	if err := WriteOmoSlots(&OmoSlots{
		Version: 1, Mode: "current",
		Slots: []OmoSlot{
			{Key: "omo-sisyphus", Kind: "agent", Name: "sisyphus", Current: "normal", Suggested: "heavy"},
			{Key: "omo-librarian", Kind: "agent", Name: "librarian", Current: "light", Suggested: "light"},
		},
		// 覆盖最优先：effective 该是 mid，而不是 current 的 normal。
		Overrides: map[string]string{"omo-sisyphus": "mid"},
	}); err != nil {
		t.Fatal(err)
	}

	concepts, err := omoConcepts()
	if err != nil {
		t.Fatal(err)
	}
	c := concepts[0]
	if c.Broken != "" {
		t.Fatalf("有注册表却说读不出来: %s", c.Broken)
	}
	if c.Apply != nil {
		t.Error("这张卡片是可写的？写槽位归属的语义只有一份（omoUse），别在这里再造一份")
	}
	table, ok := c.Data.(view.Table)
	if !ok {
		t.Fatalf("Data 该是 view.Table，实际 %T", c.Data)
	}

	columns := map[string]bool{}
	for _, col := range table.Columns {
		if col.ID == "" || col.Label == "" {
			t.Errorf("列缺 ID 或 Label: %+v", col)
		}
		columns[col.ID] = true
	}
	if len(table.Rows) != 2 {
		t.Fatalf("两个槽位该有两行，实际 %d", len(table.Rows))
	}
	for _, row := range table.Rows {
		for id, cell := range row {
			if !columns[id] {
				t.Errorf("格子 %q 没有对应的列——前端取不到，这一格是空白", id)
			}
			switch cell.Tone {
			case "", view.ToneOK, view.ToneWarn, view.ToneBad:
			default:
				t.Errorf("格子 %q 的 tone=%q 不是已知语义色（前端会当没给）", id, cell.Tone)
			}
			if cell.Text == "" {
				t.Errorf("格子 %q 是空的——该显式写占位符", id)
			}
		}
	}

	byKey := map[string]map[string]view.Cell{}
	for _, row := range table.Rows {
		byKey[row["key"].Text] = row
	}
	// override 要**说出口**：光显示 mid，用户不知道它是被谁定的（CLI 那边打一个
	// `*` 再在页脚解释；表没有页脚，所以写进格子里）。
	if got := byKey["omo-sisyphus"]["effective"].Text; !strings.Contains(got, "mid") ||
		!strings.Contains(got, "override") {
		t.Errorf("被 override 的槽位该在格子里说明，实际 %q", got)
	}
	if got := byKey["omo-librarian"]["effective"].Text; got != "light" {
		t.Errorf("没被 override 的该原样显示，实际 %q", got)
	}
}

// TestSuggestedIsYellowOnlyWhenItDiffers 钉住「与建议不符」这条判据。
//
// 与 CLI 一字不差（`Suggested != "" && Suggested != Current`）：两个界面对
// 「哪些槽位偏离了建议」必须给出同一个答案。**不能**拿 effective 去比——那是
// 另一个问题了（override 之后两者常常不等，于是整张表全黄，黄就失去意义）。
func TestSuggestedIsYellowOnlyWhenItDiffers(t *testing.T) {
	differs := OmoSlot{Key: "omo-a", Current: "normal", Suggested: "heavy"}
	same := OmoSlot{Key: "omo-b", Current: "light", Suggested: "light"}
	none := OmoSlot{Key: "omo-c", Current: "mid"}

	if cell := suggestedCell(differs); cell.Tone != view.ToneWarn {
		t.Errorf("与建议不符该标黄，实际 tone=%q", cell.Tone)
	}
	for _, s := range []OmoSlot{same, none} {
		if cell := suggestedCell(s); cell.Tone != "" {
			t.Errorf("%s 不该标黄（判据是与 current 比），实际 tone=%q", s.Key, cell.Tone)
		}
	}
	// 没有建议时给横杠，不是空串：空格子在界面上读起来像「这里出了问题」。
	if cell := suggestedCell(none); cell.Text != "-" {
		t.Errorf("没有建议该显示占位符，实际 %q", cell.Text)
	}
}
