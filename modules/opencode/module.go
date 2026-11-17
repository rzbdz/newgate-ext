// Package opencode 描述 OpenCode 客户端家族。
//
// 本模块只注册 OpenCode 的 Agent 描述符和家族身份。具体配置文件如何接管、
// OMO 有哪些额外槽位，属于 opencodeomo；这种边界使基础客户端不会反向依赖
// 某个可选插件。
package opencode

import (
	"context"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	modules "github.com/rzbdz/newgate/component"

	confighookapi "github.com/rzbdz/newgate/modules/confighook"
)

// New 声明 OpenCode 客户端组件：提供家族身份，并在生命周期内注册客户端描述符。
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name:     "opencode",
		Desc:     func() string { return i18n.T("the OpenCode client", nil) },
		Type:     "client",
		Requires: []modules.Requirement{modules.Need(confighookapi.ConfigHooksCapability)},
		Provides: []modules.Provision{
			modules.Provide(Capability,
				Client{AgentID: ID}),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			var err error
			var release modules.Release
			hooks := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			release, err = hooks.RegisterAgent(Agent())
			if err != nil {
				return err
			}
			releases = append(releases, release)
			// 安装方式：`newgate config` 在没装的机器上走的就是它（与 codex /
			// claude / hermes 同一条口子，见内核 confighook.AgentInstaller）。
			release, err = hooks.RegisterAgentInstaller(ID, confighookapi.ExecInstaller(
				"npm", "install", "--global", NpmPackage))
			if err != nil {
				return err
			}
			releases = append(releases, release)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
