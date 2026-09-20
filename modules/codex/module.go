// Package codex 描述 Codex（OpenAI 官方 CLI）客户端。
//
// 本模块只做三件事：注册**描述符**（它是什么）、**安装方式**（怎么装）、以及
// 运行期事实（装没装）。模型怎么接管、有哪些槽位，属于后续的配置模块——这种切法
// 让「装得上」与「接得管」各自可测，也让基础客户端不反向依赖某个可选能力。
package codex

import (
	"context"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	modules "github.com/rzbdz/newgate/component"

	confighookapi "github.com/rzbdz/newgate/modules/confighook"
)

// New 声明 Codex 客户端组件。
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name:     "codex",
		Desc:     func() string { return i18n.T("the Codex client", nil) },
		Type:     "client",
		Requires: []modules.Requirement{modules.Need(confighookapi.ConfigHooksCapability)},
		Provides: []modules.Provision{
			modules.Provide(Capability, Client{AgentID: ID}),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			hooks := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			release, err := hooks.RegisterAgent(Agent())
			if err != nil {
				return err
			}
			releases = append(releases, release)

			// 安装方式：`newgate codex -y`（或没装时问一句）走的就是它。
			// 命令写在这里而不是内核里——换包名、换渠道是这家客户端的事（见
			// confighook 的 AgentInstaller）。
			release, err = hooks.RegisterAgentInstaller(ID, confighookapi.ExecInstaller(
				"npm", "install", "--global", NpmPackage))
			if err != nil {
				return err
			}
			releases = append(releases, release)

			release, err = hooks.RegisterAgentFacts(ID, facts{})
			if err != nil {
				return err
			}
			releases = append(releases, release)

			// 接管：改写 ~/.codex/config.toml（见 takeover.go）。两个取值都在
			// **Apply 那一刻**求值——槽位映射与活动 profile 都是用户随时能改的，
			// 在注册期读一次就会冻在那里。
			release, err = hooks.BindTakeover(ID, Takeover{
				Tier:          func() string { return tierOf(facts{}) },
				ContextWindow: contextWindow,
			})
			if err != nil {
				return err
			}
			releases = append(releases, release)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
