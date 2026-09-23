package codex

import (
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/pluginmanager"
)

// SwitchLiftTools 这一手：把 Codex 的 additional_tools 抬到顶层 tools（见 st-tools.go）。
//
// 关掉它 = 回到 2026-09-23 之前的行为：模型看不见任何工具，于是用训练里的文本
// 格式去「调工具」，客户端解析不出来，一个 tool_result 都没有。所以它默认开着，
// 关掉是排查动作。
//
// **为什么开关归本模块而不是 crossing**：这一手修的是「Codex 发的形状」，与上游
// 是谁无关（Ark / DeepSeek / Gemini / … 都只读顶层）。开关跟着**行为的拥有者**
// 走——2026-09-23 之前这一个开关挂在 codex_deepseek 上（`codex-deepseek.lift-tools`），
// 于是「Codex 配 Ark」那条路上根本没有开关可关，而同一个行为在同一份二进制里
// 有两个名字。
const SwitchLiftTools = "codex.lift-tools"

// Switches 本模块上报的开关点清单，供 module.go 注册。
//
// 前缀是 `codex`（= Component.Name），由 pluginmanager 在注册期强制。
func Switches() []pluginmanager.Switch {
	return []pluginmanager.Switch{{
		Path:  SwitchLiftTools,
		Title: i18n.T("lift Codex tools from input[0] to the top level", nil),
		Why: i18n.T("Codex 0.155 puts tool definitions in `input[0]` "+
			"(type=additional_tools) and every upstream we ship reads tools from the "+
			"top level only: with the lift off the model sees no tools at all, answers "+
			"in its own text format, and Codex never gets a tool_result back", nil),
		Danger:  pluginmanager.DangerQuirk,
		Default: true,
	}}
}
