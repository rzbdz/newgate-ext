package upgrade

import (
	"context"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
)

// New 声明这个组件。
//
// 依赖只有 cli（Optional）：没有界面壳的装配里（`dist-dashboard.json`）本模块
// 照常提供能力，别的模块照常登记动作，只是没有 `newgate upgrade` 这个入口——
// 业务模块不依赖任何 ui 那条规矩在这里的意思是「不装终端也能活」。
func New() modules.Component {
	reg := newRegistry()
	var releases []modules.Release
	return modules.Component{
		Name: Name,
		Desc: func() string {
			return i18n.T("upgrade newgate itself and the dynamic data modules own, through actions each module registers", nil)
		},
		// 与 modellist / verify 同一层：它是基础设施，不是客户端、不是上游补丁。
		Type: "infra",
		Requires: []modules.Requirement{
			modules.Optional(cliapi.Capability),
		},
		// 必须在 Bind 期就存在：别的模块在自己的 Start 里登记动作，而它们可能
		// 比本模块先 Start（排序边是「它们 Optional 依赖本端口」，所以其实不会，
		// 但把登记表建在 New 里是这套东西一贯的写法，省得依赖 Start 顺序）。
		Provides: []modules.Provision{
			modules.Provide(Capability, Service(reg)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			ui, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			rel, err := ui.RegisterCommand(upgradeCommand{svc: reg})
			if err != nil {
				return err
			}
			releases = append(releases, rel)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
