package kernel

import (
	"context"
	"testing"

	app "github.com/rzbdz/newgate/app"
	modules "github.com/rzbdz/newgate/component"

	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// TestGraphCanBeRebuiltAfterStop is the cheapest, broadest invariant of the refactor:
// **Stop must fully undo what Start registered**.
//
// # Why its own test
//
// Every module's Start registers things elsewhere (gateway plugs in plugins, confighook
// registers agents, config registers role providers, cli takes commands). These are
// **process-global**, and one missing Release in Stop is extremely subtle:
//
//   - running any test alone is green (one graph per process);
//   - it only breaks on a second assembly in one process, or worse - **does not break**,
//     and the second graph gets the first's residue.
//
// So this assembles twice: the first residue collides with the second Start. It caught a
// role-provider residue in the 2026-09-17 refactor (opencodeomo's omo slot judged
// "key registered by omo and omo" on the second assembly).
func TestGraphCanBeRebuiltAfterStop(t *testing.T) {
	testkit.Sandbox(t)

	first, err := app.New(context.Background(), modules.Static(kernelGraph(t)))
	if err != nil {
		t.Fatalf("first assembly failed: %v", err)
	}
	firstNames := first.ComponentNames()
	if err := first.Stop(context.Background()); err != nil {
		t.Fatalf("first stop failed: %v", err)
	}

	second, err := app.New(context.Background(), modules.Static(kernelGraph(t)))
	if err != nil {
		t.Fatalf("second assembly failed (the first Stop did not clean up?): %v", err)
	}
	t.Cleanup(func() { _ = second.Stop(context.Background()) })

	if got := second.ComponentNames(); len(got) != len(firstNames) {
		t.Fatalf("the two assemblies have different component counts: %v vs %v", firstNames, got)
	}
	// Process-level registrations must be clean too. Before 2026-09-20 this counted the
	// client directory (registered by client modules) - moved to the distribution; what remains
	// is the **request plugin registry**: none doubled after the second assembly.
	plugins := special.Plugins()
	seen := map[string]int{}
	for _, plugin := range plugins {
		seen[plugin.Name()]++
	}
	for name, n := range seen {
		if n != 1 {
			t.Fatalf("plugin %s registered %d times (not cleaned up between the two assemblies): %v", name, n, seen)
		}
	}
	if len(plugins) == 0 {
		t.Fatal("the kernel has no request plugin at all -- this check has degraded (it relies on plugin count as the leak probe)")
	}
}

// TestGraphCoversEveryModule reconciles "components the generator scanned" with "components
// really started in the graph".
//
// The generator sees static files (module.go + exported New()); the graph is the runtime
// assembly. A mismatch means a scanned module is missing from the graph - either it
// provides nothing (harmless but suspicious) or the interface mismatches (dangerous).
//
// **No exceptions in this equation** (since 2026-09-20): the generated manifest is the
// **only** component source, and the composition root adds nothing. Before 2026-09-20 one
// exception existed - the entry ledger's owner lived outside modules/ and was installed by
// the root, so the equation read "graph = scanned + built-in", and that line guarded "no
// second module sneaks in bypassing the scan"; now the equation itself is the guard.
func TestGraphCoversEveryModule(t *testing.T) {
	testkit.Sandbox(t)

	built, err := app.New(context.Background(), modules.Static(kernelGraph(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })

	names := map[string]bool{}
	for _, name := range built.ComponentNames() {
		if names[name] {
			t.Fatalf("duplicate component name: %s (actual %v)", name, built.ComponentNames())
		}
		names[name] = true
	}
	want := len(kernelGraph(t))
	if got := len(names); got != want {
		t.Fatalf("the graph has %d components, the scan list has %d (the two must match one by one: installing modules has only this one path)",
			got, want)
	}
	if !names["entry"] {
		t.Fatalf("entry (the only module that cannot be removed) must always be in the graph (actual %v)", built.ComponentNames())
	}

	// The foundation plus two takeover paths are part of the product definition; need all.
	for _, must := range []string{"config", "gateway", "config-hook", "runtime", "cli", "wrapper"} {
		if !names[must] {
			t.Fatalf("required component %s is missing (actual %v)", must, built.ComponentNames())
		}
	}
}

// TestUIStaysOutOfTheDependencyGraph pins "ui is not a dependency".
//
// Before 2026-09-18 this asserted "cli starts before extension modules" - injection
// was then an ordering edge (modules Optional(cli), registering in their own Start).
// That edge was the deadlock: the UI also depended on those modules (to render their
// data), so mutual references cycled; config / runtime / config-hook never injected,
// and commands were forced to stay in the UI.
//
// The old workaround was component.Inject (an injection edge **outside ordering**)
// plus Component.Attach (an injection phase after all Starts). That was removed
// later on 2026-09-18 - the real fix was cutting the **ui's out-edges**
// (cli.Requires is now empty); with none, Optional(cli)'s **in-edge** cannot cycle,
// so injection returned to a normal ordering edge in Start (see component.Optional).
//
// So the invariant now reads: **no component declares a non-Optional edge to the UI**.
// In other words, weak only for ui - register if installed, skip if not; Need would
// claim "I cannot live without the UI" and drag it back into the dependency graph.
// uiCapabilities are the "UI" ports. **A rule must know all members**: before 2026-09-20
// this hard-coded "cli", while view already had seven consumers - a new module writing
// `Need(viewapi.Capability)` ("no dashboard, no me") would not redden - this comment says no.
//
// The criterion is "**the UI layer**: others register their face into it", not "registry
// port" - porthub and serving also have "others register, self depends on none", but
// they are **mechanisms**, not UI: a module with only the shared port, no fallback,
// may legitimately `Need(porthub)` (no cycle; porthub has no out-edges). Do not
// add members by shape; ask first "is this a UI layer".
var uiCapabilities = map[string]bool{
	"cli":  true, // the terminal UI: commands, status lines and diagnostics register here
	"view": true, // the web UI's ledger of concepts: each module registers its own face here
}

func TestUIStaysOutOfTheDependencyGraph(t *testing.T) {
	testkit.Sandbox(t)

	built, err := app.New(context.Background(), modules.Static(kernelGraph(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })

	for _, component := range built.Components() {
		for _, requirement := range component.Requires {
			if !uiCapabilities[requirement.Name()] {
				continue
			}
			if !requirement.Optional() {
				t.Fatalf("component %s declares a hard dependency on ui (%s) --"+
					"the UI depends on others rendering, others depend on the UI injecting, and that is the cycle;"+
					"use modules.Optional (weak dependency) for ui", component.Name, requirement.Name())
			}
		}
	}
}

// TestInjectorsStartAfterTheUI pins "whoever injects others waits for them".
//
// The user's 2026-09-18 words landed as a test: **a module injecting into another must
// wait until the injected one has loaded; the injected must not wait for the injector**.
// In this repo: modules registering commands and status rows into the UI start after `cli`.
//
// # Why its own test
//
// It had **no** guarantee before. In the Inject era (outside ordering) the order of nine
// injectors fell out of stable topological sort - coincidentally after cli, by directory
// name. Any rename, or one injector sorting before cli, reverses it, and the symptom is
// "UI up, commands late": who goes first differs only in that instant, invisible after.
//
// The order now comes from Optional(cli), an **ordering edge** (see component.Optional),
// i.e. from the mechanism. This test guards "the mechanism is still there" - someone
// changing Optional(cli) and deleting that Requires goes red here.
//
// "cli first" does not mean "cli before everything": cli depends on nothing
// (TestCLIDependenciesOnlyShrink), so it is among the top of the topo order.
//
// # Why this covers only cli, not view (though the rule above covers both)
//
// The two UI ports **register at different times**, so ordering requirements differ:
//
//   - `cli`'s command/status ledger is built in its own `Start` - so registrants must
//     start **after** it, else the ledger does not exist when their Start runs and the
//     command is lost. A **hard** ordering requirement; this test exists for it.
//   - `view`'s concept ledger is built in web-dashboard's `New()` (see
//     modules/web-dashboard/module.go: must precede any Start, else earlier modules
//     cannot register). So registration works in any Start - order is irrelevant to
//     view, and an assertion would enshrine a constraint that does not exist.
func TestInjectorsStartAfterTheUI(t *testing.T) {
	testkit.Sandbox(t)

	built, err := app.New(context.Background(), modules.Static(kernelGraph(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })

	names := built.ComponentNames()
	at := map[string]int{}
	for i, name := range names {
		at[name] = i
	}
	ui, ok := at["cli"]
	if !ok {
		t.Fatalf("component order = %v; the graph has no cli", names)
	}

	// The injector list is read from **declarations** (who Optional(cli)), not hard-coded:
	// a new module adding a weak dependency joins this assertion automatically.
	injectors := 0
	for _, component := range built.Components() {
		for _, requirement := range component.Requires {
			if requirement.Name() != "cli" {
				continue
			}
			injectors++
			if at[component.Name] < ui {
				t.Fatalf("component order = %v; %s(%d) is ordered before cli(%d) -- "+
					"it is the module that registers commands/status lines into the UI,"+
					"ordering it earlier means its Start may run before the UI is ready",
					names, component.Name, at[component.Name], ui)
			}
		}
	}
	if injectors == 0 {
		t.Fatal("no module declares Optional(cli) -- the UI's seven ledgers are all empty," +
			"either the injection points were removed or this dependency was deleted by mistake")
	}
}
