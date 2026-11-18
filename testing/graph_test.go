package disttesting

import (
	"context"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/manifest"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
)

// This group pins **the distribution's own graph** invariants: can each spec assemble,
// do the spec's named and disabled things really take effect, are our modules removable.
//
// It is **complementary** to the kernel's removal matrix (core's app/matrix_test.go):
// that covers the kernel's thirteen modules, this the distribution's few + spec semantics;
// same criterion: **removal may fail only on a hard dependency, naming the missing port.**

// servesEntry reports whether a component provides the **entry ledger** - the port the
// composition root itself needs. Read from Provides: the kernel's app.Selection.Load uses
// the same one (compositionRootPorts) - two statements of one sentence.
func servesEntry(c modules.Component) bool {
	for _, p := range c.Provides {
		if p.Name() == modules.Name(entry.Capability) {
			return true
		}
	}
	return false
}

// TestEverySpecBuilds: every spec must load, start and stop, and every module it
// names must really be in the graph (naming a module and not installing it is a
// silent failure: a feature is missing and nothing says so).
//
// Specs no longer *disable* anything — the kernel ships no modules to disable
// (2026-11-18). A spec is now exactly the list it names, so "the graph equals the
// list" is checked from both directions below.
func TestEverySpecBuilds(t *testing.T) {
	for _, name := range manifest.SpecNames() {
		t.Run(name, func(t *testing.T) {
			testkit.Sandbox(t)

			comps, ok := manifest.Specs()[name]
			if !ok {
				t.Fatalf("SpecNames reported %q but it is not in Specs()", name)
			}
			manager, err := modules.NewContext(context.Background(), modules.Static(comps))
			if err != nil {
				t.Fatalf("starting the graph: %v", err)
			}
			t.Cleanup(func() { _ = manager.Stop(context.Background()) })

			got := map[string]bool{}
			for _, n := range manager.ComponentNames() {
				got[n] = true
			}
			for _, c := range comps {
				if !got[c.Name] {
					t.Errorf("the spec names %s but it is not in the graph -- anything named must really be installed; graph = %v",
						c.Name, manager.ComponentNames())
				}
			}
			// The other direction: nothing beyond the list may be installed. A module
			// that shows up without being named came from a Selection.DefaultSpec
			// fallback or a stale generated table — both are "I thought I narrowed the
			// product, and it is wider than I think".
			named := map[string]bool{}
			for _, c := range comps {
				named[c.Name] = true
			}
			for _, n := range manager.ComponentNames() {
				if !named[n] {
					t.Errorf("%s installed but not named by the spec — graph = %v",
						n, manager.ComponentNames())
				}
			}
		})
	}
}

// TestEverySpecHasAtMostOneUI: **at most one** UI per spec, and the flagship exactly one.
//
// Why its own test (the kernel has a matching TestExactlyOneUI): ports do not declare
// cardinality, yet cli's capability is unique by design - a second ui is **fully silent**:
// assembly succeeds, no error, but it gets zero commands/status rows/probe items (injection
// reaches only the first in declaration order = **directory-name order**).
// `dist-simple-cli.json` exists only to swap the kernel's cli; this is the criterion on it.
//
// Why the upper bound (changed 2026-09-20, was "exactly one"): the skeleton `dist-hello.json`
// **has no UI** - it is "framework + one hello", and `newgate` is declared by hello itself
// (see modules/hello). So "more than one" is the silent failure; "none" is the skeleton's
// normal state.
//
// "At least one" is not lost, just rephrased closer to fact - see
// TestEverySpecClaimsTheBareInvocation below: a UI is only one way to declare an entry.
func TestEverySpecHasAtMostOneUI(t *testing.T) {
	for _, name := range manifest.SpecNames() {
		t.Run(name, func(t *testing.T) {
			testkit.Sandbox(t)

			comps := manifest.Specs()[name]
			manager, err := modules.NewContext(context.Background(), modules.Static(comps))
			if err != nil {
				t.Fatalf("starting the graph: %v", err)
			}
			t.Cleanup(func() { _ = manager.Stop(context.Background()) })

			names := manager.ComponentNames()
			uis := modules.GetAll(manager.Context(), cliapi.Capability)
			if len(uis) > 1 {
				t.Fatalf("installed %d uis, at most 1 is allowed -- with more than one only the one whose directory name sorts first takes effect,"+
					"and the rest are completely silent; graph = %v", len(uis), names)
			}

			// The fallback entry (entry.DefaultRank) takes one declarant only: cli and hello
			// both sit there, and with both in one graph Resolve degrades into
			// "directory-name order decides process behaviour". So the spec layer forbids it.
			if len(uis) == 1 && contains(names, "hello") {
				t.Fatalf("this graph has both a UI and hello: both claim the entry at the fallback rank,"+
					"so which one is picked depends on directory-name alphabetical order (cli sorts first) -- hello belongs to the skeleton config,"+
					"do not install it into a distribution that has a UI; graph = %v", names)
			}
		})
	}
}

// TestEverySpecClaimsTheBareInvocation: in every spec's binary, a **bare `newgate`
// must be claimed by someone**.
//
// Why better than "must have a UI": a UI is only **one** way to declare an entry. The
// skeleton (framework + hello) has no UI; hello declares `newgate` - both graphs pass,
// and what they truly share, not-to-break, is this: **typing `newgate` must yield
// behaviour, not "nobody claimed this call" plus exit code 69**.
//
// The degeneration is concrete: a distribution with only data-plane modules (gateway,
// takeover) and nothing declaring an entry - builds, tests, status fine, bare call exits.
func TestEverySpecClaimsTheBareInvocation(t *testing.T) {
	for _, name := range manifest.SpecNames() {
		t.Run(name, func(t *testing.T) {
			testkit.Sandbox(t)

			comps := manifest.Specs()[name]
			// NewContext really Starts, so declarations happened (not "a component is sitting there").
			manager, err := modules.NewContext(context.Background(), modules.Static(comps))
			if err != nil {
				t.Fatalf("starting the graph: %v", err)
			}
			t.Cleanup(func() { _ = manager.Stop(context.Background()) })

			registry := modules.MustGet(manager.Context(), entry.Capability)
			if h, why, ok := registry.Resolve(entry.Process{Argv0: "newgate"}); !ok {
				t.Fatalf("`newgate` is unclaimed (%s) -- this binary just prints one human sentence and exits when run bare; graph = %v",
					why, manager.ComponentNames())
			} else {
				t.Logf("the bare invocation goes to %s", h.Name())
			}
		})
	}
}

// TestDistModulesAreRemovable removes the distribution's own modules one by one.
//
// The criterion is word-for-word the kernel matrix: **remove a module, either it assembles**
// (nobody hard-depends), **or it fails on the spot naming the missing port** (someone does).
// Both are right; the third is wrong: does not assemble yet nobody declared a dependency -
// someone reached for another's data via `MustGet`; the remover gets an unhelpful error.
func TestDistModulesAreRemovable(t *testing.T) {
	const specName = "dist.json"
	testkit.Sandbox(t)

	base, ok := manifest.Specs()[specName]
	if !ok {
		t.Fatalf("there is no spec named %s", specName)
	}
	if len(base) < 2 {
		t.Fatalf("%s only names %d modules — the matrix degenerated", specName, len(base))
	}

	for _, victim := range base {
		t.Run(victim.Name, func(t *testing.T) {
			testkit.Sandbox(t)

			// The table minus the victim, **in the table's order**: dropping an entry cannot
			// invert an edge, so the rest is still a valid order -- and that matters here, because
			// this test asserts on the *assembly* error ("names the missing port"), not on the
			// ordering one. Going through Ordered first would report the missing Need before
			// NewContext ever runs, and the two messages are deliberately different sentences.
			var remaining []modules.Component
			for _, c := range base {
				if c.Name == victim.Name {
					continue
				}
				remaining = append(remaining, c)
			}
			comps := remaining
			dependents := hardDependents(comps, victim)
			manager, err := modules.NewContext(context.Background(), modules.Static(comps))

			if err == nil {
				t.Cleanup(func() { _ = manager.Stop(context.Background()) })
				if len(dependents) > 0 {
					t.Fatalf("removing %s still assembles, but %v declares a hard dependency on a capability it provides --"+
						"which means that dependency is missing from the graph (was Requires written as Optional?)", victim.Name, dependents)
				}
				return
			}
			if len(dependents) == 0 {
				t.Fatalf("removing %s does not assemble, but no module declares a hard dependency on it -- someone reaches"+
					"for what it provides without declaring it:\n%v", victim.Name, err)
			}
			for _, provision := range victim.Provides {
				if strings.Contains(err.Error(), provision.Name()) {
					return
				}
			}
			t.Fatalf("the error from removing %s does not name the capability it provides (%v), so the cause cannot be told:\n%v",
				victim.Name, provisionNames(victim), err)
		})
	}
}

// hardDependents returns the components that **hard-depend** on the ports victim provides
// (weak ones excluded: they already say "I run without you").
func hardDependents(all []modules.Component, victim modules.Component) []string {
	provided := map[string]bool{}
	for _, p := range victim.Provides {
		provided[p.Name()] = true
	}
	var out []string
	for _, c := range all {
		if c.Name == victim.Name {
			continue
		}
		for _, req := range c.Requires {
			if provided[req.Name()] && !req.Optional() {
				out = append(out, c.Name)
				break
			}
		}
	}
	return out
}

func provisionNames(c modules.Component) []string {
	out := make([]string, 0, len(c.Provides))
	for _, p := range c.Provides {
		out = append(out, p.Name())
	}
	return out
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
