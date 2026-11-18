package breaker

// This file is the face the health table contributes to the web UI: **a read-only table**.
//
// Why breaker reports it rather than the UI reading `/__newgate/status` itself: every
// column of this table is a field this module writes (the state machine's three states,
// which ledger it is on, the cooldown expiry, the probe grade, the cumulative spared
// count), and **how they are turned into plain words** is this module's semantics too. If
// the UI read the snapshot itself, it would have to know machine markers like `half-open`
// / `availability` -- those are tests, not copy (same reason as modules/gateway/view.go).
//
// This is the same source as the CLI side: every cell of text in `newgate breaker` is produced
// by the `*Text` functions below (that side only wraps the result in color). So "what you see
// in the terminal" and "what you see on the web page" cannot each say their own thing -- which
// is exactly why text and coloring are split into two layers (see the comment on stateText).

import (
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/lib/durarg"
	"github.com/rzbdz/newgate-ext/lib/view"
	gatewayapi "github.com/rzbdz/newgate-ext/modules/gateway"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// healthConcepts is what the health table looks like right now.
//
// It is called when **someone comes to look at the UI**, so every
// cell is "now": how much cooldown is left, when the probe was fired,
// are all computed at call time (see the package comment of lib/view).
//
// It reports **all** bindings, not just the troubled ones: that CLI command (`newgate
// breaker`) deliberately reports only the troubled ones, because lines scrolling by in
// a terminal make the healthy ones noise; a table that can sit on screen is different
// -- "which are good" and "which are broken" are two halves of the same question, and
// good is relative (how laggy a usable binding is can only be told by comparison).
func healthConcepts(table Breaker, g gatewayapi.Gateway) ([]view.Concept, error) {
	rows := table.Snapshot()
	return []view.Concept{{
		ID:    "breaker.health",
		Kind:  view.KindTable,
		Title: i18n.T("Binding health", nil),
		Data:  healthTable(rows, g),
		// Live: this table's content is **entirely decided by traffic** (who is failing, how long until
		// retry), and reading it is just an in-memory snapshot. A table frozen at the moment the page was
		// opened describes something that may be long past -- `untilText`'s relative times especially.
		Live: true,
	}}, nil
}

// healthTable lays the snapshot out as a table. The column choices follow
// `newgate breaker` (the same fields, the same wording), minus two columns:
//
//   - **Reason** (the delisting reason) is the upstream's/gateway's raw text, possibly long
//     and quoted, and stuffing it into a cell deforms the whole row. It belongs to "click
//     a row for details", not an overview -- it waits until the UI has a details panel.
//   - **Checked** (probe time) and "Waiting" ask two different things in one table,
//     and "how long ago" relative times go stale on a web page (the page stays open and
//     the number goes wrong). Latency and grade are absolute values, so they stayed.
func healthTable(rows []Status, g gatewayapi.Gateway) view.Table {
	// Rows is explicitly initialized to an empty slice, not nil: a nil slice
	// serializes to `null`, and this cell's contract is "a list". The frontend's
	// `?? []` can handle null, but the next consumer may not be that careful --
	// an empty table should be `[]` in the protocol, not "this field is absent".
	t := view.Table{Rows: []view.Row{}, Columns: []view.Column{
		{ID: "binding", Label: i18n.T("binding", nil)},
		{ID: "state", Label: i18n.T("State", nil)},
		{ID: "rule", Label: i18n.T("Ledger", nil)},
		{ID: "fails", Label: i18n.T("Failures", nil), Align: "right"},
		{ID: "until", Label: i18n.T("Waiting", nil)},
		{ID: "probe", Label: i18n.T("Last probe", nil)},
		{ID: "latency", Label: i18n.T("Latency", nil), Align: "right"},
	}}
	for _, b := range rows {
		state, stateTone := stateText(b)
		until, untilTone := untilText(b)
		t.Rows = append(t.Rows, view.Row{
			// A row's identity is the binding key (`provider/model`): it is both
			// the content of this table's first column and the target a probe hits.
			ID: BindingKey(b.Provider, b.Model),
			Cells: map[string]view.Cell{
				"binding": {Text: BindingKey(b.Provider, b.Model)},
				"state":   {Text: state, Tone: stateTone},
				"rule":    {Text: ruleText(b)},
				"fails":   failsCell(b),
				"until":   {Text: until, Tone: untilTone},
				"probe":   {Text: probeText(b)},
				"latency": latencyCell(b),
			},
			Actions: probeActions(g, b),
		})
	}
	return t
}

// probeActions are the buttons on this row: **really fire one minimal request at this binding**.
//
// # Why it is worth having
//
// Every column of this table was accumulated from **passive traffic**: who is failing,
// how much latency, when the cooldown ends. And the question "is it working right now" passive
// traffic cannot answer -- a delisted binding has precisely no traffic, so it keeps showing
// on the table that failure from minutes ago, until the cooldown expires and new evidence
// appears. The answer in the terminal is `newgate probe`, and it hits **all** candidates
// (several extra requests per round). This button shrinks that action to one row, and gives
// moments like "I just changed the key / just changed the base" a place to verify immediately.
//
// # Why it does not compute a conclusion itself
//
// The probe conclusion is fed into the policy layer **along the same path as the command
// line** (see gateway.ProbeBinding), so delisting, latency, and cooldown are updated
// by the same state machine -- the UI does not compute a "looks right" result and
// paste it on the table. The table is Live, so after a refresh those columns are new.
func probeActions(g gatewayapi.Gateway, b Status) []view.Action {
	if g == nil {
		// With no gateway installed (the removal matrix really does remove it), **do not mount
		// the button**: a button that necessarily fails when pressed is worse than no button.
		return nil
	}
	provider, model := b.Provider, b.Model
	return []view.Action{{
		ID: "probe",
		// An icon + one word: this column is narrow, and an icon
		// is the fastest way to recognize "fire a request and see".
		Label: func() string { return "⚡ " + i18n.T("Test", nil) },
		Run: func() (string, error) {
			out, err := g.ProbeBinding(provider, model)
			if err != nil {
				return "", err
			}
			if !out.OK {
				// **Never silent**: if the probe cannot get through, it cannot get through,
				// and the upstream's sentence is carried out verbatim. This cell's whole
				// reason to exist is "is it working right now", and "nothing happened" is
				// its worst answer -- the user would think the button did not take effect.
				msg := out.Err
				if msg == "" {
					msg = i18n.T("status {status}", i18n.A{"status": out.Status})
				}
				return "", i18n.E("{target} is not answering: {err}",
					i18n.A{"target": provider + "/" + model, "err": msg})
			}
			// It got through, but the policy layer may have something to say. There is **only one**
			// case where it does: this request returned 200 but was slower than the threshold,
			// so the breaker was opened on the spot (see the four tiers of RecordProbe). That is
			// not good news, so it takes the error path up onto the banner -- "got through" and "I
			// delisted it" both holding at once, and reporting only the first half would be a lie.
			//
			// After a refresh those columns on the table say the same conclusion, but the two say
			// different things: the table says "what state now", this sentence says "what just happened".
			if out.Note != "" {
				return "", i18n.E("{note}", i18n.A{"note": out.Note})
			}
			return "", nil
		},
	}}
}

// failsCell puts "consecutive failures" and the two
// counts that are counted but not failures into one cell.
//
// Squeezing them into one cell is **deliberate**: they answer the same question (how
// unhealthy this binding has been lately), and splitting into three columns would leave
// most cells empty (shape errors and spared exist only for a minority of bindings).
//
// Each segment is a **complete phrase** joined by separators -- not the "base +
// suffix" style. Splicing translated fragments is an old i18n pit: "2 + 3 shape"
// reads fine in English, becomes gibberish in a language with a different word
// order, and the translator only sees a subjectless fragment with no way in.
func failsCell(b Status) view.Cell {
	parts := []string{i18n.N("{n} failure", "{n} failures", b.Fails, nil)}
	if b.ShapeSkips > 0 {
		parts = append(parts, i18n.N("{n} shape error", "{n} shape errors", b.ShapeSkips, nil))
	}
	if b.Spared > 0 {
		parts = append(parts, i18n.N("{n} spared", "{n} spared", b.Spared, nil))
	}
	tone := ""
	if b.Fails > 0 || b.ShapeSkips > 0 {
		tone = view.ToneWarn
	}
	return view.Cell{Text: strings.Join(parts, " · "), Tone: tone}
}

// latencyCell is this row's latency cell: number + color.
//
// Coloring starts 2026-09-28 (before that this was the **only cell with no tone**,
// and it happens to be the one number people looking at the table will compare).
// Test and color are both given by one place, LatencySample -- the first screen's
// routing overview uses it too (see the comment in modules/breaker/latency.go).
func latencyCell(b Status) view.Cell {
	ms, tone, ok := LatencySample(b)
	if !ok {
		return view.Cell{Text: "-"}
	}
	return view.Cell{Text: i18n.T("{ms}ms", i18n.A{"ms": ms}), Tone: tone}
}

// ---------- Text and coloring separated ----------
//
// The `*Text` functions below are the **only** source of wording, returning "the words for
// humans" + "what color it should be". The CLI (command.go) and the web (the table above) each
// consume it: the terminal wraps the color into ANSI, the web hands it to CSS as semantics.
//
// Why not write a copy on each side: the two must say the same thing, and the place
// they most easily diverge is precisely the **test** (what counts as "delisted", how
// ledger names are translated). The symptom after divergence is "the terminal says
// healthy, the web says half-open" -- the same data in the same process, two UIs each
// saying their own thing, and readers only end up suspecting the data. Color, on the
// other hand, must be layered: ANSI escapes are gibberish on a web page and CSS class
// names are gibberish in a terminal, so sharing one string across both cannot work.

// stateText is the plain words for the state machine's three states.
// The returned tone takes view.Tone* (empty string = no color).
//
// The daemon may be old (during graceful handover CLI and daemon
// versions can differ); old snapshots have no state field, so infer
// from Open -- the old semantics had only "delisted" and "not delisted".
//
// The values of `b.State` (closed / open / half-open) are machine markers,
// used only for tests; what is translated is the human-facing side.
func stateText(b Status) (string, string) {
	switch b.State {
	case "half-open":
		if b.Trial {
			return i18n.T("half-open · trial in flight", nil), view.ToneWarn
		}
		return i18n.T("half-open · awaiting trial", nil), view.ToneWarn
	case "open":
		return i18n.T("tripped", nil), view.ToneBad
	case "closed":
		return i18n.T("healthy", nil), view.ToneOK
	}
	if b.Open {
		return i18n.T("tripped", nil), view.ToneBad
	}
	return i18n.T("healthy", nil), view.ToneOK
}

// ruleText turns the snapshot's ledger name (a machine marker) back into plain words.
// Unrecognized ones are shown as is: during graceful handover the old daemon gives **old
// values** (the display name of that time), and a strange value should not be swallowed.
func ruleText(b Status) string {
	if b.Rule == "" {
		return "-"
	}
	if name := bucketFromName(b.Rule).ruleName(); name != "" {
		return name
	}
	return b.Rule
}

// untilText states "how long until retry", and what the cooldown is for: after backoff
// the cooldown gets longer (60s -> 120s -> … -> 10 minutes), so the number the user
// sees not matching the base is normal, and the trial state is included for that reason.
//
// "How long" is a relative time. It is still computed at **call time** here: this table is
// generated fresh on every request (see healthConcepts), so the number is as old as the snapshot.
// And the snapshot itself does not sit still -- this card declares `Live` (see healthConcepts),
// the UI re-asks every few seconds, and numbers follow; so the backend **need not** give absolute
// times, and the frontend need not tick seconds itself. (Corrected 2026-09-20: this comment used
// to assume "the page does not move", and that premise stopped holding after `Concept.Live`.)
func untilText(b Status) (string, string) {
	if b.Trial {
		return i18n.T("trial in flight", nil), view.ToneWarn
	}
	if b.OpenUntil.IsZero() {
		return "-", ""
	}
	if d := time.Until(b.OpenUntil); d > 0 {
		return durarg.Format(int(d.Seconds())), ""
	}
	return i18n.T("expired", nil), view.ToneOK
}

// probeText is the grade of the last active probe.
//
// ProbeGrade is the tier name in the wire contract (fluent / usable
// / laggy / unavailable), a machine marker, displayed as is -- it is
// also the vocabulary `newgate probe` and tier chain ordering use.
func probeText(b Status) string {
	if b.Checked.IsZero() || b.Grade == "" {
		return i18n.T("never probed", nil)
	}
	return string(b.Grade)
}
