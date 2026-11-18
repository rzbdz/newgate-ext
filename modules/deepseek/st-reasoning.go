package deepseek

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/gateway/rewrite"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	"github.com/rzbdz/newgate-ext/modules/gateway/thinkcache"
	"github.com/rzbdz/newgate/lib/i18n"
)

var (
	_ special.Plugin           = (*reasoning)(nil)
	_ special.ToolLoopMigrator = (*reasoning)(nil)
	_ special.MetricProvider   = (*reasoning)(nil)
	_ special.ResponseAuditor  = (*reasoning)(nil)
)

func Treatments() []special.Plugin { return []special.Plugin{reasoning{}} }

// deepseek fixes thinking-mode 400s from DeepSeek (and gateways forwarding to DeepSeek).
//
// The field errors, one per dialect:
//
//	API Error: 400 The `reasoning_content` in the thinking mode must be
//	passed back to the API.                          (OpenAI dialect endpoint)
//	API Error: 400 The `content[].thinking` in the thinking mode must be
//	passed back to the API.                          (Anthropic dialect endpoint)
//	API Error: 400 The `reasoning_text` in the thinking mode must be
//	passed back to the API.                          (Responses dialect endpoint)
//
// One cause: DeepSeek's thinking model treats reasoning as **conversation state**.
// Its assistant message last round carried reasoning, and the next round wants it back
// verbatim; once a round has a tool call, every later round is checked. Clients like
// Claude Code **strip thinking blocks** for non-anthropic.com endpoints, so from round
// two they are always missing and every request 400s. The upstream will not relax, and
// the client cannot know, so the middle layer fills in.
//
// Five passes, matching five upstream tastes:
//
//  1. **Claude Code only**, when it wrote no thinking -> add a top-level
//     thinking:{"type":"disabled"} so thinking mode is never entered. A legal field in
//     the Anthropic protocol, disabled is the default, so a no-op for upstreams that
//     do not know it; DeepSeek requires it to be **explicitly** written.
//  2. Add reasoning_content to assistant messages (OpenAI dialect) - **only when the
//     cache/client has the original**, skip otherwise, never invent.
//  3. When thinking mode is on and this is the Anthropic dialect, add a thinking block
//     at the **start** of the assistant message's content[]. Position is part of the
//     protocol - thinking must precede text / tool_use. Also only with the original.
//  4. **Tail shape**: when the last role:user message's content[] is all tool_result
//     blocks with no text, append a minimal instruction (see toolLoopRebasePrompt).
//  5. **Responses dialect tail shape**: when input[]'s **last** entry is a
//     function_call_output and history has a foreign call_id, append a plain user
//     instruction (again toolLoopRebasePrompt). Passes 4 and 5 fix the **root
//     cause**; the first three only fill in fields.
//
// Pass 3 runs only when thinking is on: adding thinking blocks while it is off gets
// rejected upstream ("off yet you send thinking blocks"). Pass 4 **cannot** hang off
// thinkingOn (measured: thinking:disabled with no tools still triggers the check).
//
// Pass 4's criterion carries a **provenance** dimension (see repairTailShape): skip the
// injection only when every tool id in history is aggregator-produced `call_NN_...`
// (NN is a two-digit serial, see isSelfProducedToolCall). Basis: the measured table in
// docs/06-reasoning.md §1.1 - bare tail + all self-produced -> 200, any foreign id
// (even unrecognizable ones) -> 400. Conservative: rather over-add than under-add.
//
// Pass 5 is the same in the Responses dialect (/v1/responses). Measured 2026-09-22
// (direct probe against real smt-deepseek/deepseek-flash, 3/3 per cell): self-produced
// `call_NN_...` call_id + [function_call, function_call_output] ending -> 200; a
// foreign `call_faedac...` call_id with the same ending -> 400, verbatim
// "The `reasoning_text` in the thinking mode must be passed back to the API.";
// appending one plain user message -> 200, and bringing the reasoning item back
// verbatim -> 200. Note upstream validates input[] structurally: a self-produced id
// with only a tool output and no matching function_call gives another error,
// `No tool call found for tool output` - so only the two conditions above trigger.
//
// ---- Pass 4 was once deleted and restored the same day, 2026-09-18 (worth noting) ----
//
// It began with the **wording**: it injected a long English sentence ("Continue from
// the tool results above...") which went upstream and into the user's history, reading
// like "someone speaking for me". The user asked for the fewest characters possible.
//
// But deleting the whole pass that day was wrong, and subtly so: without it there is no
// note and no 400 in the log - because a 400 triggers **silent chain failover** and the
// client gets the next provider's 200. "Looks fine" was really "every request is
// silently downgraded". Measured on restoration day (real upstream):
//
//	bare [tool_result] tail             -> 400
//	bare [tool_result] + trailing system -> 400
//	bare [tool_result] + text" "         -> 200
//
// Repro (as a command, so nobody guesses again): POST any Claude Code client-sent body
// from a dump verbatim to /p/ds/v1/messages, and the log shows
// `-> 400 ... must be passed back` right before `-> next in chain`.
//
// Conclusion: the criterion (shape) was right and the wording was wrong. **Change the
// wording, never the criterion.** See docs/06-reasoning.md §2b.
//
// Pass 1 is Claude Code only (recognized by `/a/claude/`, see claudeCode): it strips
// thinking blocks, so thinking never comes back even when on, wasting time and tokens.
// **Other clients must not be turned off** - 2026-09-15 field report: opencode speaks
// the OpenAI dialect, never writes the Anthropic thinking field, so every request was
// read as "wants no thinking"; the user said "deepseek stopped thinking".
//
// Pass 3 is Anthropic dialect only: in the OpenAI dialect the carrier is
// reasoning_content (pass 2), and a thinking block in content[] would be an unknown type.
//
// What to fill in, two sources, degrading step by step:
//
//  1. the thinking block text the client itself sent back - use it if kept, no cache;
//  2. the **real** reasoning for that round from gateway/thinkcache;
//  3. **neither -> not a single byte** (changed to this on 2026-09-18 from measurement).
//
// Step 3 used to insert a non-empty placeholder ("No thinking in this round"), and
// measurement on 2026-09-18 disproved **that whole thing**:
//
// The user was right, with the simplest logic: the upstream never gave us reasoning, so
// what "must be passed back" asks for **does not exist** - why invent one? Invented
// text goes upstream, into history, burns tokens every round, and carries no information.
//
// Measured (real smt-deepseek/deepseek-flash, 3/3 per cell) - crossing reasoning_content
// shape against tail shape:
//
//	reasoning_content   tail                  result
//	real text           [tool_result]        400 400 400
//	real text           [tool_result, text]  200 200 200
//	field omitted       [tool_result]        400 400 400
//	field omitted       [tool_result, text]  200 200 200
//
// In other words: **whether reasoning_content is there and what it says does not affect
// this 400**; only the tail shape does (docs/06-reasoning.md §2b). A further probe with
// two assistant messages missing reasoning_content and a clean tail was 3/3 200 - so the
// documented rule "every assistant message must carry reasoning" does not hold upstream.
//
// So content-filling in passes 2 and 3 was wasted work: real text should be filled in
// (clients strip blocks, and without it the model cannot read its reasoning next round
// - a quality issue, not a 400), but filling without the original is wrong: skip it.
//
// The range stays **all** assistant messages (fill in those that have text). Messages
// that already carry reasoning are never touched.
//
// Removal: once DeepSeek tolerates these fields being absent, `newgate st off deepseek`
// verifies it; if confirmed unnecessary, the whole file can be deleted.
type reasoning struct{}

func (reasoning) Name() string { return "deepseek" }

// Before/After are **product-side** ordering declarations - the kernel stopped
// knowing product plugin names on 2026-09-20 (that edge was in the kernel's
// st-always.go / st-background.go, reversed, and unknown names are silently ignored
// in the ordering graph, so it idled in pure-kernel builds).
//
//	After(["claude-bg"])      claude-bg acts first (background re-route / thinking)
//	                          and this plugin handles the remaining shapes
//	Before(["always-thinks"]) this plugin acts first, the kernel translator sweeps last
func (reasoning) Before() []string { return []string{"always-thinks"} }
func (reasoning) After() []string  { return []string{"claude-bg"} }

func (reasoning) Why() string {
	return i18n.T("DeepSeek thinking mode wants reasoning passed back verbatim, but the "+
		"client strips it → 400\n"+
		"so we put it back (the original the client carries → thinkcache; **no original "+
		"means skip it, never invent one**); only the Claude Code path turns thinking "+
		"off outright (it strips the blocks, so thinking would be spent for nothing); "+
		"a foreign unclosed tool loop is first lossily rebuilt from the visible tool "+
		"results, then DeepSeek takes over", nil)
}

func (reasoning) Metrics() []special.MetricInfo {
	return []special.MetricInfo{{
		Action: "tool_loop_rebase",
		Hint: i18n.T("a lossy rebuild happened before taking over a foreign unclosed "+
			"tool loop", nil),
	}}
}

// NeedsToolLoopRebase marks that DeepSeek needs a lossy rebuild on unclosed tool loops.
//
// Not a general limitation. A/B on the same real thinking + tool_use, 2026-09-16:
//   - Ark -> Ark:      3/3 200
//   - Ark -> DeepSeek: 3/3 400 reasoning_content must be passed back
//   - DeepSeek -> Ark: 3/3 200
//   - DeepSeek -> DS:  3/3 200
//
// The API is stateless; the encoding in the request is what is incompatible. Block
// only moves into DeepSeek - do not take every provider's fallback away over it.
func (d reasoning) NeedsToolLoopRebase(originProvider, originModel string,
	candidate *special.Request) (bool, string) {
	if !d.Match(candidate) ||
		(candidate.Provider == originProvider && candidate.Model == originModel) {
		return false, ""
	}
	return true, i18n.T("taking over another upstream's unclosed reasoning/tool state "+
		"needs a lossy rebuild first", nil)
}

// toolLoopRebasePrompt is the sentence added when "the tail has no user instruction".
//
// **Shared by three call sites**:
//   - repairTailShape (Apply step 4, messages dialect tail): the tail shape itself is
//     the problem, and the client does not realize it;
//   - repairResponsesTailShape (Apply step 5, Responses dialect input[]): the same
//     thing in another dialect;
//   - RebaseToolLoop: foreign hidden reasoning cannot be taken over as-is.
//
// 2026-09-18 it went from a long English sentence to one word, and on 2026-09-22 that
// word was fixed as "continue", from the user's words: "the prompt injection should
// also be as simple as possible, like ('continue')". The injection goes upstream and
// **into history** too, where a long sentence is text the user never wrote.
//
// One honest caveat: the first A/B (Ark -> DeepSeek, a stable 400 before appending,
// 5/5 200 after) used the long sentence. What mattered was the **fact** of an extra
// plain user instruction, not its content - upstream wants "a new instruction", not
// an explanation. Re-tested with "continue" on a real Claude Code body: bare
// [tool_result] tail 400, plus one " " (space) in the same body 200 (2026-09-18).
//
// And it is **not** anything-goes: an empty string fails. Measured: [tool_result,
// text""] is rejected upstream with `missing field text` (empty string = absent field).
//
// **This does not go through i18n, and never should**: json.Marshal writes it into the
// **request body bytes**, so it is protocol data sent upstream, not copy for anyone.
// Translating it changes what is sent (upstream has no semantic requirement on it, but
// the text lives on in the user's history). It is a package const evaluated early.
const toolLoopRebasePrompt = "continue"

// RebaseToolLoop turns a tool_result into a new turn that also carries a plain user
// instruction. Old reasoning, tool_use and tool_result all stay as visible context;
// only this segment is appended. Measured: Ark -> DeepSeek 400 before, 5/5 200 after.
//
// **One branch per dialect** (the Responses branch added 2026-09-23). On the
// cross-upstream migration path, "an unclosed tool loop" looks different per dialect:
//
//	messages (Anthropic / OpenAI): the last user message's content[] holds only
//	    tool_result -> append a text block **into that content[]**;
//	Responses (Codex): the top-level input[] ends with function_call_output -> append
//	    a plain user message item **at the end of input[]**.
//
// Writing only the messages branch does not mean "one case unfixed" but **none fixed**:
// a Codex request has no top-level messages, AppendLastArrayItemArray returns
// changed=false, and the cross-upstream migration is a silent no-op - which is exactly
// RebaseToolLoop's only reason to exist (DeepSeek taking over another's unclosed
// reasoning/tool state: 3/3 400, see above). ContinuationOrigin got the same Responses
// branch, and both are needed: without that side recognizing it, this is never called.
func (reasoning) RebaseToolLoop(body []byte, _ *special.Request) ([]byte, string, error) {
	q, _ := json.Marshal(toolLoopRebasePrompt)

	// Responses dialect: this branch runs only when there are no messages. The test is
	// "does the top level have input", not r.Path - AppendLastArrayItemArray already
	// no-ops when messages are missing; this just says so explicitly.
	if _, hasMessages := rewrite.TopLevelRaw(body, "messages"); !hasMessages {
		if _, hasInput := rewrite.TopLevelRaw(body, "input"); hasInput {
			block := []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":` +
				string(q) + `}]}`)
			out, changed, err := rewrite.AppendTopLevelArrayItem(body, "input", block)
			if err != nil {
				return nil, "", err
			}
			if !changed {
				return body, "", nil
			}
			return out, i18n.T("lossily rebuilt a foreign tool loop: kept the tool results and "+
				"appended a plain user instruction to continue", nil), nil
		}
		return body, "", nil
	}

	block := []byte(`{"type":"text","text":` + string(q) + `}`)
	out, changed, err := rewrite.AppendLastArrayItemArray(body, "messages", "content",
		block, func(item []byte) bool {
			role, _ := rewrite.TopLevelString(item, "role")
			return role == "user"
		})
	if err != nil {
		return nil, "", err
	}
	if !changed {
		return body, "", nil
	}
	return out, i18n.T("lossily rebuilt a foreign tool loop: kept the tool results and "+
		"appended a plain user instruction to continue", nil), nil
}

// Match recognizes DeepSeek only: the model name, provider name or base URL says deepseek.
//
// Why all three: the model name is most reliable (deepseek-chat / deepseek-reasoner),
// but an aggregating gateway may rename it and the provider name or endpoint keeps a trace.
//
// Reverse guard: Anthropic's official endpoint is never touched. It is strict about
// unknown fields and never reports this error anyway - even if someone did hang a
// provider called deepseek off it, this patch should not add fields for it.
func (reasoning) Match(r *special.Request) bool {
	return r != nil && MatchTarget(r.Model, r.Provider, r.BaseURL)
}

func (reasoning) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	var notes []string
	out := body

	// Whether thinking is on comes only from explicit request fields. The Claude Code x
	// DeepSeek default-off case belongs to claudecode_deepseek; this module ignores callers.
	thinkingOn := true
	if raw, has := rewrite.TopLevelRaw(out, "thinking"); has {
		if t, _ := rewrite.TopLevelString(raw, "type"); t == "disabled" {
			thinkingOn = false
		}
	}
	// An unrecognized type (adaptive, or an unknown shape) is treated as "on": one extra
	// attempt is only redundant, while a missed one leaves a block unfilled.

	// No messages (say /v1/models) means we stop here, and that is not an error.
	//
	// Exception: the Responses dialect (/v1/responses) keeps the conversation in a
	// top-level input[] and never has messages - pass 5 fixes exactly that. So we stop
	// only when both messages and input are absent; passes that do not match no-op.
	_, hasMessages := rewrite.TopLevelRaw(out, "messages")
	_, hasInput := rewrite.TopLevelRaw(out, "input")
	if !hasMessages && !hasInput {
		return out, notes, nil
	}

	// 2) Add reasoning_content to assistant messages (the OpenAI dialect 400).
	//    Fill only with real text; otherwise skip the message and record **why**.
	//
	//    The note rule (set 2026-09-18): **report only if we acted**. No action means
	//    no notes - "never silent" governs rewrites, not pass-throughs. Skip causes are
	//    gathered in pending and reported, with the reasons, only if this request changed.
	var sk1 skipTally
	var pending []string
	restored := 0
	valReasoning := func(item []byte) []byte {
		if q, ok := pickReasoning(item); ok {
			restored++
			return q
		}
		sk1.add(skipCauseOf(item), toolIDsAny(item))
		return nil // no source text -> not one byte backfilled for this message
	}
	//    The pass turned off by a switch point **does not** emit a per-request note:
	//    that is the user's own setting, and repeating it is the noise the rule
	//    above removes. Visibility: `newgate plugin` and `newgate status`, not the log.
	if !hasMessages {
		// Responses dialect: there are no messages to fill, so pass 2 is skipped. Do not
		// let it hit EnsureArrayItemFieldFunc's "no messages": that would emit a bogus
		// "messages left untouched" note for nothing.
	} else if !enabled(r, SwitchBackfillReasoning) {
		// Pass 2 is switched off; skip.
	} else if nb, n, err := rewrite.EnsureArrayItemFieldFunc(out, "messages",
		"reasoning_content", valReasoning, isAssistant); err != nil {
		// Unrecognized messages shape: the earlier passes still hold; give up on this step.
		notes = append(notes, i18n.T("messages left untouched ({err})",
			i18n.A{"err": err.Error()}))
	} else if n > 0 {
		out = nb
		notes = append(notes, reasoningNote("reasoning_content", n, restored, sk1))
	} else if sk1.total > 0 {
		// Not a single one filled: **hold it back** and see whether this request changed.
		pending = append(pending, reasoningNote("reasoning_content", sk1.total, 0, sk1))
	}

	// 3) Thinking mode on -> assistant's content[] must start with a thinking block
	//    (the Anthropic dialect 400). Same rule: fill only with real text.
	//    Anthropic dialect only: in the OpenAI dialect reasoning rides reasoning_content.
	if hasMessages && thinkingOn && anthropicDialect(r) &&
		enabled(r, SwitchBackfillThinkingBlock) {
		restored := 0
		var sk2 skipTally
		valBlock := func(item []byte) []byte {
			// Same source as step 2 (pickReasoning): the client's thinking block text ->
			// thinkcache real reasoning. **Never** read step 2's freshly written
			// reasoning_content as a source - that is circular (step 2's value came from
			// here), and it would inflate the "used real text" count in the log.
			if q, ok := pickReasoning(item); ok {
				restored++
				return []byte(`{"type":"thinking","thinking":` + string(q) + `}`)
			}
			sk2.add(skipCauseOf(item), toolIDsAny(item))
			return nil // no source text -> no block inserted
		}
		if nb, n, err := rewrite.EnsureArrayItemArrayHeadFunc(out, "messages", "content",
			valBlock, isAssistant, lacksThinking); err != nil {
			notes = append(notes, i18n.T("content left untouched ({err})",
				i18n.A{"err": err.Error()}))
		} else if n > 0 {
			out = nb
			notes = append(notes, reasoningNote(i18n.T("thinking block", nil),
				n, restored, sk2))
		} else if sk2.total > 0 {
			pending = append(pending, reasoningNote(i18n.T("thinking block", nil),
				sk2.total, 0, sk2))
		}
	}

	// 4) Tail shape: the last user message's content[] holds only tool_result blocks
	//    with no text, so append a minimal user instruction (see repairTailShape).
	//
	//    **Must not** hang off thinkingOn: measured twice (2026-09-17/18), with the top
	//    level thinking:{"type":"disabled"} and no tools, the same check still fires.
	if !enabled(r, SwitchTailShape) {
		// Pass 4 is switched off; skip. It is one of the two root-cause passes, and
		// without it a bare tool_result tail hits the upstream's false 400 directly.
	} else if nb, changed, err := repairTailShape(out); err != nil {
		notes = append(notes, i18n.T("tail shape left untouched ({err})",
			i18n.A{"err": err.Error()}))
	} else if changed {
		out = nb
		notes = append(notes, i18n.T("the trailing user turn holds only tool_result and no "+
			"user instruction — appended a continue instruction (the upstream misreports "+
			"an instruction-less tail as a missing reasoning_content)", nil))
	}

	// 5) Responses dialect (/v1/responses) tail shape: when input[]'s last entry is
	//    a function_call_output and history holds a foreign call_id, append a plain
	//    user instruction at the end of input[]. See repairResponsesTailShape.
	//
	//    Same root as pass 4 (both are the "no new instruction at the tail" false
	//    report), just another dialect: one fills messages[i].content[], the other adds.
	if !enabled(r, SwitchTailShapeResponses) {
		// Pass 5 is switched off; skip.
	} else if nb, changed, err := repairResponsesTailShape(out); err != nil {
		notes = append(notes, i18n.T("Responses input left untouched ({err})",
			i18n.A{"err": err.Error()}))
	} else if changed {
		out = nb
		notes = append(notes, i18n.T("the Responses input[] ends on a function_call_output "+
			"that is not ours and holds no user instruction — appended a continue "+
			"instruction (the upstream misreports an instruction-less tail as a missing "+
			"reasoning_text)", nil))
	}

	// The "skipped" reports held back earlier are emitted only if this request changed.
	//
	// Why: "never silent" governs **rewrites** - users have a right to know what was
	// touched. "No text to fill" is not a rewrite when we did nothing (the field was
	// absent and we touched no byte), and thousands of such lines a day are this
	// session's biggest noise, drowning the useful ones when something goes wrong.
	// Conversely, when this request did change, the skips must be reported too - they
	// and "used real text" are two halves of one account, and half is dishonest.
	if len(pending) > 0 && len(notes) > 0 {
		notes = append(notes, pending...)
	}

	return out, notes, nil
}

// isSelfProducedToolCall reports whether a tool call id has the shape the aggregator
// (the one newgate goes through) produces: `call_` + **two-digit serial** + `_`.
//
// Why two digits rather than only 00: **the aggregator emits one id per parallel tool
// call in a turn** - first 00, second 01, and so on. Counted on 2026-09-22 across real
// captures and local logs: call_00_ 2917 times, call_01_ 705, call_02_ 16, call_03_ 8,
// call_04_ 2. Matching only `call_00_` would call "two tools in one turn" - the most
// common normal turn - foreign, and keep injecting into the normal path: the very bug.
//
// Basis: the measured table in docs/06-reasoning.md §1.1 (2026-09-18, real
// smt-deepseek/deepseek-flash, 3/3 per cell):
//
//	call_NN_XXXXXXXX...   produced by this aggregator - it remembers the ids it issued
//	                      and fills reasoning_content back in for the continuation turn
//	call_<hex> (say call_faedac561213487ebeea7731)  from an OpenAI-family upstream (that
//	                      fallback turn left it in history)
//	toolu_01XXXXXXXX...   from an Anthropic-family upstream (Claude via smt-claude)
//
// On 2026-09-22 "memory" itself was measured directly: replay a `call_00_605TMR...`
// the aggregator issued a minute earlier verbatim -> 200; change only the serial to
// `call_01_605TMR...` (same suffix, not a character off) -> 400 (`reasoning_text ...
// must be passed back`). What was seen is not the prefix but the memory of the id.
//
// So the direction stays conservative: **anything unrecognized counts as "not
// self-produced"** (anything after `call_` that is not two digits plus `_`, including
// `call_<hex>`, `toolu_...`, and other spellings). One extra "foreign" verdict adds
// one continue; one missed is a 400 plus silent failover (docs §1.2).
//
// **One dimension this criterion cannot see** (measured 2026-09-22, docs §1.1): memory
// is not enough - the **whole set** of that turn must come back; return only one of two
// ids from the same turn and it still cannot fill in (3/3 400). Real clients send whole
// history, so a turn's ids are never split, and the approximation held in measurement -
// but it is no law of physics: a counterexample means **one less fill**, plus failover.
func isSelfProducedToolCall(id string) bool {
	const head = "call_"
	if !strings.HasPrefix(id, head) {
		return false
	}
	rest := id[len(head):]
	return len(rest) >= 3 && rest[2] == '_' &&
		rest[0] >= '0' && rest[0] <= '9' && rest[1] >= '0' && rest[1] <= '9'
}

// repairTailShape fixes "the last user message holds only tool_result".
//
// From docs/06-reasoning.md §1: that 400 has three conditions (all required) - anchored
// on the last role:user, content[] all tool_result, and at least one referenced tool
// call **not** from this aggregator. Two are **shape**, the third is **provenance**.
// Here: when the shape hits, provenance decides whether to act.
//
//  1. **Anchor on the last role:user message, not the array's last item**. Claude Code
//     appends a role:"system" remark after a tool_result ("The user sent a new message
//     while you were working: ..."), so the array's last item is that system one while
//     what upstream rejected is the tool_result-only user turn before it. Upstream
//     ignores instructions in system. Field: dump/err-400-req000464, req000412.
//  2. **That content[] must be entirely tool_result blocks**, not "has no text block":
//     too wide, it would alter tails that already pass - [image] and [tool_result,
//     image] measured 200. The line is "any non-tool_result block counts as text".
//  3. **Provenance**: take every tool_use block id in history plus the tool_use_id the
//     tail tool_result references, and **skip the injection only when all are
//     self-produced**; any other shape (including unrecognizable) still injects.
//     Over-fixing costs one "continue"; under-fixing is a 400 plus silent failover.
//
// The third condition differs from §1's **diagnosis** rule: that needs "at least one
// foreign id" to hold, while repair acts only when "all self-produced" - conservative
// complements. This pass is **not** behind the thinkingOn gate (Apply step 4 owns it),
// and the append helper choice below is explained at the end of this comment.
//
// Tails that already carry text or images are never touched - they pass as-is, and an
// extra sentence would only be noise in the user's conversation.
//
// AppendArrayItemArrayAt rather than AppendLastArrayItemAt: pick by index, not "find
// the last matching tail", because mid-history tool_result-only turns abound and that
// would alter an old message that **must not** be touched.
func repairTailShape(body []byte) ([]byte, bool, error) {
	raw, ok := rewrite.TopLevelRaw(body, "messages")
	if !ok {
		return body, false, nil
	}
	items, ok := rewrite.ArrayItems(raw)
	if !ok || len(items) == 0 {
		return body, false, nil
	}

	// 1) The index of the last role:user message.
	idx := -1
	for i, it := range items {
		if role, _ := rewrite.TopLevelString(it, "role"); role == "user" {
			idx = i
		}
	}
	if idx < 0 {
		return body, false, nil
	}

	// **No assistant message may follow it**. That tail (array ends with assistant) is
	// another rule's business, and appending proves **useless**: measured, appending a
	// continue to that user turn still gave 3/3 400. If it cannot be fixed, do not fix
	// it: noise the model cannot see is worse than the 400.
	//
	// That family never arrives from real clients: Claude Code requests end with a user
	// tool_result, with user text, or with a role:"system" remark appended after those.
	// Every err-400 in the dump was checked; none ends with assistant.
	for _, it := range items[idx+1:] {
		if role, _ := rewrite.TopLevelString(it, "role"); role == "assistant" {
			return body, false, nil
		}
	}

	// 2) Its content[] is non-empty and **all** tool_result blocks.
	content, ok := rewrite.TopLevelRaw(items[idx], "content")
	if !ok {
		return body, false, nil
	}
	blocks, ok := rewrite.ArrayItems(content)
	if !ok || len(blocks) == 0 {
		// content is a plain string (it already has text) or an unknown shape: leave it.
		return body, false, nil
	}
	for _, b := range blocks {
		if t, _ := rewrite.TopLevelString(b, "type"); t != "tool_result" {
			return body, false, nil
		}
	}

	// 3) Provenance: skip only when every tool id is self-produced. tool_use blocks
	//    are scanned across all history (position-independent, docs §1.1), tail incl.
	ids := messageToolUseIDs(items)
	for _, b := range blocks {
		if id, _ := rewrite.TopLevelString(b, "tool_use_id"); id != "" {
			ids = append(ids, id)
		}
	}
	if allToolIDsSelfProduced(ids) {
		return body, false, nil
	}

	q, err := json.Marshal(toolLoopRebasePrompt)
	if err != nil {
		return body, false, err
	}
	block := []byte(`{"type":"text","text":` + string(q) + `}`)
	return rewrite.AppendArrayItemArrayAt(body, "messages", "content", idx, block)
}

// allToolIDsSelfProduced says whether these tool ids are **all** aggregator-produced.
//
// Only the isSelfProducedToolCall family (`call_NN_...`) counts; anything unrecognized
// counts as foreign, and an empty list returns false. Conservative on purpose: calling
// one foreign just adds a "continue"; calling one self-produced lets a 400 fail over.
func allToolIDsSelfProduced(ids []string) bool {
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		if !isSelfProducedToolCall(id) {
			return false
		}
	}
	return true
}

// messageToolUseIDs collects every tool_use block id in messages (read-only, pure byte
// scanning, no whole-JSON round trip).
//
// vs toolUseIDs: that one takes ids for **one** assistant message (logs, cache lookup);
// this walks **all history** - provenance means "any foreign id anywhere counts"
// (the measured table in docs/06-reasoning.md §1.1 shows position does not matter).
func messageToolUseIDs(items [][]byte) []string {
	var ids []string
	for _, it := range items {
		content, ok := rewrite.TopLevelRaw(it, "content")
		if !ok {
			continue
		}
		blocks, ok := rewrite.ArrayItems(content)
		if !ok {
			continue
		}
		for _, b := range blocks {
			if t, _ := rewrite.TopLevelString(b, "type"); t != "tool_use" {
				continue
			}
			if id, _ := rewrite.TopLevelString(b, "id"); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// repairResponsesTailShape fixes the Responses dialect (/v1/responses) tail shape.
//
// Two criteria, measured 2026-09-22 (a probe against real smt-deepseek/deepseek-flash,
// 3/3 per cell, full run in pass 5 of the file header):
//
//  1. **Tail shape**: the top-level input[]'s **last entry** is a function_call_output.
//     Unlike the messages dialect the anchor is the last array item itself (Responses
//     items are array elements; no Claude Code system remark after them).
//  2. **Provenance**: history holds at least one call_id that is **not** from this
//     aggregator (scan call_id on function_call and function_call_output items).
//
// Both required: append a plain user message item at the **end** of input[], with
// toolLoopRebasePrompt as its text. Measured: foreign call_id + trailing
// function_call_output -> 400 `The reasoning_text ... must be passed back`; the same
// body with one plain user message appended -> 200.
//
// As conservative as repairTailShape: an undecidable call_id (missing) counts as
// foreign. Conversely, when every call_id is `call_NN_...`, not a byte is touched -
// that shape already gives 200 (upstream wants a new instruction, not a particular one).
func repairResponsesTailShape(body []byte) ([]byte, bool, error) {
	raw, ok := rewrite.TopLevelRaw(body, "input")
	if !ok {
		return body, false, nil
	}
	items, ok := rewrite.ArrayItems(raw)
	if !ok || len(items) == 0 {
		return body, false, nil
	}
	last := items[len(items)-1]
	if t, _ := rewrite.TopLevelString(last, "type"); t != "function_call_output" {
		return body, false, nil
	}
	if !hasForeignResponsesCallID(items) {
		return body, false, nil
	}
	q, err := json.Marshal(toolLoopRebasePrompt)
	if err != nil {
		return body, false, err
	}
	block := []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":` +
		string(q) + `}]}`)
	// Uses the kernel's AppendTopLevelArrayItem (added 2026-09-22): appends to a
	// top-level array without a comma for an empty one, keeping every other byte as-is.
	return rewrite.AppendTopLevelArrayItem(body, "input", block)
}

// hasForeignResponsesCallID says whether history holds a call_id not from this aggregator.
//
// Only call_id on function_call and function_call_output items is scanned (those carry
// tool calls in the Responses dialect). A missing call_id counts as "not self-produced"
// - when provenance is unknown, one more continue beats a silent failover.
func hasForeignResponsesCallID(items [][]byte) bool {
	for _, it := range items {
		switch t, _ := rewrite.TopLevelString(it, "type"); t {
		case "function_call", "function_call_output":
		default:
			continue
		}
		id, _ := rewrite.TopLevelString(it, "call_id")
		if !isSelfProducedToolCall(id) {
			return true
		}
	}
	return false
}

// pickReasoning chooses the reasoning text to return for an assistant message (JSON string).
//
// Two sources, strictly matching what clients send back:
//  1. the thinking block in the message's own content[] - if the client kept the text,
//     use it directly, no dependence on cache survival;
//  2. the reasoning that really happened that round in thinkcache (tool id or body hash).
//
// Neither -> false, and the caller **skips that message** (fills in not one byte),
// recording why there was no text (see skipCause). Inventing a placeholder is wrong.
func pickReasoning(item []byte) ([]byte, bool) {
	if t := clientThinkingText(item); t != "" {
		if q, err := json.Marshal(t); err == nil {
			return q, true
		}
	}
	if blob, ok := thinkcache.Default.Lookup(item); ok {
		if q, err := json.Marshal(string(blob)); err == nil {
			return q, true
		}
	}
	return nil, false
}

// skipCause classifies "why this one has no reasoning text to fill in".
//
// Why it exists: this classification was the evidence chain for the 2026-09-18
// conclusion. The original question was "why is there no thinking - is the cache
// broken, or did the request never have it?" - and those need opposite handling:
//
//   - the upstream never gave it that round => skip (anything added is invention);
//   - it gave it and we failed to keep it => fix thinkcache's hit rate.
//
// The classification uses only facts readable from the request itself (not runtime cache
// state: logs are read later, when the cache is long evicted):
//
//	notool   this assistant message has no tool call - a text-only turn, recoverable
//	         only by body hash, missed if the client rewrote the text (TextKey's weakness)
//	nocache  it has a tool call, but its id is not found in thinkcache
//	nokey    neither tool_use nor a text field - an unrecognized shape
//
// "Did the client send a thinking block back" is not classified here: that was
// clientThinkingText's first try, so **reaching this layer means it sent none**,
// and that is guaranteed by the branch structure, not by re-deriving it here.
//
// The 2026-09-18 measurement: 3494 skips across the store, all classified **nocache**;
// looking each tool id up under "recorded this round's reasoning" - **not one hit**.
// So the upstream never gave us this reasoning (adaptive means the model decides whether
// to think): not a cache problem. Six identical requests in a row: 3 thoughts, 3 not.
type skipCause string

const (
	causeNoToolUse  skipCause = "notool"  // text-only turn (recovered by body hash, miss)
	causeToolNotIn  skipCause = "nocache" // has tool_use but the cache has no entry
	causeNoIdentity skipCause = "nokey"   // neither tool_use nor text: unidentifiable
)

// skipCauseOf decides why an assistant message has no text to fill in (classify above).
func skipCauseOf(item []byte) skipCause {
	if hasToolUse(item) {
		return causeToolNotIn
	}
	if text, ok := rewrite.TopLevelString(item, "content"); ok && strings.TrimSpace(text) != "" {
		return causeNoToolUse
	}
	return causeNoIdentity
}

// hasToolUse says whether this message carries a tool call (both dialects).
func hasToolUse(item []byte) bool {
	return len(toolIDsAny(item)) > 0
}

// toolIDsAny extracts this assistant message's tool call ids, both dialects.
//
//	Anthropic  content[].{type:tool_use, id}
//	OpenAI     tool_calls[].id
//
// Why both: classification logs and cache lookup must **agree on the same ids**.
// thinkcache's KeysForAssistantMessage accepts both dialects (it parses JSON); if this
// side knew only Anthropic, OpenAI-dialect turns would be misreported as notool.
func toolIDsAny(item []byte) []string {
	ids := toolUseIDs(item) // Anthropic dialect
	if len(ids) > 0 {
		return ids
	}
	raw, ok := rewrite.TopLevelRaw(item, "tool_calls") // OpenAI dialect
	if !ok {
		return nil
	}
	items, ok := rewrite.ArrayItems(raw)
	if !ok {
		return nil
	}
	for _, tc := range items {
		if id, _ := rewrite.TopLevelString(tc, "id"); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// skipTally is the classification counts plus id samples of skipped messages.
//
// Why ids (added 2026-09-18): the user asked why there is no thinking - the cache, or
// the request never having it - and demanded it be traced. Counts alone never separate
// the two: a skip is a **result**, and without an **identity** there is no lookup.
//
// With ids you can match the other two log lines:
//
//	[proxy] #N recorded … bytes of reasoning and N keys … this round
//	            ^ this round's reasoning went into the cache (kernel forward.go)
//	[proxy] #N no reasoning content from upstream this round (N tool calls)
//	            ^ the upstream never gave it
//
// Both lines' **text became English with i18n** (updated in this module's migration),
// so grep those two exact English strings, not this paraphrase.
//
// Grep those two with a skipped message's id:
//   - hits "no reasoning" => the request never had it (upstream did not think);
//   - hits "recorded" => the cache **had** it and it was lost (TTL/eviction/restart);
//   - neither => this round did not go through us (history from another provider).
//
// Memory cost: one id string per skipped message. A request has at most a few hundred,
// and they live only until the log is written - not cached, not on disk.
type skipTally struct {
	total  int
	byKind map[skipCause]int
	ids    []string // tool call ids of skipped messages (text-only turns have none)
}

func (t *skipTally) add(c skipCause, ids []string) {
	t.total++
	if t.byKind == nil {
		t.byKind = map[skipCause]int{}
	}
	t.byKind[c]++
	t.ids = append(t.ids, ids...)
}

// detail renders into a log fragment; an empty string when nothing was skipped.
//
// Order is fixed (notool -> nocache -> nokey) because logs are **compared across
// requests**: nocache dominating today and notool tomorrow mean different things, and
// map's random iteration would make every line look different. Only non-zero counts.
func (t *skipTally) detail() string {
	if t == nil || t.total == 0 {
		return ""
	}
	var parts []string
	for _, k := range []skipCause{causeNoToolUse, causeToolNotIn, causeNoIdentity} {
		if n := t.byKind[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, n))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	// The separator between counts is **language data** too (a full-width comma in
	// Chinese), so it is a message of its own: an ASCII comma would mix scripts.
	items := strings.Join(parts, i18n.T(", ", nil))
	if len(t.ids) == 0 {
		return i18n.T("({items}; skipped messages have no tool id (plain text turns))",
			i18n.A{"items": items})
	}
	// At most 8: enough to see whether "the same old batch shows up every time", without
	// stretching one log line to several KB. The rest are reported as a count only.
	shown := t.ids
	more := ""
	if len(shown) > 8 {
		more = i18n.T(" and {n} in all", i18n.A{"n": len(shown)})
		shown = shown[:8]
	}
	return i18n.T("({items}; tool ids of skipped messages: {ids}{more})",
		i18n.A{"items": items, "ids": strings.Join(shown, " "), "more": more})
}

// clientThinkingText extracts the text of thinking blocks in this message's content[].
//
// The client (Claude Code with interleaved thinking on) sometimes sends its own
// thinking blocks back - that is the reasoning text, do not waste it. Multiple blocks
// are joined. redacted_thinking has no thinking field, so no text can be taken.
func clientThinkingText(item []byte) string {
	content, ok := rewrite.TopLevelRaw(item, "content")
	if !ok || len(content) == 0 || content[0] != '[' {
		return ""
	}
	blocks, ok := rewrite.ArrayItems(content)
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		if len(b) == 0 || b[0] != '{' {
			continue
		}
		if t, _ := rewrite.TopLevelString(b, "type"); t == "thinking" {
			if s, ok := rewrite.TopLevelString(b, "thinking"); ok {
				sb.WriteString(s)
			}
		}
	}
	return sb.String()
}

// reasoningNote reports "filled with real text" and "skipped" separately; the skipped
// part **must carry its cause classification**.
//
// They must be separate: a skip means the model cannot read its own previous reasoning,
// so thinking quality drops, and that is not success.
//
// Users must see in the log how often it happened (docs/01-product.md, "never silent");
// a count without a cause cannot tell the two opposite cases apart, see skipCause.
func reasoningNote(what string, total, restored int, sk skipTally) string {
	// `assistant` is the JSON role value, kept outside the message; `{n}` is injected by N().
	s := i18n.N("backfilled {what} on {n} assistant message: {restored} used the real "+
		"reasoning text",
		"backfilled {what} on {n} assistant messages: {restored} used the real "+
			"reasoning text",
		total, i18n.A{"what": what, "restored": restored})
	if sk.total > 0 {
		s += i18n.T(", {n} had no original text to backfill and were **left untouched**"+
			"{detail} — those rounds the model cannot see its own reasoning",
			i18n.A{"n": sk.total, "detail": sk.detail()})
	}
	return s
}

func isAssistant(item []byte) bool {
	role, _ := rewrite.TopLevelString(item, "role")
	return role == "assistant"
}

// anthropicDialect says whether this request is in the Anthropic dialect (/v1/messages).
//
// The test is the path, not r.Protocol: protocol says "how it goes upstream" (measured:
// an aggregating gateway's providers all say "openai" yet receive /v1/messages Anthropic
// requests), while the path the client sent is this request's own dialect.
func anthropicDialect(r *special.Request) bool {
	return r != nil && strings.HasPrefix(r.Path, "/messages")
}

// lacksThinking says whether this content[] has any reasoning content.
// redacted_thinking counts too - it is an upstream-encrypted thinking block, and
// inserting an empty one before it would just add a block.
func lacksThinking(content []byte) bool {
	items, ok := rewrite.ArrayItems(content)
	if !ok {
		return false // unrecognized shape: leave it alone
	}
	for _, it := range items {
		if len(it) == 0 || it[0] != '{' {
			continue
		}
		switch t, _ := rewrite.TopLevelString(it, "type"); t {
		case "thinking", "redacted_thinking":
			return false
		}
	}
	return true
}

// AuditReasoning produces a per-message audit of "reasoning sent back this round".
//
// It parses the we-sent messages directly and marks each assistant message for
// reasoning_content. **The missing ones are not our fault**: the upstream never gave it
// that round (see the measurement in the file header). The report prints tool_use ids
// too; grep the log's `recorded ... bytes of reasoning` line (kernel forward.go) with
// them - a hit means we lost it, a miss means it never existed.
//
// Pure read-only analysis, independent of runtime cache state: the cache may be
// evicted later, but this report is the snapshot of what was true then.
func AuditReasoning(out []byte) string {
	msgs, ok := rewrite.TopLevelRaw(out, "messages")
	if !ok {
		return i18n.T("(no messages field, cannot audit)\n", nil)
	}
	items, ok := rewrite.ArrayItems(msgs)
	if !ok {
		return i18n.T("(messages is not an array, cannot audit)\n", nil)
	}
	var b strings.Builder
	assistant, real, missing := 0, 0, 0
	for i, it := range items {
		if !isAssistant(it) {
			continue
		}
		assistant++
		if _, has := rewrite.TopLevelString(it, "reasoning_content"); has {
			real++
			continue
		}
		missing++
		// `msg[0]` / `reasoning_content` are machine markers (field name, index): keep outside.
		b.WriteString(i18n.T("msg[{i}] has no reasoning_content  {keys}\n",
			i18n.A{"i": i, "keys": msgKeys(it)}))
	}
	// The "bytes of reasoning" line is the kernel thinkcache side's actual log text
	// (forward.go's recorded ... bytes of reasoning); grepping needs the verbatim string.
	return i18n.T("assistant messages: {total} in all, {real} with reasoning text, "+
		"{missing} without\n"+
		"(the ones without are **what the upstream never gave us that round**, not "+
		"something we lost — see the tool id below,\n"+
		"  grep the log for \"bytes of reasoning\" and you will see) one by one:\n{listing}",
		i18n.A{"total": assistant, "real": real, "missing": missing, "listing": b.String()})
}

func (reasoning) AuditResponse(body []byte) string { return AuditReasoning(body) }

// msgKeys extracts this assistant message's tool_use ids (the cache lookup keys), and
// text-only turns fall back to the body hash. Evidence uses ids to check thinkcache.
func msgKeys(item []byte) string {
	toolIDs := toolUseIDs(item)
	if len(toolIDs) == 0 {
		return i18n.T("(plain text turn, no tool_use)", nil)
	}
	return "tool_use ids: " + strings.Join(toolIDs, " ")
}

// toolUseIDs extracts tool_use block ids from an Anthropic dialect content[].
//
// The same ids thinkcache.KeysForAssistantMessage knows (JSON there, byte surgery
// here) - **they must be the same**, or the "why no text" classification would not
// match the cache's real keys and the log's nocache would point at the wrong conclusion.
func toolUseIDs(item []byte) []string {
	c, ok := rewrite.TopLevelRaw(item, "content")
	if !ok {
		return nil
	}
	bs, ok := rewrite.ArrayItems(c)
	if !ok {
		return nil
	}
	var ids []string
	for _, blk := range bs {
		if t, _ := rewrite.TopLevelString(blk, "type"); t != "tool_use" {
			continue
		}
		if id, _ := rewrite.TopLevelString(blk, "id"); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}
