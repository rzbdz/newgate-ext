// Package claudecode 描述 Claude Code 这个客户端家族。
//
// 客户端模块只拥有 Claude Code 自身的事实：二进制名、环境槽位、协议以及
// 客户端普遍需要的 request treatments。它把 Agent 注册到 confighook，
// 把插件注册到 gateway，并提供一个很小的 Client capability 供交叉组件识别。
//
// DeepSeek 或 GLM 的特殊回传规则不属于这里；只有“Claude Code 遇到某模型”
// 才成立的行为放在 claudecode_deepseek / claudecode_glm。
package claudecode

import (
	"context"

	modules "github.com/rzbdz/newgate/component"

	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	confighookapi "github.com/rzbdz/newgate/modules/confighook"
	gatewayapi "github.com/rzbdz/newgate/modules/gateway"
	thinkingapi "github.com/rzbdz/newgate/modules/thinking"
)

// New 声明 Claude Code 客户端组件。它注册客户端描述符和状态字段，
// 再将客户端专属处理器接入网关；所有注册句柄都由 Stop 统一释放。
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "claudecode",
		Type: "client",
		Requires: []modules.Requirement{
			modules.Need(gatewayapi.Capability),
			modules.Need(confighookapi.ConfigHooksCapability),
			modules.Need(thinkingapi.Capability),
			// ui 是**弱依赖**（见 component.Optional）：`newgate naked` 是本模块的
			// 命令，装着界面就注册进去，没装就跳过。依赖方向是**本模块 → cli**：
			// cli 不认识任何业务模块，模块反过来认识它。
			modules.Optional(cliapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability,
				Client{AgentID: ID}),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			config := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			release, err := config.RegisterAgent(Agent())
			if err != nil {
				return err
			}
			releases = append(releases, release)
			release, err = config.RegisterStateField("claudecode", "classifier_override")
			if err != nil {
				return err
			}
			releases = append(releases, release)
			release, err = config.RegisterStateField("claudecode", "classifier_naked")
			if err != nil {
				return err
			}
			releases = append(releases, release)
			gateway := modules.MustGet(ctx, gatewayapi.Capability)
			thinking := modules.MustGet(ctx, thinkingapi.Capability)
			for _, treatment := range Treatments(thinking) {
				release, err = gateway.RegisterRequestHook(treatment)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			release, err = gateway.RegisterRequestHook(classifierNaked{})
			if err != nil {
				return err
			}
			releases = append(releases, release)

			// 自己的命令自己贡献：界面不认识本模块，是本模块认识界面。
			// ui 没装就跳过（见上面那条 Optional）：模块功能照常。
			cli, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			release, err = cli.RegisterCommand(nakedCommand{})
			if err != nil {
				return err
			}
			releases = append(releases, release)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
