// Command orderprobe runs the **real** assembly resolution and prints each spec's start order.
//
// It is distgen's second pass: the first compiles specs into manifest/modules_gen.go,
// then this program runs (a **new process**, so it reads the just-written manifest),
// returns the order, and writes manifest/order_gen.go.
//
// # Why a new process instead of distgen computing it
//
// The manifest in the distgen process is **compiled in** - the previous run's result.
// With a just-edited spec (module added, disable changed), order from the old manifest
// is silently wrong, and "wrong order" is miles from "stale manifest". A new process
// has no such problem: it necessarily reads the one on disk.
//
// # It computes the "from scratch" one
//
// `Specs()` hands out **plain** choices (no order), so `Load()` here gives declaration
// order and `Resolve` honestly builds edges and sorts - exactly what we freeze. It also
// self-checks: every spec really assembles, or generation fails (not at the first runtime
// command).
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

	out := map[string][]string{}
	for _, name := range names {
		components, err := specs[name].Load()
		if err != nil {
			fail(name, err)
		}
		plan, err := modules.Resolve(staticLoader(components))
		if err != nil {
			fail(name, err)
		}
		out[name] = plan.Names()
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

// staticLoader hands out a batch of pre-built components (same-named helper in app's tests).
type staticLoader []modules.Component

func (s staticLoader) Load() ([]modules.Component, error) { return s, nil }
