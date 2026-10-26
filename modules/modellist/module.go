// Package modellist 让 web 界面问出「这家上游此刻提供哪些模型」。
//
// # 它解决的那件事
//
// providers.json 里的 `models` 是**用户手写**的清单：上游上一个新模型，用户得先
// 去 provider 卡片里补一行，再回到档位里选它。而多数上游其实有
// `GET /models`——能直接列出此刻真正可用的模型，有些还带分组。
//
// 本模块做三件事：
//
//  1. **探测**：按 provider 的 `protocol` 挑适配器（OpenAI / Anthropic 两种方言），
//     问一次上游。问不到（没有这个接口、key 不允许、网络不通）**不是错误**：
//     回一份 `supported:false` + 原因，界面回落到声明清单。
//  2. **收敛**：不同上游的字段名不一样（`id` / `slug`、`owned_by` / `group` /
//     `provider`），这里收敛成 `Model{id,name,group}`——前端只认识一种形状。
//  3. **缓存**：进程内、按 provider 定义的哈希分桶（换了 key/base 就是另一桶），
//     正结果 10 分钟、负结果 1 分钟；下拉旁边的 ↻ 跳过缓存。
//
// # 为什么住在发行版
//
// 它是**产品取舍**而不是内核机制：装不装它，内核都照常工作。它的机制只有两个
// 公开口子——`Service`（别的模块可以问清单）与 web-dashboard 的扩展端点
// （界面可以问清单）。两个方向都只有「本模块 → 界面」，界面不认识它。
package modellist

import (
	"context"

	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// New 声明这个组件。
//
// 依赖只有一条，而且是 Optional：装 web-dashboard 就把扩展端点登记上去；没装
// （`dist-simple-cli.json` 那种只有终端界面的装配）本模块照常装着、`Service`
// 照常可用，只是没有浏览器入口。这正是「业务模块不依赖任何 ui」那条规矩。
func New() modules.Component {
	svc := newService()
	var releases []modules.Release
	return modules.Component{
		Name: Name,
		Desc: func() string {
			return i18n.T("ask a provider which models it serves, for the model picker in the web interface", nil)
		},
		// 与内核的 config/gateway 同一层：它是基础设施，不是客户端、不是上游补丁。
		Type: "infra",
		Requires: []modules.Requirement{
			modules.Optional(webapi.ExtensionCapability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Service(svc)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			// 没装界面就什么都不登记：功能（Service）还在，只是没有入口。
			web, ok := modules.Get(ctx, webapi.ExtensionCapability)
			if !ok {
				return nil
			}
			rel, err := web.Register(Endpoint, svc.handle)
			if err != nil {
				return err
			}
			releases = append(releases, rel)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
