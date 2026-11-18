package testkit_test

import (
	"testing"

	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// Compile-time pin: the fake must track the real interface. A new method not mirrored here
// fails the whole package's build, instead of a case silently getting zero values.
var _ agentapi.AgentCatalog = (*testkit.FakeCatalog)(nil)

func TestSandboxIsolatesState(t *testing.T) {
	env := testkit.Sandbox(t)
	if got := env.Home; got == "" {
		t.Fatal("the sandbox did not set NEWGATE_HOME")
	}
	if env.Targets == env.Home {
		t.Fatal("NEWGATE_TARGET_DIR must not be the same path as NEWGATE_HOME")
	}
}

func TestFakeCatalogNamesAreSorted(t *testing.T) {
	catalog := testkit.NewCatalog().Add(
		&agentapi.Agent{ID: "opencode"},
		&agentapi.Agent{ID: "claude"},
	)
	names := catalog.Names()
	if len(names) != 2 || names[0] != "claude" || names[1] != "opencode" {
		t.Fatalf("Names() = %v, want alphabetical [claude opencode]", names)
	}
	if _, ok := catalog.Get("claude"); !ok {
		t.Fatal("Get(claude) not found")
	}
	if _, ok := catalog.Get("nope"); ok {
		t.Fatal("Get(nope) should not be found")
	}
}
