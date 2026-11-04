package hermes

import (
	"context"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	servingapi "github.com/rzbdz/newgate/lib/serving"
	viewapi "github.com/rzbdz/newgate/lib/view"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	confighookapi "github.com/rzbdz/newgate/modules/confighook"
	gatewayapi "github.com/rzbdz/newgate/modules/gateway"
)

// New 声明 Hermes 客户端组件。
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "hermes",
		Desc: func() string { return i18n.T("the Hermes Agent client", nil) },
		Type: "client",
		Requires: []modules.Requirement{
			modules.Need(confighookapi.ConfigHooksCapability),
			// web 界面：在就挂「Hermes 档位」那张卡（view.go），不在就跳过。
			modules.Optional(viewapi.Capability),
			// 终端：status 那一行与 `newgate hermes-gateway`（status.go / command.go）。
			modules.Optional(cliapi.Capability),
			// 服务期：模型清单的后台同步只在守护进程里跑（sync.go）。没有这条边
			// （骨架发行版关掉了 gateway）就不同步——`newgate on hermes` 与
			// `newgate hermes-gateway sync` 照常可用，只是不自动。
			modules.Optional(servingapi.Capability),
			// 网关：替 hermes 的 URL 回答 /models（st-models.go）。弱依赖——骨架发行版
			// 关掉了 gateway，那时也就没有代理可问。
			modules.Optional(gatewayapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Client{AgentID: ID}),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			hooks := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			add := func(r modules.Release, err error) error {
				if err != nil {
					return err
				}
				releases = append(releases, r)
				return nil
			}
			if err := add(hooks.RegisterAgent(Agent())); err != nil {
				return err
			}
			// 安装方式：官方那一行安装脚本（`newgate hermes -y`，或没装时问一句）。
			// 命令写在这里而不是内核里——换渠道是这家客户端的事。
			if err := add(hooks.RegisterAgentInstaller(ID, confighookapi.ExecInstaller(
				"bash", "-c", "curl -fsSL "+InstallScript+" | bash"))); err != nil {
				return err
			}
			if err := add(hooks.RegisterAgentFacts(ID, facts{})); err != nil {
				return err
			}
			// 接管：改写 $HERMES_HOME/config.yaml（takeover.go）。值在 Apply 那一刻现算。
			if err := add(hooks.BindTakeover(ID, Takeover{})); err != nil {
				return err
			}
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				if err := add(registerView(v)); err != nil {
					return err
				}
			}
			if ui, ok := modules.Get(ctx, cliapi.Capability); ok {
				if err := add(ui.RegisterStatus(status{})); err != nil {
					return err
				}
				if err := add(ui.RegisterCommand(gatewayCommand{})); err != nil {
					return err
				}
			}
			if gw, ok := modules.Get(ctx, gatewayapi.Capability); ok {
				if err := add(gw.RegisterRequestHook(modelsLister{})); err != nil {
					return err
				}
			}
			if s, ok := modules.Get(ctx, servingapi.Capability); ok {
				if err := add(s.OnServe("hermes-sync", startSync)); err != nil {
					return err
				}
			}
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
