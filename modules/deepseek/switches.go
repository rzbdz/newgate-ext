package deepseek

import (
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	"github.com/rzbdz/newgate-ext/modules/pluginmanager"
	"github.com/rzbdz/newgate/lib/i18n"
)

// This module's runtime switch points.
//
// Why such fine granularity: this module's Apply carries **four** unrelated patches
// (fill reasoning_content, insert a thinking block, fix the messages tail shape, fix
// the Responses tail shape), yet at the `newgate st` level they have one name,
// "deepseek" - all or nothing. What you most often need when debugging is "turn off
// only pass 4 and see if the 400 remains", precisely what coarse granularity blocks.
//
// The path prefix must be the module name, enforced by pluginmanager (see validateSwitches).
const (
	// SwitchBackfillReasoning is pass 2: add reasoning_content to assistant messages
	// (the OpenAI dialect 400).
	SwitchBackfillReasoning = "deepseek.backfill-reasoning"
	// SwitchBackfillThinkingBlock is pass 3: in the Anthropic dialect, put a thinking
	// block at the start of the assistant message's content[].
	SwitchBackfillThinkingBlock = "deepseek.backfill-thinking-block"
	// SwitchTailShape is pass 4: when the last user message is tool_result-only, append a
	// minimal instruction (messages dialect). Passes 4 and 5 are the root-cause ones.
	SwitchTailShape = "deepseek.tail-shape"
	// SwitchTailShapeResponses is pass 5: in the Responses dialect (/v1/responses), when
	// input[] ends with a foreign function_call_output, append a minimal instruction at
	// the end of input[]. The same thing as pass 4 in another dialect, switchable apart.
	SwitchTailShapeResponses = "deepseek.tail-shape-responses"
)

// Switches is this module's switch point list, for module.go to register.
//
// All four are quirks, not footguns: turning them off does **not** break newgate
// itself, it only makes DeepSeek start answering 400 (they fix an upstream quirk).
// So no default time limit - when debugging you often want one off for a while.
// That is also how they differ from gateway.passthrough, which tears down the whole
// layer and therefore must be time-limited.
// The text is computed here, not in a package-level var: **package init runs before
// languages are installed** (`modules/locale` installs catalogs in Start), while
// Switches runs once in Start, after locale (deepseek is last). `reasoning_content` /
// `content[].thinking` / `tool_result` / `user` / `assistant` stay outside the text.
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
		{
			Path:  SwitchTailShapeResponses,
			Title: i18n.T("Repair the Responses tail shape (hand 5)", nil),
			Why: i18n.T("\"the Responses input[] ends on a function_call_output whose "+
				"call_id is not ours\" is no longer repaired (no \"continue\" appended), "+
				"and the upstream misreports such an instruction-less tail as a missing "+
				"reasoning_text and answers 400", nil),
			Danger:  pluginmanager.DangerQuirk,
			Default: true,
		},
	}
}

// enabled says whether this switch point is on now.
//
// **r.State nil counts as on**: switch points default to on; a missing snapshot must
// not turn patches off - that is fail-closed, the wrong direction (old callers, tests).
func enabled(r *special.Request, path string) bool {
	if r == nil {
		return true
	}
	return !pluginmanager.Off(r.State, path)
}
