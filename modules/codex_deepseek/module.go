// Package codex_deepseek 只处理 Codex 与 DeepSeek 的交叉语义。
//
// 它修的是**两家各自都没错、只有放在一起才坏**的那件事：Codex 0.155 把工具定义
// 放在 `input[0]` 的 `additional_tools` 里（它自己的方言），而 DeepSeek 的
// /responses 只从顶层 `tools` 读工具。两边各按自己的规矩办事，结果模型一个工具
// 都看不见——它会退回训练里的文本格式去「调工具」，客户端解析不出来，于是
// **一个 tool_result 都没有**（现场原话：「从来没成功获取 tool_result」）。
//
// 判据属于发行版：这是「客户端×模型的交叉语义」，换个上游或换个客户端都不成立
// （见 CLAUDE.md §1）。内核不认识 codex，也不认识 DeepSeek。
//
// 四处改动，两个方向，缺一处就坏：
//
//	请求侧 1. input[0].additional_tools → 顶层 tools（并原样保留那条 input 项）
//	       2. 工具树里的 `custom` 声明 → `function`（上游只认 apply_patch 一个
//	          custom 类型，别的一律 400 Unsupported custom tool）
//	响应侧 3. 被降级过的工具，其 function_call → custom_tool_call
//	       4. function_call_arguments.* → custom_tool_call_input.*，并把
//	          {"input": "…"} 那层壳拆掉（codex 要的是里面那串原文）
//
// 为什么请求侧要**保留** input[0]：实测过，上游对多出来的 `additional_tools`
// 项不报错（留与不留都 200、都拿到真正的 function_call），所以删它是没有依据的
// 改动——「转发一个请求就该是转发」这条规矩下，能不动就不动。
//
// 响应侧凭什么知道该改哪几条：**从客户端发来的原文里读**（Apply 收到的那份），
// 那里每个 custom 工具都还是 `type: "custom"`。从「Apply 之后」的 body 倒推是
// 猜（降级出来的 function 与原生 function 长得一模一样），从原文读是事实。
// 见 special.Egressor 的说明。
package codex_deepseek

import (
	"context"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	gatewayapi "github.com/rzbdz/newgate/modules/gateway"
	pluginmanagerapi "github.com/rzbdz/newgate/modules/pluginmanager"

	codexapi "github.com/rzbdz/newgate-ext/modules/codex"
	deepseekapi "github.com/rzbdz/newgate-ext/modules/deepseek"
)

// New 声明 Codex × DeepSeek 交叉组件。
// 只有同时解析到客户端、模型和网关端口时，它才注册双方组合后才需要的修补。
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "codex-deepseek",
		Desc: func() string { return i18n.T("the Codex × DeepSeek crossing", nil) },
		Type: "bridge",
		Requires: []modules.Requirement{
			modules.Need(gatewayapi.Capability),
			modules.Need(codexapi.Capability),
			modules.Need(deepseekapi.Capability),
			modules.Need(pluginmanagerapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			gateway := modules.MustGet(ctx, gatewayapi.Capability)
			client := modules.MustGet(ctx, codexapi.Capability)
			model := modules.MustGet(ctx, deepseekapi.Capability)
			for _, treatment := range Treatments(client, model) {
				release, err := gateway.RegisterRequestHook(treatment)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			pm := modules.MustGet(ctx, pluginmanagerapi.Capability)
			self, err := pm.RegisterSelf("codex-deepseek", Switches())
			if err != nil {
				return err
			}
			releases = append(releases, self)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
