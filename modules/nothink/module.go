// Package nothink 是「全局不思考」开关：`newgate nothink on|forever|off`。
//
// # 它是什么
//
// 一条**用户显式打开**的产品策略：开着的时候，每一条 Anthropic 方言的请求在发往
// 上游之前都被写上 `thinking:{"type":"disabled"}`——**包括客户端自己写着
// adaptive / enabled 的那些**。后面这半句是这个开关成不成立的关键：Claude Code
// 的主循环每一条请求都带 `thinking:{"type":"adaptive"}`（2026-09-29 的现场 dump
// err-400-req000010：stream=true、28 个 tools、model=heavy），尊重客户端意图
// （内核 claude-bg 那条规矩）在这里就等于什么都不做。
//
// # 为什么是 best effort
//
// 有的模型关不掉思考：GLM 1210「该模型始终思考，不支持关闭思考」、kimi
// 「only type=enabled is allowed」。对它们，「不想思考」唯一能落地的最小形态是
// `enabled` + `reasoning_effort:"low"`；翻不动就原样发出去。这一手的语义**只有
// 一份**，住内核 `modules/thinking`（BestEffortDisable，连着 quirk 表与探活缓存
// 一起）：本模块只表达「全局都别思考」这个意图，怎么翻是模型那一侧的事，
// 所以这里一行模型知识都没有。
//
// # 为什么只认 Anthropic 方言
//
// `thinking` 是那一套协议里的字段。OpenAI 方言的客户端（opencode / codex）
// **压根没有这个字段可写**——替它们补 disabled 的现场是「deepseek 不思考了」
// （2026-09-15，见 modules/claudecode_glm 的注释）。所以这里按**方言**认领
// （`r.Path`，判据 domain.IsAnthropicPath），不按客户端：谁说的是这套方言，
// 谁就被这条开关覆盖。
//
// # 它为什么住在发行版
//
// 它不是「修上游怪癖」（那是模型模块的事），是一条**产品取舍**：用户愿意用
// 「这轮不推理」换钱和等待。所以默认全程关、必须显式打开，开着的时候
// `newgate status` 一直打出来（不静默），随时 `newgate nothink off` 收手。
package nothink

import (
	"context"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	confighookapi "github.com/rzbdz/newgate/modules/confighook"
	gatewayapi "github.com/rzbdz/newgate/modules/gateway"
	thinkingapi "github.com/rzbdz/newgate/modules/thinking"
)

// Name 是这个模块在装配表与 `newgate plugin` 里的名字（= 目录名），
// 也是 state.json 里那一格的键（见 StateKey）。
const Name = "nothink"

// New 声明这个组件。四条依赖各自的理由：
//
//   - gateway（Need）：插件挂在 special 链上，改的是请求体；
//   - thinking（Need）：翻译那一半住内核，本模块不重造（见包注释）；
//   - config-hook（Need）：往 state.json 里占下 "nothink" 这一格（同
//     classifier_naked）。两个模块抢同一个键在**注册期**就该被拒，
//     而不是看谁后写谁赢；
//   - cli（Optional）：`newgate nothink` 是这条策略的入口。界面不在的装配里
//     （只有网页那一份）本模块照常装着、策略照常生效，只是没有命令能改它
//     ——业务模块不依赖任何 ui 那条规矩在这里的意思是「不装界面也能活」。
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: Name,
		Desc: func() string {
			return i18n.T("a global no-reasoning switch, best effort (newgate nothink on|forever|off)", nil)
		},
		// 与内核的 modules/thinking 同一类：它是一条与模型家族无关的思考策略。
		Type: "model",
		Requires: []modules.Requirement{
			modules.Need(gatewayapi.Capability),
			modules.Need(thinkingapi.Capability),
			modules.Need(confighookapi.ConfigHooksCapability),
			modules.Optional(cliapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			gateway := modules.MustGet(ctx, gatewayapi.Capability)
			hook, err := gateway.RegisterRequestHook(nothink{
				thinking: modules.MustGet(ctx, thinkingapi.Capability),
			})
			if err != nil {
				return err
			}
			releases = append(releases, hook)

			hooks := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			field, err := hooks.RegisterStateField(Name, StateKey)
			if err != nil {
				return err
			}
			releases = append(releases, field)

			if ui, ok := modules.Get(ctx, cliapi.Capability); ok {
				command, err := ui.RegisterCommand(nothinkCommand{})
				if err != nil {
					return err
				}
				releases = append(releases, command)
			}
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
