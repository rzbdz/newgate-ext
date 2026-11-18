// Package gateway implements "pick a route and forward one model request" as a standalone data
// plane.
//
// A request first resolves its candidate chain from the profile, then tries each binding in
// turn. Upstream differences must not scatter along this hot path, so gateway exposes only a
// Plugin registration port: models, clients and cross-cutting components each register the
// patches that hold only for them, and the gateway handles ordering, execution, note recording
// and fail-open uniformly.
//
// The component layer owns only the plugin registry. The HTTP server, retries, health state,
// protocol assembly and byte rewriting still belong to this module's internal packages; the
// generic component kernel understands no request concepts.
package gateway

import (
	"context"
	"strings"
	"time"

	servingapi "github.com/rzbdz/newgate-ext/lib/serving"
	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	configapi "github.com/rzbdz/newgate-ext/modules/config"
	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	porthubapi "github.com/rzbdz/newgate-ext/modules/porthub"
	modules "github.com/rzbdz/newgate/component"
	entryapi "github.com/rzbdz/newgate/component/entry"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate-ext/modules/gateway/policy"
	"github.com/rzbdz/newgate-ext/modules/gateway/probe"
	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
)

type port struct {
	registry *special.Registry
	// filters is the data-plane policy ledger. It is **not carried on port itself for outsiders
	// to see** -- the Gateway interface exposes only RegisterFilter, and on the read side only
	// forward.Server (during Serve) and handleStatus use it.
	filters *policy.Registry
}

var _ Gateway = (*port)(nil)

// New declares the gateway control-plane component. The Need on Config expresses a real
// dependency and ensures all config semantics are ready before plugins may enter the request path.
//
// It declares only **Optional** against ui (a weak dependency, see component.Optional):
// `newgate st` / `metrics` / `probe` are this layer's user interface, and plugin names and
// switch semantics are this module's knowledge, so the commands are registered by this module
// itself (see the note in command_special.go). Optional instead of Need is required -- the
// gateway must not fail to start because "no UI is installed"; while the edge to the UI is a
// **sorting edge**, ensuring this module's Start runs when the UI is already ready.
func New() modules.Component {
	port := &port{registry: special.NewRegistry(), filters: policy.New()}
	var restore func()
	var restoreFilters func()
	var releases []modules.Release
	return modules.Component{
		Name: "gateway",
		Desc: func() string {
			return i18n.T("the data plane: forwards requests, applies upstream patches, passes reasoning back", nil)
		},
		Type: "gateway",
		Requires: []modules.Requirement{
			modules.Need(configapi.Capability),
			// This module's section in state.json is registered through confighook (see
			// RegisterStateField in Start below). This was **not declared before**, while Start
			// used MustGet -- dug out on 2026-09-21 by tools/archmap's "imported but declared no
			// edge" probe: removing confighook, graph construction failed first on **someone
			// else** (locale / pluginmanager), so this panic never got its turn and tests were all
			// green. With no confighook in the assembly but this module present, it panics at Start.
			modules.Need(confighookapi.ConfigHooksCapability),
			// ui is a **weak dependency** (see component.Optional): `newgate st` / `metrics` /
			// `probe` are this layer's user interface; with a UI installed it is mounted, without
			// one it is skipped -- the gateway works as usual, just with no entry point. The UI does
			// not depend on this module (it has no out-edges), so this edge cannot form a cycle.
			modules.Optional(cliapi.Capability),
			// porthub is a weak dependency too, and **only the daemon entry uses it**: the process
			// `__serve` starts hands "who answers on this port" to it (see rootHandler in
			// serve.go). The data plane (the whole forward directory) knows not one word of it --
			// direction_test.go pins that, the same ruler as "the data plane knows no policy".
			modules.Optional(porthubapi.Capability),
			// Same for serving: only the daemon entry uses it -- "I am about to start serving" is
			// said by this module, and modules that want to come up in the serving process (a UI
			// that starts its own port when porthub is absent) register on it. Nobody knows anybody,
			// only this ledger.
			modules.Optional(servingapi.Capability),
			// The web UI is like ui: a weak dependency. Installed, it reports its own face (counters);
			// absent, it is skipped.
			modules.Optional(viewapi.Capability),
			// The entry ledger: **the daemon itself declares on it** (`newgate __serve`, see
			// serve.go). Same reason as wrapper for Need instead of Optional: remove the ledger
			// and nothing can start this process, and that failure only shows at the first
			// `newgate start`. Declaring it makes graph construction fail on the spot and name the
			// port (see app/matrix_test.go's removal matrix).
			modules.Need(entryapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Gateway(port)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			// The "gateway" section in state.json belongs to this module (patch switches, debug
			// expiry). Only registering it makes someone stop "two modules grabbing the same
			// field" -- before, this table covered half of the three writes, and a key taken by
			// gateway and runtime did not error on the spot.
			hooks := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			fieldRelease, err := hooks.RegisterStateField("gateway", gatewaystate.Key)
			if err != nil {
				return err
			}
			releases = append(releases, fieldRelease)

			restore = special.InstallDefault(port.registry)

			// The data plane's policy ledger is installed as the default too: the data plane is
			// started in two places (the daemon main loop uses the package field, testing/system
			// uses this entry), and without it the latter would New() its own empty ledger,
			// turning regressions like "did the contributor really get wired into the data plane"
			// into idle greens -- the shape-criterion test went idle exactly this way on 2026-09-17.
			restoreFilters = policy.InstallDefault(port.filters)

			// ui is a **weak dependency** (see component.Optional): with no ui installed these
			// commands have no entry, but gateway features work as usual -- this module does not
			// depend on the UI existing. The web UI's part (counters) registers first: it **does
			// not depend on cli** and must exist in a dashboard-only assembly too -- the block
			// below returns early, and this would then never run.
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				rel, err := v.Register("gateway",
					viewapi.Title(func() string { return i18n.T("Gateway", nil) }).
						In(func() string { return i18n.T("Data plane", nil) }).
						Of(viewapi.FieldRoutes), gatewayConcepts)
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}

			// The shared-port table (take it if installed, nil if not → the data plane serves the
			// port itself). This only **takes** it; how to use it is serve.go's business: the rest
			// of this module (data plane, commands, status line) need not know it exists.
			hub, _ := modules.Get(ctx, porthubapi.Capability)
			// The serving-callback ledger (same: take if installed, nil if not). Modules that
			// want to come up when "this process starts serving" register on it -- so neither the
			// data plane nor the UI needs to know the other.
			listeners, _ := modules.Get(ctx, servingapi.Capability)

			// The daemon itself declares as an **entry** (the cli block below returns when cli is
			// absent, so it must be here): a `disable: ["cli"]` assembly has no UI, yet the daemon
			// still needs a starter -- a dashboard-only distribution is that shape (see serve.go).
			//
			// The policy ledger is passed in as-is: **this layer knows no policy**, who plugs in is
			// each one's own Start's decision.
			cli, hasUI := modules.Get(ctx, cliapi.Capability)
			// With no terminal UI, fall back to the **fallback** slot: then `newgate` with no
			// arguments has nobody else to answer, and the only behavior that makes sense for this
			// process is to start serving (see headless in serve.go).
			//
			// Position is the criterion: the entry ledger asks by rank, so "the more specific one
			// answers first" comes for free. The other entry that would fall back (the
			// distribution's own shell, like hello) is either at RankPreferred or mutually exclusive
			// with this one (when it shows up, the gateway usually is not installed at all).
			rank := entryapi.RankShim
			if !hasUI {
				rank = entryapi.DefaultRank
			}
			entries := modules.MustGet(ctx, entryapi.Capability)
			entryRelease, err := entries.Register(
				serveEntry{filters: port.filters, hub: hub, listeners: listeners, headless: !hasUI},
				rank)
			if err != nil {
				return err
			}
			releases = append(releases, entryRelease)

			if !hasUI {
				return nil
			}
			for _, cmd := range []cliapi.Command{
				specialCommand{}, schemaRepairCommand{}, fallbackCommand{}, debugCommand{},
				// The observability surface belongs to the data plane too: how counters are grouped
				// and what probing found out are gateway semantics (see command_metrics.go / command_probe.go).
				metricsCommand{}, probeCommand{}, logsCommand{},
			} {
				release, err := cli.RegisterCommand(cmd)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			// The patch-switch status line, the proxy line, and the two health checks (environment
			// / proxy) are all self-reported by this module (see status.go / diagnostics.go).
			for _, register := range []func() (modules.Release, error){
				func() (modules.Release, error) { return cli.RegisterStatus(switchStatus{}) },
				func() (modules.Release, error) { return cli.RegisterStatus(gatewayReporter{}) },
				func() (modules.Release, error) { return cli.RegisterDiagnostics(gatewayReporter{}) },
				func() (modules.Release, error) { return cli.RegisterDump(gatewayReporter{}) },
				func() (modules.Release, error) { return cli.RegisterVerbose(switchStatus{}) },
			} {
				release, err := register()
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			return nil
		},
		Stop: func(context.Context) error {
			err := modules.ReleaseAll(releases)
			if restoreFilters != nil {
				restoreFilters()
			}
			if restore != nil {
				restore()
			}
			return err
		},
	}
}

// Quirks see api.go (the data plane uses exactly this one instance).
func (p *port) Quirks() *quirk.Table { return quirk.Default }

// FallbackOn / SetFallback see api.go.
//
// This is one layer of forwarding only: the switch's value and validation are gatewaystate's
// business (it parses that section of state.json on the **read** side, see Parse there), and
// this layer only hands it to the control plane. Two implementations would diverge between
// "clicked on the web page" and "typed on the command line" -- and that divergence looks like
// the two paths each saying their own thing, with nobody able to tell from the UI who is right.
func (p *port) FallbackOn() bool {
	return !gatewaystate.FallbackOff(store.LoadState())
}

func (p *port) SetFallback(on bool) error { return gatewaystate.SetFallback(on) }

// ProbeBinding sends one minimal request, then feeds the conclusion into the policy layer
// (see api.go).
//
// The step into the policy layer reuses `p.filters.ObserveProbes` -- **the same function the
// `newgate probe` path ends in** (the command line goes over HTTP to the control plane and in
// here). Writing "record one probe conclusion" twice would make what is clicked on the web page
// and what is typed in the terminal slowly diverge, and that divergence shows as "the web page
// says it works, the terminal says it does not" -- nobody can tell from the UI who is right.
func (p *port) ProbeBinding(provider, model string) (ProbeOutcome, error) {
	slow := store.LoadState().Timeouts.ClassifierFirstByte()
	var out ProbeOutcome
	_, err := probe.Run(probe.Options{
		OnlyTarget:  &probe.Target{Provider: provider, Model: model},
		Timeout:     slow,
		SlowAfter:   slow,
		Concurrency: 1,
		OnDone: func(_ probe.Target, status int, lat, _ time.Duration, err error) {
			out.Status, out.Latency = status, lat
			out.OK = err == nil && status == 200
			if err != nil {
				out.Err = err.Error()
			}
		},
	})
	if err != nil {
		return ProbeOutcome{}, err
	}
	// The threshold is the **classifier first byte** tier: a probe sends a tiny 4-token request,
	// and still not finished past this threshold means it cannot enter the interactive fallback
	// chain -- the same criterion as the command line (see the note on
	// policy.ProbeObservation.SlowAfter).
	// The policy layer's reply **goes back to the caller**, not to the log here: this probe was
	// **clicked by someone**, and that sentence ("this one's gate opened") is the feedback they
	// want. Writing it to the log sends the feedback somewhere they are not looking.
	var notes []string
	for _, ack := range p.filters.ObserveProbes([]policy.ProbeObservation{{
		Provider: provider, Model: model, Status: out.Status,
		Latency: out.Latency, ContextBytes: 1, Error: out.Err, SlowAfter: slow,
	}}) {
		if ack.Note != "" {
			notes = append(notes, ack.Note)
		}
	}
	out.Note = strings.Join(notes, " · ")
	return out, nil
}

// RegisterRequestHook confines plugin registration to inside the gateway owner, and hands the
// revocation right back to the calling component.
func (p *port) RegisterRequestHook(hook Plugin) (modules.Release, error) {
	return p.registry.Register(hook)
}

// RegisterFilter lets policy plug decision logic into the data plane's four decision points.
//
// This is **the only insertion point**: the data plane handler, the internal registry, and
// "who plugged in" all stay behind this interface. So gateway can know nothing of that health
// table (bindings' availability and latency ledger), and the health table just declares
// Need(gateway) to hang its own state machine on. The shape is on par with RegisterRequestHook;
// for the reason see the package comment of modules/gateway/policy.
func (p *port) RegisterFilter(f Filter) (modules.Release, error) {
	return p.filters.Register(f)
}
