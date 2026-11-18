// Package style is the **layout primitives** shared by the
// three frontend shells: color, alignment, sections, tables.
//
// Why it deserves its own layer: CLI output is the only vehicle for "detail
// in proportion" -- the user must get the conclusion at a glance and see details only
// when something goes wrong. Each command used to write its own `fmt.Printf("%-22s ...")`,
// with uneven widths and mixed Chinese/English misaligned (Go's %-22s pads by **bytes**,
// a Chinese character is 3 bytes but 2 display columns, and the whole table skews), and
// colors hardcoded in strings. Once unified here, "what it looks like" has one definition.
//
// Layout only, no business judgment -- all three shells (CLI / TUI / Web) can depend on it.
package style

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// ---------- Color ----------

// Semantic colors; do not write raw ANSI at call sites.
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

// colorEnabled colors only when output goes to a terminal.
//
// Three tests, highest priority first:
//
//	NEWGATE_COLOR=always|never   explicit decision (for tests, screen recordings)
//	NO_COLOR non-empty            the user/ecosystem common convention
//	stdout is a character device  strip color automatically when redirected to a file or pipe
//
// The third is the most important: `newgate status > report.txt` used to write
// a pile of \033[1m into the file, and what the user copied out was broken.
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

// TTY reports whether stdout is a terminal. Behaviors that "should only
// happen when interactive", like paging and progress bars, use it as the
// test -- they must degrade to plain printing when redirected to a file.
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

// ---------- Symbols ----------

// Four status marks. The whole CLI recognizes only these four; do not invent a fifth.
//
//	OK   done / checked and fine
//	Bad  failed / unavailable
//	Skip deliberately skipped (not an error, not a success)
//	Warn works but not right
const (
	OK   = "✓"
	Bad  = "✗"
	Skip = "·"
	Warn = "⚠"
)

// Mark colors a mark; call sites only pass constants.
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

// ---------- Display width ----------

// Width is how many columns a string occupies in the terminal.
//
// Neither len() nor utf8.RuneCountInString will do: Chinese/Japanese/emoji
// are **double width** characters. Table alignment relies entirely on this.
func Width(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// runeWidth is the width of one character. A practical subset of Unicode East Asian / Width --
// covering what we really print (CJK, full-width punctuation, common emoji), not the whole table.
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

// stripANSI removes SGR sequences -- they must be stripped before
// measuring width, otherwise color codes count as characters.
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

// VisibleWidth is the display width after stripping color.
func VisibleWidth(s string) int { return Width(stripANSI(s)) }

// widestRun is the display width of the longest **unbreakable run** in a piece of text.
//
// Break tests: whitespace and wide characters (CJK) are naturally
// breakable -- breaking Chinese per character is right; but a run of narrow characters
// (`special.claude-bg.route_light`, `ANTHROPIC_BASE_URL`) is **one whole**, and breaking
// it in the middle yields **two names that do not exist**: neither greppable nor copyable.
//
// When narrowing a table it is the column's lower bound (see Table.String):
// prose wraps by word fine (words are short), identifiers must not be broken.
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

// Pad pads s to w columns (returns it unchanged if wider; never truncates content).
func Pad(s string, w int) string {
	if n := w - VisibleWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// PadLeft pads right-aligned.
func PadLeft(s string, w int) string {
	if n := w - VisibleWidth(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// Truncate truncates by display width and adds an
// ellipsis. For columns in a table that "may be very long".
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

// Wrap hard-wraps by terminal display width without losing characters. ANSI SGR sequences
// do not count toward width; across a line break it resets temporarily and restores
// the color on the next line, so color does not pollute the indent or later output.
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

// WrapLine limits free text to 75 columns, with continuation lines using the given indent.
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

// ---------- Structure ----------

// MaxColumns is the hard limit of the CLI layout. 75 columns leaves one
// column of slack in common narrow terminals, split panes, and WSL windows,
// so column 76 does not trigger automatic wrapping that tears the table apart.
const MaxColumns = 75

// Layout conventions (uniform across the CLI; do not invent new forms at call sites):
//
//	Title      one-line page header, closed by a Rule after it
//	Section    section heading; use Table / Field / Item inside
//	Field      definition line: fixed-width label + value (left-aligned, value may carry color)
//	Item       one conclusion, preceded by a status mark
//	Bullet     detail belonging to the item above
//	Hint       secondary note, dim by default -- skipped automatically when scanning
//
// Tone is part of the layout too: always **declarative, subjectless, with units,
// no filler words**. Words like "don't worry", "actually", "we" do not appear; long
// explanatory sentences all go into Hint or into details that only expand on error.

const labelW = 8

// Title is the page header: title on the left + secondary info on the right. When the right
// side is empty/unknown it takes no room -- a word like `unknown` is of no use to the user.
func Title(left, right string) string {
	if right == "" || right == "unknown" {
		return WrapLine(Bold(left), "  ")
	}
	return WrapLine(Bold(left)+"  "+Dim(right), "  ")
}

// Rule is the dim separator under the page header. Only used
// here -- drawing lines in the body would fight the tables.
func Rule(w int) string {
	if w > MaxColumns {
		w = MaxColumns
	}
	return Dim(strings.Repeat("─", w))
}

// Section is a section heading (with a leading blank line); call sites Println it directly.
func Section(name string) string {
	return "\n" + WrapLine(Bold(name), "  ")
}

// Field is a line of "label + value": the label has
// a fixed column width, the value may carry color.
//
//	Field("proxy", "● running   pid 364367")  ->
//	  proxy  ● running   pid 364367
//
// After padding the label to labelW a space **always** follows: when the label already fills labelW
// (easy with double-width Chinese), without this space the value would sit flush against the label.
func Field(label, value string) string {
	return wrapPrefixed("  "+Dim(Pad(label, labelW))+" ", value)
}

// Item is one indented level of detail, with the mark colored separately.
func Item(mark, text string) string {
	return wrapPrefixed("  "+Mark(mark)+" ", text)
}

// Bullet is a detail line without a mark.
func Bullet(text string) string {
	return wrapPrefixed("    ", text)
}

// Hint is a secondary note. Every "not a conclusion" sentence in the CLI
// should go here -- they are dim by default and skipped automatically
// when scanning. One sentence at most; do not write paragraphs.
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

// ---------- Tables ----------

type align int

const (
	Left align = iota
	Right
)

// Table is fixed-width columns. Column widths are computed by **display
// width** (double width for Chinese), and call sites write no format strings.
//
// Usage:
//
//	t := style.NewTable("tier", "model")
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

// AlignRight right-aligns column i (for numeric columns).
func (t *Table) AlignRight(i int) *Table {
	if i >= 0 && i < len(t.aligns) {
		t.aligns[i] = Right
	}
	return t
}

// Indent indents the whole table (two spaces by default).
func (t *Table) Indent(s string) *Table { t.indent = s; return t }

func (t *Table) Row(cells ...string) *Table {
	t.rows = append(t.rows, cells)
	return t
}

func (t *Table) Len() int { return len(t.rows) }

// String renders it. Column width = the widest cell in that column (the
// header counts too), two spaces between columns, and no trailing space at
// line end -- so redirecting to a file does not leave annoying trailing space.
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
	// The table's total width must not exceed 75 columns. Narrow the widest column
	// first, but **never below this column's longest unbreakable run** (at least 4
	// columns per column): prose wraps by word fine, but breaking a machine identifier
	// yields **two names that do not exist** -- neither greppable nor copyable.
	//
	// This floor was added on 2026-09-20: after English headers (`description`
	// is twice as wide as the Chinese label) ate up the slack from the Chinese era,
	// `special.claude-bg.route_light` in `newgate metrics` really was wrapped onto two
	// lines (caught by the distribution's e2e). Before that, "narrow the widest column
	// first" looked fair, except it did not know "this column cannot be wrapped".
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
	// Two passes: first narrow only columns with **slack** (not below their unbreakable run)
	// -- in a normal table the prose column has plenty of slack and one pass is enough; only
	// when nothing can be narrowed (the whole row is long identifiers) fall back to the old
	// rule, preferring to break one name over overflowing: past 75 columns the terminal's
	// own wrapping tears the table apart, which is harder to read than one broken name.
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

// Die writes an error to stderr in the uniform layout and returns the exit code.
//
// Why here (instead of three lines in every module): every module may
// need to report an error from a command, and "newgate: prefix + newline
// indent" is part of the **UI layout** -- written separately it will drift.
func Die(code int, msg string) int {
	fmt.Fprintln(os.Stderr, WrapLine("newgate: "+msg, "  "))
	return code
}
