package config

import (
	"context"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	modules "github.com/rzbdz/newgate/component"

	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/roleprov"
)

type port struct{ roles *roleprov.Registry }

var _ Config = (*port)(nil)

// New 声明配置组件：它提供唯一 Config 端口，并在生命周期内安装动态档位注册表。
// Stop 恢复旧默认值，使测试和重复组装不会遗留进程级兼容状态。
func New() modules.Component {
	port := &port{roles: roleprov.NewRegistry()}
	var restore func()
	var releases []modules.Release
	return modules.Component{
		Name: "config",
		Desc: func() string {
			return i18n.T("tiers, providers and state.json: what they mean, and how they are read and written", nil)
		},
		Type: "infra",
		Requires: []modules.Requirement{
			// ui 是**弱依赖**（见 component.Optional）：装着界面就把本模块自己的
			// 命令与展示面挂上去；没装就跳过，配置照常工作，只是没有命令行入口。
			modules.Optional(cliapi.Capability),
			// web 界面同样是**弱依赖**，理由一字不差：装着 web-dashboard 就把本模块
			// 的控制面挂上去，没装就跳过。两个界面各注册各的，可以同时存在。
			modules.Optional(viewapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Config(port)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			restore = roleprov.InstallDefault(port.roles)

			// web 界面那一份展示面：**与 cli 各注册各的**（两个界面可以在同一个
			// 进程里并存）。所以这一段的先后与下面那段无关，也不能被
			// 「没装 cli 就 return」跳过——那正是「只装 dashboard」那一档。
			//
			// 这里只登记产出函数，一个文件都不读：每条 `newgate …` 命令都会跑到
			// 这里，而绝大多数进程没有人会打开界面（见 lib/view 的包注释）。
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				rel, err := registerView(v)
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}

			// 配置**自己**的命令与展示面（tier / profiles / profile / --set-profile /
			// init / reload，体检的文件与链路两项，status 的「配置」一行与两张表）
			// 由本模块注入界面：界面不认识 profile，也不认识链。
			ui, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			for _, cmd := range commands() {
				release, err := ui.RegisterCommand(cmd)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			for _, register := range []func() (modules.Release, error){
				func() (modules.Release, error) { return ui.RegisterDiagnostics(reporter{}) },
				func() (modules.Release, error) { return ui.RegisterStatus(reporter{}) },
				func() (modules.Release, error) { return ui.RegisterStatusBlocks(reporter{}) },
				func() (modules.Release, error) { return ui.RegisterDump(reporter{}) },
				func() (modules.Release, error) { return ui.RegisterGlossary(glossary{}) },
			} {
				release, err := register()
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			return nil
		},
		Stop: func(context.Context) error {
			err := modules.ReleaseAll(releases)
			if restore != nil {
				restore()
			}
			return err
		},
	}
}

// RegisterRoleProvider 把扩展所有权委托给 role registry，并原样返回 Release。
func (p *port) RegisterRoleProvider(provider RoleProvider) (modules.Release, error) {
	return p.roles.Register(provider)
}
