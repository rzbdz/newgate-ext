// Package deepseek owns DeepSeek model-family detection and upstream protocol patches.
//
// The model family decides from model/provider/base URL whether a request is its own,
// and registers the treatments only DeepSeek upstreams require. It does not know the client.
//
// A problem only appearing with Claude Code plus DeepSeek belongs to claudecode_deepseek
// - so the model module never turns a client guess into global policy.
package deepseek

import (
	"context"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	breakerapi "github.com/rzbdz/newgate-ext/modules/breaker"
	gatewayapi "github.com/rzbdz/newgate-ext/modules/gateway"
	pluginmanagerapi "github.com/rzbdz/newgate-ext/modules/pluginmanager"
	modules "github.com/rzbdz/newgate/component"
)

// New declares the DeepSeek model component, provides the detection port and registers
// the model-side protocol patches. Client-times-model behavior belongs to a cross component.
//
// It also registers a **shape criterion** with the health table (reasoningShape): the
// two "must be passed back" 400s are request-shape problems and must not be recorded
// against any provider's availability. The criterion lives here, not core, because the
// text is DeepSeek's dialect - core must hold no upstream-specific strings (shape.go).
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "deepseek",
		Desc: func() string { return i18n.T("DeepSeek's quirks: reasoning backfill, tail shape, /responses", nil) },
		Type: "model",
		Requires: []modules.Requirement{
			modules.Need(gatewayapi.Capability),
			modules.Need(breakerapi.Capability),
			// Report fine-grained switch points (2nd/3rd/4th pass each switchable), voluntarily:
			// without the line assembly still works, the points just miss from `newgate plugin`.
			modules.Need(pluginmanagerapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Model{
				MatchTarget: MatchTarget,
			}),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			gateway := modules.MustGet(ctx, gatewayapi.Capability)
			for _, treatment := range Treatments() {
				release, err := gateway.RegisterRequestHook(treatment)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			health := modules.MustGet(ctx, breakerapi.Capability)
			release, err := health.RegisterShapeDetector(reasoningShape{})
			if err != nil {
				return err
			}
			releases = append(releases, release)

			// Report switch points: each of the three passes in Apply can be turned off.
			pm := modules.MustGet(ctx, pluginmanagerapi.Capability)
			self, err := pm.RegisterSelf("deepseek", Switches())
			if err != nil {
				return err
			}
			releases = append(releases, self)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
