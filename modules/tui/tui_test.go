package tui

import (
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

type fakeCatalog struct{ agents map[string]*agentapi.Agent }

func (f fakeCatalog) Get(id string) (*agentapi.Agent, bool) { a, ok := f.agents[id]; return a, ok }
func (f fakeCatalog) Names() []string {
	out := make([]string, 0, len(f.agents))
	for id := range f.agents {
		out = append(out, id)
	}
	return out
}
func (fakeCatalog) Facts(string) agentapi.AgentFacts         { return nil }
func (fakeCatalog) Installer(string) agentapi.AgentInstaller { return nil }
func (fakeCatalog) Installed(string) bool                    { return true }

func seed(t *testing.T) {
	t.Helper()
	testkit.Sandbox(t)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ds", "work"} {
		if err := store.SaveProfile(&domain.Profile{Name: name, Roles: map[string]domain.Candidates{
			"normal": {{Provider: "relay", Model: "model-" + name}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveState(&domain.State{DefaultProfile: "ds", Port: 8899}); err != nil {
		t.Fatal(err)
	}
}

// This one locks that "left picks the client, right picks the profile" really lands in
// state.Active, not only the global default - the earlier TUI did just the latter.
func TestApplyPerAgentAndFollowDefault(t *testing.T) {
	seed(t)
	cat := fakeCatalog{agents: map[string]*agentapi.Agent{
		"claude": {ID: "claude", Bin: []string{"claude"}, Dialect: "anthropic"},
	}}
	m := &model{catalog: cat, names: []string{"ds", "work"}}
	m.reload("")

	m.scopeCur = 1 // 0 = global, 1 = claude
	m.focus = 1
	m.syncProfileCur()
	// options for an agent: ["", "ds", "work"]; choose work.
	m.profileCur = 2
	m.apply()
	if got := store.LoadState().Active["claude"]; got != "work" {
		t.Fatalf("active[claude] = %q, want work", got)
	}
	if got := store.LoadState().DefaultProfile; got != "ds" {
		t.Fatalf("default = %q, want ds (per-agent must not touch it)", got)
	}

	// The first item is "follow the global default": it writes a delete key, not a same-value entry.
	m.scopeCur = 1
	m.focus = 1
	m.profileCur = 0
	m.apply()
	if _, ok := store.LoadState().Active["claude"]; ok {
		t.Fatalf("active[claude] still present: %v", store.LoadState().Active)
	}
}

func TestGlobalScopeNotOfferedFollowDefault(t *testing.T) {
	seed(t)
	m := &model{names: []string{"ds", "work"}}
	m.reload("")
	if got := len(m.options()); got != 2 {
		t.Fatalf("global options = %d, want 2", got)
	}
	m.scopeCur = 1 // no catalog -> only global exists; out of range
	if got := len(m.options()); got != 2 {
		t.Fatalf("out-of-range scope options = %d, want 2", got)
	}
}
