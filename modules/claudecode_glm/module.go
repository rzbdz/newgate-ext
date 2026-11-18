// Package claudecode_glm handles only the compatibility
// behavior when Claude Code and GLM appear together.
//
// The cross-cutting component depends on the client identity, the model identity, and the
// Gateway port at once. Only when all three exist does the plugin enter the request chain;
// that is easier to remove and test than checking brand names in gateway's hot path.
package claudecode_glm

import (
	"context"

	i18n "github.com/rzbdz/newgate/lib/i18n"

	claudeapi "github.com/rzbdz/newgate-ext/modules/claudecode"
	gatewayapi "github.com/rzbdz/newgate-ext/modules/gateway"
	glmapi "github.com/rzbdz/newgate-ext/modules/glm"
	modules "github.com/rzbdz/newgate/component"
)

// New declares the Claude Code x GLM cross-cutting component, keeping cross-module
// quirks at the composition boundary so neither module's own implementation is polluted.
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "claudecode-glm",
		Desc: func() string { return i18n.T("the Claude Code × GLM crossing", nil) },
		Type: "bridge",
		Requires: []modules.Requirement{
			modules.Need(gatewayapi.Capability),
			modules.Need(claudeapi.Capability),
			modules.Need(glmapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			gateway := modules.MustGet(ctx, gatewayapi.Capability)
			client := modules.MustGet(ctx, claudeapi.Capability)
			model := modules.MustGet(ctx, glmapi.Capability)
			for _, treatment := range Treatments(client, model) {
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
