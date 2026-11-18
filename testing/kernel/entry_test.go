package kernel

import (
	"context"
	"testing"

	"github.com/rzbdz/newgate-ext/testing/testkit"
	modules "github.com/rzbdz/newgate/component"
	entryapi "github.com/rzbdz/newgate/component/entry"
)

// TestTheDaemonEntrySurvivesARemovedUI is the ratchet for the 2026-09-20 bug.
//
// Scene: `__serve` (the daemon itself) was registered as a cli command, so a
// `disable: ["cli"]` assembly **built but never started** - the entry ledger answered
// "no entry claimed this call". A pure-web distribution (no terminal UI, browser only)
// was therefore impossible, though the spec allows that shape.
//
// **The matrix test cannot see this**: it asks "does removing this module still assemble",
// while this asks "after assembling, can this process still be the daemon". Assembling and
// starting are two things apart by the entry ledger - so the ratchet lands here.
//
// The criterion uses **the entry ledger**, not "is cli there": if the daemon later declares
// another way, this test still guards it.
func TestTheDaemonEntrySurvivesARemovedUI(t *testing.T) {
	testkit.Sandbox(t)
	all := kernelGraph(t)

	manager, err := modules.NewContext(context.Background(), modules.Static(without(all, "cli")))
	if err != nil {
		t.Fatalf("does not assemble after removing cli: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })

	reg, ok := modules.Get(manager.Context(), entryapi.Capability)
	if !ok {
		t.Fatal("the entry ledger is not in the graph -- it is the only module that cannot be removed (see modules/entry)")
	}

	// Claimable: the explicit internal entry, and the subcommand-less call **without a UI**
	// (no subcommand to dispatch; flags can only be the daemon's own).
	for _, args := range [][]string{
		{"__serve", "--port", "8899"},
		{"--port", "8899"},
		{"--port=8899"},
		nil,
	} {
		h, why, claimed := reg.Resolve(entryapi.Process{Argv0: "newgate", Args: args})
		if !claimed {
			t.Errorf("without a UI, %v is unclaimed -- this assembly compiles but cannot start (%s)", args, why)
			continue
		}
		// The name comes from the handler: Resolve's second return value is the "who was asked"
		// trail (the sentence for logs), not an identity.
		if name := h.Name(); name != "__serve" {
			t.Errorf("%v was claimed by %q, expected the daemon itself (__serve)", args, name)
		}
	}

	// Unclaimable: **over-claiming is harder to spot than under-claiming** - it shows up as
	// "a typo'd command started a daemon", surfacing only on a taken port or no connection.
	for _, args := range [][]string{
		{"frobnicate"},
		{"--help"},
		{"--port", "8899", "status"},
	} {
		if h, why, claimed := reg.Resolve(entryapi.Process{Argv0: "newgate", Args: args}); claimed {
			t.Errorf("%v was claimed by %q -- without a UI these should get \"nobody claimed it\" (%s)",
				args, h.Name(), why)
		}
	}
}

// TestABareInvocationIsStillTheUIs is the **reverse**: with a UI installed, the
// no-argument call still belongs to the UI (help / status), not the daemon's to swallow.
//
// Together they are the criterion: the fallback holds only where nobody else can answer.
func TestABareInvocationIsStillTheUIs(t *testing.T) {
	testkit.Sandbox(t)
	manager, err := modules.NewContext(context.Background(), modules.Static(kernelGraph(t)))
	if err != nil {
		t.Fatalf("does not assemble: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })

	reg, ok := modules.Get(manager.Context(), entryapi.Capability)
	if !ok {
		t.Fatal("the entry ledger is not in the graph")
	}
	for _, args := range [][]string{nil, {"status"}, {"--help"}} {
		h, why, claimed := reg.Resolve(entryapi.Process{Argv0: "newgate", Args: args})
		if !claimed {
			t.Errorf("with a UI installed %v should be claimed by the UI, but nobody claimed it", args)
			continue
		}
		if h.Name() == "__serve" {
			t.Errorf("with a UI installed %v was swallowed by the daemon -- no arguments should be answered by the UI (%s)", args, why)
		}
	}
	// Explicit internal entries still belong to the daemon.
	if h, _, _ := reg.Resolve(entryapi.Process{Argv0: "newgate", Args: []string{"__serve"}}); h.Name() != "__serve" {
		t.Errorf("__serve should belong to the daemon, actually %q", h.Name())
	}
}
