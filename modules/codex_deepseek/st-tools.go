package codex_deepseek

import (
	"encoding/json"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/gateway/rewrite"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	"github.com/rzbdz/newgate-ext/modules/pluginmanager"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	codexapi "github.com/rzbdz/newgate-ext/modules/codex"
	deepseekapi "github.com/rzbdz/newgate-ext/modules/deepseek"
)

func Treatments(client codexapi.Client, model deepseekapi.Model) []special.Plugin {
	return []special.Plugin{tools{client: client, model: model}}
}

// tools is this move's patch: lift Codex's tool dialect into the shape DeepSeek recognizes
// (request side), and change the lifted ones back in the response (response side).
//
// Both directions belong to **one plugin**, not split into two. The reason
// is not convenience: which entries the response side changes is judged by "which tools the
// request side downgraded", and splitting would mean passing that conclusion across plugins
// -- and plugins have only the one Request channel, so stuffing "who was downgraded"
// into it means adding a field to the public contract that only two parties use.
type tools struct {
	client codexapi.Client
	model  deepseekapi.Model
}

func (tools) Name() string { return "codex-deepseek" }

// Before gets ahead of other plugins: it reads **the source the client sent** (input[0]),
// and if someone else changed input first, what it reads is no longer the source.
func (tools) Before() []string { return []string{"deepseek"} }
func (tools) After() []string  { return nil }

func (tools) Why() string {
	return i18n.T("Codex puts its tool definitions in input[0] (additional_tools) "+
		"while DeepSeek reads tools from the top level; without the lift the model "+
		"sees no tools at all and answers in its own text format, so the client "+
		"never gets a tool_result", nil)
}

func (t tools) Match(r *special.Request) bool {
	return r != nil && r.Agent == t.client.AgentID &&
		t.model.MatchTarget != nil &&
		t.model.MatchTarget(r.Model, r.Provider, r.BaseURL)
}

// Apply rewrites the request: lift the tools up, or only downgrade the top level.
//
// Two routes (mutually exclusive):
//
//  1. Read input[0].additional_tools (old format): flatten this tree, downgrade custom
//     to function, write it into the top-level tools (overwriting the existing one), and
//     keep the original input[0] as is. DeepSeek reads tools from this newly built tree.
//  2. No additional_tools, but a top-level tools exists (new format):
//     it may mix namespace/function/web_search/custom. DeepSeek digests
//     the first three (namespaces are echoed back as is, measured 2026-09-21),
//     but reports 400 on an unknown custom. So scan through and downgrade only
//     custom (other than apply_patch) to function, touching not one other byte.
//
// **Route 1's half moved to `modules/codex` on 2026-09-23** (`codex.lift-tools`):
// lifting tools from input[0] to the top level has nothing to do with which upstream
// it is (Codex with Ark / Claude / Kimi needs it just the same), so the judgment
// should not live in a module named codex-deepseek. After the move this module **still
// reads that switch** (see liftEnabled); otherwise "turn off codex.lift-tools" would
// become a no-op on the DeepSeek path -- a switch that reports a name nobody reads is
// a lie worse than having no switch (measured live on 2026-09-21 in the same family).
//
// Route 1 is kept because **it now runs only in the right place**: the criterion is "the top
// level has no usable tools yet" (`HasUsableTopLevelTools`), and codex-tools sorts ahead of
// this plugin -- so a normal assembly always takes route 2 (the lift is already done up front,
// only the downgrade happens here). What is left for it is when the client move is not mounted
// (this module's unit tests, and any assembly without the codex client hook); then this branch
// can still fix the request, rather than sending custom straight to an upstream that will 400.
//
// This move can be turned off on its own (`newgate plugin codex-deepseek.degrade-tools
// off`) -- **the switch point must really be read**, otherwise `newgate plugin` reports "off" while
// requests are rewritten anyway (measured 2026-09-21: after turning it off the top-level tools were
// still lifted on live). Its default is on, so turning it off is a debugging action, not the norm.
func (tools) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	if pluginmanager.Off(stateOf(r), SwitchDegradeTools) {
		return body, nil, nil
	}
	if liftEnabled(r) && !codexapi.HasUsableTopLevelTools(body) {
		if arr, ok := codexTools(body); ok {
			lifted, degraded, n := liftTools(arr)
			if n == 0 {
				return body, nil, nil
			}
			out, err := setTopLevelTools(body, lifted)
			if err != nil {
				return body, nil, err
			}
			// note is reported as two lines, because they are **two things**: how many were lifted
			// (the step that fixes the root cause), and how many of those were downgraded. Merging them
			// into one sentence would make it read "lifted N, 0 downgraded" when there is no custom at
			// all -- and 0 downgraded is not noise, it is the information "no tool field was touched
			// this time". See the "the lift itself is what fixes the root cause" passage in lift.go.
			//
			// The lifted sentence says the same thing in the same wording as modules/codex's codex-tools
			// -- reaching here by either route is one thing to the reader (the tools were lifted), and
			// two wordings would only make the person debugging first guess which route acted. Normally
			// this never fires here: codex-tools sorts ahead of this plugin (see ToolsLift.Before).
			notes := []string{i18n.N(
				"lifted {n} tool declaration from input[0] to the top level (DeepSeek reads tools only there)",
				"lifted {n} tool declarations from input[0] to the top level (DeepSeek reads tools only there)",
				n, i18n.A{"n": n})}
			if len(degraded) > 0 {
				notes = append(notes, degradeNote(len(degraded)))
			}
			return out, notes, nil
		}
	}

	if toolsRaw, ok := rewrite.TopLevelRaw(body, "tools"); ok {
		degradedArr, degraded, changed := degradeTopLevel(toolsRaw)
		if !changed {
			return body, nil, nil
		}
		out, err := rewrite.ReplaceTopLevelRaw(body, "tools", degradedArr)
		if err != nil {
			return body, nil, err
		}
		return out, []string{degradeNote(len(degraded))}, nil
	}

	return body, nil, nil
}

// degradeNote is the sentence "downgraded custom to function".
//
// **The two sources share one wording** (lifted from input[0], and already at
// the top level): to the reader it is one thing -- the same upstream rejecting
// the same family of declarations -- and two wordings would give one cause two
// texts in the log, making the person debugging first guess which route acted.
func degradeNote(n int) string {
	return i18n.N(
		"degraded {n} custom tool to a standard function (DeepSeek rejects any custom tool other than apply_patch with 400)",
		"degraded {n} custom tools to standard functions (DeepSeek rejects any custom tool other than apply_patch with 400)",
		n, i18n.A{"n": n})
}

// stateOf takes the config snapshot from the plugin context. nil is allowed (common
// with the old call surface and unit tests) -- pluginmanager.Off always answers
// false for nil, i.e. "not off". The direction must not be reversed: a missing
// snapshot should not turn the patch off. Same as the claudecode_deepseek one.
func stateOf(r *special.Request) *domain.State {
	if r == nil {
		return nil
	}
	return r.State
}

// Egress claims the response rewrite of this exchange.
//
// The criterion is read from **the source the client sent** (`original`), because
// after Apply those customs have become functions, and from that copy there
// is no telling "who was originally custom". See the explanation in lift.go.
//
// The switch point uses **the same ruler** as Apply: once off, the request side is
// unchanged, and the response side has nothing that should change -- if one side reads
// the switch and the other does not, you get "the request was not downgraded, but
// the response was changed to custom_tool_call", an asymmetry that breaks the client.
func (tools) Egress(r *special.Request, original []byte) special.Egress {
	if pluginmanager.Off(stateOf(r), SwitchDegradeTools) {
		return nil
	}
	degraded := degradedNames(r, original)
	if len(degraded) == 0 {
		return nil
	}
	return &egressTools{degraded: degraded}
}

// liftEnabled reads **the client move's** switch (`codex.lift-tools`, living in modules/codex).
//
// This module is only its reader, not its owner: lifting tools is the client
// dialect's business, independent of the upstream (see the why in switches.go).
// There are two reasons to read it, both "the two rulers must agree":
//
//   - Apply: when the lift is off, this module's fallback branch **must not** lift
//     by itself. Otherwise "turn off codex.lift-tools" would become a no-op on the
//     DeepSeek path -- a switch that reports a name nobody reads is a lie worse
//     than having no switch (measured live on 2026-09-21 in the same family).
//   - degradedNames: with no lift there are no "names that were downgraded" to speak of, so the
//     response side should not claim either. An asymmetry on either route breaks the client.
//
// A missing snapshot = not off (pluginmanager.Off answers
// false for nil); the direction must not be reversed.
func liftEnabled(r *special.Request) bool {
	return !pluginmanager.Off(stateOf(r), codexapi.SwitchLiftTools)
}

// degradedNames reads from the client source the names of tools "that will be downgraded".
//
// The criterion is word for word the same as Apply (`type: "custom"`
// and not apply_patch) -- the two places must use the same ruler, otherwise you get the
// mismatch "the request side changed nothing while the response side goes and changes
// things". So both sources (input[0].additional_tools and the top-level tools) get
// their own corresponding downgrade scan, not a second judgment written by hand; and the
// input[0] branch **follows the client's lift switch**, for the reason in liftEnabled.
func degradedNames(r *special.Request, original []byte) map[string]bool {
	degraded := map[string]bool{}
	if liftEnabled(r) {
		if arr, ok := codexTools(original); ok {
			_, d, _ := liftTools(arr)
			for k := range d {
				degraded[k] = true
			}
		}
	}
	if toolsRaw, ok := rewrite.TopLevelRaw(original, "tools"); ok {
		_, d, _ := degradeTopLevel(toolsRaw)
		for k := range d {
			degraded[k] = true
		}
	}
	if len(degraded) == 0 {
		return nil
	}
	return degraded
}

// egressTools is one exchange's response rewriter.
//
// It carries **this exchange's state** (which tools were downgraded, how far the calls
// being streamed out have accumulated), so each claim gets a new instance: several requests
// run on the wire at once, and state must not leak across requests. The other reason
// for no lock is that its lifetime is this one exchange -- from claim to end of stream.
type egressTools struct {
	degraded map[string]bool

	// calls are the downgraded calls **being streamed** in this exchange, indexed by output_index.
	// The request side declared those tools as functions, and the response's arguments come
	// out as delta after delta; they must be accumulated before we know what the input is.
	calls map[int]*degradedCall
}

// degradedCall is the scene of one downgraded call.
//
// Why accumulate: the upstream spits the parameters out in dozens of delta events (measured,
// one exec call had 47), and **only the done one carries the complete arguments**. But codex
// recognizes custom tools, and it waits for the complete item on output_item.done -- so
// either the deltas must be fixed too (and the complete input assembled by ourselves), or at
// output_item.done we go look elsewhere. The former is the only way to satisfy both streams
// at once: the accumulated args are exactly the input that output_item.done must fill in.
type degradedCall struct {
	// item is the item we rewrote at the moment of added (type is already custom_tool_call).
	item map[string]any

	// args is the accumulated arguments source (the JSON the deltas were joined into).
	args string
}

// Egress rewrites **one SSE event**. Returns nil when it
// cannot or should not change it (the caller forwards as is).
func (e *egressTools) Egress(event []byte) []byte {
	payload, start, end, ok := splitDataLine(event)
	if !ok {
		return nil
	}
	var ev map[string]any
	if json.Unmarshal(payload, &ev) != nil {
		return nil // 不是 JSON（注释行、[DONE]）：不归我管
	}
	idx := indexOf(ev["output_index"])
	switch ev["type"] {
	case "response.output_item.added":
		item, _ := ev["item"].(map[string]any)
		if item == nil || item["type"] != "function_call" {
			return nil
		}
		name, _ := item["name"].(string)
		if !e.degraded[name] {
			return nil
		}
		// The client believes it is custom: the field name changes with it (input rather than arguments),
		// and the value starts as the empty string, to be filled with the accumulated source at done.
		delete(item, "arguments")
		item["type"] = "custom_tool_call"
		item["input"] = ""
		e.remember(idx, item)
		return spliceEvent(event, start, end, ev)

	case "response.function_call_arguments.delta":
		call, ok := e.calls[idx]
		if !ok {
			return nil
		}
		call.args += fmtString(ev["delta"])
		ev["type"] = "response.custom_tool_call_input.delta"
		return spliceEvent(event, start, end, ev)

	case "response.function_call_arguments.done":
		call, ok := e.calls[idx]
		if !ok {
			return nil
		}
		input := call.finalize(fmtString(ev["arguments"]))
		ev["type"] = "response.custom_tool_call_input.done"
		ev["input"] = input
		delete(ev, "arguments")
		return spliceEvent(event, start, end, ev)

	case "response.output_item.done":
		call, ok := e.take(idx)
		if !ok {
			return nil
		}
		// This event itself carries the complete arguments (measured, the upstream sends both: the joined
		// deltas and the one on done are the same), so prefer it, with the accumulated copy as fallback.
		item, _ := ev["item"].(map[string]any)
		fromItem := ""
		if item != nil {
			fromItem = fmtString(item["arguments"])
		}
		call.item["input"] = call.finalize(fromItem)
		ev["item"] = call.item
		return spliceEvent(event, start, end, ev)

	case "response.completed":
		resp, _ := ev["response"].(map[string]any)
		if resp == nil {
			return nil
		}
		out, _ := resp["output"].([]any)
		touched := false
		for _, raw := range out {
			item, _ := raw.(map[string]any)
			if item == nil || item["type"] != "function_call" {
				continue
			}
			name, _ := item["name"].(string)
			if !e.degraded[name] {
				continue
			}
			item["type"] = "custom_tool_call"
			item["input"] = unwrapInput(fmtString(item["arguments"]))
			delete(item, "arguments")
			touched = true
		}
		if !touched {
			return nil
		}
		return spliceEvent(event, start, end, ev)
	}
	return nil
}

// finalize settles the input of this rewrite: prefer the copy the event itself carries
// (it is always complete), falling back to the accumulated deltas only when there is none.
//
// The two are the same bytes in a normal stream (measured: one exec call with 47
// deltas, joined, is identical to the one on done), so this is not "pick a better
// one" -- it is "an upstream that sends fewer deltas also comes out right".
func (c *degradedCall) finalize(fromEvent string) string {
	raw := fromEvent
	if raw == "" {
		raw = c.args
	}
	return unwrapInput(raw)
}

func (e *egressTools) remember(idx int, item map[string]any) {
	if e.calls == nil {
		e.calls = map[int]*degradedCall{}
	}
	e.calls[idx] = &degradedCall{item: item}
}

func (e *egressTools) take(idx int) (*degradedCall, bool) {
	call, ok := e.calls[idx]
	if ok {
		delete(e.calls, idx)
	}
	return call, ok
}

// unwrapInput removes the shell around the function parameters,
// taking out the real source text the custom tool needs.
//
// The request side stuffed the whole `{"cmd": "..."}` into the function's `input` parameter,
// so the response-side model naturally replies in that shape too: `{"input": "<the JS
// source>"}`. Codex wants **the source text inside** -- without unwrapping it it takes the
// whole JSON to eval, gets `SyntaxError: Unexpected token ':'`, and retries the same bad
// call round after round (measured: still going after 18 rounds, `codex exec` never exits).
//
// When it cannot be unwrapped, return as is: better that the client sees
// a piece of source it cannot read (it reports its own error, which is a
// debuggable error) than that we make up a piece of code it never wrote.
func unwrapInput(s string) string {
	var args map[string]any
	if json.Unmarshal([]byte(s), &args) != nil {
		return s
	}
	inner, ok := args["input"].(string)
	if !ok {
		return s
	}
	return inner
}

func fmtString(v any) string {
	s, _ := v.(string)
	return s
}

func indexOf(v any) int {
	f, ok := v.(float64)
	if !ok {
		return -1
	}
	return int(f)
}

// splitDataLine cuts the data line out of an SSE event into (JSON payload, payload start, end).
//
// There is only one data line in the event (that is how the upstream sends it), so find
// the first `data: ` line. The two returned indices are **the payload's position in the
// whole event's bytes**, and the rewrite replaces only that span -- the `event:` line,
// the newlines around it and the trailing blank line all stay untouched byte for byte.
func splitDataLine(event []byte) ([]byte, int, int, bool) {
	at := 0
	for at < len(event) {
		lineEnd := indexByte(event, at, '\n')
		if lineEnd < 0 {
			lineEnd = len(event)
		}
		line := event[at:lineEnd]
		trimmed := trimCR(line)
		if len(trimmed) >= 5 && string(trimmed[:5]) == "data:" {
			start := at + (len(line) - len(trimmed)) + 5
			start = skipSpaces(event, start)
			return event[start:lineEnd], start, lineEnd, true
		}
		if lineEnd >= len(event) {
			break
		}
		at = lineEnd + 1
	}
	return nil, 0, 0, false
}

// spliceEvent splices the rewritten payload back into the whole event.
//
// The payload length changes (more `"input"`, less `"arguments"`), but **this is a
// response**: there is no length field tying events to each other, and SSE boundaries are
// blank lines. So replacing this span is enough, and the rest of the bytes stay as is.
func spliceEvent(event []byte, start, end int, payload map[string]any) []byte {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	out := make([]byte, 0, len(event)-(end-start)+len(body))
	out = append(out, event[:start]...)
	out = append(out, body...)
	out = append(out, event[end:]...)
	return out
}

func indexByte(b []byte, from int, c byte) int {
	for i := from; i < len(b); i++ {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func trimCR(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\r' {
		return line[:n-1]
	}
	return line
}

func skipSpaces(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	return i
}
