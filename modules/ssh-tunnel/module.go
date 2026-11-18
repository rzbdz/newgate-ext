package sshtunnel

import (
	"context"
	"net/http"

	servingapi "github.com/rzbdz/newgate-ext/lib/serving"
	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	porthubapi "github.com/rzbdz/newgate-ext/modules/porthub"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Name is this module's name in the assembly table, the concept ledger and `newgate plugin`.
//
// It is also the key of its cell in state.json's ModuleConfig (see StateKey).
const Name = "ssh-tunnel"

// New declares this component.
//
// Deps are **all Optional**: with no UI, no shared port, no command line it still holds -
// its function is "a channel through SSH", and a channel needs no onlooker. Same rule as
// the kernel's "business modules depend on no ui": whoever operates it installs one.
//
// It also **declares no config dependency**: it reads its own cell in state.json, which
// is `store.LoadState()` reading disk, not the config component's semantics (same path
// as web-dashboard reading skin choice).
func New() modules.Component {
	p := newPool(newSSHDialer())
	m := newManager(p, Load)
	// One proxy instance goes onto the shared port and to the manager (which closes its
	// idle connections at shutdown, see manager.shutdown). Built here, not in Start: Start
	// runs on every invocation, and a connection pool should be **one per process**.
	px := newProxy(p, Load)
	m.proxy = px

	var (
		mount    func()
		releases []modules.Release
	)
	return modules.Component{
		Name: Name,
		Desc: func() string {
			return i18n.T("reaches a remote machine's newgate interface over SSH", nil)
		},
		// "Infrastructure" class: not a UI, not a client, not a model, and it handles no
		// forwarded request. It is a **channel** - same tier as porthub / i18n.
		Type: "infra",
		Requires: []modules.Requirement{
			// The shared-port route (the norm). Absent, the module still works: the local
			// forwarded port goes through serving, and the two routes are independent.
			modules.Optional(porthubapi.Capability),
			// Local forwarded ports + the background loop. **Only the daemon calls it** (see
			// the manager note), so short-lived CLI processes do not spawn a pile of things.
			modules.Optional(servingapi.Capability),
			modules.Optional(cliapi.Capability),
			modules.Optional(viewapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			// Mounting is in-process registration (no socket, no goroutine), so mounting in a
			// CLI process is harmless - nothing reads that table. Same reason and shape as
			// web-dashboard: porthub **does not strip the prefix**, we strip outside the handler.
			if hub, ok := modules.Get(ctx, porthubapi.Capability); ok {
				rel, err := hub.Mount(Prefix, Name, http.StripPrefix(Prefix, px))
				if err != nil {
					return err
				}
				mount = rel
			}
			// Forwarded ports and the background loop: registered on serving, waiting for the
			// process that owns the port to say "I am serving". **Not** directly in Start -
			// that road ends in "one `newgate status` starts a server".
			if listeners, ok := modules.Get(ctx, servingapi.Capability); ok {
				rel, err := listeners.OnServe(Name, m.Serve)
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}
			if ui, ok := modules.Get(ctx, cliapi.Capability); ok {
				rel, err := ui.RegisterCommand(&tunnelCommand{m: m, pool: p})
				if err != nil {
					return err
				}
				releases = append(releases, rel)
				// One line in status, one check item per target. Both read memory only, no dialing.
				if rel, err := ui.RegisterStatus(tunnelStatus{m}); err != nil {
					return err
				} else {
					releases = append(releases, rel)
				}
				if rel, err := ui.RegisterDiagnostics(tunnelDiagnostics{m}); err != nil {
					return err
				} else {
					releases = append(releases, rel)
				}
			}
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				rel, err := registerView(v, m)
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}
			return nil
		},
		Stop: func(context.Context) error {
			err := modules.ReleaseAll(releases)
			releases = nil
			if mount != nil {
				mount()
				mount = nil
			}
			return err
		},
	}
}
