// Package claudecode owns Claude Code's client descriptor and client-only
// behavior. It must not import model-family modules.
package claudecode

import (
	i18n "github.com/rzbdz/newgate/lib/i18n"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
	"github.com/rzbdz/newgate/modules/gateway/protocol"
)

const ID = "claude"

// oneM 是「这个槽位注入的模型名后面要带 1M 能力标记」那件事的**唯一**读法。
//
// 为什么是一个这样的短名字、而不是在下面每个槽位上写一遍
// `ModelSuffix: protocol.OneMMarker`：常量本身住在数据面（gateway/protocol），
// 那里只有一句「转发前剥掉它」的用法——**需要它的那一侧是客户端**。所以这里
// 留一个本地入口，五个槽位表里出现的是它，读者一眼能看出「这一列说的是同一件事」。
//
// 反过来，**绝不在本模块里再写一遍 `"[1m]"`**：字符串只有一份（protocol.OneMMarker），
// 谁抄一份谁就在改协议时漏掉一处——那一处的症状是「注入没生效」，而不是编译不过。
const oneM = protocol.OneMMarker

// Agent 返回 Claude Code 的描述符。
//
// # 模型名后面为什么都带 [1m]
//
// Claude Code 用模型名后缀 `[1m]` 声明「这个模型按 100 万上下文对待」。它**不是**
// 上游模型 id 的一部分：数据面在转发前把它剥掉（gateway/protocol.NormalizeRole），
// 剥完剩下的名字正是配置里那个模型名，所以「客户端点名要某个真实模型」的反查
// 照旧命中（resolve.ResolveRequest）。
//
// 代价与取舍：不带这个标记时 Claude Code 按 200k 假设，窗口远没到就自己 compact
// ——而用户配的可能是几百万上下文的模型。反过来带错了的风险只落在**模型名本身**：
// 我们注入的是它，它原样发回来，我们剥掉之后还是同一个名字。
//
// 例外只有一个：subagent 槽位的 `inherit`（见下）——那不是模型名，永远不加。
func Agent() *agentapi.Agent {
	return &agentapi.Agent{
		ID:      ID,
		Bin:     []string{"claude"},
		Dialect: "anthropic",
		Slots: []agentapi.Slot{
			{Name: "fable", Tier: "heavy", EnvVar: "ANTHROPIC_DEFAULT_FABLE_MODEL", ModelSuffix: oneM,
				Desc: i18n.T("the fable tier (the most expensive); also the marker used to spot a third-party model family falling back automatically", nil)},
			{Name: "opus", Tier: "normal", EnvVar: "ANTHROPIC_DEFAULT_OPUS_MODEL", ModelSuffix: oneM,
				Desc: i18n.T("the opus tier (the workhorse); the main loop runs here (Claude Code's default), and so does opusplan in Plan Mode", nil)},
			{Name: "sonnet", Tier: "mid", EnvVar: "ANTHROPIC_DEFAULT_SONNET_MODEL", ModelSuffix: oneM,
				Desc: i18n.T("the sonnet tier; the Bash classifier and /compact summaries run here", nil)},
			{Name: "haiku", Tier: "light", EnvVar: "ANTHROPIC_DEFAULT_HAIKU_MODEL", ModelSuffix: oneM,
				Desc: i18n.T("the haiku tier; background features", nil)},
			// Also：`inherit` 是 **Claude Code 自己的**取值（跟父会话走），不是我们的
			// 档位。声明在这里，槽位映射那条可配置的路才不会把它当成打错的档位名滤掉
			// ——说明里写着可以这么设，界面与配置就必须真的收得下（见 agentapi.Slot.Also）。
			//
			// **不给它 ModelSuffix**：它注入的可能是 `inherit` 这个取值，加了后缀等于
			// 让 Claude Code 去找一个叫 `inherit[1m]` 的模型。能力标记说明的是「这个名字
			// 指向的模型有多大的上下文」，而 `inherit` 压根不指向一个模型。
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
