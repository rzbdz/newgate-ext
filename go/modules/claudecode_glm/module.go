// Package claudecode_glm 只处理 Claude Code 与 GLM 同时出现时的兼容行为。
//
// 交叉组件同时依赖客户端身份、模型身份和 Gateway 端口。只有三者均存在，
// 插件才进入请求链；这比在 gateway 热路径里判断品牌名更容易删除和测试。
package claudecode_glm

import (
	"context"

	glmapi "github.com/rzbdz/newgate-modules-ext/go/modules/glm"
	modules "github.com/rzbdz/newgate/go/component"
	claudeapi "github.com/rzbdz/newgate/go/modules/claudecode"
	gatewayapi "github.com/rzbdz/newgate/go/modules/gateway"
)

// New 声明 Claude Code × GLM 交叉组件，
// 把跨模块怪癖留在组合边界，避免污染任一模块的单方实现。
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "claudecode-glm",
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
