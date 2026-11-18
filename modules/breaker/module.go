package breaker

import (
	"context"

	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	gatewayapi "github.com/rzbdz/newgate-ext/modules/gateway"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// New 声明健康表组件。
//
// **依赖方向（2026-09-18 翻转）**：它现在声明 `Need(gateway)`，因为它是数据面
// 的**消费者**——健康表是一个有状态、有所有者的服务，它把自己挂进 gateway 留出
// 的四个决策点（见 plane.go）。在那之前方向是反的：gateway 声明 Need(breaker)，
// 把一个 breaker.Breaker 塞进 forward.Server 的字段里当 lib 使，热路径里写着
// `breaker.BucketShape`。按 docs/03-architecture.md §2 的判据（「需要被启动、
// 被停止、被撤销吗？」）那是错的。
//
// 它不认识 agent、profile 或上游协议：健康表只认 binding 键、状态码和延迟。
//
// 装载 health.json 失败**不**让装配失败：坏掉的健康表只该退化成纯内存，不能
// 因为一个可再生的缓存文件让整个代理起不来。错误存下来，等 BindEnv 装上日志
// 出口后补报出去（「不静默」）。
func New() modules.Component {
	table := newTable()
	var releases []modules.Release
	return modules.Component{
		Name: "breaker",
		Desc: func() string {
			return i18n.T("binding health — availability and latency — consulted at the data plane's decision points", nil)
		},
		Type: "infra",
		Requires: []modules.Requirement{
			// 数据面是 owner，我往它的口里插自己。这条边也是启动顺序的排序边
			// ——gateway → breaker → deepseek（deepseek 要用我的形状判据口）。
			modules.Need(gatewayapi.Capability),
			// ui 是**弱依赖**（见 component.Optional）：装着界面就把 `newgate
			// breaker` 挂上去，没装就跳过——健康表本身照常工作，只是没有入口。
			modules.Optional(cliapi.Capability),
			// web 界面那条同理，而且它是**另一条**：只装 dashboard（不装 cli）的
			// 装配里，这张表照样该出现在网页上。
			modules.Optional(viewapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Breaker(table)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			// 装载归自己：健康状态跨优雅重启存活。
			_ = table.UseFile(paths.HealthFile())

			// **写侧**：把自己挂进数据面。Start 只做「启动自己的服务 + 往 owner
			// 注册自己的贡献」，不读别人注册了什么（见 docs/02-component-framework.md
			// 的三段法则）——注册表是 offer 的，读它的时刻在 Serve 期。
			gateway := modules.MustGet(ctx, gatewayapi.Capability)
			release, err := gateway.RegisterFilter(plane{t: table})
			if err != nil {
				return err
			}
			releases = append(releases, release)

			// web 界面那一份**先**注册：它不依赖 cli，只装 dashboard 的装配里也要
			// 有——下面那段一旦 return，这里就永远不会跑（与 gateway 同一条）。
			// 探一条 binding 那条缝由网关提供（见 gateway.ProbeBinding）：发起探活
			// 需要的东西（base、key、方言）只有它有。Need(gateway) 是本模块既有的
			// 依赖，所以这里一定有。
			prober := modules.MustGet(ctx, gatewayapi.Capability)

			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				viewRelease, err := v.Register("breaker",
					viewapi.Title(func() string { return i18n.T("Breaker", nil) }).
						In(func() string { return i18n.T("Data plane", nil) }).
						Of(viewapi.FieldRoutes),
					func() ([]viewapi.Concept, error) {
						return healthConcepts(table, prober)
					})
				if err != nil {
					return err
				}
				releases = append(releases, viewRelease)
			}

			ui, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			cmdRelease, err := ui.RegisterCommand(breakerCommand{})
			if err != nil {
				return err
			}
			releases = append(releases, cmdRelease)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
