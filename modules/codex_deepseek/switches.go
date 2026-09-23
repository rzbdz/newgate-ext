package codex_deepseek

import (
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/pluginmanager"
)

// SwitchDegradeTools 这一手：把 Codex 工具树里的 `custom` 声明降成 `function`
// （见 st-tools.go）。
//
// 关掉它的症状很直接：上游 400 `Unsupported custom tool: 'exec'. Only
// 'apply_patch' is supported.`（藏在 namespace 里时换一句 `Currently custom tools
// are not allowed inside a namespace`）。所以关掉是排查动作，默认开着。
//
// **为什么开关归本模块**：只认 apply_patch 一个 custom 是 **DeepSeek** 的规矩；
// Codex 配 Ark / Claude / Kimi 时那一手根本不该跑（他们原生吃 custom，见
// modules/codex/st-tools.go 文件头那张实测表）。所以它是 crossing 的开关，
// 不是客户端的。
//
// **它 2026-09-23 之前叫 `codex-deepseek.lift-tools`**，改名是因为同一个开关
// 底下其实压着两件事——「把工具从 input[0] 抬到顶层」与「把 custom 降成
// function」——而前者与上游是谁无关。抬的那一手搬去了 modules/codex
// （`codex.lift-tools`，行为与开关的拥有者对齐），这里只留 DeepSeek 那一半。
const SwitchDegradeTools = "codex-deepseek.degrade-tools"

// Switches 本模块上报的开关点清单，供 module.go 注册。
//
// 前缀是 `codex-deepseek`（连字符）：path 的前缀必须逐字等于 Component.Name，
// 目录名才用下划线。写错了注册期就拒（实测过）。
func Switches() []pluginmanager.Switch {
	return []pluginmanager.Switch{{
		Path:  SwitchDegradeTools,
		Title: i18n.T("degrade Codex custom tools for DeepSeek", nil),
		Why: i18n.T("DeepSeek's /responses accepts `custom` tools only for "+
			"apply_patch and answers 400 for every other one (the message inside a "+
			"namespace is a different one); with the degrade off a Codex request "+
			"carrying any other custom tool is rejected outright", nil),
		Danger:  pluginmanager.DangerQuirk,
		Default: true,
	}}
}
