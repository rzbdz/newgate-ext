// Package claudecode owns Claude Code's client descriptor and client-only
// behavior. It must not import model-family modules.
package claudecode

import agentapi "github.com/rzbdz/newgate/modules/confighook"

const ID = "claude"

func Agent() *agentapi.Agent {
	return &agentapi.Agent{
		ID:      ID,
		Bin:     []string{"claude"},
		Dialect: "anthropic",
		Slots: []agentapi.Slot{
			{Name: "fable", Tier: "heavy", EnvVar: "ANTHROPIC_DEFAULT_FABLE_MODEL",
				Desc: "fable 档（最贵档）；也是第三方模型族自动回退的识别依据"},
			{Name: "opus", Tier: "normal", EnvVar: "ANTHROPIC_DEFAULT_OPUS_MODEL",
				Desc: "opus 档（主力档）；主循环在这跑（cc 默认），Plan Mode 下的 opusplan"},
			{Name: "sonnet", Tier: "mid", EnvVar: "ANTHROPIC_DEFAULT_SONNET_MODEL",
				Desc: "sonnet 档；Bash 分类器与 /compact 总结在这跑"},
			{Name: "haiku", Tier: "light", EnvVar: "ANTHROPIC_DEFAULT_HAIKU_MODEL",
				Desc: "haiku 档；后台功能"},
			{Name: "subagent", Tier: "mid", EnvVar: "CLAUDE_CODE_SUBAGENT_MODEL",
				Desc: "所有 subagent / agent team / workflow；设 inherit 可交还给各自解析"},
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
		Notes: "settings.json 的 model 字段不动；档位含义由 env 决定",
	}
}
