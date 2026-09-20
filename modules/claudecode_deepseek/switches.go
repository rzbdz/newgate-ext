package claudecode_deepseek

import (
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/gateway/special"
	"github.com/rzbdz/newgate/modules/pluginmanager"
)

// SwitchInjectThinking 第 1 手：Claude Code 没写 thinking 时补
// thinking:{"type":"disabled"}，从根上不进 DeepSeek 思考模式。
//
// 它是这个交叉模块**唯一**的一手，所以它就是「整个插件」的开关——没必要再单独
// 造一个模块级按钮。
//
// 前缀是 `claudecode-deepseek`（连字符）：path 的前缀必须逐字等于 Component.Name，
// 而组件名用连字符（config-hook / opencode-omo / plugin-manager 都是），目录名才用
// 下划线。写错了 registration 会当场拒绝——这条是实测出来的（第一版写成下划线）。
const SwitchInjectThinking = "claudecode-deepseek.inject-thinking"

// Switches 本模块上报的开关点清单，供 module.go 注册。
func Switches() []pluginmanager.Switch {
	return []pluginmanager.Switch{{
		Path:  SwitchInjectThinking,
		Title: i18n.T("inject thinking:disabled (the first step)", nil),
		Why: i18n.T("Claude Code's requests fall into DeepSeek thinking mode by default, "+
			"but it does not carry third-party thinking blocks into the next round — "+
			"so every call hits the 400 that says thinking must be passed back", nil),
		Danger:  pluginmanager.DangerQuirk,
		Default: true,
	}}
}

// stateOf 从插件上下文里取配置快照。允许 nil（旧调用面与单测常见）——
// pluginmanager.Off 对 nil 一律回 false，也就是「没关」。方向不能反：
// 快照缺席不该把补丁关掉。
func stateOf(r *special.Request) *domain.State {
	if r == nil {
		return nil
	}
	return r.State
}
