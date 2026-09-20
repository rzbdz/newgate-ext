// Package claudecode_deepseek 只处理 Claude Code 与 DeepSeek 的交叉语义。
//
// Claude Code 可能不会把非官方 thinking block 原样带回下一轮，而 DeepSeek
// 的 tool loop 又要求真实 reasoning_content 连续回传。任何一方单独都无法
// 正确解决这个问题：客户端模块不该认识模型，上游模块也不该猜客户端。
//
// 因此本组件同时 Need 两个家族 capability，在两者都存在时才向 gateway
// 注册桥接 treatments；Stop 使用注册句柄精确撤销这些组合行为。
package claudecode_deepseek

import (
	"context"

	deepseekapi "github.com/rzbdz/newgate-ext/go/modules/deepseek"
	modules "github.com/rzbdz/newgate/go/component"
	claudeapi "github.com/rzbdz/newgate/go/modules/claudecode"
	gatewayapi "github.com/rzbdz/newgate/go/modules/gateway"
	pluginmanagerapi "github.com/rzbdz/newgate/go/modules/pluginmanager"
)

// New 声明 Claude Code × DeepSeek 交叉组件。
// 只有同时解析到客户端、模型和网关端口时，它才注册双方组合后才需要的修补。
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "claudecode-deepseek",
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
