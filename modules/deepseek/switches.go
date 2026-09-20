package deepseek

import (
	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/gateway/special"
	"github.com/rzbdz/newgate/modules/pluginmanager"
)

// 本模块上报的运行期开关点。
//
// 为什么要细到这个程度：这个模块的 Apply 里压着**三手**互不相干的修补（补
// reasoning_content、补 thinking 块、修尾部形状），而它们在 `newgate st` 那层
// 只有一个名字 "deepseek"——「要么全要要么全不要」。排查时最常需要的就是
// 「只关掉第四手看看 400 还在不在」，那正是粒度不够时会卡住的动作。
//
// 路径前缀必须是模块名，由 pluginmanager 在注册期强制（见它的 validateSwitches）。
const (
	// SwitchBackfillReasoning 第 2 手：给 assistant 消息补 reasoning_content
	// （OpenAI 方言那句 400）。
	SwitchBackfillReasoning = "deepseek.backfill-reasoning"
	// SwitchBackfillThinkingBlock 第 3 手：Anthropic 方言下给 assistant 消息的
	// content[] 开头补 thinking 块。
	SwitchBackfillThinkingBlock = "deepseek.backfill-thinking-block"
	// SwitchTailShape 第 4 手：最后一条 user 消息只有 tool_result 时追加一条最简
	// 指令。这是唯一修**根因**的一手。
	SwitchTailShape = "deepseek.tail-shape"
)

// Switches 本模块上报的开关点清单，供 module.go 注册。
//
// 三条都是 quirk 而不是 footgun：关掉它们**不会**破坏 newgate 自己，只会让
// DeepSeek 那边开始回 400（因为它们是来修上游怪癖的）。所以不设默认时限——
// 排查时经常需要开着观察一阵。这也是它们与 gateway.passthrough 的区别：
// 那个是把这一层整个拆掉，所以必须限时。
// 文案在这里现算而不做成包级变量：**包初始化早于装语言**（`modules/locale`
// 在它的 Start 里才把目录装进 lib/i18n），而 Switches 只在 Start 里被调一次
// ——装配序里 locale 排在 deepseek 之前（本模块排在最末），所以这一刻目录已经
// 就绪。`reasoning_content` / `content[].thinking` / `tool_result` / `user` /
// `assistant` 是协议字段名，留在消息外面。
func Switches() []pluginmanager.Switch {
	return []pluginmanager.Switch{
		{
			Path:  SwitchBackfillReasoning,
			Title: i18n.T("Backfill reasoning_content (hand 2)", nil),
			Why: i18n.T("assistant messages in the history no longer get their reasoning "+
				"text backfilled, and OpenAI-dialect DeepSeek answers 400 to "+
				"\"reasoning_content must be passed back\" (the chain then advances on "+
				"its own; the symptom is a silent provider switch)", nil),
			Danger:  pluginmanager.DangerQuirk,
			Default: true,
		},
		{
			Path:  SwitchBackfillThinkingBlock,
			Title: i18n.T("Backfill the thinking block (hand 3)", nil),
			Why: i18n.T("Anthropic-dialect requests no longer get a thinking block "+
				"prepended to content[], which triggers the same "+
				"\"content[].thinking must be passed back\" 400", nil),
			Danger:  pluginmanager.DangerQuirk,
			Default: true,
		},
		{
			Path:  SwitchTailShape,
			Title: i18n.T("Repair the tail shape (hand 4)", nil),
			Why: i18n.T("\"the last user message holds only tool_result\" is no longer "+
				"repaired (no \"continue\" appended), and the upstream misreports such "+
				"an instruction-less tail as a missing reasoning_content and answers 400",
				nil),
			Danger:  pluginmanager.DangerQuirk,
			Default: true,
		},
	}
}

// enabled 这条开关点现在是开着的吗。
//
// **r.State 为 nil 时一律算开着**：开关点默认就是开的，缺席的快照不该把补丁
// 关掉——那方向反了（fail-closed）。旧调用面与单测里 State 常常是 nil。
func enabled(r *special.Request, path string) bool {
	if r == nil {
		return true
	}
	return !pluginmanager.Off(r.State, path)
}
