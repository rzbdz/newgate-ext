// Package claudecode owns Claude Code's client descriptor and client-only
// behavior. It must not import model-family modules.
package claudecode

import (
	i18n "github.com/rzbdz/newgate/lib/i18n"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
)

const ID = "claude"

// Agent 返回 Claude Code 的描述符。
//
// # 窗口为什么不再用模型名后缀 [1m]
//
// 2026-09-28 到 2026-11-12 之间，这里给每个槽位拼了 `[1m]`，让 Claude Code
// 按 100 万上下文对待。现场（2026-11-12，deepseek-flash）证明那个做法把会话
// 顶到了真实窗口的最后一格：会话涨到 1.01M tokens，下一条请求 400
// "Input tokens exceed the model context limit"，而 /compact 也发不出去——
// 压缩请求本身带着整段会话，同样超限。客户端于是**没有自救路径**。
//
// 现在名字原样注入（档位名或真实模型名），窗口由
// CLAUDE_CODE_MAX_CONTEXT_TOKENS 声明，并且封顶 512K（ContextWindowCap）。
// 不认识的名字会走 Claude Code 的「未知模型」分支，它**强制**按声明的窗口
// 自动 compact（2.1.322 实测），所以 512K 是一个真的天花板，不只是显示。
// 也不再发 `context-1m` beta 头，上游不会被告知按 1M 处理。
//
// compact 点另外封到 400K（AutoCompactCap）：newgate 的推理回填会把请求
// 再撑大（2026-11-12 实测 7.9MB → 9.0MB，约 14%），如果等到 500K 才压缩，
// 回填后已经贴住 512K 了。
func Agent() *agentapi.Agent {
	return &agentapi.Agent{
		// 界面上的图标（见 icon.go：它为什么住在这个模块里）。
		Icon:    iconPath,
		ID:      ID,
		Bin:     []string{"claude"},
		Dialect: "anthropic",
		Slots: []agentapi.Slot{
			{Name: "fable", Tier: "heavy", EnvVar: "ANTHROPIC_DEFAULT_FABLE_MODEL",
				Desc: i18n.T("the fable tier (the most expensive); also the marker used to spot a third-party model family falling back automatically", nil)},
			{Name: "opus", Tier: "normal", EnvVar: "ANTHROPIC_DEFAULT_OPUS_MODEL",
				Desc: i18n.T("the opus tier (the workhorse); the main loop runs here (Claude Code's default), and so does opusplan in Plan Mode", nil)},
			{Name: "sonnet", Tier: "mid", EnvVar: "ANTHROPIC_DEFAULT_SONNET_MODEL",
				Desc: i18n.T("the sonnet tier; the Bash classifier and /compact summaries run here", nil)},
			{Name: "haiku", Tier: "light", EnvVar: "ANTHROPIC_DEFAULT_HAIKU_MODEL",
				Desc: i18n.T("the haiku tier; background features", nil)},
			// Also：`inherit` 是 **Claude Code 自己的**取值（跟父会话走），不是我们的
			// 档位。声明在这里，槽位映射那条可配置的路才不会把它当成打错的档位名滤掉
			// ——说明里写着可以这么设，界面与配置就必须真的收得下（见 agentapi.Slot.Also）。
			//
			// **没有能力标记**：它注入的可能是 `inherit` 这个取值，任何后缀都会
			// 让 Claude Code 去找一个叫 `inherit…` 的模型。能力标记说明的是「这个
			// 名字指向的模型有多大的上下文」，而 `inherit` 压根不指向一个模型。
			{Name: "subagent", Tier: "mid", EnvVar: "CLAUDE_CODE_SUBAGENT_MODEL", Also: []string{"inherit"},
				Desc: i18n.T("every subagent / agent team / workflow; set it to inherit to hand that back to per-slot resolution", nil)},
		},
		BaseURLEnv: "ANTHROPIC_BASE_URL",
		AuthEnv:    "ANTHROPIC_AUTH_TOKEN",
		// 窗口声明两个变量的**名字**（内核只负责「配置里声明了就按这个名字注入」，
		// 名字是客户端的知识，见内核 modules/confighook 的 Agent 字段注释）。
		// 不给的话，Claude Code 对不认识的模型名按 200k 假设提前 compact。
		ContextWindowEnv: "CLAUDE_CODE_MAX_CONTEXT_TOKENS",
		AutoCompactEnv:   "CLAUDE_CODE_AUTO_COMPACT_WINDOW",
		// 安全上限：512K 窗口 / 400K 压缩点。理由见 Agent 的注释——超过 1M 的
		// 会话一旦被拒，连 /compact 都救不回来，留余量是唯一能自动恢复的做法。
		ContextWindowCap: 512000,
		AutoCompactCap:   400000,
		UnsetEnv: []string{
			"ANTHROPIC_API_KEY",
			"ANTHROPIC_MODEL",
			"ANTHROPIC_SMALL_FAST_MODEL",
		},
		Notes: i18n.T("the model field in settings.json is left untouched; what a tier means is decided by the env", nil),
	}
}
