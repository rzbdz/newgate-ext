// Package wrapper owns the policy of the "PATH shim takeover" path.
//
// # Why a module and not a few lines in cmd
//
// After takeover: the user types `claude` -> the kernel execs the **symlink named
// claude** in PATH (pointing at newgate) -> our process wakes, argv[0] is `claude`.
// It has two halves:
//
//	install the link: `newgate on claude` creates the symlink (runtime/injection bytes)
//	parse:           the woken process recognizes "I run in place of claude", injects env,
//	                 then execs the real claude (was argv0 guessing in cmd/newgate)
//
// Two halves apart means a silent broken link: link installed, nobody parses argv0, and
// `claude` starts without going through newgate - users think takeover works. So they are
// one module, and the module's name is the name of the thing.
//
// Dependency direction: wrapper -> runtime (needs Launch), wrapper -> confighook (needs the
// Agent descriptor by name). Not the reverse - runtime does not know shims exist; it only
// "given an Agent, launch it".
package wrapper

import (
	"context"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	runtimeapi "github.com/rzbdz/newgate-ext/modules/runtime"
	"github.com/rzbdz/newgate-ext/modules/runtime/injection"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
)

type service struct {
	agents  agentapi.AgentCatalog
	runtime runtimeapi.Runtime
	entry   modules.Release
}

var (
	_ Wrapper       = (*service)(nil)
	_ entry.Handler = (*service)(nil)
)

// New declares the shim takeover component. It offers no cross-module extension point -
// outside needs only "dispatch one call" and "install/remove a link"; no third registers.
func New() modules.Component {
	instance := &service{}
	return modules.Component{
		Name: "wrapper",
		Desc: func() string { return i18n.T("argv0 dispatch: what runs when we are called by a client's name", nil) },
		Type: "client",
		Requires: []modules.Requirement{
			modules.Need(runtimeapi.Capability),
			modules.Need(agentapi.AgentCatalogCapability),
			// The entry ledger (provided by root). **Declared not for ordering** - root has no
			// out-edges; order is moot. It is declared to make "root is the only built-in"
			// checkable: remove root and graph construction fails on the spot, naming the port,
			// instead of starting a process that discovers on its first call that nobody
			// claimed it (see app/matrix_test.go's removal matrix).
			modules.Need(entry.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Wrapper(instance)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			instance.agents = modules.MustGet(ctx, agentapi.AgentCatalogCapability)
			instance.runtime = modules.MustGet(ctx, runtimeapi.Capability)
			// Declares itself a **conditional** entry (entry.RankShim): when argv0 is a
			// taken-over client, ask this module first; otherwise pass and let the default take it.
			//
			// Why wrapper must do this itself: which names count as clients, when to claim a
			// call, is this module's policy. It used to live in cmd/newgate/main.go - criterion
			// and data apart, and the composition root imported this module (uncompilable).
			registry := modules.MustGet(ctx, entry.Capability)
			release, err := registry.Register(instance, entry.RankShim)
			if err != nil {
				return err
			}
			instance.entry = release
			return nil
		},
		Stop: func(context.Context) error {
			if instance.entry == nil {
				return nil
			}
			return instance.entry()
		},
	}
}

// Claims, see entry.Handler: claim when argv0 (basename, .exe stripped) is a taken-over client.
//
// The criterion reads only Process, never os.Args - "how this process was invoked" is
// installed by the composition root and handed in (see entry.Process's comment).
func (s *service) Claims(p entry.Process) bool {
	if p.Argv0 == "" {
		return false
	}
	_, ok := s.agents.Get(p.Argv0)
	return ok
}

// Handle runs this call on the client's behalf. Success never returns: syscall.Exec swaps us.
func (s *service) Handle(p entry.Process) int {
	agent, ok := s.agents.Get(p.Argv0)
	if !ok {
		return 1
	}
	// Empty profile = dynamic mode (inject the tier name, not the real model): argv0
	// dispatch's established semantics - no profile pinned, the proxy resolves from the
	// config current at request time. facts come from the directory port: which slot a
	// tier uses now is **the client module's** knowledge (mappings live there); pass, no interpret.
	return s.runtime.Launch(agent, s.agents.Facts(agent.ID), p.Args, "")
}

// Name goes into logs and diagnostics.
func (s *service) Name() string { return "wrapper" }

// Install/Uninstall/Installed/Foreign forward the shim bytes to injection.
//
// Deliberately not capability ports: their consumers are CLI commands (newgate on/off/
// shim), package-level calls, not inter-component dependencies. What this module provides
// is **ownership** - shims are wrapper's, so others ask wrapper before touching them,
// instead of each poking the injection library.
func (*service) Install(agentID string) (string, error) { return injection.Install(agentID) }
func (*service) Uninstall(agentID string) error         { return injection.Uninstall(agentID) }
func (*service) Installed() []string                    { return injection.Installed() }
func (*service) Foreign() []string                      { return injection.Foreign() }
