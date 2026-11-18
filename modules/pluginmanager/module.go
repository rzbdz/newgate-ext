package pluginmanager

import (
	"context"

	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// New 声明运行期开关账本。
//
// 形状就是一个普通模块：一个 capability、一个 Start、一个 Stop。**没有任何为它
// 加的内核钩子**——它出现在图里靠的是 tools/genmodules 扫目录，和别的 15 个模块
// 一样。它「特殊」只特殊在大部分模块选择依赖它。
//
// 依赖方向（2026-09-18 修正三次，这里是**当前**的形状）：
//
//	其他模块 ──Need──▶ plugin-manager ──Need──▶ confighook
//	其他模块 ──Optional──▶ cli（装入界面就是它的 Start 里那一步）
//
// 两处都要看清楚：
//
//   - **本模块不依赖 cli**，只有一条弱依赖（见 component.Optional）：cli 为了
//     渲染 `newgate plugin` 与那一行 status 曾经 Need 过本模块，本模块又要
//     Need(cli) 才能注册命令 —— 环，于是命令只能被迫写在 cli 里。
//   - **cli 也不依赖本模块**（它的 Requires 是空的）。状态行经 cli.RegisterStatus
//     自报、命令经 cli.RegisterCommand 自注册，方向是「本模块 → ui」，界面不认识
//     任何人。ui 把出边砍干净之后，这条弱依赖不会成环。
//
// 唯一的硬依赖是 confighook：登记 state.json 字段必须走它的端口
// （与 modules/claudecode 登记 classifier_naked、classifier_override 同款）。
// 本模块想要「图里有哪些组件」这份名单，所以实现内核的 CatalogAware：装配完成
// 后内核递一次（见 api.go 里 Manager 的注释）。
var _ modules.CatalogAware = (*service)(nil)

func New() modules.Component {
	service := &service{}
	var releases []modules.Release
	return modules.Component{
		Name: "plugin-manager",
		Desc: func() string { return i18n.T("the module list and the runtime switch points", nil) },
		Type: TypeInfra,
		Requires: []modules.Requirement{
			// ui 是**弱依赖**（见 component.Optional）：装着界面就把 `newgate plugin`
			// 与那一行 status 挂上去，没装就跳过——开关账本照常工作。
			// web 界面同款、各注册各的（两个界面可以并存）。
			modules.Optional(cliapi.Capability),
			modules.Optional(viewapi.Capability),
			modules.Need(confighookapi.ConfigHooksCapability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Manager(service)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			hooks := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			release, err := hooks.RegisterStateField("plugin-manager", StateKey)
			if err != nil {
				return err
			}
			releases = append(releases, release)

			// 它自己也在册，走的是同一个 RegisterSelf、同一个前缀规则——不因为
			// 自己是账本就有后门。newgate plugin 会把它列在 infra 组里。
			self, err := service.RegisterSelf("plugin-manager", nil)
			if err != nil {
				return err
			}
			releases = append(releases, self)

			// web 界面那一份：开关点画成开关。放在 cli 那段「没装就 return」**之前**
			// ——只装 dashboard 的装配里也要有（与 config/gateway 同一条）。
			//
			// 注册时只登记产出函数，不算「现在开着没有」：每条 `newgate …` 命令都会
			// 跑到这里，而绝大多数进程没有人会看界面（见 lib/view 的包注释）。
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				rel, err := v.Register("plugin-manager",
					viewapi.Title(func() string { return i18n.T("Plugins", nil) }).
						In(func() string { return i18n.T("Runtime", nil) }).
						Of(viewapi.FieldSettings),
					func() ([]viewapi.Concept, error) {
						return viewConcepts(service)
					})
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}

			// 命令与状态行都是这个模块的用户界面，由它自己贡献——界面不认识它。
			// ui 没装就跳过：开关账本照常工作，只是没有入口。
			cli, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			cmd := &command{manager: service}
			cmdRelease, err := cli.RegisterCommand(cmd)
			if err != nil {
				return err
			}
			releases = append(releases, cmdRelease)
			statusRelease, err := cli.RegisterStatus(cmd)
			if err != nil {
				return err
			}
			releases = append(releases, statusRelease)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
