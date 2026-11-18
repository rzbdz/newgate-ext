// Package thinking 管理与具体模型无关的思考模式策略。
//
// “模型是否支持 thinking”与“某家上游要求怎样回传 reasoning”是两件事：
// 本模块提供通用的安全降级和通用插件；DeepSeek 等模型家族保留自己的协议
// 约束；Claude Code × DeepSeek 之类交叉问题由交叉组件处理。
//
// 这种拆分使单方模块不需要猜测另一方行为，也让每条修补都能独立注册、观测
// 和撤销。
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

// New 声明通用 thinking 组件：它既提供跨模型降级端口，
// 也把与模型无关的处理器注册进网关，并在 Stop 时逆序撤销。
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

			// 判据由**拥有补丁的模块**注册（见 signatures.go）：这三条是各家上游的
			// 方言原文，而把 disabled 翻成 enabled 的补丁住在本模块。数据面在每次
			// 请求上把同一张表交给插件，所以注册完对热路径立刻生效。
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

// BestEffortDisable 暴露保守降级：无法安全改写时返回原请求和明确错误，而非猜测结构。
func (service) BestEffortDisable(body []byte, request *special.Request) ([]byte, []string, error) {
	return BestEffortDisableThink(body, request)
}
