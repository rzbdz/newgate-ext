package setup

// 本文件是向导的**那几屏**：多选客户端、填表单、看结局。
//
// 它只在终端上画东西，所有「改这台机器」的动作都在 agents.go / profile.go 里，
// 而按键与 raw mode 在 lib/term。所以这一屏可以被一个假的 Terminal + buffer
// 驱动，e2e 里就不需要一台真终端来验行为。
//
// # 一条贯穿整屏的规矩：Esc = 退回上一步，Ctrl-C = 放弃
//
// 向导会写磁盘（provider、profile、接管），所以每一条退回的路都要么什么都没写、
// 要么写完之后明确告诉人「已经写了什么」。中途放弃的结局在 main 里说清楚。

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/style"
	term "github.com/rzbdz/newgate-ext/lib/term"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate/lib/i18n"
)

const (
	clear = "\033[2J\033[H"
	rev   = "\033[7m"
	reset = "\033[0m"
	hideC = "\033[?25l"
	showC = "\033[?25h"
)

// Screen 是向导要画的那一屏的接口（真身是 *term.Terminal，测试里是假的）。
type Screen interface {
	ReadEvent() term.Event
	ReadLine(term.LineMode, func(text, errMsg string, cursor int)) term.LineResult
}

// pickResult 是多选那一屏的结局。
type pickResult struct {
	Chosen  []string
	Cancel  bool
	Nothing bool // 用户一个都没选就按了回车
}

// pickAgents 画「选装哪些客户端」那一屏。
//
// 默认勾选**已经装上、但还没被接管**的那些：用户敲 `newgate config` 时最想做的
// 那件事就是这个（装好的工具让它走 newgate），而重新勾一个已经接管的没有代价。
func pickAgents(s Screen, agents agentapi.AgentCatalog, controlled func(string) bool) pickResult {
	ids := agents.Names()
	sort.Strings(ids)
	sel := make([]bool, len(ids))
	installed := make([]bool, len(ids))
	takenOver := make([]bool, len(ids))
	for i, id := range ids {
		installed[i] = agents.Installed(id)
		takenOver[i] = controlled(id)
		sel[i] = installed[i] && !takenOver[i]
	}
	cur := 0
	for {
		drawPick(ids, sel, installed, takenOver, cur)
		ev := s.ReadEvent()
		switch ev.Key {
		case term.KeyUp:
			if cur > 0 {
				cur--
			}
		case term.KeyDown:
			if cur < len(ids)-1 {
				cur++
			}
		case term.KeyRune:
			switch ev.Rune {
			case 'j':
				if cur < len(ids)-1 {
					cur++
				}
			case 'k':
				if cur > 0 {
					cur--
				}
			case ' ':
				if cur < len(sel) {
					sel[cur] = !sel[cur]
				}
			case 'a', 'A':
				all := true
				for _, v := range sel {
					if !v {
						all = false
					}
				}
				for i := range sel {
					sel[i] = !all
				}
			}
		case term.KeySpace:
			if cur < len(sel) {
				sel[cur] = !sel[cur]
			}
		case term.KeyEnter:
			var chosen []string
			for i, v := range sel {
				if v {
					chosen = append(chosen, ids[i])
				}
			}
			if len(chosen) == 0 {
				return pickResult{Nothing: true}
			}
			return pickResult{Chosen: chosen}
		case term.KeyEsc:
			return pickResult{Cancel: true}
		case term.KeyInterrupt, term.KeyEOF:
			return pickResult{Cancel: true}
		}
	}
}

func drawPick(ids []string, sel, installed, takenOver []bool, cur int) {
	var b strings.Builder
	b.WriteString(clear)
	b.WriteString(style.Bold(" newgate · "+i18n.T("first setup", nil)) + "\n")
	b.WriteString(style.Dim(" "+i18n.T(
		"↑/↓ move · space toggle · a all · enter continue · esc back · ctrl-c quit", nil)) + "\n\n")
	b.WriteString(" " + style.Bold(i18n.T("Which clients should go through newgate?", nil)) + "\n\n")
	for i, id := range ids {
		mark := "[ ]"
		if sel[i] {
			mark = "[x]"
		}
		state := i18n.T("not installed — will be installed", nil)
		if installed[i] {
			if takenOver[i] {
				state = i18n.T("installed, already taken over", nil)
			} else {
				state = i18n.T("installed", nil)
			}
		}
		line := fmt.Sprintf("  %s %-10s %s", mark, id, state)
		if i == cur {
			b.WriteString(rev + padRight(line, 74) + reset + "\n")
		} else {
			b.WriteString(line + "\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(style.Dim(" "+i18n.T(
		"Clients already taken over stay checked-off; nothing about them changes if you skip them.", nil)) + "\n")
	fmt.Print(b.String())
}

// field 是表单里的一格。
type field struct {
	Label       string
	Placeholder string
	Value       string
	Secret      bool
	Note        string // 灰字说明（放在标签下面）
	Validate    func(string) string
	// Suggest 在用户按 Tab 时给一个建议值（provider 名那种可以由 base 推出来的）。
	Suggest func(current string) (string, string)
}

// formResult 是表单那一屏的结局。
type formResult struct {
	Values []string
	Cancel bool
}

// form 画一页多字段表单。Esc 回到上一格（第一格时取消），Tab 跳到下一格。
func form(s Screen, title, intro string, fields []field) formResult {
	cur := 0
	for {
		drawForm(title, intro, fields, cur)
		// 光标不显示：本实现不做格内光标移动（一行的输入很短），画出「当前值」
		// 就够了。这样 render 与 ReadLine 的状态只有一份。
		res := s.ReadLine(term.LineMode{
			Secret:      fields[cur].Secret,
			Placeholder: fields[cur].Placeholder,
			Validate: func(text string) string {
				if text == "" {
					return i18n.T("this one is required", nil)
				}
				if fields[cur].Validate != nil {
					return fields[cur].Validate(text)
				}
				return ""
			},
		}, func(text, errMsg string, _ int) {
			drawFormWith(title, intro, fields, cur, text, errMsg)
		})
		if res.Cancel {
			if cur == 0 {
				return formResult{Cancel: true}
			}
			cur--
			continue
		}
		if res.EOF {
			return formResult{Cancel: true}
		}
		text := res.Text
		if f := fields[cur]; f.Suggest != nil {
			if next, note := f.Suggest(text); next != "" {
				text = next
				f.Note = note
				fields[cur].Note = note
			}
		}
		fields[cur].Value = text
		if cur < len(fields)-1 {
			cur++
			continue
		}
		values := make([]string, len(fields))
		for i, f := range fields {
			values[i] = f.Value
		}
		return formResult{Values: values}
	}
}

func drawForm(title, intro string, fields []field, cur int) {
	drawFormWith(title, intro, fields, cur, "", "")
}

func drawFormWith(title, intro string, fields []field, cur int, typed, errMsg string) {
	var b strings.Builder
	b.WriteString(clear)
	b.WriteString(style.Bold(" newgate · "+title) + "\n")
	b.WriteString(style.Dim(" "+i18n.T("enter next · esc back · ctrl-c quit", nil)) + "\n\n")
	if intro != "" {
		b.WriteString(" " + intro + "\n\n")
	}
	for i, f := range fields {
		label := " " + f.Label
		if i == cur {
			label = style.Cyan("›") + " " + style.Bold(f.Label)
		} else {
			label = "  " + f.Label
		}
		b.WriteString(label + "\n")
		if f.Note != "" {
			b.WriteString(style.Dim("    "+f.Note) + "\n")
		}
		val := f.Value
		if i == cur {
			val = typed
		}
		switch {
		case i == cur:
			if val == "" && f.Placeholder != "" {
				b.WriteString("    " + rev + " " + style.Dim(f.Placeholder) + " " + reset + "\n")
			} else {
				shown := val
				if f.Secret {
					shown = strings.Repeat("•", len([]rune(val)))
				}
				b.WriteString("    " + rev + " " + shown + " " + reset + "\n")
			}
		case val != "":
			shown := val
			if f.Secret {
				shown = strings.Repeat("•", len([]rune(val)))
			}
			b.WriteString("    " + shown + "\n")
		}
		b.WriteString("\n")
	}
	if errMsg != "" {
		b.WriteString(" " + style.Mark(style.Bad) + " " + errMsg + "\n")
	}
	fmt.Print(b.String())
}

func padRight(s string, w int) string {
	if n := w - style.VisibleWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// printOutcome 画最后一屏：这次动了什么、下一步是什么。
func printOutcome(b *strings.Builder, prof ApplyResult, steps []Step, report StartReporter) {
	b.WriteString("\n")
	b.WriteString(style.Bold(" "+i18n.T("Done", nil)) + "\n\n")

	if len(prof.Notes) > 0 || len(prof.Warnings) > 0 {
		b.WriteString(" " + style.Bold(i18n.T("Configuration", nil)) + "\n")
		for _, n := range prof.Notes {
			b.WriteString("   " + style.Mark(style.OK) + " " + n + "\n")
		}
		for _, w := range prof.Warnings {
			b.WriteString("   " + style.Mark(style.Warn) + " " + w + "\n")
		}
		b.WriteString("\n")
	}

	if len(steps) > 0 {
		b.WriteString(" " + style.Bold(i18n.T("Clients", nil)) + "\n")
		for _, s := range steps {
			switch {
			case s.Err != nil:
				b.WriteString("   " + style.Mark(style.Bad) + " " + s.Agent + ": " + s.Err.Error() + "\n")
			case s.Takeover.Err != nil:
				b.WriteString("   " + style.Mark(style.Bad) + " " + s.Agent + ": " +
					i18n.T("installed, but taking it over failed: {err}",
						i18n.A{"err": s.Takeover.Err.Error()}) + "\n")
			default:
				what := i18n.T("already installed", nil)
				if s.DidInstall {
					what = i18n.T("installed", nil)
				}
				line := s.Agent + ": " + what
				switch {
				case s.TakenOver && len(s.Takeover.Lines) > 0:
					line += i18n.T(", taken over ({first})", i18n.A{"first": s.Takeover.Lines[0]})
				case !s.TakenOver:
					line += i18n.T(", not taken over yet", nil)
				}
				b.WriteString("   " + style.Mark(style.OK) + " " + line + "\n")
			}
			for _, w := range s.Takeover.Warn {
				b.WriteString("     " + style.Dim(i18n.T("note: {warn}", i18n.A{"warn": w})) + "\n")
			}
		}
		b.WriteString("\n")
	}

	if report.ProxyLine != "" {
		b.WriteString(" " + style.Mark(style.OK) + " " + report.ProxyLine + "\n\n")
	}
	for _, w := range report.Warnings {
		b.WriteString(" " + style.Mark(style.Warn) + " " + w + "\n")
	}
	if len(report.Warnings) > 0 {
		b.WriteString("\n")
	}

	// 收尾那句话是这整个向导的**目的**：让用户知道现在可以起 AI，然后交给 AI。
	b.WriteString(" " + style.Bold(i18n.T("Next", nil)) + "\n")
	for _, c := range report.Next {
		b.WriteString("   " + c + "\n")
	}
	b.WriteString("\n")
	b.WriteString(style.Dim(" "+i18n.T(
		"Everything else — more providers, per-client profiles, per-tier models — can be changed later: `newgate tui`, or just ask the agent to do it.",
		nil)) + "\n")
}

// StartReporter 是 main 收来的一份「起代理那一步」的结局（给最后一屏用）。
type StartReporter struct {
	ProxyLine string
	Warnings  []string
	Next      []string
}
