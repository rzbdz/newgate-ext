// Package disttesting holds the **distribution's own** tests.
//
// # Why the tests live in the distribution repo
//
// It is one of the lines between "a distribution" and "a pile of module directories":
// module correctness and distribution invariants (which must coexist, which must be
// exclusive) are known only to its author. Core's tests test the kernel - not hello.
//
// # How they run
//
// These packages live in the distribution's **own module** (`go.mod`, one `replace` to
// the `core/` submodule), so they import the kernel's test facilities and modules directly:
//
//	go test ./...
//
// It runs here, no kernel help. And the reverse: **the kernel's tests never run these** -
// its manifest sees only its own `modules/`. "Tests follow owners" holds both ways.
package disttesting

import (
	"context"
	"testing"

	entrymod "github.com/rzbdz/newgate-ext/modules/entry"
	"github.com/rzbdz/newgate-ext/modules/hello"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
)

// TestHelloClaimsTheDefaultEntry is the skeleton spec's whole behaviour: after "framework
// + hello" comes up, this `newgate` call belongs to hello, which prints one hello world.
//
// Before 2026-09-20 this asserted "a dependency-free module loads alone" - hello depended
// on nothing then. Now it declares into **the entry ledger**, gaining a hard dependency
// (`Need(entry.Capability)`, see modules/hello) and installing with entry. The criterion
// now asserts **what it really does**: declare -> picked by Resolve -> Handle returns 0.
//
// "A dependency-free module installs alone" is not lost: the kernel's removal matrix
// guards it module by module (core's app/matrix_test.go, TestDependencyFreeModules...).
func TestHelloClaimsTheDefaultEntry(t *testing.T) {
	testkit.Sandbox(t)
	g := testkit.Start(t, entrymod.New(), hello.New())

	registry := testkit.Get(g, entry.Capability)
	process := entry.Process{Argv0: "newgate"}
	h, why, ok := registry.Resolve(process)
	if !ok {
		t.Fatalf("no entry claims `newgate` (%s) -- this distribution's binary will just exit when run", why)
	}
	if got := h.Name(); got != "hello" {
		t.Fatalf("the one claiming this invocation was %q, want hello", got)
	}
	if code := h.Handle(process); code != 0 {
		t.Errorf("hello world exit code = %d, want 0", code)
	}
}

// TestHelloUnregistersOnStop pins the kernel rule on this module's side too:
// **Stop must undo what Start registered**.
//
// Why its own test: the ledger is **process-level**; missing the Release leaves a residue
// into the second graph - and errors nothing (the kernel's rebuild test was bitten by
// this). Distribution modules will multiply; this is the template to copy.
func TestHelloUnregistersOnStop(t *testing.T) {
	testkit.Sandbox(t)

	comps := []modules.Component{entrymod.New(), hello.New()}
	manager, err := modules.NewContext(context.Background(), modules.Static(comps))
	if err != nil {
		t.Fatalf("starting the graph: %v", err)
	}
	// The ledger object is still in hand after Stop (it is that Table), so ask "who is left".
	registry := modules.MustGet(manager.Context(), entry.Capability)
	process := entry.Process{Argv0: "newgate"}
	if _, _, ok := registry.Resolve(process); !ok {
		t.Fatal("hello did not claim an entry after the graph came up")
	}

	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("stopping: %v", err)
	}
	if h, why, ok := registry.Resolve(process); ok {
		t.Fatalf("the ledger still holds %q after stopping (%s) -- Stop missed that Release", h.Name(), why)
	}
}
