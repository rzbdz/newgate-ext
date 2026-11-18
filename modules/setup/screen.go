package setup

// This file is the wizard's **screens**: pick clients, fill the form, see the outcome.
//
// It only draws on the terminal; every "change this machine" action lives in agents.go /
// profile.go, and keys and raw mode in lib/term. So a fake Terminal + buffer can drive
// this screen, and the e2e does not need a real terminal to verify behavior.
//
// # One rule across every screen: Esc = back a step, Ctrl-C = abandon
//
// The wizard writes disk (provider, profile, takeover), so every way back must either
// write nothing or clearly say what was written. Abandoning mid-way is spelled out in main.

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

// Screen is the wizard screen's interface (real one *term.Terminal, fake in tests).
type Screen interface {
	ReadEvent() term.Event
	ReadLine(term.LineMode, func(text, errMsg string, cursor int)) term.LineResult
}

// pickResult is the multi-pick screen's outcome.
type pickResult struct {
	Chosen  []string
	Cancel  bool
	Nothing bool // the user picked nothing and just pressed enter
}

// pickAgents draws the "which clients to install" screen.
//
// Default-checked are those **installed but never taken over**: what users want when
// typing `newgate config` (the installed tool, through newgate); re-checking costs nothing.
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

// field is one cell in the form.
type field struct {
	Label       string
	Placeholder string
	Value       string
	Secret      bool
	Note        string // dimmed caption (drawn under the label)
	Validate    func(string) string
	// Suggest offers a value when Tab is pressed (a provider name derived from base, say).
	Suggest func(current string) (string, string)
}

// formResult is the form screen's outcome.
type formResult struct {
	Values []string
	Cancel bool
}

// form draws a multi-field form page. Esc goes back a cell (cancels on the first), Tab moves on.
func form(s Screen, title, intro string, fields []field) formResult {
	cur := 0
	for {
		drawForm(title, intro, fields, cur)
		// No cursor shown: this implementation has no in-cell cursor movement (inputs are
		// short), drawing "the current value" suffices. That keeps one source for render and ReadLine.
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

// printOutcome draws the last screen: what this run changed, what comes next.
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

	// The closing line is the wizard's **purpose**: tell the user AI can start now, hand over.
	b.WriteString(" " + style.Bold(i18n.T("Next", nil)) + "\n")
	for _, c := range report.Next {
		b.WriteString("   " + c + "\n")
	}
	b.WriteString("\n")
	b.WriteString(style.Dim(" "+i18n.T(
		"Everything else — more providers, per-client profiles, per-tier models — can be changed later: `newgate tui`, or just ask the agent to do it.",
		nil)) + "\n")
}

// StartReporter is the outcome of "start the proxy" collected by main (for the last screen).
type StartReporter struct {
	ProxyLine string
	Warnings  []string
	Next      []string
}
