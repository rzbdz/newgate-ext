// Package deepseek 拥有 DeepSeek 模型家族的识别和上游协议修补。
//
// 模型家族根据 model/provider/base URL 判断请求是否属于自己，并注册只由
// DeepSeek 上游要求的 treatments。它不知道请求来自哪个客户端。
//
// 如果某个问题只在 Claude Code 与 DeepSeek 组合时出现，则交给
// claudecode_deepseek；这样模型模块不会把客户端猜测变成全局行为。
package deepseek

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	breakerapi "github.com/rzbdz/newgate/go/modules/breaker"
	gatewayapi "github.com/rzbdz/newgate/go/modules/gateway"
	pluginmanagerapi "github.com/rzbdz/newgate/go/modules/pluginmanager"
)

// New 声明 DeepSeek 模型组件，同时提供模型判定端口并注册模型侧协议修补。
// 客户端与模型的组合行为不放在这里，而由独立交叉组件拥有。
//
// 它还向健康表注册一条**形状判据**（reasoningShape）：那两句「must be passed
// back」的 400 是请求形状问题，不该记在任何 provider 的可用性账本上。判据放
// 在这里而不是 core，是因为文案是 DeepSeek 的方言——core 里不该出现任何上游
// 专有字符串（见 shape.go 的注释）。
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "deepseek",
		Type: "model",
		Requires: []modules.Requirement{
			modules.Need(gatewayapi.Capability),
			modules.Need(breakerapi.Capability),
			// 上报细粒度开关点（第 2/3/4 手各自可关）。这是**自愿**参与：
			// 不写这一行也能装配，只是这些开关点不会出现在 newgate plugin 里。
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

			// 上报开关点：Apply 里那三手各自可关。名字与路径都在 switches.go。
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
