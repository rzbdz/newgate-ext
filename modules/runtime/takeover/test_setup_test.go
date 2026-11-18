package takeover

import (
	"os"
	"sort"
	"strings"
	"testing"

	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/runtime/agentstate"
)

type testCatalog map[string]*agentapi.Agent

func (catalog testCatalog) Get(id string) (*agentapi.Agent, bool) {
	agent, ok := catalog[id]
	return agent, ok
}

// Installer / Facts / Installed complete the catalog port (agentapi.AgentCatalog); same criterion.
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

// fakeConfigTakeover is a test-only minimal ConfigTakeover: this package tests how the
// shim/config mechanisms are chosen and switched, not a specific client plugin's rewrite
// logic, so it must not depend on optional plugin modules like opencodeomo (that would
// make runtime/takeover's tests and cli -> runtime/takeover's production dependency a
// cycle, hit live on 2026-09-17).
type fakeConfigTakeover struct{}

func (fakeConfigTakeover) Targets() []string       { return nil }
func (fakeConfigTakeover) IsTakenOver(string) bool { return false }
func (fakeConfigTakeover) Apply(int) ([]*agentapi.TakeoverReport, error) {
	return nil, nil
}
func (fakeConfigTakeover) Restore() ([]string, error) { return nil, nil }

// syntheticAgent is the kernel tests' **synthetic** client definition.
//
// Real claude / opencode definitions live in the distribution (client integration is a
// product choice, moved 2026-09-20), and this package tests "how shim / config takeover
// is chosen and switched" - it wants "an agent with config takeover", not "what claude
// looks like". Synthetic is steadier: ids stay claude/opencode just for readable assertions.
func syntheticAgent(id, dialect string) *agentapi.Agent {
	upper := strings.ToUpper(id)
	return &agentapi.Agent{
		ID:         id,
		Bin:        []string{id},
		Dialect:    dialect,
		BaseURLEnv: upper + "_BASE_URL",
		AuthEnv:    upper + "_API_KEY",
		// Config empty = this client is taken over by the **shim** (PATH prepend); to test
		// the config rewrite, the caller supplies one (see open in TestMain).
	}
}

func TestMain(m *testing.M) {
	claude := syntheticAgent("claude", "anthropic")
	open := syntheticAgent("opencode", "openai")
	open.Config = fakeConfigTakeover{}
	restore := agentstate.Set(testCatalog{claude.ID: claude, open.ID: open})
	code := m.Run()
	restore()
	os.Exit(code)
}
