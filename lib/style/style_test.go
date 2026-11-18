package style

import (
	"strings"
	"testing"
)

// TestWidth: the entire bet on table alignment rests here: Go's %-22s pads by **bytes**, a
// Chinese character is 3 bytes but 2 display columns, and using it directly skews the whole table.
func TestWidth(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"档位", 4},          // two CJK characters
		{"heavy 档", 8},     // 5 + space + 2
		{"（全角括号）", 12},     // six full-width characters
		{"a（b）c", 7},       // 1 + 2 + 1 + 2 + 1
		{"✓ ✗ ·", 5},       // each status mark is one column
		{"⚠ 警告", 6},        // ⚠ is ambiguous, counted as one column
		{"a\u0301", 1},     // combining marks are zero width
		{"emoji 🟢 ok", 11}, // the colored circle is double width (1F7E2 is outside the emoji ranges)
		{"🟡🔴", 4},
	}
	for _, c := range cases {
		if got := Width(c.in); got != c.want {
			t.Errorf("Width(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestStripANSI: color codes must be stripped before measuring
// width, otherwise a run of \033[1m counts as 4 columns.
func TestStripANSI(t *testing.T) {
	if got := VisibleWidth("\033[1mred\033[0m"); got != 3 {
		t.Errorf("VisibleWidth(colored red) = %d, want 3", got)
	}
	if got := VisibleWidth("\033[2m档位\033[0m"); got != 4 {
		t.Errorf("VisibleWidth(colored 档位) = %d, want 4", got)
	}
}

func TestPad(t *testing.T) {
	if got := Pad("tier", 8); VisibleWidth(got) != 8 {
		t.Errorf("display width after Pad = %d, want 8", VisibleWidth(got))
	}
	// Do not truncate when already too wide (truncation
	// silently eats content, worse than misalignment)
	if got := Pad("a very long CJK string", 4); got != "a very long CJK string" {
		t.Errorf("Pad must not truncate: %q", got)
	}
	if got := PadLeft("42", 5); got != "   42" {
		t.Errorf("PadLeft = %q", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("abcdef", 4); got != "abc…" {
		t.Errorf("Truncate = %q, want abc…", got)
	}
	if got := Truncate("CJK content", 5); VisibleWidth(got) > 5 {
		t.Errorf("still too wide after Truncate: %q (%d)", got, VisibleWidth(got))
	}
	if got := Truncate("abc", 10); got != "abc" {
		t.Errorf("must not add an ellipsis to a short enough string: %q", got)
	}
	colored := "\033[31mabcdefgh\033[0m"
	if got := Truncate(colored, 5); VisibleWidth(got) != 5 || !strings.Contains(got, "\033[31m") {
		t.Errorf("colored truncation is broken: %q (%d)", got, VisibleWidth(got))
	}
}

// TestTableColumnsAlign: the test is **column start alignment**, not equal row
// widths -- trailing spaces are trimmed, so the last row is naturally shorter.
func TestTableColumnsAlign(t *testing.T) {
	tbl := NewTable("tier", "binding")
	tbl.Row("heavy", "smt-deepseek/deepseek-flash")
	tbl.Row("normal", "ark/ark-code-latest")
	tbl.Row("vision", "smt-gemini/gemini-3.1-pro-preview")
	lines := strings.Split(strings.TrimRight(tbl.String(), "\n"), "\n")

	// The first column is at most 6 columns wide (vision/normal), indent
	// 2, two between columns -> the second column starts at column 10.
	const want = 2 + 6 + 2
	for _, l := range lines {
		var cell string
		switch {
		case strings.Contains(l, "tier"):
			cell = "binding"
		case strings.Contains(l, "smt-deepseek"):
			cell = "smt-deepseek/deepseek-flash"
		case strings.Contains(l, "ark/"):
			cell = "ark/ark-code-latest"
		default:
			cell = "smt-gemini/gemini-3.1-pro-preview"
		}
		i := strings.Index(l, cell)
		if i < 0 {
			t.Fatalf("second column content %q not found: %q", cell, l)
		}
		if got := VisibleWidth(l[:i]); got != want {
			t.Errorf("second column starts at column %d, want %d: %q", got, want, l)
		}
	}
	// No trailing space at line end: no annoying trailing space when redirected to a file
	for _, l := range lines {
		if strings.HasSuffix(l, " ") {
			t.Errorf("trailing space at end of line: %q", l)
		}
	}
}

func TestTableNeverExceedsMaxColumns(t *testing.T) {
	tbl := NewTable("profile", "binding", "description")
	tbl.Row(
		"a-profile-with-a-very-long-name",
		"smt-provider/a-model-name-that-is-deliberately-far-too-long-for-a-narrow-terminal",
		"this is a long CJK description, used to confirm that display width rather than UTF-8 byte count decides where truncation happens",
	)
	out := tbl.String()
	if strings.Contains(out, "…") {
		t.Fatalf("the table must not truncate content:\n%s", out)
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if got := VisibleWidth(line); got > MaxColumns {
			t.Fatalf("table width = %d, exceeds %d: %q", got, MaxColumns, line)
		}
	}
}

// TestTableNeverBreaksAnIdentifier pins the column-width floor: when the
// table as a whole overflows, what narrows is the **prose** column; machine
// identifiers (runs of narrow characters with no spaces) must not be broken.
//
// Before 2026-09-20 there was no such floor, and the narrowing rule was "shave the widest column
// each round" -- which looked fair, except it did not know "this column cannot be wrapped". After
// English headers (`description` is twice as wide as the Chinese label) ate the slack from the
// Chinese era, `special.claude-bg.route_light` in `newgate metrics` was wrapped onto two lines
// (caught by the distribution e2e). Those two lines were **two counter names that do not exist**.
func TestTableNeverBreaksAnIdentifier(t *testing.T) {
	const id = "special.claude-bg.route_light"
	tbl := NewTable("group", "counter", "count", "description")
	tbl.Row("plugin", id, "4",
		"Bash classifier: the whole chain is rerouted to light, and the rest of this sentence exists only to push the table past the limit")
	out := tbl.String()
	if !strings.Contains(out, id) {
		t.Errorf("the identifier was broken -- it is no longer a single unit in the output:\n%s", out)
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if got := VisibleWidth(line); got > MaxColumns {
			t.Errorf("table width = %d, exceeds %d: %q", got, MaxColumns, line)
		}
	}
	// The reverse: a prose column **should** wrap (the floor
	// only protects unbreakable runs, not whole sentences).
	if !strings.Contains(out, "classifier:") {
		t.Errorf("no room to wrap the prose? output:\n%s", out)
	}
}

func TestRuleClampsToMaxColumns(t *testing.T) {
	if got := VisibleWidth(Rule(200)); got != MaxColumns {
		t.Fatalf("Rule width = %d, want %d", got, MaxColumns)
	}
}

func TestLinePrimitivesNeverExceedMaxColumns(t *testing.T) {
	long := strings.Repeat("very long", 50)
	lines := []string{
		Title(long, long),
		Field("label", long),
		Item(Warn, long),
		Bullet(long),
		Hint(long),
	}
	for _, line := range lines {
		if strings.Contains(line, "…") {
			t.Fatalf("single-line primitives must not truncate content: %q", line)
		}
		for _, physical := range strings.Split(line, "\n") {
			if got := VisibleWidth(physical); got > MaxColumns {
				t.Fatalf("line width = %d, exceeds %d: %q", got, MaxColumns, physical)
			}
		}
	}
}

func TestWrapPreservesANSIAndContent(t *testing.T) {
	in := Red("abcdefgh")
	lines := Wrap(in, 3)
	if len(lines) != 3 {
		t.Fatalf("Wrap lines = %d, want 3: %#v", len(lines), lines)
	}
	var plain strings.Builder
	for _, line := range lines {
		if VisibleWidth(line) > 3 {
			t.Fatalf("still too wide after wrapping: %q", line)
		}
		plain.WriteString(stripANSI(line))
	}
	if plain.String() != "abcdefgh" {
		t.Fatalf("wrapping lost content: %q", plain.String())
	}
}

// TestColorSwitch: color may only appear on a terminal. colorEnabled is a package-level variable,
// so tests must restore it after changing it, or other tests in the package are affected.
func TestColorSwitch(t *testing.T) {
	old := colorEnabled
	defer func() { colorEnabled = old }()

	colorEnabled = false
	if got := Bold("x"); got != "x" {
		t.Errorf("must not contain %q after colors are off", got)
	}
	if got := Mark(OK); got != OK {
		t.Errorf("Mark with colors off = %q", got)
	}
	tbl := NewTable("h")
	tbl.Row("a")
	if strings.Contains(tbl.String(), "\033") {
		t.Errorf("escape codes remain in the table after colors are off: %q", tbl.String())
	}

	colorEnabled = true
	if !strings.Contains(Bold("x"), "\033") {
		t.Error("escape codes should be present after colors are on")
	}
}
