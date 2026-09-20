// Package claudecode owns Claude Code's client descriptor and client-only
// behavior. It must not import model-family modules.
package claudecode

import (
	i18n "github.com/rzbdz/newgate/lib/i18n"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
)

const ID = "claude"

func Agent() *agentapi.Agent {
	return &agentapi.Agent{
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
		UnsetEnv: []string{
			"ANTHROPIC_API_KEY",
			"ANTHROPIC_MODEL",
			"ANTHROPIC_SMALL_FAST_MODEL",
		},
		Notes: i18n.T("the model field in settings.json is left untouched; what a tier means is decided by the env", nil),
	}
}
