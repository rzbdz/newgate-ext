package codex_deepseek

import (
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/pluginmanager"
)

// SwitchLiftTools 这一手：把 Codex 的 additional_tools 抬到顶层 tools。
//
// 关掉它的症状是**一整类**的：模型看不见任何工具，于是用训练里的文本格式去
// 「调工具」，客户端解析不出来，一个 tool_result 都没有（现场原话：「从来没成功
// 获取 tool_result」）。这比报错更难查——请求 200、正文看着也像模像样。
const SwitchLiftTools = "codex-deepseek.lift-tools"

// Switches 本模块上报的开关点清单，供 module.go 注册。
//
// 前缀是 `codex-deepseek`（连字符）：path 的前缀必须逐字等于 Component.Name，
// 目录名才用下划线。写错了注册期就拒（实测过）。
func Switches() []pluginmanager.Switch {
	return []pluginmanager.Switch{{
		Path:  SwitchLiftTools,
		Title: i18n.T("lift Codex tools to the top level (the only step)", nil),
		Why: i18n.T("the tool definitions stay in input[0], where DeepSeek does not "+
			"read them: the model sees no tools at all, answers in its own text "+
			"format, and Codex never gets a tool_result back", nil),
		Danger:  pluginmanager.DangerQuirk,
		Default: true,
	}}
}
