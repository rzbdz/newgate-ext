package manifest

import (
	"strings"
	"testing"

	modules "github.com/rzbdz/newgate/component"
)

// What this guards is that **the frozen table is a start order at all**.
//
// The order is computed at build time (tools/distgen runs tools/distgen/orderprobe, which calls
// component.Ordered) and then committed, so at runtime nobody re-derives it: `Resolve` only
// verifies that every provider sits before its consumer. "Is this table right" therefore has to
// be watched here, or it is frozen data nobody checked.
//
// Two properties, and missing either is not enough:
//
//  1. **It works**: every spec's table passes assembly, and the order that comes out is the
//     table's order (Resolve does not rearrange anything).
//  2. **It is not decoration**: the table must be what component.Ordered would produce from the
//     same components, otherwise a hand-edit or a stale generator slips through -- assembly
//     would still accept it (a valid order need not be the canonical one), and nothing else
//     would notice.
func TestGeneratedTablesAreStartOrders(t *testing.T) {
	specs := Specs()
	if len(specs) == 0 {
		t.Fatal("there are no specs at all")
	}
	for _, name := range SpecNames() {
		table := specs[name]
		if len(table) == 0 {
			t.Errorf("%s has an empty component table -- was the generated manifest not written? "+
				"(run `go run ./tools/distgen`)", name)
			continue
		}

		// (1) Assemble by the table: it must assemble, and the order must be the table's.
		plan, err := modules.Resolve(modules.Static(table))
		if err != nil {
			t.Errorf("%s does not assemble from its table: %v", name, err)
			continue
		}
		if got := strings.Join(plan.Names(), ","); got != strings.Join(namesOf(table), ",") {
			t.Errorf("%s's assembly order differs from the table:\n  table:    %s\n  assembly: %s",
				name, strings.Join(namesOf(table), ","), got)
		}

		// (2) The table must be the **canonical** order, i.e. what the only ordering
		// implementation says. This is the one that catches a stale or hand-edited table.
		ordered, err := modules.Ordered(table)
		if err != nil {
			t.Errorf("%s cannot be ordered at all: %v", name, err)
			continue
		}
		if got := strings.Join(namesOf(ordered), ","); got != strings.Join(namesOf(table), ",") {
			t.Errorf("%s's table differs from the computed order (a stale generator or a hand edit? "+
				"run `go run ./tools/distgen`):\n  table:    %s\n  computed: %s",
				name, strings.Join(namesOf(table), ","), got)
		}
	}
}

func namesOf(components []modules.Component) []string {
	out := make([]string, 0, len(components))
	for _, c := range components {
		out = append(out, c.Name)
	}
	return out
}
