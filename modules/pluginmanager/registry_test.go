package pluginmanager

import (
	"strings"
	"testing"
	"time"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// stubCLI lets plugin-manager's Start finish: it injects its command and status line.
// The UI's command dedup, diagnostics and help assembly are the UI's own tests' job.
type stubCLI struct {
	commands []cliapi.Command
	statuses []cliapi.StatusProvider
}

func (s *stubCLI) Run(entry.Process) int { return 0 }

func (s *stubCLI) RegisterCommand(c cliapi.Command) (modules.Release, error) {
	s.commands = append(s.commands, c)
	return func() error { return nil }, nil
}

func (s *stubCLI) RegisterDiagnostics(cliapi.DiagnosticProvider) (modules.Release, error) {
	return func() error { return nil }, nil
}

func (s *stubCLI) RegisterStatus(p cliapi.StatusProvider) (modules.Release, error) {
	s.statuses = append(s.statuses, p)
	return func() error { return nil }, nil
}

func (s *stubCLI) RegisterStatusBlocks(cliapi.BlockProvider) (modules.Release, error) {
	return func() error { return nil }, nil
}

func (s *stubCLI) RegisterDump(cliapi.Dumper) (modules.Release, error) {
	return func() error { return nil }, nil
}

func (s *stubCLI) RegisterGlossary(cliapi.Glossarist) (modules.Release, error) {
	return func() error { return nil }, nil
}

func (s *stubCLI) RegisterVerbose(cliapi.Verbose) (modules.Release, error) {
	return func() error { return nil }, nil
}

// stubHooks lets plugin-manager's Start finish. The real confighook does field dedup,
// which its own tests cover; here only plugin-manager's own ledger is at stake.
type stubHooks struct{ fields map[string]string }

func newStubHooks() *stubHooks { return &stubHooks{fields: map[string]string{}} }

func (s *stubHooks) RegisterAgent(*confighookapi.Agent) (modules.Release, error) {
	return func() error { return nil }, nil
}

func (s *stubHooks) RegisterAgentInstaller(string, confighookapi.AgentInstaller) (modules.Release, error) {
	return func() error { return nil }, nil
}

func (s *stubHooks) RegisterAgentFacts(string, confighookapi.AgentFacts) (modules.Release, error) {
	return func() error { return nil }, nil
}

func (s *stubHooks) BindTakeover(string, confighookapi.ConfigTakeover) (modules.Release, error) {
	return func() error { return nil }, nil
}

func (s *stubHooks) RegisterStateField(owner, name string) (modules.Release, error) {
	s.fields[owner] = name
	return func() error { return nil }, nil
}

// start builds the minimal real graph "plugin-manager + its confighook dependency".
func start(t *testing.T) Manager {
	t.Helper()
	testkit.Sandbox(t)
	hooks := newStubHooks()
	graph := testkit.Start(t,
		modules.Component{
			Name:     "stub-confighook",
			Type:     "infra",
			Provides: []modules.Provision{modules.Provide(confighookapi.ConfigHooksCapability, confighookapi.ConfigHooks(hooks))},
		},
		// Hand-written stub: the ledger lives on modules/cli's service, and cli is a heavy
		// package (runtime / gateway / breaker), which this module's tests cannot import -
		// importing it is a cycle. Real dedup is guarded by modules/cli's own tests.
		modules.Component{
			Name:     "stub-cli",
			Type:     "cli",
			Provides: []modules.Provision{modules.Provide(cliapi.Capability, cliapi.CLI(&stubCLI{}))},
		},
		New(),
	)
	if hooks.fields["plugin-manager"] != StateKey {
		t.Fatalf("Start did not register the state field, got %v", hooks.fields)
	}
	return testkit.Get(graph, Capability)
}

func goodSwitch(path string) Switch {
	return Switch{Path: path, Title: "title", Why: "reason", Danger: DangerQuirk, Default: true}
}

func TestRegisterSelfRejectsBadInput(t *testing.T) {
	m := start(t)

	cases := []struct {
		name     string
		module   string
		switches []Switch
		want     string
	}{
		{"empty module name", "", nil, "must not be empty"},
		{"duplicate report", "plugin-manager", nil, "already reported"},
		{"Path does not belong to this module", "deepseek", []Switch{goodSwitch("thinking.always-thinks")}, "must start with the module name"},
		{"Path equals only the module name", "deepseek", []Switch{goodSwitch("deepseek")}, "must start with the module name"},
		{"Title/Why not written", "deepseek", []Switch{{Path: "deepseek.x", Danger: DangerQuirk}}, "Title"},
		{
			"a footgun without a time limit", "deepseek",
			[]Switch{{Path: "deepseek.x", Title: "t", Why: "w", Danger: DangerFootgun}},
			"must carry a default time limit",
		},
		{
			"unknown Danger", "deepseek",
			[]Switch{{Path: "deepseek.x", Title: "t", Why: "w", Danger: "dangerous"}},
			"unknown Danger",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			release, err := m.RegisterSelf(c.module, c.switches)
			if err == nil {
				if release != nil {
					_ = release()
				}
				t.Fatalf("expected a rejection, yet it registered")
			}
			// It asserts **message identity** (the English source), not the rendered text:
			// another language or a reword must not turn this test red (lib/i18n's Error).
			if !strings.Contains(i18n.ID(err), c.want) {
				t.Fatalf("the error identity %q does not contain %q", i18n.ID(err), c.want)
			}
		})
	}
}

func TestRegisterSelfRejectsCrossModulePathCollision(t *testing.T) {
	m := start(t)
	// Two modules report the same Path. The second must fail on the spot - silent
	// first-come-first-served hides "who owns this switch" from the list.
	ok, err := m.RegisterSelf("deepseek", []Switch{goodSwitch("deepseek.tail-shape")})
	if err != nil {
		t.Fatalf("the first registration must not fail: %v", err)
	}
	defer func() { _ = ok() }()

	// A different module name, its own Path prefix, colliding with an existing Path.
	if _, err := m.RegisterSelf("gateway", []Switch{goodSwitch("deepseek.tail-shape")}); err == nil {
		t.Fatal("a cross-module Path collision should be rejected")
	}
}

func TestRegisterSelfReleaseIsSymmetric(t *testing.T) {
	m := start(t)
	before := len(m.Modules())

	release, err := m.RegisterSelf("deepseek", []Switch{goodSwitch("deepseek.tail-shape")})
	if err != nil {
		t.Fatalf("registration failed: %v", err)
	}
	if _, ok := m.Lookup("deepseek.tail-shape"); !ok {
		t.Fatal("after registering, Lookup should find it")
	}

	if err := release(); err != nil {
		t.Fatalf("revoking failed: %v", err)
	}
	if _, ok := m.Lookup("deepseek.tail-shape"); ok {
		t.Fatal("after revoking, Lookup must not find it")
	}
	if got := len(m.Modules()); got != before {
		t.Fatalf("modules after revoking = %d, want %d", got, before)
	}
	// A stale Release must be an idempotent no-op (Stop may be called more than once).
	if err := release(); err != nil {
		t.Fatalf("a repeated revoke should be a no-op, but it errored: %v", err)
	}
}

func TestFootgunWithTTLIsAccepted(t *testing.T) {
	m := start(t)
	release, err := m.RegisterSelf("gateway", []Switch{{
		Path: "gateway.passthrough", Title: "pass through", Why: "bypass all patches",
		Danger: DangerFootgun, Default: false, TTL: 5 * time.Minute,
	}})
	if err != nil {
		t.Fatalf("a footgun with a time limit should register: %v", err)
	}
	defer func() { _ = release() }()

	sw, ok := m.Lookup("gateway.passthrough")
	if !ok {
		t.Fatal("Lookup failed")
	}
	if sw.TTL != 5*time.Minute || sw.Default {
		t.Fatalf("the field was not preserved: %+v", sw)
	}
}

// TestModulesEnumerateUnregisteredOnes is the foundation of "newgate plugin lists all
// plugins": modules outside the switch system must be listed too. The criterion is the
// graph, not "who reported" - else "no switches" and "forgot to register" look alike.
func TestModulesEnumerateUnregisteredOnes(t *testing.T) {
	m := start(t)
	// The list is handed over once by the **kernel** after assembly (component.CatalogAware);
	// a type assertion here calls the same method directly, since "a graph is handed in"
	// only ever happens in the kernel in production; an exported entry would leave two paths.
	//
	// What is handed in is the whole graph - plugin-manager itself included (it registers
	// itself via RegisterSelf in Start too, same door, no back door).
	catalog := m.(interface{ SetCatalog([]modules.Component) })
	catalog.SetCatalog([]modules.Component{
		{Name: "config", Type: "infra"},
		{Name: "plugin-manager", Type: "infra"},
		{Name: "deepseek", Type: "model"},
		{Name: "wrapper", Type: "client"},
	})
	if _, err := m.RegisterSelf("deepseek", []Switch{goodSwitch("deepseek.tail-shape")}); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	got := map[string]Module{}
	var order []string
	for _, mod := range m.Modules() {
		got[mod.Name] = mod
		order = append(order, mod.Name)
	}
	if len(got) != 4 {
		t.Fatalf("it should list 4 modules, got %v", order)
	}
	// Order = component graph order (start order), not registration order.
	want := []string{"config", "plugin-manager", "deepseek", "wrapper"}
	for i, name := range want {
		if order[i] != name {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
	if len(got["wrapper"].Switches) != 0 {
		t.Fatal("a module that did not report should have no switch points")
	}
	if got["wrapper"].Type != "client" {
		t.Fatalf("Type = %q", got["wrapper"].Type)
	}
	if len(got["deepseek"].Switches) != 1 {
		t.Fatalf("the reported switch points were not merged in: %+v", got["deepseek"])
	}
	// Reported but not in the graph: listed explicitly, not silently swallowed (app-level
	// tests catch this config error - name vs component). Here: plugin-manager itself + deepseek.
	catalog.SetCatalog(nil)
	if n := len(m.Modules()); n != 2 {
		t.Fatalf("with an empty graph it should list the 2 modules that reported (plugin-manager + deepseek), got %d", n)
	}
}

func TestDisplayOrderHasNoDuplicates(t *testing.T) {
	seen := map[Type]bool{}
	for _, typ := range DisplayOrder() {
		if seen[typ] {
			t.Fatalf("%q appears twice in DisplayOrder - the same group would render as two sections", typ)
		}
		seen[typ] = true
	}
	if !seen[TypeOthers] {
		t.Fatal("DisplayOrder must contain others: categories not in the table rely on it as the fallback")
	}
}
