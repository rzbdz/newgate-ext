package thinkcache

import (
	"encoding/json"
	"fmt"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// KeysForAssistantMessage computes the candidate keys from one assistant message **in the
// request**; the order is the priority. This is the mirror of Observer.Keys(): one side
// attaches keys in the response, the other finds them back by the same rules in the next
// round's request; the two must correspond exactly, change one and you must change the other.
//
// Both dialects are recognized (newgate does no protocol conversion: whatever dialect the
// client sends is forwarded verbatim, so both must be recognized here):
//
//	OpenAI    {"role":"assistant","content":"…","tool_calls":[{"id":"…"}]}
//	Anthropic {"role":"assistant","content":[{"type":"tool_use","id":"…"},…]}
func KeysForAssistantMessage(item []byte) []string {
	keys, _ := keysForAssistantMessage(item)
	return keys
}

// keysForAssistantMessage is like KeysForAssistantMessage but also answers "was this
// message (or its content) parsed at all".
//
// Why the distinction: the three failure paths (the whole message won't parse, content is a
// string that won't parse, content is a block array that won't parse) used to collapse into
// one result -- an empty key list -- so the caller saw a **miss**. And "miss" sends
// investigation toward the cache and the upstream, when the real cause is a JSON shape sent
// by the client that we do not know. The sole reason modules/deepseek's skipCause exists is
// to answer "why is there no thinking"; it now has a third kind of cause to distinguish (see
// the note on Cache.unparsable).
func keysForAssistantMessage(item []byte) ([]string, bool) {
	var m struct {
		ToolCalls []struct {
			ID string `json:"id"`
		} `json:"tool_calls"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(item, &m) != nil {
		return nil, false
	}

	ok := true
	var keys []string
	for _, tc := range m.ToolCalls { // the OpenAI dialect
		if k := ToolKey(tc.ID); k != "" {
			keys = append(keys, k)
		}
	}

	text := ""
	if len(m.Content) > 0 {
		switch m.Content[0] {
		case '"': // content is a plain string
			if json.Unmarshal(m.Content, &text) != nil {
				ok = false
			}
		case '[': // content is an array of blocks (the Anthropic dialect)
			var blocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
				ID   string `json:"id"`
			}
			if json.Unmarshal(m.Content, &blocks) != nil {
				ok = false
				break
			}
			for _, b := range blocks {
				switch b.Type {
				case "tool_use":
					if k := ToolKey(b.ID); k != "" {
						keys = append(keys, k)
					}
				case "text":
					text += b.Text
				}
			}
		}
	}
	if k := TextKey(text); k != "" {
		keys = append(keys, k)
	}
	return keys, ok
}

// Lookup tries the candidate keys in order, returning on the first hit.
//
// An entry that fails to parse is recorded in Stats().Unparsable and logged on one line: in
// outcome it is the same as a "miss" (the caller then backfills an empty string), but the
// cause is entirely different, and the log only shows "backfilled an empty string". Without
// the counter this classification is broken forever -- the investigator goes to the cache
// and the upstream, when what should be looked at is the JSON the client sent.
func (c *Cache) Lookup(item []byte) ([]byte, bool) {
	keys, parsed := keysForAssistantMessage(item)
	if !parsed {
		c.mu.Lock()
		c.unparsable++
		c.mu.Unlock()
		// The first 200 bytes are still truncated by bytes (%.200s), byte for byte as before
		// i18n. The message must be a **single literal** (tools/i18n/scan recognizes only one
		// BasicLit) -- a concatenated sentence is not scanned and the ledger would miss one
		// entry.
		c.fail(i18n.E("thinkcache: cannot parse an assistant message in the request; this round is treated as a miss (empty string filled in) — a JSON shape problem on the client side, not a cache problem (first 200 bytes: {snippet})",
			i18n.A{"snippet": fmt.Sprintf("%.200s", item)}))
	}
	for _, k := range keys {
		if blob, ok := c.Get(k); ok {
			return blob, true
		}
	}
	return nil, false
}

// ContinuationOrigin decides whether the request is in an unclosed tool loop, and finds the
// upstream that produced the tool call. It looks only at the request tail: a normal new user
// turn must not stay stuck forever even if the history contains tool calls.
//
// Three dialects:
//   - Anthropic: the last user.content[] contains tool_result
//   - OpenAI: consecutive role=tool messages at the end of the request
//   - Responses (the one Codex uses): consecutive function_call_output at the end of top-level
//     input[]
//
// Why the Responses branch had to be added (2026-09-23): without it, Codex's tool loop does
// **not count as unclosed** by this criterion -- so "which vendor produced this turn's tool
// call" is unknowable, cross-upstream migration (special.RebaseToolLoop) never fires, and
// DeepSeek answers 400 when it takes over another vendor's unclosed reasoning/tool state (see
// the A/B measurement in modules/deepseek/st-reasoning.go). The symptom looks like "the patch
// did not take effect", but the root cause is on the observation side: we never recorded the
// origin of that call_id in the request.
//
// The shape follows real traffic (dump/err-429-req001474.client-sent.json, 2026-09-23): every
// entry in top-level input[] is
// `{"type":"message"|"function_call"|"function_call_output", …}`, and the tool output entry
// carries `call_id`.
func (c *Cache) ContinuationOrigin(body []byte) (Origin, bool) {
	var req struct {
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
		} `json:"messages"`
		// Responses dialect: the conversation is in top-level input[], there are no messages.
		Input []struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
		} `json:"input"`
	}
	if json.Unmarshal(body, &req) != nil {
		return Origin{}, false
	}

	var ids []string
	switch {
	case len(req.Messages) > 0:
		last := req.Messages[len(req.Messages)-1]
		switch last.Role {
		case "tool": // OpenAI dialect; one parallel call can leave several consecutive tool messages
			for i := len(req.Messages) - 1; i >= 0 && req.Messages[i].Role == "tool"; i-- {
				if id := req.Messages[i].ToolCallID; id != "" {
					ids = append(ids, id)
				}
			}
		case "user": // Anthropic dialect; several tool_results in one content[]
			if len(last.Content) == 0 || last.Content[0] != '[' {
				return Origin{}, false
			}
			var blocks []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
			}
			if json.Unmarshal(last.Content, &blocks) != nil {
				return Origin{}, false
			}
			for _, b := range blocks {
				if b.Type == "tool_result" && b.ToolUseID != "" {
					ids = append(ids, b.ToolUseID)
				}
			}
		default:
			return Origin{}, false
		}
	case len(req.Input) > 0:
		// Responses dialect: consecutive function_call_output at the end. Same shape as the
		// OpenAI branch -- a parallel call yields several in a row, and another kind in between
		// (a user message, a function_call) means the turn is already closed (the client is
		// waiting for new instructions, not for tool results).
		for i := len(req.Input) - 1; i >= 0 && req.Input[i].Type == "function_call_output"; i-- {
			if id := req.Input[i].CallID; id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return Origin{}, false
		}
	default:
		return Origin{}, false
	}

	var found Origin
	for _, id := range ids {
		origin, ok := c.GetOrigin(id)
		if !ok {
			continue
		}
		if found.Provider == "" {
			found = origin
			continue
		}
		if found.Provider != origin.Provider || found.Model != origin.Model {
			return Origin{}, false // same batch of tool results, conflicting origins: do not guess
		}
	}
	return found, found.Provider != ""
}
