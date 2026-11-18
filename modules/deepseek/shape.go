package deepseek

import (
	"bytes"

	breakerapi "github.com/rzbdz/newgate-ext/modules/breaker"
)

var _ breakerapi.ShapeDetector = (*reasoningShape)(nil)

// reasoningShape recognizes DeepSeek thinking mode's three 400 messages:
//
//	The `reasoning_content` in the thinking mode must be passed back to the API.
//	The `content[].thinking` in the thinking mode must be passed back to the API.
//	The `reasoning_text` in the thinking mode must be passed back to the API.
//
// The three are one upstream's dialect across three endpoints: the first two from the
// OpenAI dialect (/v1/chat/completions) and the Anthropic dialect (/v1/messages), the
// third from the Responses dialect (/v1/responses, measured 2026-09-22 with a probe
// against the real upstream, see docs/06-reasoning.md §1). All three are **request
// shape** problems, not "is this upstream usable": the same body is rejected the same
// way by any provider (rules come from the model side, not the channel), so it must
// not count against any provider's availability. Once recognized, the health table
// only counts and never trips, and the data plane logs [shape-400] with evidence.
//
// Why this criterion lives in deepseek, not core (moved 2026-09-17): the text is
// DeepSeek's dialect. The "must be passed back" wording and the field names it cites
// are this vendor's. A criterion living in the module that knows the truth can be
// tested and evolved there, and core holds no upstream-specific strings.
//
// Deliberately **lenient in, strict out**:
//   - it must really carry "must be passed" (not just the field name) - otherwise a
//     plain "reasoning_content field format error" counts as a shape error, letting a
//     genuine request problem through;
//   - it must name one of the three fields - otherwise "the value must be passed as
//     header" would match too.
//
// Both must hold. Better a miss (one count less, nobody blamed) than a false positive
// (a real availability fault let through).
type reasoningShape struct{}

// ShapeDetector's name goes into logs, metrics and the description in `newgate st`.
func (reasoningShape) Name() string { return "deepseek" }

func (reasoningShape) Match(status int, body []byte) bool {
	if status != 400 || len(body) == 0 {
		return false
	}
	if !bytes.Contains(body, []byte("must be passed back")) &&
		!bytes.Contains(body, []byte("must be passed")) {
		return false
	}
	// Each dialect names one field: OpenAI says reasoning_content, Anthropic says
	// content[].thinking, Responses says reasoning_text. All three must match - one
	// upstream, three endpoints, three wordings (third measured on 2026-09-22).
	//
	// Lenient-in: one missed field records a shape 400 as an availability fault (a
	// false trip), while an extra match only costs a count - so all three match, and
	// the endpoint is never guessed from the wording.
	return bytes.Contains(body, []byte("reasoning_content")) ||
		bytes.Contains(body, []byte("content[].thinking")) ||
		bytes.Contains(body, []byte("reasoning_text"))
}
