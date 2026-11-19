package thinking

import (
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/gateway/rewrite"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

var _ special.Plugin = emptyThinking{}

// emptyThinking drops assistant content[] blocks that carry no thinking at all -
// {"type":"thinking","thinking":" ","signature":""}.
//
// # The live failure (2026-11-19, dump/err-400-req002560)
//
// Claude Code 2.1.328 sent a 227-message session in which seven assistant turns opened with
// exactly that block (thinking = one space, signature = empty). The client sent it; we changed
// one byte in the body (the model) and the upstream answered
//
//	400 … thinking: each thinking block must contain non-whitespace thinking
//
// The block is worthless by construction: no text, no signature, no encrypted payload. So the
// repair is to **remove it**, not to fill it: writing a placeholder would hand the model
// reasoning it never produced, which is the one thing this repo's reasoning patches never do
// (see modules/deepseek/st-reasoning.go, "no original text means skip it, never invent one").
//
// # Why a drop rather than a rewrite of the empty string
//
// It was measured live first: after the dump's seven blocks were removed by hand and the body
// replayed, the same upstream answered 200. The block is optional in the protocol - a client
// may send assistant turns with no thinking block at all - so removing it cannot make a valid
// history invalid.
//
// The criterion is deliberately narrow ("whitespace only"), not "thinking looks short" or
// "signature is missing": 78 of the 85 thinking blocks in that same dump carry real text with
// no signature, and they are perfectly fine. Only a block with **nothing in it** is dropped.
type emptyThinking struct{}

func (emptyThinking) Name() string { return "empty-thinking" }

// Why is user-facing: `newgate st` prints it next to the switch.
func (emptyThinking) Why() string {
	return i18n.T("some clients send assistant turns carrying a thinking block with only "+
		"whitespace in it; strict Anthropic upstreams refuse the whole request\n"+
		"drop those blocks (they hold no text and no signature, so removing one changes "+
		"no meaning) - never fill them with invention", nil)
}

// After puts this plugin behind the ones that **write** thinking blocks (deepseek backfills
// real reasoning into turns that have none). Ordering it earlier would turn "this turn already
// has a thinking block" into "this turn has none", and deepseek would then rebuild one from
// its cache - a different repair than the one being asked for here. Running last means it only
// ever cleans up what is left.
func (emptyThinking) After() []string  { return []string{"deepseek"} }
func (emptyThinking) Before() []string { return nil }

// Match claims the Anthropic dialect only: "thinking" is a block type of that protocol, and an
// OpenAI-dialect body has nothing here to drop.
//
// Match cannot see the body (that is the plugin contract), so it cannot know whether this
// particular request has an empty block; Apply returns zero notes when there is none, and the
// framework treats "no notes" as "nothing changed" - the request is forwarded byte for byte.
func (emptyThinking) Match(r *special.Request) bool {
	return r != nil && domain.IsAnthropicPath(r.Path)
}

func (emptyThinking) Apply(body []byte, _ *special.Request) ([]byte, []string, error) {
	out, n, err := rewrite.DropArrayItemBlocks(body, "messages", "content", isAssistantMessage, isEmptyThinkingBlock)
	if err != nil || n == 0 {
		// Unrecognized shape: leave the body alone and let the upstream answer. A repair that
		// cannot read the request must never guess at it.
		return body, nil, nil
	}
	return out, []string{i18n.N(
		"dropped {n} empty thinking block (whitespace only, no signature) that a strict upstream refuses",
		"dropped {n} empty thinking blocks (whitespace only, no signature) that a strict upstream refuses",
		n, nil)}, nil
}

func isAssistantMessage(item []byte) bool {
	role, _ := rewrite.TopLevelString(item, "role")
	return role == "assistant"
}

// isEmptyThinkingBlock: a thinking block whose text is whitespace only.
//
// absent text (`{}` or `{"type":"thinking"}`) counts as empty too - the upstream's check is on
// the text it sees, and there is none.
func isEmptyThinkingBlock(block []byte) bool {
	if t, _ := rewrite.TopLevelString(block, "type"); t != "thinking" {
		return false
	}
	text, _ := rewrite.TopLevelString(block, "thinking")
	return strings.TrimSpace(text) == ""
}
