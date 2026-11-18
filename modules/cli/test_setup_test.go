package cli

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

// Installer / Facts / Installed fill in the catalog port (see agentapi.AgentCatalog):
// these tests only care about "which agents exist", with no facts at all, and
// installed-or-not goes the default path (the same test as the real implementation).
func (catalog testCatalog) Facts(string) agentapi.AgentFacts { return nil }

// Installer: these tests do not check installation, so it
// is always absent (which means no self-service install).
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

// TestMain installs a minimal client catalog for cli's tests.
//
// **The fields are hand-written, not claudecode.Agent() / opencode.Agent()** (changed 2026-09-18):
// cli's tests only need the fact "there are two clients with ids claude and opencode", not those
// two modules' real descriptors. And importing them was once **half of a two-way dependency** --
// cli's internal tests imported claudecode, while claudecode needed to import cliapi to register
// its own command (naked), so cli ⇄ claudecode formed a cycle and failed to compile outright.
//
// This is not "dodging a compile error for convenience", it is lowering the
// dependency to the level the tests really need: if someone makes cli's tests use the
// real descriptors (say asserting claude's slots), that means what is being asserted
// actually belongs to the claudecode module's own tests and should move there.
func TestMain(m *testing.M) {
	claude := &agentapi.Agent{ID: "claude", Bin: []string{"claude"}}
	open := &agentapi.Agent{ID: "opencode", Bin: []string{"opencode"}}
	restore := agentstate.Set(testCatalog{claude.ID: claude, open.ID: open})
	code := m.Run()
	restore()
	os.Exit(code)
}
