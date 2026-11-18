// Command orderprobe computes each spec's **start order** with the framework's own algorithm.
//
// It is distgen's second pass: the first compiles the specs' component tables (in spec order)
// into manifest/modules_gen.go, then this program runs -- in a **new process**, so it reads the
// manifest just written -- computes the order, and prints `spec -> component names`.
//
// # Why a new process instead of distgen computing it
//
// The manifest in the distgen process is **compiled in**: the previous run's result. With a
// just-edited spec (module added, removed), an order derived from the old manifest is silently
// wrong, and "wrong order" is miles from "stale manifest". A new process has no such problem:
// it necessarily reads the one on disk.
//
// # Why it calls component.Ordered and not distgen
//
// There is exactly one implementation of "compute a valid start order", and it lives in the
// framework (`component.Ordered`) -- the same function testkit and any third-party composition
// root use. A second implementation here would be the beginning of "the generator's order" and
// "the framework's order" disagreeing, which is precisely the split this pass exists to avoid.
//
// It also self-checks: every spec must assemble, or generation fails -- rather than the failure
// surfacing on somebody's first command. A dependency cycle, in particular, can only be
// reported here (a declared order cannot express one).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	modules "github.com/rzbdz/newgate/component"

	"github.com/rzbdz/newgate-ext/manifest"
)

func main() {
	specs := manifest.Specs()
	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	sort.Strings(names)

	// Indices into the spec's table, **not component names**: distgen has to translate the
	// result back into import aliases, and the directory name need not equal the component name
	// (`claudecode_deepseek` is `claudecode-deepseek`). An index needs no such mapping and
	// cannot be misspelled.
	out := map[string][]int{}
	for _, name := range names {
		declared := specs[name]
		index := make(map[string]int, len(declared))
		for i, c := range declared {
			index[c.Name] = i
		}
		ordered, err := modules.Ordered(declared)
		if err != nil {
			fail(name, err)
		}
		positions := make([]int, 0, len(ordered))
		for _, c := range ordered {
			positions = append(positions, index[c.Name])
		}
		out[name] = positions
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fail("(encoding)", err)
	}
}

func fail(name string, err error) {
	fmt.Fprintf(os.Stderr, "orderprobe: spec %s does not assemble: %v\n", name, err)
	os.Exit(1)
}
