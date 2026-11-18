// Package testkit is the shared foundation for module unit tests: isolated env + graph + fakes.
//
// # Why it exists
//
// Before, every package had its own: `isolate()` in opencodeomo / takeover / forward,
// `testCatalog` in cli / takeover (verbatim duplicates), `sandboxStore` again. Worse, no
// "install only my components + fakes" entrance - each package wrote a Loader and managed
// Stop, so most skipped the graph and called internals directly, leaving **the wiring
// between components (who registers what in Start, whether Stop undoes it) untested**.
//
// Four layers (docs/10-testing-security.md)
//
//	unit     a pure function / a struct             -- no testkit
//	module   this package: "me + my dependency chain" -- testkit.Start
//	system   testing/system: full real graph + real forward -- only "together fine"
//	e2e      mock/*.sh: real binary, process, takeover -- only this is e2e
//
// This package serves **module** layer: install one module and its dependencies into a real
// graph and run it, asserting interface pass-through, register/undo symmetry, and the
// module's own "only it knows" behaviour (dynamically updated keys, upstream patch
// criteria). Whole-graph interplay is testing/system's.
//
// One constraint: it imports confighook and agentstate, so **those packages' internal tests
// cannot import this one** (a test-only import cycle). They inline the few lines needed.
package testkit

import (
	"context"
	"github.com/rzbdz/newgate/lib/i18n"
	"os"
	"path/filepath"
	"sort"
	"testing"

	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/runtime/agentstate"
	modules "github.com/rzbdz/newgate/component"
)

// Sandbox corrals all global state a test touches into a temp dir and restores on Cleanup.
//
// Three things, or they pollute each other:
//
//	NEWGATE_HOME        newgate's config/logs/pid/thinkcache (paths package reads it)
//	NEWGATE_TARGET_DIR  where client config takeover lands (unaffected by NEWGATE_HOME)
//	HOME                shell rc, ~/.claude etc. (discovery walks it)
//
// It also **clears** variables leaking from the parent, else results depend on the calling
// shell - typically CLAUDE_CODE_MAX_CONTEXT_TOKENS and NEWGATE_DEPTH (testing inside a
// taken-over session without cleaning gives false failures).
func Sandbox(t *testing.T) *Env {
	t.Helper()

	root := t.TempDir()
	env := &Env{
		Root:    root,
		Home:    filepath.Join(root, "ng"),
		Targets: filepath.Join(root, "cfg"),
	}
	for _, dir := range []string{env.Home, env.Targets, filepath.Join(root, "home")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("testkit: creating sandbox directory %s: %v", dir, err)
		}
	}

	restore := setEnv(map[string]string{
		"NEWGATE_HOME":       env.Home,
		"NEWGATE_TARGET_DIR": env.Targets,
		"HOME":               filepath.Join(root, "home"),
		// The language is **pinned**, not just cleared: tests assert rendered text, and pinning
		// the "source language" (English) gives stable expectations. Translation cases setenv.
		"NEWGATE_LANG": i18n.SourceLang,
	})
	unset := clearEnv(
		// The parent session may carry these; not clearing them changes the tested behaviour.
		"NEWGATE_DEPTH", "NEWGATE_DISABLE", "NEWGATE_PROFILE", "NEWGATE_PRESET",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS", "CLAUDE_CODE_AUTO_COMPACT_WINDOW",
		// The graceful-handover fd contract: inherited here it is taken for a real socket.
		"NEWGATE_LISTENER_FD", "NEWGATE_READY_FD",
		// The test shell usually has LANG=zh_CN.UTF-8 (dev) or C (runner): not clearing it
		// makes "default language = source language" green in one env, red in another.
		"LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE",
	)
	t.Cleanup(func() {
		unset()
		restore()
	})
	return env
}

// Env is one sandbox's path set.
type Env struct {
	Root    string // 临时根，所有东西都在它下面
	Home    string // NEWGATE_HOME
	Targets string // NEWGATE_TARGET_DIR
}

// Path joins a sandbox-internal path, avoiding filepath.Join everywhere in tests.
func (e *Env) Path(parts ...string) string {
	return filepath.Join(append([]string{e.Root}, parts...)...)
}

// Graph is a started component graph. It provides every assertion entrance.
type Graph struct {
	t       *testing.T
	manager *modules.Manager
}

// Start installs and starts a component graph.
//
// Pass only what you care about: real modules (`cli.New()`) and hand-written stubs mixed,
// dependencies derived from capability declarations, no manual ordering. Failure is
// t.Fatal - "the graph will not assemble" is a broken test, not the behaviour under test.
//
// The graph stops in reverse on Cleanup. **Stop is on this path too**: if a component's
// Stop misses an undo, the next Start hits a duplicate registration and fails - and this
// test finds it for you before production does.
func Start(t *testing.T, components ...modules.Component) *Graph {
	t.Helper()
	manager, err := modules.New(loader(components))
	if err != nil {
		t.Fatalf("testkit: assembling the component graph failed: %v", err)
	}
	t.Cleanup(func() {
		if err := manager.Stop(context.Background()); err != nil {
			t.Errorf("testkit: stopping the component graph failed: %v", err)
		}
	})
	return &Graph{t: t, manager: manager}
}

// Get reads a port. Missing is Fatal - a declared Need that gives nothing is mis-assembly.
func Get[T any](g *Graph, capability modules.Capability[T]) T {
	g.t.Helper()
	value, ok := modules.Get(g.manager.Context(), capability)
	if !ok {
		g.t.Fatalf("testkit: port %s is not in the graph", modules.Name(capability))
	}
	return value
}

// Maybe reads an optional port.
func Maybe[T any](g *Graph, capability modules.Capability[T]) (T, bool) {
	return modules.Get(g.manager.Context(), capability)
}

// All reads every contribution of a port.
func All[T any](g *Graph, capability modules.Capability[T]) []T {
	return modules.GetAll(g.manager.Context(), capability)
}

// Context exposes the read-only port table, for tests that look ports up themselves.
func (g *Graph) Context() modules.Context { return g.manager.Context() }

// Names returns component names in real start order, for asserting dependency order.
func (g *Graph) Names() []string { return g.manager.ComponentNames() }

// Before asserts a starts before b (a is the one depended on).
// Wrong ordering is often subtle - zero values instead of errors - so it is worth
// pinning down explicitly.
func (g *Graph) Before(a, b string) {
	g.t.Helper()
	ia, ib := -1, -1
	for i, name := range g.Names() {
		switch name {
		case a:
			ia = i
		case b:
			ib = i
		}
	}
	if ia < 0 {
		g.t.Fatalf("testkit: component %s is not in the graph (actual: %v)", a, g.Names())
	}
	if ib < 0 {
		g.t.Fatalf("testkit: component %s is not in the graph (actual: %v)", b, g.Names())
	}
	if ia >= ib {
		g.t.Fatalf("testkit: expected %s before %s, actual order %v", a, b, g.Names())
	}
}

// Stop stops the graph now (no need to wait for Cleanup). Repeated calls are harmless.
func (g *Graph) Stop() {
	if err := g.manager.Stop(context.Background()); err != nil {
		g.t.Errorf("testkit: stopping the component graph failed: %v", err)
	}
}

// ---- shared fakes ----

// FakeCatalog is AgentCatalog's minimal implementation: one map.
// Fill it with Set once; Get/Names follow.
type FakeCatalog struct {
	agents     map[string]*agentapi.Agent
	facts      map[string]agentapi.AgentFacts
	installers map[string]agentapi.AgentInstaller
}

// NewCatalog builds an empty directory.
func NewCatalog() *FakeCatalog {
	return &FakeCatalog{agents: map[string]*agentapi.Agent{}}
}

// Add registers an Agent and returns the catalog for chaining.
func (c *FakeCatalog) Add(agents ...*agentapi.Agent) *FakeCatalog {
	for _, a := range agents {
		c.agents[a.ID] = a
	}
	return c
}

// Get / Names / Facts / Installed are confighook.AgentCatalog.
func (c *FakeCatalog) Get(id string) (*agentapi.Agent, bool) {
	a, ok := c.agents[id]
	return a, ok
}

// Installer says "did this agent provide an install method". None by default; use
// WithInstaller for a fake (running npm for real turns the test into a network test).
func (c *FakeCatalog) Installer(string) agentapi.AgentInstaller { return nil }

// WithInstaller puts a fake installer on one agent.
func (c *FakeCatalog) WithInstaller(id string, in agentapi.AgentInstaller) *FakeCatalog {
	if c.installers == nil {
		c.installers = map[string]agentapi.AgentInstaller{}
	}
	c.installers[id] = in
	return c
}

// Facts says "did this agent provide runtime facts". Never in the fake - tests verify
// "no facts, default path", the **only** implementation (confighook.TierOf); copying it
// would duplicate the criterion. For the facts path, use WithFacts.
func (c *FakeCatalog) Facts(string) agentapi.AgentFacts { return nil }

// WithFacts puts fake facts on one agent (see confighook.AgentFacts).
func (c *FakeCatalog) WithFacts(id string, f agentapi.AgentFacts) *FakeCatalog {
	if c.facts == nil {
		c.facts = map[string]agentapi.AgentFacts{}
	}
	c.facts[id] = f
	return c
}

// Installed uses the real criterion (confighook.InstalledDefault), not a copy: a fake and a
// real directory answering "installed?" differently is that criterion's worst failure.
func (c *FakeCatalog) Installed(id string) bool {
	return agentapi.InstalledDefault(c.agents[id], c.facts[id])
}

// Names returns alphabetically, like the real directory (which sorts; do not rely on order).
func (c *FakeCatalog) Names() []string {
	out := make([]string, 0, len(c.agents))
	for name := range c.agents {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// AsCatalog returns the interface form, for feeding agentstate or components.
func (c *FakeCatalog) AsCatalog() agentapi.AgentCatalog { return c }

// InstallCatalog wires the fake directory onto agentstate's compat bridge, returning restore.
//
// agentstate is the legacy global bridge (launch / injection still read through it),
// installed in the graph by the runtime module. Use this to test one module without runtime.
func InstallCatalog(t *testing.T, catalog agentapi.AgentCatalog) {
	t.Helper()
	restore := agentstate.Set(catalog)
	t.Cleanup(restore)
}

// ---- internals ----

type loader []modules.Component

func (l loader) Load() ([]modules.Component, error) { return l, nil }

func setEnv(vals map[string]string) (restore func()) {
	type prior struct {
		value string
		had   bool
	}
	saved := make(map[string]prior, len(vals))
	for k, v := range vals {
		old, had := os.LookupEnv(k)
		saved[k] = prior{old, had}
		_ = os.Setenv(k, v)
	}
	return func() {
		for k, p := range saved {
			if p.had {
				_ = os.Setenv(k, p.value)
			} else {
				_ = os.Unsetenv(k)
			}
		}
	}
}

func clearEnv(keys ...string) (restore func()) {
	type prior struct {
		value string
		had   bool
	}
	saved := make(map[string]prior, len(keys))
	for _, k := range keys {
		old, had := os.LookupEnv(k)
		saved[k] = prior{old, had}
		_ = os.Unsetenv(k)
	}
	return func() {
		for k, p := range saved {
			if p.had {
				_ = os.Setenv(k, p.value)
			}
		}
	}
}
