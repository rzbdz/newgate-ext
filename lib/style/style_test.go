package style

import (
	"strings"
	"testing"
)

// TestWidth 表格对齐的全部赌注都压在这里：Go 的 %-22s 按**字节**补齐，
// 中文一个字 3 字节、显示却占 2 列，直接用它整张表都会歪。
func TestWidth(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"档位", 4},          // 两个 CJK
		{"heavy 档", 8},     // 5 + 空格 + 2
		{"（全角括号）", 12},     // 6 个全角字符
		{"a（b）c", 7},       // 1 + 2 + 1 + 2 + 1
		{"✓ ✗ ·", 5},       // 状态标记各占 1 列
		{"⚠ 警告", 6},        // ⚠ 是 ambiguous，按 1 列算
		{"á", 1},          // 组合记号零宽
		{"emoji 🟢 ok", 11}, // 彩色圆是双宽（1F7E2 不在最初的 emoji 区间里）
		{"🟡🔴", 4},
	}
	for _, c := range cases {
		if got := Width(c.in); got != c.want {
			t.Errorf("Width(%q) = %d，想要 %d", c.in, got, c.want)
		}
	}
}

// TestStripANSI 量宽度前必须剥颜色码，否则一串 \033[1m 会被算成 4 列。
func TestStripANSI(t *testing.T) {
	if got := VisibleWidth("\033[1mred\033[0m"); got != 3 {
		t.Errorf("VisibleWidth(带色 red) = %d，想要 3", got)
	}
	if got := VisibleWidth("\033[2m档位\033[0m"); got != 4 {
		t.Errorf("VisibleWidth(带色 档位) = %d，想要 4", got)
	}
}

func TestPad(t *testing.T) {
	if got := Pad("档位", 8); VisibleWidth(got) != 8 {
		t.Errorf("Pad 后显示宽度 = %d，想要 8", VisibleWidth(got))
	}
	// 已经超宽时不截断（截断会把内容悄悄吃掉，比错位更糟）
	if got := Pad("很长的中文内容", 4); got != "很长的中文内容" {
		t.Errorf("Pad 不该截断: %q", got)
	}
	if got := PadLeft("42", 5); got != "   42" {
		t.Errorf("PadLeft = %q", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("abcdef", 4); got != "abc…" {
		t.Errorf("Truncate = %q，想要 abc…", got)
	}
	if got := Truncate("中文内容", 5); VisibleWidth(got) > 5 {
		t.Errorf("Truncate 后仍超宽: %q (%d)", got, VisibleWidth(got))
	}
	if got := Truncate("abc", 10); got != "abc" {
		t.Errorf("不该给够短的串加省略号: %q", got)
	}
	colored := "\033[31mabcdefgh\033[0m"
	if got := Truncate(colored, 5); VisibleWidth(got) != 5 || !strings.Contains(got, "\033[31m") {
		t.Errorf("带色截断损坏: %q (%d)", got, VisibleWidth(got))
	}
}

// TestTableColumnsAlign 表格的判据是**列起点对齐**，不是各行等宽——
// 行尾空格会被裁掉，最后一行天然更短。
func TestTableColumnsAlign(t *testing.T) {
	tbl := NewTable("档位", "绑定")
	tbl.Row("heavy", "smt-deepseek/deepseek-flash")
	tbl.Row("normal", "ark/ark-code-latest")
	tbl.Row("vision", "smt-gemini/gemini-3.1-pro-preview")
	lines := strings.Split(strings.TrimRight(tbl.String(), "\n"), "\n")

	// 第一列最宽 6 列（vision/normal），缩进 2，列间 2 → 第二列起点在第 10 列。
	const want = 2 + 6 + 2
	for _, l := range lines {
		var cell string
		switch {
		case strings.Contains(l, "档位"):
			cell = "绑定"
		case strings.Contains(l, "smt-deepseek"):
			cell = "smt-deepseek/deepseek-flash"
		case strings.Contains(l, "ark/"):
			cell = "ark/ark-code-latest"
		default:
			cell = "smt-gemini/gemini-3.1-pro-preview"
		}
		i := strings.Index(l, cell)
		if i < 0 {
			t.Fatalf("找不到第二列内容 %q：%q", cell, l)
		}
		if got := VisibleWidth(l[:i]); got != want {
			t.Errorf("第二列起点在第 %d 列，想要 %d：%q", got, want, l)
		}
	}
	// 行尾不留空格：重定向到文件时不该有恼人的 trailing space
	for _, l := range lines {
		if strings.HasSuffix(l, " ") {
			t.Errorf("行尾有空格: %q", l)
		}
	}
}

func TestTableNeverExceedsMaxColumns(t *testing.T) {
	tbl := NewTable("profile", "绑定", "说明")
	tbl.Row(
		"a-profile-with-a-very-long-name",
		"smt-provider/a-model-name-that-is-deliberately-far-too-long-for-a-narrow-terminal",
		"这是一段很长的中文说明，用来确认显示宽度而不是 UTF-8 字节数决定截断位置",
	)
	out := tbl.String()
	if strings.Contains(out, "…") {
		t.Fatalf("表格不应截断内容:\n%s", out)
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if got := VisibleWidth(line); got > MaxColumns {
			t.Fatalf("表格宽度 = %d，超过 %d：%q", got, MaxColumns, line)
		}
	}
}

// TestTableNeverBreaksAnIdentifier 锁住列宽的下限：表格整体超宽时，收缩的是
// **散文**那一列；机器标识符（没有空格的连续窄字符）不能被折断。
//
// 2026-09-20 之前没有这条下限，收缩规则是「每轮削最宽的那一列」——看着很公平，
// 只是它不认识「这一列折不得」。英文表头（`description` 比「说明」宽一倍）把中文
// 时代那点余量吃掉之后，`newgate metrics` 的 `special.claude-bg.route_light`
// 被折成了两行（发行版 e2e 抓到的）。那两行是**两个不存在的计数器名**。
func TestTableNeverBreaksAnIdentifier(t *testing.T) {
	const id = "special.claude-bg.route_light"
	tbl := NewTable("group", "counter", "count", "description")
	tbl.Row("plugin", id, "4",
		"Bash classifier: the whole chain is rerouted to light, and the rest of this sentence exists only to push the table past the limit")
	out := tbl.String()
	if !strings.Contains(out, id) {
		t.Errorf("标识符被折断了——它在输出里不再是一个整体：\n%s", out)
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if got := VisibleWidth(line); got > MaxColumns {
			t.Errorf("表格宽度 = %d，超过 %d：%q", got, MaxColumns, line)
		}
	}
	// 反面：散文列**该**折（下限只护不可断片段，不护整句）。
	if !strings.Contains(out, "classifier:") {
		t.Errorf("散文没有折行的余地？输出：\n%s", out)
	}
}

func TestRuleClampsToMaxColumns(t *testing.T) {
	if got := VisibleWidth(Rule(200)); got != MaxColumns {
		t.Fatalf("Rule 宽度 = %d，想要 %d", got, MaxColumns)
	}
}

func TestLinePrimitivesNeverExceedMaxColumns(t *testing.T) {
	long := strings.Repeat("很长", 50)
	lines := []string{
		Title(long, long),
		Field("标签", long),
		Item(Warn, long),
		Bullet(long),
		Hint(long),
	}
	for _, line := range lines {
		if strings.Contains(line, "…") {
			t.Fatalf("单行原语不应截断内容: %q", line)
		}
		for _, physical := range strings.Split(line, "\n") {
			if got := VisibleWidth(physical); got > MaxColumns {
				t.Fatalf("行宽 = %d，超过 %d：%q", got, MaxColumns, physical)
			}
		}
	}
}

func TestWrapPreservesANSIAndContent(t *testing.T) {
	in := Red("abcdefgh")
	lines := Wrap(in, 3)
	if len(lines) != 3 {
		t.Fatalf("Wrap 行数 = %d，想要 3: %#v", len(lines), lines)
	}
	var plain strings.Builder
	for _, line := range lines {
		if VisibleWidth(line) > 3 {
			t.Fatalf("换行后仍超宽: %q", line)
		}
		plain.WriteString(stripANSI(line))
	}
	if plain.String() != "abcdefgh" {
		t.Fatalf("换行丢内容: %q", plain.String())
	}
}

// TestColorSwitch 颜色只能出现在终端上。colorEnabled 是包级变量，
// 测试里改完必须还原，否则会影响同包其它测试。
func TestColorSwitch(t *testing.T) {
	old := colorEnabled
	defer func() { colorEnabled = old }()

	colorEnabled = false
	if got := Bold("x"); got != "x" {
		t.Errorf("关掉颜色后不该有 %q", got)
	}
	if got := Mark(OK); got != OK {
		t.Errorf("Mark 关色后 = %q", got)
	}
	tbl := NewTable("h")
	tbl.Row("a")
	if strings.Contains(tbl.String(), "\033") {
		t.Errorf("关掉颜色后表格里仍有转义码: %q", tbl.String())
	}

	colorEnabled = true
	if !strings.Contains(Bold("x"), "\033") {
		t.Error("开色后应该有转义码")
	}
}
