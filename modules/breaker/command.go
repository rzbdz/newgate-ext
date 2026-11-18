package breaker

// This file is `newgate breaker`: **which bindings are in trouble right
// now, and why**. Moved wholesale from modules/cli/breaker.go on 2026-09-18.
//
// Why moved: this table is this module's health ledger itself -- state, ledger, cooldown
// expiry, reason, whether a trial is in flight are all fields this module writes. While
// it stayed in the UI, the UI had to know every field of Status to draw this table.
//
// The command is registered into the UI by this module in Start: this module declares a
// **weak dependency** on ui (Optional(cli), see component.Optional), so it sorts after the
// UI -- by the time Start gets here the UI's ledger is ready. The UI **does not depend on
// this module** (it only reads control-plane documents), so this edge cannot form a cycle.

import (
	"fmt"
	"time"

	"github.com/rzbdz/newgate-ext/lib/style"
	"github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// The position of the observation section (the same as when it lived in the UI).
const rankBreaker = 40

type breakerCommand struct{}

var (
	_ cliapi.Command    = (*breakerCommand)(nil)
	_ cliapi.Documented = (*breakerCommand)(nil)
)

func (breakerCommand) Names() []string { return []string{"breaker", "breakers"} }

func (breakerCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionObserve, Rank: rankBreaker,
		Usage:   "breaker",
		Summary: i18n.T("which bindings are tripped, why, and for how long", nil)}
}

func (breakerCommand) Run(host cliapi.Host, _ []string) int { return runBreaker(host) }

// cmdBreaker answers one thing only: **which bindings are in trouble right now, and why**.
//
// Why a separate command rather than folding it into `metrics`: the "model health" section
// of `metrics` (printModelHealth) deliberately hides lagging/unavailable details and states
// only a count, for the reason written in that comment -- to keep a health check from degenerating
// into a wall of logs. But when troubleshooting something like "why was deepseek delisted again",
// those hidden details are exactly what is needed: when it was delisted, how long until retry, how
// many consecutive failures, which ledger it is on, and the grade and latency from the last probe.
//
// After the half-open refactor of 2026-09-17 this command has two sections:
//
//   - delisted: state, ledger, cooldown expiry, reason, whether a trial is in flight;
//   - counted but not delisted: request-shape errors (never delisted) and failures below the
//     threshold. The second section is the visibility added this round -- before, "failed once,
//     breaker not open yet" did not exist in the snapshot at all, and shape errors only sat in
//     the metrics counters, so a user seeing "it works but got delisted" could find no cause.
func runBreaker(host cliapi.Host) int {
	info, ps := controlplane.State()
	if info == nil {
		// Die's prose goes through i18n at the **call site**: style only handles layout
		// (the "newgate: " prefix is the program name, a machine marker, not translated).
		return host.Die(69, i18n.T("the proxy is not running (newgate start) — "+
			"the breaker table lives in the daemon's memory", nil))
	}
	if ps == nil {
		return host.Die(69, i18n.T("cannot reach the proxy at 127.0.0.1:{port} (newgate doctor)",
			i18n.A{"port": info.Port}))
	}

	var open, counted []Status
	for _, b := range ps.Breakers {
		switch {
		case b.Open:
			open = append(open, b)
		case b.Fails > 0 || b.ShapeSkips > 0 || b.Spared > 0:
			counted = append(counted, b)
		}
	}

	if len(open) == 0 && len(counted) == 0 {
		fmt.Println(style.Title("newgate breaker", fmt.Sprintf("pid %d", info.PID)))
		fmt.Println(style.Hint(i18n.T("no bindings are in trouble", nil)))
		return 0
	}

	fmt.Println(style.Title("newgate breaker",
		i18n.T("pid {pid} · {tripped} tripped · {counted} counted only", i18n.A{
			"pid": info.PID, "tripped": len(open), "counted": len(counted)})))

	if len(open) > 0 {
		t := style.NewTable("binding", i18n.T("State", nil), i18n.T("Ledger", nil),
			i18n.T("Tripped ago", nil), i18n.T("Waiting", nil),
			i18n.T("Failures", nil), i18n.T("Reason", nil))
		for _, b := range open {
			t.Row(b.Provider+"/"+b.Model, stateLabel(b), ruleLabel(b),
				agoLabel(b.OpenedAt), untilLabel(b),
				fmt.Sprintf("%d", b.Fails), b.Reason)
		}
		fmt.Print(t.String())
		fmt.Println()
		fmt.Print(probeTable(open))
	}

	if len(counted) > 0 {
		fmt.Println()
		fmt.Println(style.Section(i18n.T("counted only, not tripped", nil)) +
			style.Dim(i18n.T("   these bindings are still in the chain", nil)))
		t := style.NewTable("binding", i18n.T("Ledger", nil), i18n.T("Failures", nil),
			i18n.T("Shape errors", nil), i18n.T("Spared", nil), i18n.T("Last probe", nil))
		for _, b := range counted {
			t.Row(b.Provider+"/"+b.Model, ruleLabel(b),
				fmt.Sprintf("%d", b.Fails), shapeLabel(b), sparedLabel(b), probeLabel(b))
		}
		fmt.Print(t.String())
	}

	fmt.Println()
	if spared := totalSpared(ps.Breakers); spared > 0 {
		// "Spared" is the visibility added this round: the diagnostic probe before
		// tripping pulled the binding back from the edge of delisting. The number does
		// not reset (cumulative across restarts), so it answers "how many times this
		// chain has been misjudged in total", not "how many are still uncleared now".
		fmt.Println(style.Hint(i18n.T(
			"diagnostic probes spared {n} bindings in total: real traffic reached the threshold, "+
				"but an active probe proved it still works, so the counter was cleared and it was not tripped",
			i18n.A{"n": spared})))
	}
	// After half-open, unsealing is no longer only through probe: real traffic is
	// automatically let through once after the cooldown expires as a trial, and if it succeeds
	// the breaker closes. probe is still the way to change the conclusion **immediately**.
	fmt.Println(style.Hint(i18n.T("once the cooldown expires, one real request is let through "+
		"as a trial: success closes the breaker, failure re-trips it and doubles the cooldown "+
		"(capped at 10 minutes)", nil)))
	fmt.Println(style.Hint(i18n.T("  newgate probe        # change the verdict right now with "+
		"an active probe, without waiting for the cooldown", nil)))
	fmt.Println(style.Hint(i18n.T("  newgate tier <tier>   # see where this binding ranks "+
		"in the chain, and whether it is skipped", nil)))
	return 0
}

// probeTable gets its own section: the probe conclusion is **another
// source** (active probing vs real traffic), and mixing it into
// delisting reasons would make people think they are the same thing.
func probeTable(rows []Status) string {
	t := style.NewTable("binding", i18n.T("Last probe", nil),
		i18n.T("Latency", nil), i18n.T("Checked", nil))
	for _, b := range rows {
		t.Row(b.Provider+"/"+b.Model, probeLabel(b), latencyLabel(b), checkedLabel(b))
	}
	return t.String()
}

func probeLabel(b Status) string {
	if b.Checked.IsZero() || b.Grade == "" {
		return style.Dim(i18n.T("never probed", nil))
	}
	// The text and the test share one source (see "text and coloring
	// separated" in view.go): here only the coloring is decided.
	return probeText(b)
}

func latencyLabel(b Status) string {
	if b.Checked.IsZero() {
		return style.Dim("-")
	}
	return fmt.Sprintf("%dms", b.Latency)
}

func checkedLabel(b Status) string {
	if b.Checked.IsZero() {
		return style.Dim("-")
	}
	return i18n.T("{ago} ago",
		i18n.A{"ago": time.Since(b.Checked).Round(time.Second)})
}

// stateLabel turns the state machine's three states into human labels. The daemon may be
// old (during graceful handover the CLI and daemon versions can differ); old snapshots have no
// state field, so infer from Open -- the old semantics had only "delisted" and "not delisted".
//
// The values of `b.State` (closed / open / half-open) are machine markers,
// used only for tests; what is translated is the human-facing side.
func stateLabel(b Status) string {
	// Both the text and the test are in view.go (shared
	// by the two UIs); here only the coloring is handled.
	text, tone := stateText(b)
	return paint(text, tone)
}

// paint applies semantic color as ANSI. **Color appears only in the render
// layer**: for the same stateText result, the web side applies a CSS class name
// and the terminal side an escape sequence (see the separation note in view.go).
func paint(text, tone string) string {
	switch tone {
	case view.ToneOK:
		return style.Green(text)
	case view.ToneWarn:
		return style.Yellow(text)
	case view.ToneBad:
		return style.Red(text)
	}
	return text
}

// ruleLabel turns the snapshot's ledger name (a machine marker) back into plain words.
// Unrecognized ones are shown as is: during graceful handover the old daemon gives **old
// values** (the display name of that time), and a strange value should not be swallowed.
func ruleLabel(b Status) string {
	if b.Rule == "" {
		return style.Dim("-")
	}
	return ruleText(b)
}

func shapeLabel(b Status) string {
	if b.ShapeSkips == 0 {
		return style.Dim("-")
	}
	return i18n.N("{n} time", "{n} times", b.ShapeSkips, nil)
}

// sparedLabel shows the cumulative count of "almost delisted, saved by the diagnostic probe".
func sparedLabel(b Status) string {
	if b.Spared == 0 {
		return style.Dim("-")
	}
	return style.Green(i18n.N("{n} time", "{n} times", b.Spared, nil))
}

// totalSpared totals the spared counts across the table, for the footer sentence.
func totalSpared(rows []Status) int {
	n := 0
	for _, b := range rows {
		n += b.Spared
	}
	return n
}

func agoLabel(at time.Time) string {
	if at.IsZero() {
		return style.Dim("-")
	}
	return i18n.T("{ago} ago", i18n.A{"ago": time.Since(at).Round(time.Second)})
}

// untilLabel states "how long until retry", and what the cooldown is for: after backoff the
// cooldown gets longer and longer (60s -> 120s -> … -> 10 minutes), so the number the user
// sees not matching the base is normal, and the trial state is included for that reason.
func untilLabel(b Status) string {
	text, tone := untilText(b)
	if text == "-" {
		return style.Dim(text)
	}
	return paint(text, tone)
}
