package verify

import (
	"context"

	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	viewapi "github.com/rzbdz/newgate/lib/view"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
)

// New 声明这个组件。
//
// 三条依赖全是 Optional，判据是同一条：**界面/终端不在，功能还在吗？**
//
//   - view 不在：没有那一节网页，`newgate verify` 照常；
//   - web 扩展端点不在：没有按钮，命令照常；
//   - cli 不在（`dist-dashboard.json` 那种只有网页的装配）：没有命令，网页上的
//     「验一下」照常，扩展端点直接调 service。
//
// 所以它装进任何一份规格书都不会因为缺一个界面而装不起来。
func New() modules.Component {
	svc := newService()
	var releases []modules.Release
	return modules.Component{
		Name: Name,
		Desc: func() string {
			return i18n.T("verify which model an upstream really serves, by fingerprint (newgate verify)", nil)
		},
		// 与 modellist 同一层：它是诊断/基础设施，不是客户端、不是上游补丁。
		Type: "infra",
		Requires: []modules.Requirement{
			modules.Optional(viewapi.Capability),
			modules.Optional(webapi.ExtensionCapability),
			modules.Optional(cliapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				rel, err := registerView(v, svc)
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}
			if web, ok := modules.Get(ctx, webapi.ExtensionCapability); ok {
				rel, err := web.Register(Endpoint, svc.handle)
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}
			if ui, ok := modules.Get(ctx, cliapi.Capability); ok {
				rel, err := ui.RegisterCommand(verifyCommand{svc: svc})
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
