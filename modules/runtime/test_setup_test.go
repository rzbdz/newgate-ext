package runtime

import (
	"os"
	"sort"
	"testing"

	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/runtime/agentstate"
)

type testCatalog map[string]*agentapi.Agent

func (catalog testCatalog) Get(id string) (*agentapi.Agent, bool) {
	agent, ok := catalog[id]
	return agent, ok
}

// Installer / Facts / Installed complete the catalog port (see agentapi.AgentCatalog);
// these tests only care about "which agents exist", and installation falls to the default.
func (catalog testCatalog) Facts(string) agentapi.AgentFacts { return nil }

// Installer: these tests do not verify installs, so none (no self-install offered).
func (catalog testCatalog) Installer(string) agentapi.AgentInstaller { return nil }

func (catalog testCatalog) Installed(id string) bool {
	return agentapi.InstalledDefault(catalog[id], nil)
}

func (catalog testCatalog) Names() []string {
	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TestMain installs a minimal client catalog for runtime's tests (launch looks clients up by id).
//
// **The fields are hand-written, not claudecode.Agent() / opencode.Agent()** (changed
// 2026-09-18): runtime's tests only need "two clients with ids claude and opencode", not
// their real descriptors. Importing them was **half of a two-way dependency** - cli's
// internal tests imported claudecode, and claudecode imported cliapi to register its own
// command (naked), so cli <-> claudecode was a cycle and did not compile.
//
// This is not "dodging a compile error", but lowering the dependency to what the test
// really needs: if someone makes cli's tests use real descriptors (asserting claude's
// slots, say), what is asserted belongs to claudecode's own tests, and should move there.
func TestMain(m *testing.M) {
	claude := &agentapi.Agent{ID: "claude", Bin: []string{"claude"}}
	open := &agentapi.Agent{ID: "opencode", Bin: []string{"opencode"}}
	restore := agentstate.Set(testCatalog{claude.ID: claude, open.ID: open})
	code := m.Run()
	restore()
	os.Exit(code)
}
