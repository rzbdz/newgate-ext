// Package thinking manages thinking-mode policy that is independent of any concrete model.
//
// "Does a model support thinking" and "how an upstream wants reasoning echoed back" are two
// separate things: this module provides the generic safe downgrade plus generic plugins; model
// families such as DeepSeek keep their own protocol constraints; cross problems like Claude
// Code x DeepSeek are handled by cross components.
//
// So neither side guesses the other; each patch registers, is observable and undoes on its own.
package thinking

import (
	"context"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	gatewayapi "github.com/rzbdz/newgate-ext/modules/gateway"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	modules "github.com/rzbdz/newgate/component"
)

type service struct{}

var _ Service = (*service)(nil)

// New declares the generic thinking component: it provides the cross-model downgrade port
// and registers the model-agnostic handlers into the gateway, undoing them in reverse on Stop.
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name:     "thinking",
		Desc:     func() string { return i18n.T("thinking-mode policy that is not tied to one model family", nil) },
		Type:     "model",
		Requires: []modules.Requirement{modules.Need(gatewayapi.Capability)},
		Provides: []modules.Provision{
			modules.Provide(Capability, Service(service{})),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			gateway := modules.MustGet(ctx, gatewayapi.Capability)

			// The criteria are registered by the **module owning the patch** (see signatures.go): these
			// three are upstreams' dialect text, and the patch turning disabled into enabled lives here.
			// The data plane hands plugins the same table on every request, so it is live on the hot path.
			for _, sig := range signatures() {
				release, err := gateway.Quirks().RegisterSignature(sig)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}

			for _, treatment := range Treatments() {
				release, err := gateway.RegisterRequestHook(treatment)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}

// BestEffortDisable: without a safe rewrite, return the request and a clear error - no guessing.
func (service) BestEffortDisable(body []byte, request *special.Request) ([]byte, []string, error) {
	return BestEffortDisableThink(body, request)
}
