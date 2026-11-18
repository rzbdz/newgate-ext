package sshtunnel

import (
	"encoding/json"
	"testing"

	viewapi "github.com/rzbdz/newgate-ext/lib/view"
)

// This family locks down **whether this card works in the UI**. All are "over the wire"
// criteria: a value right on the Go side but a lost cell in the UI is a different product,
// and compile, tests and review all stay green.

// TestABlankTargetHasEveryField: a config with **not a single** entry must be fillable via "add".
//
// This is the module's normal state (a machine never configured for a remote), and it was
// broken: the field shape grew on each record, so with no records the UI had nothing to copy
// and "add" gave an empty card **with no inputs at all** - only an empty set shows it.
func TestABlankTargetHasEveryField(t *testing.T) {
	c := targetsConcept(Loaded{})
	recs, ok := c.Data.(viewapi.Records)
	if !ok {
		t.Fatalf("Data is not Records: %T", c.Data)
	}
	if !recs.CanAdd {
		t.Fatal("this card should support adding")
	}
	if len(recs.Blank) == 0 {
		t.Fatal("Blank on an empty set is empty - clicking 'new' would give a card with no input boxes")
	}

	// The field table must be **verbatim identical** to a real record's. A missing cell just
	// looks like "that field is not configurable", when it was simply not carried over.
	full := targetFields(Target{}.normalize())
	if len(recs.Blank) != len(full) {
		t.Fatalf("Blank has %d cells, a real record has %d", len(recs.Blank), len(full))
	}
	for i := range full {
		if recs.Blank[i].ID != full[i].ID {
			t.Errorf("cell %d is %q, want %q", i, recs.Blank[i].ID, full[i].ID)
		}
	}

	// The dropdown's options must come along: a select without Options is an **empty**
	// dropdown, and the user just concludes the UI is broken.
	for _, f := range recs.Blank {
		if f.Kind != viewapi.FieldSelect {
			continue
		}
		if len(f.Options) == 0 {
			t.Errorf("dropdown %q has no candidates", f.ID)
		}
	}
}

// TestEveryBlankFieldSurvivesJSON: the one above compares Go values; this one compares
// **after an actual JSON round trip** - the UI gets JSON, and a lost cell only shows here.
func TestEveryBlankFieldSurvivesJSON(t *testing.T) {
	recs := targetsConcept(Loaded{}).Data.(viewapi.Records)
	raw, err := json.Marshal(recs)
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Blank []struct {
			ID      string   `json:"id"`
			Kind    string   `json:"kind"`
			Options []string `json:"options"`
		} `json:"blank"`
		CanAdd bool `json:"can_add"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("cannot decode: %v", err)
	}
	if !back.CanAdd || len(back.Blank) != len(recs.Blank) {
		t.Fatalf("after JSON can_add=%v, blank=%d cells (Go has %d)",
			back.CanAdd, len(back.Blank), len(recs.Blank))
	}
	seen := map[string]bool{}
	for _, f := range back.Blank {
		seen[f.ID] = true
	}
	for _, want := range []string{"id", "host", "user", "port", "identity",
		"remote_host", "remote_port", "local_port", "persistent", "idle_seconds", "host_key"} {
		if !seen[want] {
			t.Errorf("the UI is missing the cell %q", want)
		}
	}
}

// TestTheAddressesAreClickable: both paths' addresses should be **clickable links**, and
// the one whose port is not up should not.
//
// Giving an address that fails when clicked is worse than no link: users think they misconfigured.
func TestTheAddressesAreClickable(t *testing.T) {
	loaded := Loaded{Targets: []Target{target("ds")}}
	m := newManager(newPool(newFakeDialer("127.0.0.1:1")), func() Loaded { return loaded })
	defer m.shutdown()

	c := statusConcept(m)
	tbl, ok := c.Data.(viewapi.Table)
	if !ok {
		t.Fatalf("Data is not a Table: %T", c.Data)
	}
	if len(tbl.Rows) != 1 {
		t.Fatalf("there should be one row, got %d", len(tbl.Rows))
	}
	route := tbl.Rows[0].Cells["route"]
	if route.Href != "/ui/remote/ds/" {
		t.Errorf("the route cell is not a link: %+v", route)
	}
	if route.Text != route.Href {
		t.Errorf("the link text does not match the address: %+v", route)
	}
	// This target has no local_port, so that cell is "none" - **there should be no link**.
	if port := tbl.Rows[0].Cells["port"]; port.Href != "" {
		t.Errorf("there is a link but no forwarded port: %+v", port)
	}
}

func TestAForwardingPortLinksToItsOwnRoot(t *testing.T) {
	// The forward port's address is its own root (`http://127.0.0.1:9401/ui/`), not the route
	// on the shared port - different addresses; the wrong click looks like the faithful path broke.
	got := forwardHref(TargetStatus{
		Target:  target("ds"),
		Forward: forwardStatus{Addr: "127.0.0.1:9401"},
	})
	if got != "http://127.0.0.1:9401/ui/" {
		t.Errorf("forwardHref = %q", got)
	}
	// Port not up: no link (clicking fails, which looks more like a misconfiguration than no link).
	if got := forwardHref(TargetStatus{ForwardErr: "address already in use"}); got != "" {
		t.Errorf("a link was given although the port is not up: %q", got)
	}
}
