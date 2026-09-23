// Package codex 描述 Codex（OpenAI 官方 CLI）客户端。
//
// 本模块只做三件事：注册**描述符**（它是什么）、**安装方式**（怎么装）、以及
// 运行期事实（装没装）。模型怎么接管、有哪些槽位，属于后续的配置模块——这种切法
// 让「装得上」与「接得管」各自可测，也让基础客户端不反向依赖某个可选能力。
package codex

import (
	"context"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	viewapi "github.com/rzbdz/newgate/lib/view"
	configapi "github.com/rzbdz/newgate/modules/config"
	confighookapi "github.com/rzbdz/newgate/modules/confighook"
	gatewayapi "github.com/rzbdz/newgate/modules/gateway"
	pluginmanagerapi "github.com/rzbdz/newgate/modules/pluginmanager"
)

// New 声明 Codex 客户端组件。
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "codex",
		Desc: func() string { return i18n.T("the Codex client", nil) },
		Type: "client",
		Requires: []modules.Requirement{
			modules.Need(confighookapi.ConfigHooksCapability),
			// 动态角色（codex 的模型名 → 档位，见 models.go）。**强依赖**而不是
			// 弱依赖：那张表登记不上时的症状是「在 codex 里换了个模型就 404」，
			// 而那正是本模块现在要解决的事——静默降级在这里是不能接受的。
			modules.Need(configapi.Capability),
			// web 界面：在就把「Codex 档位」那张卡挂上去（见 view.go），不在就跳过。
			// 弱依赖——本模块的功能一个都不少，只是没有浏览器入口。
			modules.Optional(viewapi.Capability),
			// 网关：装配这一手（client → 通用上游的 tool lift）需要一个能 Register
			// request hook 的端口。**弱依赖**：骨架发行版（dist-hello）关掉了
			// gateway，那时 hook 不挂、客户端没有转发——但客户端本身的描述符/事实
			// 不受影响，强行 Need 会让 hello 跑不起来。
			modules.Optional(gatewayapi.Capability),
			// plugin-manager：抬工具那一手的**运行期开关点**（`codex.lift-tools`）。
			// 弱依赖——它不在时抬工具照常跑，只是没有开关可关（见 st-tools.go 的
			// registerToolsLift）。
			modules.Optional(pluginmanagerapi.Capability),
		},
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

			// 接管：改写 ~/.codex/config.toml（见 takeover.go）。档位与窗口声明
			// 都在 **Apply 那一刻**现算——槽位映射与活动 profile 都是用户随时能改
			// 的，注册期读一次就会冻在那里。
			release, err = hooks.BindTakeover(ID, Takeover{})
			if err != nil {
				return err
			}
			releases = append(releases, release)

			// codex 的模型名 ↔ 档位（见 models.go）。这份贡献**跟着接管模式走**：
			// 只有 rename 模式下才真的登记出角色来，理由写在 rolesProvider 上
			// ——无条件登记会短路掉「具体模型名反解回档位」那条路，而那会改掉
			// **没让路的人**的行为。
			release, err = modules.MustGet(ctx, configapi.Capability).RegisterRoleProvider(rolesProvider{})
			if err != nil {
				return err
			}
			releases = append(releases, release)

			// 工具方言归一化（lift，见 st-tools.go）。没有网关上就跳过——
			// 见 Optional 的注释。开关点报给 plugin-manager（它不在时那一手照
			// 常跑，只是没有运行期开关——见 registerToolsLift）。
			if gw, ok := modules.Get(ctx, gatewayapi.Capability); ok {
				pm, _ := modules.Get(ctx, pluginmanagerapi.Capability)
				hookReleases, err := registerToolsLift(gw, pm)
				if err != nil {
					return err
				}
				releases = append(releases, hookReleases...)
			}

			// web 界面：登记「Codex 档位」那张卡（见 view.go）。
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				release, err := registerView(v)
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
