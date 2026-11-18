// Package claudecode_deepseek handles only the cross-cutting semantics of Claude Code and DeepSeek.
//
// Claude Code may not pass non-official thinking blocks back as is into the next
// round, while DeepSeek's tool loop requires real reasoning_content to be replayed
// continuously. Neither side can solve this correctly alone: the client module
// should not know models, and the upstream module should not guess the client.
//
// So this component Needs both family capabilities and registers
// bridge treatments with gateway only when both exist; Stop uses the
// registration handles to precisely undo these composed behaviors.
package claudecode_deepseek

import (
	"context"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	claudeapi "github.com/rzbdz/newgate-ext/modules/claudecode"
	deepseekapi "github.com/rzbdz/newgate-ext/modules/deepseek"
	gatewayapi "github.com/rzbdz/newgate-ext/modules/gateway"
	pluginmanagerapi "github.com/rzbdz/newgate-ext/modules/pluginmanager"
	modules "github.com/rzbdz/newgate/component"
)

// New declares the Claude Code x DeepSeek cross-cutting component.
// Only when the client, the model, and the gateway port all resolve
// does it register the patches needed only after the two are combined.
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "claudecode-deepseek",
		Desc: func() string { return i18n.T("Claude Code × DeepSeek: replay reasoning, disable thinking", nil) },
		Type: "bridge",
		Requires: []modules.Requirement{
			modules.Need(gatewayapi.Capability),
			modules.Need(claudeapi.Capability),
			modules.Need(deepseekapi.Capability),
			modules.Need(pluginmanagerapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			gateway := modules.MustGet(ctx, gatewayapi.Capability)
			client := modules.MustGet(ctx, claudeapi.Capability)
			model := modules.MustGet(ctx, deepseekapi.Capability)
			for _, treatment := range Treatments(client, model) {
				release, err := gateway.RegisterRequestHook(treatment)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			pm := modules.MustGet(ctx, pluginmanagerapi.Capability)
			self, err := pm.RegisterSelf("claudecode-deepseek", Switches())
			if err != nil {
				return err
			}
			releases = append(releases, self)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
