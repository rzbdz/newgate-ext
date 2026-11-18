package kernel

import (
	"context"
	"strings"
	"testing"

	entrymod "github.com/rzbdz/newgate-ext/modules/entry"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
)

// Removal matrix: **only modules declaring themselves built-in are unremovable**; all else go.
//
// # Why this invariant deserves a suite
//
// "Installing a module = copying a directory into modules/" has another half, easier to
// break: **not installing = removing it from the manifest**. It breaks silently:
//
//   - the composition root imports a concrete module -> remove it, core does not compile
//     (guarded by app/direction_test.go; a compile-time symptom, the luckiest kind);
//   - another module **reaches for** an undeclared port in Start -> remove the provider,
//     construction does not fail (nobody declared), panic only at run time. The worst
//     kind: builds, tests, and the shipped binary dies on some command.
//
// The second is why this suite exists. It removes one module at a time and watches **how
// failure happens**: removal either assembles or **fails on the spot naming the port**, due
// to a hard dependency. Any other failure (panic, vague error, root not compiling) is a bug.
//
// # Criterion boundary
//
// The boundary is each module's declared `Type`: only `entrymod.TypeBuiltin` is unremovable
// (the one module the kernel knows - it provides the entry ledger, see modules/entry). Not
// set here but **checked**: if someone marks a business module built-in, the matrix tests
// one less, and TestTheOnlyBuiltinCannotBeRemoved finds "the built-in one is not the root's
// dependency" - both red.
func TestRemovalMatrix(t *testing.T) {
	testkit.Sandbox(t)
	all := kernelGraph(t)

	victims := 0
	for _, victim := range all {
		if victim.Type == entrymod.TypeBuiltin {
			continue
		}
		victims++
		t.Run(victim.Name, func(t *testing.T) {
			dependents := hardDependents(all, victim)
			manager, err := modules.NewContext(context.Background(), modules.Static(without(all, victim.Name)))

			if err == nil {
				t.Cleanup(func() { _ = manager.Stop(context.Background()) })
				if len(dependents) > 0 {
					t.Fatalf("removing %s still assembles, but %v declares a hard dependency on a capability it provides --"+
						"which means that dependency is missing from the graph (check whether Requires was written as Optional)",
						victim.Name, dependents)
				}
				return
			}

			// Not assembling is **expected** half the time, provided someone hard-depends on it.
			if len(dependents) == 0 {
				t.Fatalf("removing %s does not assemble, but no module declares a hard dependency on it --"+
					"someone reaches for what it provides without declaring it (the second kind of bad practice from the top of this document):\n%v",
					victim.Name, err)
			}
			// The error must name the missing port: proof "because of a dependency", not chance.
			for _, provision := range victim.Provides {
				if strings.Contains(err.Error(), provision.Name()) {
					return
				}
			}
			t.Fatalf("the error from removing %s does not name the capability %v it provides, so the cause cannot be told:\n%v",
				victim.Name, provisionNames(victim), err)
		})
	}

	if victims < 5 {
		t.Fatalf("only %d modules tested -- the matrix has degraded (are all the rest built-in?)", victims)
	}
}

// TestTheOnlyBuiltinCannotBeRemoved is the matrix's complement: **the one unremovable**
// module must fail on the spot when removed, and say why.
//
// Why its own test: the matrix skips built-ins, so "is a built-in really unremovable" is
// **an unchecked assumption** there. Here it becomes a check - remove entry and the graph
// must refuse to start for the missing `entry` port.
//
// It also **reconciles two statements**. Today "unremovable" has two sources:
//
//	the module's declared class  Type = entrymod.TypeBuiltin   (the matrix draws its boundary)
//	the root's own dependency    provides app.compositionRootPorts() (Selection.Load refuses)
//
// Both must point at one component. Mismatch is subtle: mark a business module built-in and
// the matrix **skips** it (one less), while Selection.Load allows removing it (it provides
// no root port) - the only module nobody watches, exactly the degeneration this guards.
func TestTheOnlyBuiltinCannotBeRemoved(t *testing.T) {
	testkit.Sandbox(t)
	all := kernelGraph(t)

	var builtins []modules.Component
	for _, component := range all {
		if component.Type == entrymod.TypeBuiltin {
			builtins = append(builtins, component)
		}
	}
	if len(builtins) != 1 {
		var names []string
		for _, b := range builtins {
			names = append(names, b.Name)
		}
		t.Fatalf("there should be exactly one built-in (entry), actual %v", names)
	}
	if name := builtins[0].Name; name != "entry" {
		t.Fatalf("the built-in is %q, want entry (the only module the kernel knows)", name)
	}
	if !providesPort(builtins[0], modules.Name(entry.Capability)) {
		t.Fatalf("entry is marked built-in but does not provide the composition root's own "+
			"port %q — Selection.Load would let it be disabled while the matrix skips it, "+
			"leaving it the one module nobody watches", modules.Name(entry.Capability))
	}
	// The reverse too: the port the composition root needs has one source. A second
	// provider fails construction with "duplicate provide", far from "who is the entry ledger".
	for _, component := range all {
		if component.Name == builtins[0].Name {
			continue
		}
		if providesPort(component, modules.Name(entry.Capability)) {
			t.Fatalf("component %s also provides the composition root's port %q — "+
				"the entry ledger may have exactly one source",
				component.Name, modules.Name(entry.Capability))
		}
	}

	_, err := modules.NewContext(context.Background(), modules.Static(without(all, "entry")))
	if err == nil {
		t.Fatal("removing entry still assembles: the entry ledger is gone, the process starts but nothing is the entry")
	}
	if !strings.Contains(err.Error(), modules.Name(entry.Capability)) {
		t.Fatalf("the error from removing entry should name the %q capability (that is what it provides), actual:\n%v",
			modules.Name(entry.Capability), err)
	}
}

// TestDependencyFreeModulesLoadStandalone is the matrix's other half: **a module with
// no declared dependencies must assemble into a graph with only itself**.
//
// The criterion is "not one Requires in the declaration", not "I think it depends on
// nothing" - names come from declarations, so a new module adding a dependency leaves
// this assertion, no test edit needed.
//
// It stops one concrete error: a module reaching in Start for an undeclared port - fine
// in the full graph (someone provides it), panics alone; people are then on another bug.
func TestDependencyFreeModulesLoadStandalone(t *testing.T) {
	all := kernelGraph(t)

	checked := 0
	for _, component := range all {
		if len(component.Requires) > 0 {
			continue
		}
		checked++
		t.Run(component.Name, func(t *testing.T) {
			testkit.Sandbox(t)
			graph := testkit.Start(t, component)
			if names := graph.Names(); len(names) != 1 || names[0] != component.Name {
				t.Fatalf("a graph with only %s produced %v", component.Name, names)
			}
		})
	}
	if checked == 0 {
		t.Fatal("not a single zero-dependency module -- either the dependency declarations got filled in everywhere, or this check is wrong")
	}
}

// hardDependents returns components with a **hard dependency** (Need) on victim's ports.
//
// Need only: optional deps form no edge when the provider is absent - removal must work.
func hardDependents(all []modules.Component, victim modules.Component) []string {
	provided := map[string]bool{}
	for _, provision := range victim.Provides {
		provided[provision.Name()] = true
	}
	var out []string
	for _, component := range all {
		if component.Name == victim.Name {
			continue
		}
		for _, requirement := range component.Requires {
			if !requirement.Optional() && provided[requirement.Name()] {
				out = append(out, component.Name)
				break
			}
		}
	}
	return out
}

func without(all []modules.Component, name string) []modules.Component {
	out := make([]modules.Component, 0, len(all))
	for _, component := range all {
		if component.Name != name {
			out = append(out, component)
		}
	}
	return out
}

func provisionNames(component modules.Component) []string {
	out := make([]string, 0, len(component.Provides))
	for _, provision := range component.Provides {
		out = append(out, provision.Name())
	}
	return out
}
