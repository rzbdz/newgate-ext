package kernel

import (
	"context"
	app "github.com/rzbdz/newgate/app"
	"testing"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	modules "github.com/rzbdz/newgate/component"
)

// TestDefaultGraphLayering pins "owner before registrant".
//
// A declaration of design intent, not a restatement of topological sort: topo sort only
// guarantees Requires, while the table below says **who we think should come first**.
// Add a dependency inverting the layering (config on a client module) and it goes red.
//
// Commands/diagnostics/status rows live on **cli**'s service, and injection is an
// **ordering edge**: modules declare modules.Optional(cli) (weak, see component.Optional),
// so they all start after the UI - the ledger is ready when their Start registers; no UI,
// they skip, nothing lost. Installing ui only changes where their contributions go.
//
// The table's order comes from two edge kinds: hard Need and weak Optional. The weak
// ones (`plugin-manager` registering state fields etc.) are ordering edges too - owner
// first, registrant after; owner absent, the registrant still runs, minus a destination.
//
// This table covers the flagship spec (manifest.DefaultSpec), which since
// 2026-11-18 is *the* graph — the kernel ships none. Per-module behaviour still
// lives with its module; this is the "who must start before whom" declaration.
func TestDefaultGraphLayering(t *testing.T) {
	built, err := app.New(context.Background(), modules.Static(kernelGraph(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })
	names := built.ComponentNames()
	index := map[string]int{}
	for i, name := range names {
		index[name] = i
	}
	at := func(name string) int {
		i, ok := index[name]
		if !ok {
			t.Fatalf("component order = %v; %s is not in the graph", names, name)
		}
		return i
	}

	pairs := []struct{ owner, registrant, why string }{
		{"config", "runtime", "runtime reads config"},
		{"config", "gateway", "gateway reads config"},
		{"config-hook", "runtime", "runtime uses AgentCatalog"},
		{"config-hook", "plugin-manager", "plugin-manager registers the state field"},
		{"runtime", "wrapper", "wrapper lazily starts the proxy"},
		{"gateway", "thinking", "thinking registers a request plugin"},
	}
	for _, p := range pairs {
		if at(p.owner) > at(p.registrant) {
			t.Fatalf("component order = %v; %s(%d) is ordered after %s(%d), but it %s",
				names, p.owner, at(p.owner), p.registrant, at(p.registrant), p.why)
		}
	}

	// Client directory: **ask via the port, not the composition root's facade**. The root
	// knows no module (see direction_test.go), so it has no Get/Names conveniences - each is
	// an "app -> that module" edge, existing only for test convenience.
	//
	// This **only asserts the port exists**, not who is in it: client access (claude /
	// opencode) is a product decision, in the distribution since 2026-09-20, so this kernel
	// graph has no client - the empty directory is normal. The distribution asserts who.
	if _, ok := modules.Get(built.Context(), agentapi.AgentCatalogCapability); !ok {
		t.Fatal("the graph has no client catalog capability -- the takeover mechanism has no registry then")
	}
}

// TestCLIDependenciesOnlyShrink pins "the UI depends on no module".
//
// The goal is **not** "nobody depends on the UI" - impossible and unnecessary: modules
// injecting commands must Need it. The goal is the reverse: **the UI depends on nobody**,
// it is just a ui; others' data arrives as callbacks they inject, which it calls in a loop.
//
// Not there yet: the remaining allowed names below serve commands still in the UI; each
// must move back to its module (start/stop to runtime, tier/profiles to config,
// probe/metrics to gateway...).
//
// This table **can only shrink**. Adding one = pinning a command in the UI forever, which
// is what this refactor dismantles; the symptom appears only when someone wants to move it
// ("cannot, it cycles"), and red here is far earlier than red there.
func TestCLIDependenciesOnlyShrink(t *testing.T) {
	// The list is **empty** now, and it may only shrink (cleared 2026-09-18):
	//
	// The UI once depended on config (read config), runtime (pull clients), agent-catalog
	// (dispatch by agent), breaker (build the data plane). Four edges, four cases of "the UI
	// knows a module's internals": tier/profile rendering, wrapped launch, agent list, health
	// table. All moved back to their owning modules; the UI now takes what others report
	// through injection points (commands, status rows, probe items, terms, diagnostics).
	//
	// So this assertion now means: **the UI must have no out-edge at all**. Any new edge
	// means someone's knowledge went back into the UI.
	allowed := map[string]bool{}

	built, err := app.New(context.Background(), modules.Static(kernelGraph(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })

	for _, c := range built.Components() {
		if c.Name != "cli" {
			continue
		}
		for _, req := range c.Requires {
			if !allowed[req.Name()] {
				t.Fatalf("the UI depends on %q, which is not in the allowlist. The UI must **not depend on any module**:"+
					"commands are injected by the module that owns that capability (see modules/cli/extension)."+
					"Either move that command back to its module, or think through why the UI has to know about it.", req.Name())
			}
		}
		return
	}
	t.Fatal("the graph has no cli component")
}

func TestGatewayReceivesModuleHooks(t *testing.T) {
	built, err := app.New(context.Background(), modules.Static(kernelGraph(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })
	var names []string
	for _, plugin := range special.Plugins() {
		names = append(names, plugin.Name())
	}
	// The kernel's own plugins: always-thinks (thinking's fallback translator).
	// claude-bg used to be in this list - it moved to the distribution with claudecode
	// (client access is a product decision), and a matching assertion there guards it.
	for _, want := range []string{
		"always-thinks",
	} {
		found := false
		for _, name := range names {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("gateway hooks = %v, missing %s", names, want)
		}
	}
}

// TestExactlyOneUI asserts "exactly one ui is installed".
//
// Why needed (measured 2026-09-18): ports declare no cardinality (component's design),
// yet cli.Capability is **unique by design** - the root calls one entry. The owner
// guarantees nothing, so a second ui is **fully silent**: assembly succeeds, no error,
// and it gets zero commands/status/probe/diagnostics/terms (injection uses modules.Get,
// giving only the first in declaration order).
//
// Worse, "who wins" depends on **directory-name order** (generatedComponents sorts;
// Get takes the first): add a ui directory before cli and the real cli is ignored,
// while app.CLI().Run() hands it the whole command line - a silent naming takeover.
//
// This test turns silence red. Real multi-ui (tui / web are on the roadmap) needs
// injection switched to GetAll first; this assertion changes then.
func TestExactlyOneUI(t *testing.T) {
	built, err := app.New(context.Background(), modules.Static(kernelGraph(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })

	uis := modules.GetAll(built.Context(), cliapi.Capability)
	if len(uis) != 1 {
		t.Fatalf("installed %d uis, there should be 1."+
			"with several uis the injection only reaches \"the first in declaration order\" (which follows directory-name alphabetical order),"+
			"and the rest are completely silent -- see the comment on this test.", len(uis))
	}
}
