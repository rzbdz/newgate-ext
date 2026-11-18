package wrapper_test

import (
	"reflect"
	"testing"

	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	entrymod "github.com/rzbdz/newgate-ext/modules/entry"
	runtimeapi "github.com/rzbdz/newgate-ext/modules/runtime"
	wrapperapi "github.com/rzbdz/newgate-ext/modules/wrapper"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
)

// launchCall records the arguments of one Runtime.Launch.
type launchCall struct {
	agent   string
	args    []string
	profile string
}

// fakeRuntime is a stub for the Runtime port: records calls, execs nothing.
// The real Launch syscall.Execs the whole test process away, so a stub is mandatory.
type fakeRuntime struct{ calls []launchCall }

func (f *fakeRuntime) Launch(agent *agentapi.Agent, _ agentapi.AgentFacts, args []string, profile string) int {
	id := ""
	if agent != nil {
		id = agent.ID
	}
	f.calls = append(f.calls, launchCall{agent: id, args: args, profile: profile})
	return 0
}

var _ runtimeapi.Runtime = (*fakeRuntime)(nil)

// harness assembles the minimal graph "wrapper + its two dependencies".
//
// This is why testkit exists: the module under test need not know where deps come from;
// its Start works if the graph provides the ports. Here runtime is stubbed (real Launch
// execs the process), but the wrapper module itself is **real** - Requires/Provides/Start.
func harness(t *testing.T, agents ...*agentapi.Agent) (entry.Registry, *fakeRuntime) {
	t.Helper()
	runtime := &fakeRuntime{}
	catalog := testkit.NewCatalog().Add(agents...)
	graph := testkit.Start(t,
		modules.Component{
			Name:     "stub-runtime",
			Type:     "test",
			Provides: []modules.Provision{modules.Provide(runtimeapi.Capability, runtimeapi.Runtime(runtime))},
		},
		modules.Component{
			Name:     "stub-catalog",
			Type:     "test",
			Provides: []modules.Provision{modules.Provide(agentapi.AgentCatalogCapability, catalog.AsCatalog())},
		},
		// The entry ledger's provider is modules/entry - the one module the kernel knows and
		// cannot remove: this module declares into it in Start, so the test graph needs it -
		// missing, it should panic on the spot: that is "the declaration door is gone".
		entrymod.New(),
		wrapperapi.New(),
	)
	// Dependency order: wrapper must start after both providers, or MustGet panics in Start.
	graph.Before("stub-runtime", "wrapper")
	graph.Before("stub-catalog", "wrapper")
	// What is handed out is **the entry ledger**, not the Wrapper interface: this module's
	// process side now says "I declared an entry once", and the test walks that way (ask
	// the ledger -> claimant -> call it), not poking the module directly.
	return testkit.Get(graph, entry.Capability), runtime
}

func TestDispatchClaimsAgentInvocations(t *testing.T) {
	registry, runtime := harness(t,
		&agentapi.Agent{ID: "claude"},
		&agentapi.Agent{ID: "opencode"},
	)

	tests := []struct {
		name     string
		argv     []string
		claimed  bool
		wantID   string
		wantArgs []string
	}{
		{
			name:     "an absolute path passed in by the PATH shim",
			argv:     []string{"/home/u/.config/newgate/bin/claude", "--resume", "abc"},
			claimed:  true,
			wantID:   "claude",
			wantArgs: []string{"--resume", "abc"},
		},
		{
			name:     "argv0 is the bare name",
			argv:     []string{"opencode"},
			claimed:  true,
			wantID:   "opencode",
			wantArgs: []string{},
		},
		{
			// The npm-installed fallback script appears as claude.exe; stripping is historical.
			// Use a POSIX path, not `C:\bin\claude.exe`: backslash is a separator only on
			// Windows; on Linux filepath.Base keeps the whole string - that tests Windows
			// semantics and always fails on Linux: a broken test, not broken code.
			name:     "a Windows-style .exe suffix must be stripped",
			argv:     []string{"/usr/local/bin/claude.exe", "hi"},
			claimed:  true,
			wantID:   "claude",
			wantArgs: []string{"hi"},
		},
		{
			name:     "a bare .exe name should be recognized too",
			argv:     []string{"claude.exe"},
			claimed:  true,
			wantID:   "claude",
			wantArgs: []string{},
		},
		{
			name:    "the control CLI itself must not be claimed",
			argv:    []string{"/usr/local/bin/newgate", "status"},
			claimed: false,
		},
		{
			name:    "an unknown command name must not be claimed",
			argv:    []string{"/usr/bin/ls", "-la"},
			claimed: false,
		},
		{
			// Empty argv: theory says the kernel never gives it; defend anyway (e.g. hand-built).
			name:    "empty argv does not panic",
			argv:    nil,
			claimed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(runtime.calls)
			// argv0 normalization is **the composition root's pass** (entry.MakeProcess), so
			// use the real pipeline, not hand-built Process: absolute paths and .exe forms must
			// be normalized to actually be under test.
			p := entry.MakeProcess(tt.argv, nil, "", "", "")
			handler, why, claimed := registry.Resolve(p)
			if claimed != tt.claimed {
				t.Fatalf("Resolve(%v) claimed = %v, want %v（%s）", tt.argv, claimed, tt.claimed, why)
			}
			if !tt.claimed {
				if len(runtime.calls) != before {
					t.Fatalf("Launch was called although not claimed: %v", runtime.calls[before:])
				}
				return
			}
			if handler.Name() != "wrapper" {
				t.Fatalf("claimer = %q, want wrapper", handler.Name())
			}
			if code := handler.Handle(p); code != 0 {
				t.Fatalf("Handle exit code = %d, want 0", code)
			}
			if len(runtime.calls) != before+1 {
				t.Fatalf("claimed, but Launch call count = %d, want %d", len(runtime.calls)-before, 1)
			}
			call := runtime.calls[before]
			if call.agent != tt.wantID {
				t.Errorf("Launch agent = %q, want %q", call.agent, tt.wantID)
			}
			if !reflect.DeepEqual(call.args, tt.wantArgs) {
				t.Errorf("Launch args = %v, want %v", call.args, tt.wantArgs)
			}
			// argv0 dispatch is **dynamic mode**: no pinned profile; the proxy resolves from
			// the config current at request time. Pinning would make --set-profile inert for live sessions.
			if call.profile != "" {
				t.Errorf("Launch profile = %q, want empty (argv0 dispatch must use dynamic mode)", call.profile)
			}
		})
	}
}

// TestDispatchStripsArgv0 pins "argv0 does not enter the passthrough args".
// The real launch is syscall.Exec(real, [real]+args, env); an extra argv0 makes
// claude treat `claude` as the directory to open.
func TestDispatchStripsArgv0(t *testing.T) {
	registry, runtime := harness(t, &agentapi.Agent{ID: "claude"})
	p := entry.MakeProcess([]string{"claude", "a", "b"}, nil, "", "", "")
	handler, why, ok := registry.Resolve(p)
	if !ok {
		t.Fatalf("no entry point claimed %v (%s)", p.Argv0, why)
	}
	handler.Handle(p)
	if len(runtime.calls) != 1 {
		t.Fatalf("Launch call count = %d, want 1", len(runtime.calls))
	}
	if got := runtime.calls[0].args; !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("passthrough args = %v, want [a b] (argv0 must not appear)", got)
	}
}
