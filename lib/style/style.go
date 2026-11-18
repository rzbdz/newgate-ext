// Package style 是三个前端壳共用的**排版原语**：颜色、对齐、分区、表格。
//
// 为什么值得单独一层：CLI 的输出是「详略得当」这件事的唯一载体——用户看
// 一眼要能拿到结论，出问题时才展开细节。以前每个命令各写各的
// fmt.Printf("%-22s ...")，宽窄不一、中英混排错位（Go 的 %-22s 按**字节**
// 补空格，中文一个字 3 字节、显示却占 2 列，整张表都会歪）、颜色写死在
// 字符串里。统一到这里之后，「长什么样」只有一处定义。
//
// 只管排版，不含任何业务判断——三个壳（CLI / TUI / Web）都可以依赖它。
package style

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// ---------- 颜色 ----------

// 语义色，别在调用点写裸 ANSI。
const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cDim    = "\033[2m"
	cRed    = "\033[31m"
	cGreen  = "\033[32m"
	cYellow = "\033[33m"
	cBlue   = "\033[34m"
	cCyan   = "\033[36m"
)

// colorEnabled 输出到终端时才上色。
//
// 判据三条，优先级从高到低：
//
//	NEWGATE_COLOR=always|never   显式说了算（写测试、录屏用）
//	NO_COLOR 非空                用户/生态的通用约定
//	stdout 是字符设备            重定向到文件或管道时自动褪色
//
// 第三条是最要紧的：`newgate status > 报告.txt` 以前会把一堆 \033[1m 写进
// 文件里，用户复制出来的东西是坏的。
var colorEnabled = func() bool {
	switch os.Getenv("NEWGATE_COLOR") {
	case "always", "1":
		return true
	case "never", "0":
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}()

func paint(code, s string) string {
	if s == "" || !colorEnabled {
		return s
	}
	return code + s + cReset
}

// TTY stdout 是不是终端。分页、进度条这类「只有交互时才该发生」的行为
// 都拿它当判据——重定向到文件时必须退化成老实打印。
func TTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func Bold(s string) string   { return paint(cBold, s) }
func Dim(s string) string    { return paint(cDim, s) }
func Red(s string) string    { return paint(cRed, s) }
func Green(s string) string  { return paint(cGreen, s) }
func Yellow(s string) string { return paint(cYellow, s) }
func Cyan(s string) string   { return paint(cCyan, s) }

// ---------- 符号 ----------

// 四种状态标记。整份 CLI 只认这四个，别再造第五个。
//
//	OK   做到了 / 查过没问题
//	Bad  出错了 / 不可用
//	Skip 有意跳过（不是错，也不是成功）
//	Warn 能用但不对劲
const (
	OK   = "✓"
	Bad  = "✗"
	Skip = "·"
	Warn = "⚠"
)

// Mark 给标记上色，调用点只管传常量。
func Mark(m string) string {
	switch m {
	case OK:
		return Green(m)
	case Bad:
		return Red(m)
	case Warn:
		return Yellow(m)
	}
	return Dim(m)
}

// ---------- 显示宽度 ----------

// Width 字符串在终端里占几列。
//
// 不能用 len() 也不能用 utf8.RuneCountInString：中文/日文/emoji 是**双宽**
// 字符。表格对齐全靠它。
func Width(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// runeWidth 单字符宽度。基于 Unicode East Asian Width 的实用子集——
// 覆盖我们真的会打出来的东西（CJK、全角标点、常用 emoji），不做全表。
func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 32 || (r >= 0x7f && r < 0xa0):
		return 0 // 控制字符（含剥不干净的 ESC 残渣）
	case r >= 0x0300 && r <= 0x036f, r >= 0x200b && r <= 0x200f:
		return 0 // 组合记号与零宽字符
	case r >= 0x1100 && (r <= 0x115f || // 谚文字母
		r == 0x2329 || r == 0x232a || // 〈 〉
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) || // CJK 部首 … 彝文
		(r >= 0xac00 && r <= 0xd7a3) || // 谚文音节
		(r >= 0xf900 && r <= 0xfaff) || // CJK 兼容
		(r >= 0xfe30 && r <= 0xfe6f) || // CJK 兼容标点
		(r >= 0xff00 && r <= 0xff60) || // 全角形式
		(r >= 0xffe0 && r <= 0xffe6) || // 全角符号
		(r >= 0x1f300 && r <= 0x1f5ff) || // 杂项符号与象形
		(r >= 0x1f600 && r <= 0x1f64f) || // 表情
		(r >= 0x1f680 && r <= 0x1f6ff) || // 交通与地图
		(r >= 0x1f7e0 && r <= 0x1f7eb) || // 彩色圆/方（🟢🔴）
		(r >= 0x1f90c && r <= 0x1f9ff) || // 补充符号与象形
		(r >= 0x1fa70 && r <= 0x1faff) || // 扩展 A（含大量新 emoji）
		(r >= 0x20000 && r <= 0x3fffd)): // CJK 扩展
		return 2
	}
	return 1
}

// stripANSI 去掉 SGR 序列——量宽度前必须先剥，否则颜色码会被算成字符。
func stripANSI(s string) string {
	if !strings.Contains(s, "\033") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !(s[j] >= '@' && s[j] <= '~') {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// VisibleWidth 剥掉颜色后的显示宽度。
func VisibleWidth(s string) int { return Width(stripANSI(s)) }

// widestRun 取一段文本里最长的**不可断片段**的显示宽度。
//
// 断点判据：空白与宽字符（CJK）都天然可断——中文按字折行是对的；而一段连续的
// 窄字符（`special.claude-bg.route_light`、`ANTHROPIC_BASE_URL`）是一个**整体**，
// 从中间折断得到的是**两个不存在的名字**：既 grep 不到，也拷不走。
//
// 表格收窄时拿它当列的下限（见 Table.String）：散文按词折行没问题（词短），
// 标识符不能被折。
func widestRun(s string) int {
	best, cur := 0, 0
	flush := func() {
		if cur > best {
			best = cur
		}
		cur = 0
	}
	for _, r := range stripANSI(s) {
		w := runeWidth(r)
		if r == ' ' || r == '\t' || w > 1 {
			flush()
			if w > best {
				best = w
			}
			continue
		}
		cur += w
	}
	flush()
	return best
}

// Pad 把 s 补齐到 w 列（超出则原样返回，绝不截断内容）。
func Pad(s string, w int) string {
	if n := w - VisibleWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// PadLeft 右对齐补齐。
func PadLeft(s string, w int) string {
	if n := w - VisibleWidth(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// Truncate 按显示宽度截断并加省略号。表格里给"可能很长"的列用。
func Truncate(s string, w int) string {
	if VisibleWidth(s) <= w {
		return s
	}
	if w <= 1 {
		return "…"
	}
	var out strings.Builder
	cur := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !(s[j] >= '@' && s[j] <= '~') {
				j++
			}
			if j < len(s) {
				j++
			}
			out.WriteString(s[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runeWidth(r)
		if cur+rw > w-1 {
			break
		}
		out.WriteRune(r)
		cur += rw
		i += size
	}
	out.WriteRune('…')
	if strings.Contains(s, "\033") {
		out.WriteString(cReset)
	}
	return out.String()
}

// Wrap 按终端显示宽度硬换行，不丢字符。ANSI SGR 序列不计宽度；跨行时
// 临时 reset，再在下一行恢复颜色，避免颜色污染缩进或后续输出。
func Wrap(s string, w int) []string {
	if w <= 0 {
		return []string{s}
	}
	var lines []string
	var out strings.Builder
	active := ""
	cur := 0
	flush := func() {
		if active != "" {
			out.WriteString(cReset)
		}
		lines = append(lines, out.String())
		out.Reset()
		if active != "" {
			out.WriteString(active)
		}
		cur = 0
	}
	for i := 0; i < len(s); {
		if s[i] == '\n' {
			flush()
			i++
			continue
		}
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !(s[j] >= '@' && s[j] <= '~') {
				j++
			}
			if j < len(s) {
				j++
			}
			seq := s[i:j]
			out.WriteString(seq)
			if seq == cReset {
				active = ""
			} else if strings.HasSuffix(seq, "m") {
				active += seq
			}
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runeWidth(r)
		if cur > 0 && cur+rw > w {
			flush()
		}
		out.WriteRune(r)
		cur += rw
		i += size
	}
	if active != "" {
		out.WriteString(cReset)
	}
	lines = append(lines, out.String())
	return lines
}

// WrapLine 把一条自由文本限制到 75 列，续行使用指定缩进。
func WrapLine(s, continuation string) string {
	lines := Wrap(s, MaxColumns)
	if len(lines) <= 1 {
		return s
	}
	width := MaxColumns - VisibleWidth(continuation)
	var out []string
	out = append(out, lines[0])
	for _, line := range lines[1:] {
		for _, part := range Wrap(line, width) {
			out = append(out, continuation+part)
		}
	}
	return strings.Join(out, "\n")
}

// ---------- 结构 ----------

// MaxColumns 是 CLI 版面的硬上限。75 列能在常见的窄终端、分屏和 WSL
// 窗口里留一列余量，避免第 76 列触发自动折行把表格撕开。
const MaxColumns = 75

// 版式约定（全 CLI 统一，别在调用点发明新写法）：
//
//	Title      一行页头，后面跟一条 Rule 收口
//	Section    段标题，段内用 Table / Field / Item
//	Field      定义式一行：固定宽度标签 + 值（左对齐，值里可带颜色与次要信息）
//	Item       一条结论，前面一个状态标记
//	Bullet     从属于上一条的明细
//	Hint       次要说明，默认暗色 —— 扫读时自动跳过
//
// 语气也是版式的一部分：一律**陈述句、无主语、带单位、不加语气词**。
// 「别担心」「其实」「我们」这类词不出现；解释性的长句一律进 Hint 或
// 只在出错时展开的 details 里。

const labelW = 8

// Title 页面头：左标题 + 右侧次要信息。右侧为空/未知时不占版面——
// `unknown` 这种字对用户没有任何用。
func Title(left, right string) string {
	if right == "" || right == "unknown" {
		return WrapLine(Bold(left), "  ")
	}
	return WrapLine(Bold(left)+"  "+Dim(right), "  ")
}

// Rule 页头下的暗色分隔线。只用在这里——正文里再画线会和表格打架。
func Rule(w int) string {
	if w > MaxColumns {
		w = MaxColumns
	}
	return Dim(strings.Repeat("─", w))
}

// Section 段标题（含前导空行），调用点直接 Println。
func Section(name string) string {
	return "\n" + WrapLine(Bold(name), "  ")
}

// Field 一行「标签 + 值」：标签固定列宽，值可以带颜色。
//
//	Field("代理", "● 运行中   pid 364367")  →
//	  代理  ● 运行中   pid 364367
//
// 标签补到 labelW 之后**总是**再跟一个空格：标签本身就占满 labelW 时
// （中文双宽很容易占满），没有这格空格值会紧贴着标签。
func Field(label, value string) string {
	return wrapPrefixed("  "+Dim(Pad(label, labelW))+" ", value)
}

// Item 缩进一层的一条明细，标记单独上色。
func Item(mark, text string) string {
	return wrapPrefixed("  "+Mark(mark)+" ", text)
}

// Bullet 无标记的明细行。
func Bullet(text string) string {
	return wrapPrefixed("    ", text)
}

// Hint 次要说明。整份 CLI 里所有「不是结论的话」都应该走这里——
// 它们默认是暗的，扫读时自动跳过。一句话为限，不要写成段落。
func Hint(text string) string {
	return wrapPrefixed("    ", Dim(text))
}

func wrapPrefixed(prefix, text string) string {
	width := MaxColumns - VisibleWidth(prefix)
	lines := Wrap(text, width)
	continuation := strings.Repeat(" ", VisibleWidth(prefix))
	for i := range lines {
		if i == 0 {
			lines[i] = prefix + lines[i]
		} else {
			lines[i] = continuation + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

// ---------- 表格 ----------

type align int

const (
	Left align = iota
	Right
)

// Table 定宽列。列宽按**显示宽度**算（中文双宽），调用点不写格式串。
//
// 用法：
//
//	t := style.NewTable("档位", "模型")
//	t.AlignRight(0)
//	t.Row("heavy", "smt-deepseek/deepseek-flash")
//	fmt.Print(t.String())
type Table struct {
	headers []string
	rows    [][]string
	aligns  []align
	indent  string
}

func NewTable(headers ...string) *Table {
	t := &Table{headers: headers, indent: "  "}
	t.aligns = make([]align, len(headers))
	return t
}

// AlignRight 第 i 列右对齐（数字列用）。
func (t *Table) AlignRight(i int) *Table {
	if i >= 0 && i < len(t.aligns) {
		t.aligns[i] = Right
	}
	return t
}

// Indent 整表左缩进（默认两格）。
func (t *Table) Indent(s string) *Table { t.indent = s; return t }

func (t *Table) Row(cells ...string) *Table {
	t.rows = append(t.rows, cells)
	return t
}

func (t *Table) Len() int { return len(t.rows) }

// String 渲染。列宽 = 该列最宽的一格（表头也参与），列间两空格，
// 行尾不留空格——重定向到文件时不会有恼人的 trailing space。
func (t *Table) String() string {
	n := len(t.headers)
	for _, r := range t.rows {
		if len(r) > n {
			n = len(r)
		}
	}
	widths := make([]int, n)
	floors := make([]int, n)
	measure := func(cells []string) {
		for i, c := range cells {
			if i >= n {
				continue
			}
			if vw := VisibleWidth(c); vw > widths[i] {
				widths[i] = vw
			}
			if wr := widestRun(c); wr > floors[i] {
				floors[i] = wr
			}
		}
	}
	measure(t.headers)
	for _, r := range t.rows {
		measure(r)
	}
	// 表格总宽不得超过 75 列。优先收缩最宽的列，但**不缩到这一列最长的
	// 不可断片段以下**（每列至少 4 列）：散文按词折行没问题，机器标识符折断
	// 出来的是**两个不存在的名字**——既 grep 不到，也拷不走。
	//
	// 2026-09-20 加这条下限：英文表头（`description` 比「说明」宽一倍）把中文
	// 时代那点余量吃掉之后，`newgate metrics` 里的
	// `special.claude-bg.route_light` 真的被折成了两行（发行版的 e2e 抓到的）。
	// 那之前"优先收最宽的列"看着很公平，只是它不认识「这一列折不得」。
	available := MaxColumns - VisibleWidth(t.indent) - 2*(n-1)
	if available < n {
		available = n
	}
	const minW = 4
	for i := range floors {
		if floors[i] < minW {
			floors[i] = minW
		}
	}
	//
	// 两轮：先只收**有余量**的列（收不到不可断片段以下）——正常的表格里散文那一列
	// 有的是余量，一轮就够；实在收不动了（整行都是长标识符）才回到老规则，宁可
	// 折断一个名字也不能超宽：超过 75 列之后终端自己的自动折行会把表格撕开，
	// 那比折断一个名字更难读。
	for sum(widths) > available {
		widest, room := -1, 0
		for i, w := range widths {
			if w-floors[i] > room {
				widest, room = i, w-floors[i]
			}
		}
		if widest < 0 {
			break
		}
		widths[widest]--
	}
	for sum(widths) > available {
		widest, room := -1, 0
		for i, w := range widths {
			if w-minW > room {
				widest, room = i, w-minW
			}
		}
		if widest < 0 {
			break
		}
		widths[widest]--
	}

	var b strings.Builder
	line := func(cells []string, dim bool) {
		wrapped := make([][]string, n)
		height := 1
		for i, c := range cells {
			if i >= n {
				break
			}
			if dim {
				c = Dim(c)
			}
			wrapped[i] = Wrap(c, widths[i])
			if len(wrapped[i]) > height {
				height = len(wrapped[i])
			}
		}
		for row := 0; row < height; row++ {
			b.WriteString(t.indent)
			var parts []string
			for i := 0; i < n; i++ {
				c := ""
				if row < len(wrapped[i]) {
					c = wrapped[i][row]
				}
				if t.aligns[i] == Right {
					parts = append(parts, PadLeft(c, widths[i]))
				} else {
					parts = append(parts, Pad(c, widths[i]))
				}
			}
			b.WriteString(strings.TrimRight(strings.Join(parts, "  "), " "))
			b.WriteString("\n")
		}
	}
	line(t.headers, true)
	for _, r := range t.rows {
		line(r, false)
	}
	return b.String()
}

func sum(ns []int) int {
	total := 0
	for _, n := range ns {
		total += n
	}
	return total
}

// Die 把一条错误按统一版式写到 stderr，并返回退出码。
//
// 放在这里（而不是每个模块自己写三行）的理由：每个模块都可能要从命令里报错，
// 而「newgate: 前缀 + 换行缩进」是**界面版式**的一部分——各写一份必然漂移。
func Die(code int, msg string) int {
	fmt.Fprintln(os.Stderr, WrapLine("newgate: "+msg, "  "))
	return code
}
