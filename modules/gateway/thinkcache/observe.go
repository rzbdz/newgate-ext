package thinkcache

import (
	"bytes"
	"encoding/json"
	"strings"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Observer watches one response on the side, accumulating "this turn's reasoning content"
// and the keys it belongs to.
//
// Strictly read-only: the bytes fed in are exactly the ones forwarded to the client; the
// Observer does not touch them and writes nothing back. Byte fidelity of the forwarded bytes
// is a hard constraint (docs/01-product.md); this just rides along for a look. The worst
// outcome of a misread is that this turn is not cached and the next round cannot backfill the
// original text for that message (the plugin never fabricates content, see the file header of
// modules/deepseek/st-reasoning.go).
//
// Usage:
//
//	ob := thinkcache.NewObserver()
//	… on every Read, ob.Write(buf[:n]) …
//	ob.Commit(thinkcache.Default)   // at end of stream
type Observer struct {
	pending bytes.Buffer // tail of an SSE event not yet complete
	reason  bytes.Buffer // accumulated reasoning content
	text    bytes.Buffer // accumulated body text (the text-only turn's key depends on it)
	toolIDs []string
	seenID  map[string]bool
	// responsesDeltaItem records "this Responses reasoning item has already emitted deltas".
	// See feedResponses: the deltas and the full text in output_item.done are the same content;
	// collecting both doubles what the next round backfills.
	responsesDeltaItem map[string]bool

	// streamFailure / streamFailed record "the failure the upstream reported **inside the
	// event stream**".
	//
	// Why the observer has to handle this: the Responses dialect's upstream stuffs the failure
	// into the stream (`response.failed`) with an HTTP status of **200**. So the data plane's
	// learnQuirks (which looks only at status >= 400) never learns it, and no one keeps the
	// evidence -- the user sees "always reproduces, then fixes itself after a while", and the
	// log does not even contain a line "the upstream rejected this request". This stream is
	// read only here, so this can only be done here (see StreamFailure and the debounce
	// criterion).
	streamFailure string
	streamFailed  bool

	// ---- Counters (added 2026-09-18, purely for log forensics) ----
	//
	// Reason to exist: when "the upstream gave no reasoning content" happens in production,
	// the outcome alone (0 reasoning bytes) cannot distinguish **we failed to parse it** from
	// **the upstream really sent none**. The user's actual words were "you fucking trace it
	// out for me". These counters are that trace: which dialect, how many chunks received, how
	// many reasoning deltas / body-text deltas among them, how many chunks whose JSON won't
	// parse.
	chunks       int
	badChunks    int
	reasonDeltas int
	textDeltas   int
	sawOpenAI    bool
	sawAnthropic bool
	sawResponses bool
}

func NewObserver() *Observer {
	return &Observer{seenID: map[string]bool{}, responsesDeltaItem: map[string]bool{}}
}

// Write feeds raw response bytes. The caller may chunk arbitrarily -- SSE event boundaries are
// found here.
func (o *Observer) Write(p []byte) {
	if o == nil {
		return
	}
	// Cap: however long one stream's reasoning is, it must not eat hundreds of MB.
	// Past the cap, stop accumulating -- better this turn goes uncached than the gateway blows up.
	if o.reason.Len() > 8<<20 {
		return
	}
	o.pending.Write(p)
	for {
		s := o.pending.Bytes()
		i := bytes.Index(s, []byte("\n\n"))
		if i < 0 {
			// Half an event can be long too (one chunk stuffed with a big block of reasoning),
			// but without a boundary it cannot be parsed. Cap it so it cannot accumulate
			// without bound.
			if o.pending.Len() > 4<<20 {
				o.pending.Reset()
			}
			return
		}
		event := make([]byte, i)
		copy(event, s[:i])
		o.pending.Next(i + 2)
		o.feedEvent(event)
	}
}

func (o *Observer) feedEvent(event []byte) {
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue // event: / id: / comment lines: not ours
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		o.feedChunk(payload)
	}
}

// sseChunk holds **three** dialects at once. Using json.Unmarshal is safe here: this is
// read-only, nothing is written back, so there is no "round-trip corrupts the request" problem
// (that rule is about the forwarded bytes).
//
// `Delta` must be a RawMessage and must not be declared as an object: across the three
// dialects it has two shapes -- OpenAI and Anthropic use an object, Responses
// (`response.reasoning_text.delta`) uses a **string**. Declared as an object, every one of
// Responses' delta events makes the **whole chunk** fail to unmarshal (badChunks++ then the
// whole block is dropped), and the symptom is that the Codex path observes not one reasoning
// byte -- while in the log it looks exactly like "the upstream really sent no reasoning" (the
// Wire line just reports a pile of bad chunks).
// Field evidence: 2026-09-23, with Codex on /responses, thinkcache recorded not a single entry.
type sseChunk struct {
	// OpenAI dialect
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"` // some aggregating gateways use this name
			ToolCalls        []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`

	// Shared by Anthropic and Responses: the type name is at the top level.
	Type string `json:"type"`

	// Anthropic dialect: delta is an object
	Delta json.RawMessage `json:"delta"`

	ContentBlock struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		Thinking string `json:"thinking"`
		Text     string `json:"text"`
	} `json:"content_block"`

	// Responses dialect: a delta event uses item_id to say which reasoning item it belongs to
	// (`id` is the item's identity, `output_index` is only its position -- the rewriter
	// accounts by the latter, the observer dedups by the former).
	ItemID string `json:"item_id"`

	// Responses dialect: output_item.added / .done carry an item.
	Item responsesItem `json:"item"`

	// Responses dialect: the closing response.completed carries the whole response object
	// (the non-streaming path uses the same shape, see ObserveBody).
	Response responsesBody `json:"response"`

	// The top-level error object: one variant of `response.failed` is this (no response
	// attached).
	Error json.RawMessage `json:"error"`
}

// responsesItem is one output item of Responses.
//
// `id` and `call_id` are two different things: the former is the item's identity (a reasoning
// item's id looks like that); the latter is the id of a **tool call** -- the value the client
// puts into function_call_output next round, and the key thinkcache uses to attach keys and
// record origins.
type responsesItem struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	CallID  string `json:"call_id"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// anthropicDelta is Anthropic's delta object.
type anthropicDelta struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
}

// responsesBody is the Responses response object (streaming close and non-streaming share the
// shape).
type responsesBody struct {
	Output []responsesItem `json:"output"`
	// The error the failure branch carries (`response.failed`'s response.error).
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// responsesReasoning assembles the original reasoning text from a Responses output item.
//
// The shape is `{"type":"reasoning","content":[{"type":"reasoning_text","text":…}]}`.
// **The summary path (`summary_text`) does not count**: it is a summary for humans, not the
// original text the upstream requires back; backfilling it is fabricating content (see
// "never fabricate" in modules/deepseek/st-reasoning.go).
func responsesReasoning(item responsesItem) string {
	if item.Type != "reasoning" {
		return ""
	}
	var b strings.Builder
	for _, c := range item.Content {
		if c.Type == "reasoning_text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func (o *Observer) feedChunk(payload []byte) {
	var c sseChunk
	if json.Unmarshal(payload, &c) != nil {
		o.badChunks++
		return // unrecognized shape: skip it, it is not an error
	}
	o.chunks++

	for _, ch := range c.Choices { // OpenAI dialect
		d := ch.Delta
		o.sawOpenAI = true
		if d.ReasoningContent != "" {
			o.reasonDeltas++
			o.reason.WriteString(d.ReasoningContent)
		} else if d.Reasoning != "" {
			o.reasonDeltas++
			o.reason.WriteString(d.Reasoning)
		}
		if d.Content != "" {
			o.textDeltas++
			o.text.WriteString(d.Content)
		}
		for _, tc := range d.ToolCalls {
			o.addTool(tc.ID)
		}
	}

	switch c.Type { // Anthropic dialect
	case "content_block_start":
		o.sawAnthropic = true
		if c.ContentBlock.Type == "tool_use" {
			o.addTool(c.ContentBlock.ID)
		}
		if c.ContentBlock.Thinking != "" {
			o.reasonDeltas++
			o.reason.WriteString(c.ContentBlock.Thinking)
		}
		if c.ContentBlock.Text != "" {
			o.textDeltas++
			o.text.WriteString(c.ContentBlock.Text)
		}
	case "content_block_delta":
		o.sawAnthropic = true
		var d anthropicDelta
		if len(c.Delta) > 0 && json.Unmarshal(c.Delta, &d) == nil {
			switch d.Type {
			case "thinking_delta":
				o.reasonDeltas++
				o.reason.WriteString(d.Thinking)
			case "text_delta":
				o.textDeltas++
				o.text.WriteString(d.Text)
			}
		}
	}

	o.feedResponses(c)
	o.noteStreamFailure(c)
}

// noteStreamFailure records "the failure the upstream reported in the stream".
//
// Both shapes are recognized: the `response.error.message` carried by the `response.failed`
// event itself, and the variant carrying only an error object. When the original text is
// unavailable, keep a fallback line -- **the fact that there was a failure** matters more than
// the sentence (the sentence is for the log and the criterion; the fact "the upstream rejected
// it" is what the data plane needs to learn).
func (o *Observer) noteStreamFailure(c sseChunk) {
	if c.Type != "response.failed" || o.streamFailed {
		return
	}
	o.streamFailed = true
	o.streamFailure = strings.TrimSpace(c.Response.Error.Message)
	if o.streamFailure == "" {
		o.streamFailure = strings.TrimSpace(string(c.Error))
	}
}

// feedResponses observes the events of the Responses dialect (the one Codex uses).
//
// Event names are the three-part `response.<x>`, which do not overlap Anthropic's
// `content_block_*` on the same `type` field, so they get their own branch instead of
// lengthening the cases above.
//
// Reasoning text has **two** sources, and they must be deduplicated:
//
//	response.reasoning_text.delta   deltas (the normal path)
//	response.output_item.done       the **complete** text in item.content[]
//
// Collecting both records the same reasoning twice, and the next round backfills duplicated
// content (the upstream wants the original; a second copy changes it). So record per item id
// whether "this item has emitted deltas": if it has, the deltas win; only if it has not, use
// the complete copy in done. The response.output inside `response.completed` is a fallback
// only when **not a single reasoning byte arrived on the whole stream** -- on a normal stream
// everything is recorded long before that.
func (o *Observer) feedResponses(c sseChunk) {
	if !strings.HasPrefix(c.Type, "response.") {
		return
	}
	o.sawResponses = true
	switch c.Type {
	case "response.reasoning_text.delta":
		var s string
		if len(c.Delta) > 0 && json.Unmarshal(c.Delta, &s) == nil && s != "" {
			o.reasonDeltas++
			if c.ItemID != "" {
				o.responsesDeltaItem[c.ItemID] = true
			}
			o.reason.WriteString(s)
		}
	case "response.output_text.delta":
		var s string
		if len(c.Delta) > 0 && json.Unmarshal(c.Delta, &s) == nil && s != "" {
			o.textDeltas++
			o.text.WriteString(s)
		}
	case "response.output_item.added", "response.output_item.done":
		if c.Item.Type == "function_call" && c.Item.CallID != "" {
			o.addTool(c.Item.CallID)
		}
		if c.Item.Type == "reasoning" && !o.responsesDeltaItem[c.Item.ID] {
			if s := responsesReasoning(c.Item); s != "" {
				o.reasonDeltas++
				o.reason.WriteString(s)
			}
		}
	case "response.completed":
		if o.reason.Len() == 0 {
			o.absorbResponsesBody(c.Response)
		}
		for _, item := range c.Response.Output {
			if item.Type == "function_call" && item.CallID != "" {
				o.addTool(item.CallID)
			}
		}
	}
}

// absorbResponsesBody absorbs the reasoning and tool calls from one Responses response object.
func (o *Observer) absorbResponsesBody(r responsesBody) {
	for _, item := range r.Output {
		if s := responsesReasoning(item); s != "" {
			o.reasonDeltas++
			o.reason.WriteString(s)
		}
		if item.Type == "function_call" && item.CallID != "" {
			o.addTool(item.CallID)
		}
	}
}

func (o *Observer) addTool(id string) {
	if id == "" || o.seenID[id] {
		return
	}
	o.seenID[id] = true
	o.toolIDs = append(o.toolIDs, id)
}

// ObserveBody handles a non-streaming response: the whole body in one look.
func (o *Observer) ObserveBody(body []byte) {
	var r struct {
		// OpenAI
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
				ToolCalls        []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		// Anthropic
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
			ID       string `json:"id"`
		} `json:"content"`
		// Responses (the non-streaming one is a single response object, same shape as the
		// streaming close)
		Output []responsesItem `json:"output"`
	}
	if json.Unmarshal(body, &r) != nil {
		return
	}
	for _, ch := range r.Choices {
		m := ch.Message
		if m.ReasoningContent != "" {
			o.reason.WriteString(m.ReasoningContent)
		} else if m.Reasoning != "" {
			o.reason.WriteString(m.Reasoning)
		}
		o.text.WriteString(m.Content)
		for _, tc := range m.ToolCalls {
			o.addTool(tc.ID)
		}
	}
	for _, b := range r.Content {
		switch b.Type {
		case "thinking":
			o.reason.WriteString(b.Thinking)
		case "text":
			o.text.WriteString(b.Text)
		case "tool_use":
			o.addTool(b.ID)
		}
	}
	if len(r.Output) > 0 {
		o.sawResponses = true
		o.absorbResponsesBody(responsesBody{Output: r.Output})
	}
}

// StreamFailure reports the failure the upstream reported **in the stream** for this request:
// the original text, plus whether there was one.
func (o *Observer) StreamFailure() (string, bool) { return o.streamFailure, o.streamFailed }

// Wire reports what this response looked like **at the wire level**, for log forensics.
//
// One line says what the upstream actually sent: which dialect, how many chunks, how many
// reasoning deltas among them. Read together with ReasoningBytes it tells two cases apart at
// once:
//
//	Wire: 200 chunks / 0 reasoning deltas / 40 body deltas  ⇒ upstream sent none (request side)
//	Wire: 0 chunks (all bad) / …                            ⇒ we failed to parse (gateway side)
//
// Prints no content, only counts and booleans.
func (o *Observer) Wire() string {
	if o == nil {
		return i18n.T("(no observer)", nil)
	}
	dialect := i18n.T("unknown dialect", nil)
	switch {
	case o.sawAnthropic && o.sawOpenAI:
		dialect = i18n.T("mixed dialects", nil)
	case o.sawAnthropic:
		dialect = "anthropic"
	case o.sawOpenAI:
		dialect = "openai"
	case o.sawResponses:
		dialect = "responses"
	}
	if o.badChunks > 0 {
		return i18n.T("{dialect} {chunks} chunks / {reasoning} reasoning deltas / {text} text deltas / **{bad} chunks unparsable**",
			i18n.A{"dialect": dialect, "chunks": o.chunks, "reasoning": o.reasonDeltas,
				"text": o.textDeltas, "bad": o.badChunks})
	}
	return i18n.T("{dialect} {chunks} chunks / {reasoning} reasoning deltas / {text} text deltas",
		i18n.A{"dialect": dialect, "chunks": o.chunks, "reasoning": o.reasonDeltas,
			"text": o.textDeltas})
}

// Keys returns the keys this turn's reasoning content should be attached to.
func (o *Observer) Keys() []string {
	var keys []string
	for _, id := range o.toolIDs {
		keys = append(keys, ToolKey(id))
	}
	if k := TextKey(o.text.String()); k != "" {
		keys = append(keys, k)
	}
	return keys
}

// Commit stores it. Returns how many bytes were cached and how many keys were attached; with
// no reasoning content seen it is (0,0). The caller uses these two numbers for logging --
// **never print the content**.
func (o *Observer) Commit(c *Cache) (nbytes, nkeys int) {
	return o.CommitWithOrigin(c, Origin{})
}

// CommitWithOrigin records, besides the original reasoning text, the upstream that actually
// produced the tool call. Even when this turn has no reasoning, the origin must be recorded: a
// tool loop's dialect state still cannot cross providers safely.
func (o *Observer) CommitWithOrigin(c *Cache, origin Origin) (nbytes, nkeys int) {
	if o == nil || c == nil {
		return 0, 0
	}
	c.PutOrigin(o.toolIDs, origin)
	if o.reason.Len() == 0 {
		return 0, 0
	}
	keys := o.Keys()
	if len(keys) == 0 {
		return 0, 0 // no key matches anything: storing it would never be found again
	}
	c.Put(keys, o.reason.Bytes())
	return o.reason.Len(), len(keys)
}

// Reasoning returns the observed reasoning content (used by tests and the non-streaming path).
func (o *Observer) Reasoning() string { return o.reason.String() }

// ReasoningBytes is the length of the observed reasoning content, with no copy.
//
// Why it exists instead of len(Reasoning()): the call site is in the forwarding loop and runs
// for every response, while reasoning content is often hundreds of KB -- copying the whole
// thing into a string just to write one log line is a waste.
func (o *Observer) ReasoningBytes() int {
	if o == nil {
		return 0
	}
	return o.reason.Len()
}

// ToolCalls is the number of tool calls observed this turn (after dedup).
//
// For the log: when a turn has tool calls but the upstream gave not one byte of reasoning, the
// next round's patch can backfill **nothing** for this message (the plugin does not fabricate
// placeholders) -- and **that is because the upstream never gave it, not because we lost it**.
// The two are handled in opposite ways; the log must tell them apart (see skipCause in
// modules/deepseek/st-reasoning.go).
func (o *Observer) ToolCalls() int {
	if o == nil {
		return 0
	}
	return len(o.toolIDs)
}

// LooksLikeSSE roughly decides whether a Content-Type is an event stream.
func LooksLikeSSE(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "event-stream")
}
