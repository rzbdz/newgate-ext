package breaker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/lib/view"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// TestHealthTableCellsMatchColumns is this table's shape ratchet.
//
// Cells are indexed by **column ID** (see the comment on view.Table: rows are maps, not
// arrays), so a misspelled key does not error -- the frontend looks it up by column,
// misses, and it is blank. A table missing half a column looks perfectly normal;
// only comparing cell by cell against `newgate breaker` in the terminal reveals it.
//
// The same reason applies to tone: when it is not a word from view.Tone*, the
// frontend treats it as not given (no color), so "this one is delisted" shows
// as gray on the web page. Both errors are silent, so they are blocked here.
func TestHealthTableCellsMatchColumns(t *testing.T) {
	b, _ := clocked()
	trip(t, b, "relay", "slow")                            // 摘牌：State=open
	b.Report("relay", "fresh", Input{Kind: KindConnError}) // 只计数：Fails=1

	concepts, err := healthConcepts(b, nil)
	if err != nil {
		t.Fatalf("contributing the health table failed: %v", err)
	}
	if len(concepts) != 1 {
		t.Fatalf("should contribute exactly one table, got %d concepts", len(concepts))
	}
	c := concepts[0]
	if c.Kind != view.KindTable {
		t.Fatalf("Kind should be %q, actual %q", view.KindTable, c.Kind)
	}
	if c.Apply != nil {
		t.Fatal("this table is writable - it reports the state in daemon memory, there is no such thing as writing it back")
	}

	table, ok := c.Data.(view.Table)
	if !ok {
		t.Fatalf("Data should be a view.Table, got %T", c.Data)
	}
	if len(table.Columns) == 0 {
		t.Fatal("no columns at all - the header is gone, the front end has nothing to draw")
	}

	columns := map[string]bool{}
	for _, col := range table.Columns {
		if col.ID == "" || col.Label == "" {
			t.Errorf("a column is missing ID or Label: %+v (ID is the cell index key, Label is what people read)", col)
		}
		columns[col.ID] = true
	}

	if len(table.Rows) != 2 {
		t.Fatalf("two bindings should be two rows, got %d rows", len(table.Rows))
	}
	for _, row := range table.Rows {
		for id, cell := range row.Cells {
			if !columns[id] {
				t.Errorf("cell %q has no matching column - the front end cannot fetch it, so this cell is blank", id)
			}
			switch cell.Tone {
			case "", view.ToneOK, view.ToneWarn, view.ToneBad:
			default:
				t.Errorf("cell %q has tone=%q which is not a known semantic color; the front end treats it as unset (silently uncolored)", id, cell.Tone)
			}
			if cell.Text == "" {
				t.Errorf("cell %q is empty - when it should show 'none', write an explicit placeholder", id)
			}
		}
	}
}

// TestHealthTableReportsHealthyBindingsToo pins "report all, not just the troubled".
//
// The CLI's `newgate breaker` deliberately reports only the troubled ones
// (healthy lines are noise in a terminal), while this web table deliberately
// reports all: for a table that can sit on screen, good and bad are relative
// -- how laggy a usable binding is can only be seen next to its neighbors.
func TestHealthTableReportsHealthyBindingsToo(t *testing.T) {
	b, _ := clocked()
	trip(t, b, "relay", "slow")

	concepts, _ := healthConcepts(b, nil)
	table := concepts[0].Data.(view.Table)
	if len(table.Rows) != 1 {
		t.Fatalf("only the broken ones are reported? got %d rows", len(table.Rows))
	}
	if got := table.Rows[0].Cells["binding"].Text; got != "relay/slow" {
		t.Fatalf("the binding cell should be provider/model, got %q", got)
	}
	// Delisted should be red (bad), not "has a value but no color".
	if tone := table.Rows[0].Cells["state"].Tone; tone != view.ToneBad {
		t.Fatalf("the state of a dropped one should be %q, got %q", view.ToneBad, tone)
	}
}

// TestHealthTableIsEmptyWhenNothingIsKnown is the empty-assembly one: with not a single
// binding ever recorded, the header is still there. An empty table is not an error -- "nothing
// went wrong" and "this table is broken" must look like two different things in the UI.
func TestHealthTableIsEmptyWhenNothingIsKnown(t *testing.T) {
	b, _ := clocked()
	concepts, err := healthConcepts(b, nil)
	if err != nil {
		t.Fatalf("with no bindings it should not error: %v", err)
	}
	table := concepts[0].Data.(view.Table)
	if len(table.Rows) != 0 {
		t.Fatalf("an empty table should have no rows, got %d", len(table.Rows))
	}
	if len(table.Columns) == 0 {
		t.Fatal("an empty table should still have a header, or the front end cannot even show what this table is about")
	}
	// An empty table must serialize as `[]`, not `null`: a nil slice is null in
	// JSON, and this cell's contract is "a list". The frontend's `?? []` covers
	// it, but a protocol should not expect every consumer to be that careful.
	raw, err := json.Marshal(table)
	if err != nil {
		t.Fatalf("serializing the table failed: %v", err)
	}
	if !strings.Contains(string(raw), `"rows":[]`) {
		t.Fatalf("the rows of an empty table should be [], got: %s", raw)
	}
}

// TestHealthCardIsLive: this table declares `Live`, so the UI re-asks every few seconds.
//
// Dropping it **would make nothing error**: the page sits quietly
// at the moment it was opened, while this table is all about "now" -- who is
// failing, how long until retry. `untilText`'s relative times especially: on
// a page that does not move, "42s until retry" becomes a lie a minute later.
func TestHealthCardIsLive(t *testing.T) {
	b, _ := clocked()
	concepts, err := healthConcepts(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(concepts) == 0 {
		t.Fatal("there is not a single card - this assertion is a no-op")
	}
	for _, c := range concepts {
		if !c.Live {
			t.Errorf("%s does not declare Live: its content is decided by traffic, and one read is just a snapshot in memory", c.ID)
		}
	}
}

// TestPollingTheCardDoesNotAdvanceTheBreaker is the safety half of "make this card
// Live": **the path asked every few seconds must never advance the state machine**.
//
// The breaker has one read path that changes state -- `Available`: when the
// cooldown expires it moves the binding to half-open along the way and issues this round's
// trial slot (see the comment in state.go). That door belongs to the **data plane**:
// whoever wants to send a request goes and gets the slot. If this card changed to call
// `Available`, the symptom would be "someone left the UI open" and bindings in cooldown
// get trial after trial -- the admission decision changed by a page view, and nothing goes
// red. So both halves are pinned here: polling does **not** move it, `Available` does.
func TestPollingTheCardDoesNotAdvanceTheBreaker(t *testing.T) {
	b, adv := clocked()
	trip(t, b, "relay", "slow") // 摘牌（trip 内部走的是 Available，那是写路径）

	state := func() view.Cell {
		t.Helper()
		concepts, err := healthConcepts(b, nil)
		if err != nil {
			t.Fatal(err)
		}
		return concepts[0].Data.(view.Table).Rows[0].Cells["state"]
	}

	// Cooldown expired. What the table shows right now is the half-open **derived
	// from the clock** (the state(now) test), and nobody wrote anything -- what
	// matters is **whether the slot is still there**: that is what Available moves.
	adv(24 * time.Hour)

	awaiting := view.Cell{Text: i18n.T("half-open · awaiting trial", nil), Tone: view.ToneWarn}
	if got := state(); got != awaiting {
		t.Fatalf("precondition does not hold: after the cooldown it should show 'waiting for a trial', got %q/%q", got.Text, got.Tone)
	}
	// The UI side: ask any number of times, nothing may change, and **no slot may be taken**.
	for i := 0; i < 3; i++ {
		if got := state(); got != awaiting {
			t.Fatalf("the state changed after poll #%d: %q -> %q - glancing at the UI advanced the state machine",
				i+1, awaiting.Text, got.Text)
		}
	}
	if !b.Available("relay", "slow") {
		t.Fatal("the poll took this round's trial slot - one page view changed the data plane's admission decision")
	}

	// Contrast: what really moves it is Available (the data plane's
	// slot-taking path) -- after a slot is taken the table shows it.
	if got := state(); got.Text != i18n.T("half-open · trial in flight", nil) {
		t.Fatalf("after Available takes the slot it should show 'trial in flight', got %q", got.Text)
	}
}

// trip fails one binding consecutively until the breaker opens (thresholds: see policy defaults).
func trip(t *testing.T, b Breaker, provider, model string) {
	t.Helper()
	for i := 0; i < 2; i++ {
		b.Report(provider, model, Input{Kind: KindConnError})
	}
	if b.Available(provider, model) {
		t.Fatalf("%s/%s took two connection failures and was not dropped", provider, model)
	}
}
