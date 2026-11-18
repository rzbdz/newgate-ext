package breaker

import (
	"context"

	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	gatewayapi "github.com/rzbdz/newgate-ext/modules/gateway"
	pluginmanagerapi "github.com/rzbdz/newgate-ext/modules/pluginmanager"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// New declares the health table component.
//
// **Dependency direction (flipped 2026-09-18)**: it now declares `Need(gateway)`, because
// it is a **consumer** of the data plane -- the health table is a stateful, owned service, and it
// plugs itself into the four decision points gateway leaves open (see plane.go). Before that the
// direction was reversed: gateway declared Need(breaker), stuffed a breaker.Breaker into a field
// of forward.Server to use as a lib, and the hot path wrote `breaker.BucketShape`. By the test in
// docs/03-architecture.md §2 ("does it need to be started, stopped, released?"), that was wrong.
//
// It knows no agent, profile, or upstream protocol: the health
// table only knows binding keys, status codes, and latency.
//
// Failing to load health.json **does not** fail assembly: a broken health table should
// only degrade to pure memory, and a regenerable cache file must not keep the whole proxy from
// starting. The error is stored and reported once BindEnv installs the log outlet ("never silent").
func New() modules.Component {
	table := newTable()
	var releases []modules.Release
	return modules.Component{
		Name: "breaker",
		Desc: func() string {
			return i18n.T("which bindings are usable right now (health + latency); the gateway asks before each hop", nil)
		},
		Type: "infra",
		Requires: []modules.Requirement{
			// The data plane is the owner; I plug myself into its outlets.
			// This edge is also the startup-order edge -- gateway ->
			// breaker -> deepseek (deepseek needs my shape-detector outlet).
			modules.Need(gatewayapi.Capability),
			// The switch system: this module reports the pause switch (see switches.go) and
			// reads it back on the admission path. The direction is breaker -> plugin-manager,
			// never the other way -- the ledger lists modules, it does not know their business.
			modules.Need(pluginmanagerapi.Capability),
			// ui is a **weak dependency** (see component.Optional): mount `newgate breaker` if a UI is
			// installed, skip it if not -- the health table itself works as usual, just with no entry point.
			modules.Optional(cliapi.Capability),
			// Same for the web UI, and it is a **separate** edge: in an assembly with only
			// the dashboard (no cli), this table should still show up on the web page.
			modules.Optional(viewapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Breaker(table)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			// Loading is ours: health state survives a graceful restart.
			_ = table.UseFile(paths.HealthFile())

			// The **write side**: plug myself into the data plane. Start only does "start my
			// own service + register my contribution with the owner", and does not read what
			// others registered (see the three-phase rule in docs/02-component-framework.md)
			// -- the registry is offered, and the moment to read it is during Serve.
			gateway := modules.MustGet(ctx, gatewayapi.Capability)
			release, err := gateway.RegisterFilter(&plane{t: table})
			if err != nil {
				return err
			}
			releases = append(releases, release)

			// Report the pause switch. Not optional: this is the only way out of "a network
			// wobble delisted everything", so an assembly that silently lacks the port would
			// ship the failure mode with no cure (hence Need above, not Optional).
			pm := modules.MustGet(ctx, pluginmanagerapi.Capability)
			self, err := pm.RegisterSelf("breaker", Switches())
			if err != nil {
				return err
			}
			releases = append(releases, self)

			// The web UI copy registers **first**: it does not depend on cli and must be there in
			// an assembly with only the dashboard -- once the block below returns, this would never
			// run (same as gateway). The seam for probing one binding is provided by the gateway
			// (see gateway.ProbeBinding): what starting a probe needs (base, key, dialect) only it
			// has. Need(gateway) is this module's existing dependency, so it is guaranteed here.
			prober := modules.MustGet(ctx, gatewayapi.Capability)

			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				viewRelease, err := v.Register("breaker",
					viewapi.Title(func() string { return i18n.T("Breaker", nil) }).
						In(func() string { return i18n.T("Data plane", nil) }).
						Of(viewapi.FieldRoutes),
					func() ([]viewapi.Concept, error) {
						return healthConcepts(table, prober)
					})
				if err != nil {
					return err
				}
				releases = append(releases, viewRelease)
			}

			ui, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			cmdRelease, err := ui.RegisterCommand(breakerCommand{})
			if err != nil {
				return err
			}
			releases = append(releases, cmdRelease)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
