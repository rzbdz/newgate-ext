package thinkcache

import (
	"strings"
	"testing"
	"time"
)

const reasoning = "look at \"main.go\"\nthen look at src\\a"

// SSE event boundaries have nothing to do with TCP read boundaries: a piece of reasoning can
// be split right between `reasoning_` and `content`. Feeding byte by byte is the harshest
// boundary test.
func TestObserverSurvivesArbitraryChunking(t *testing.T) {
	stream := `data: {"choices":[{"delta":{"reasoning_content":"look at \"main.go\"\n"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"reasoning_content":"then look at src\\a"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":"ok"},"index":0}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_x"}]}}]}` + "\n\n" +
		"data: [DONE]\n\n"

	for _, size := range []int{1, 3, 17, 4096} {
		o := NewObserver()
		for i := 0; i < len(stream); i += size {
			j := i + size
			if j > len(stream) {
				j = len(stream)
			}
			o.Write([]byte(stream[i:j]))
		}
		if got := o.Reasoning(); got != reasoning {
			t.Fatalf("wrong reasoning content with %d-byte chunks\n want %q\n got  %q", size, reasoning, got)
		}
		keys := o.Keys()
		if len(keys) != 2 || keys[0] != ToolKey("call_x") {
			t.Fatalf("wrong key with %d-byte chunks: %v", size, keys)
		}
	}
}

// Anthropic dialect: thinking_delta / content_block_start(tool_use)
func TestObserverAnthropicDialect(t *testing.T) {
	o := NewObserver()
	for _, ev := range []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"look at \"main.go\"\n"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"then look at src\\a"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"Read"}}`,
	} {
		o.Write([]byte("event: x\ndata: " + ev + "\n\n"))
	}
	if got := o.Reasoning(); got != reasoning {
		t.Fatalf("want %q, got %q", reasoning, got)
	}
	if k := o.Keys(); len(k) == 0 || k[0] != ToolKey("toolu_9") {
		t.Fatalf("wrong key: %v", k)
	}
}

// The keys attached on the response side and the keys looked up on the request side must
// correspond exactly; change one and you must change the other.
func TestKeysMirrorBetweenResponseAndRequest(t *testing.T) {
	cases := []struct {
		name     string
		respTool string
		respText string
		reqItem  string
	}{
		{"OpenAI dialect tool_call", "call_x", "",
			`{"role":"assistant","content":"","tool_calls":[{"id":"call_x"}]}`},
		{"Anthropic dialect tool_use", "toolu_9", "",
			`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_9"}]}`},
		{"plain-text turn (OpenAI)", "", "ok, I have read it all",
			`{"role":"assistant","content":"ok, I have read it all"}`},
		{"plain-text turn (Anthropic)", "", "ok, I have read it all",
			`{"role":"assistant","content":[{"type":"text","text":"ok, I have read it all"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New(1<<20, time.Hour)
			o := NewObserver()
			o.reason.WriteString(reasoning)
			o.text.WriteString(tc.respText)
			if tc.respTool != "" {
				o.addTool(tc.respTool)
			}
			if n, _ := o.Commit(c); n == 0 {
				t.Fatal("not stored")
			}
			blob, ok := c.Lookup([]byte(tc.reqItem))
			if !ok {
				t.Fatalf("cannot find it on the request side. response-side key=%v, request-side key=%v",
					o.Keys(), KeysForAssistantMessage([]byte(tc.reqItem)))
			}
			if string(blob) != reasoning {
				t.Fatalf("content mismatch: %q", blob)
			}
		})
	}
}

// Several tool_calls in one turn: recognizing any one id must find back the same piece of
// reasoning.
func TestAnyToolIDFindsTheSameReasoning(t *testing.T) {
	c := New(1<<20, time.Hour)
	c.Put([]string{ToolKey("a"), ToolKey("b"), ToolKey("c")}, []byte(reasoning))
	for _, id := range []string{"a", "b", "c"} {
		item := `{"role":"assistant","tool_calls":[{"id":"` + id + `"}]}`
		if blob, ok := c.Lookup([]byte(item)); !ok || string(blob) != reasoning {
			t.Fatalf("id=%s cannot be found", id)
		}
	}
}

func TestTTLAndByteCap(t *testing.T) {
	c := New(1<<20, 20*time.Millisecond)
	c.Put([]string{"k"}, []byte(reasoning))
	if _, ok := c.Get("k"); !ok {
		t.Fatal("it was gone right after being stored")
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("still there past the TTL")
	}

	// Byte cap: after overfilling, the total byte count must not exceed the bound; the oldest
	// is evicted
	small := New(300, time.Hour)
	blob := []byte(strings.Repeat("x", 100))
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		small.Put([]string{k}, blob)
	}
	st := small.Stats()
	if st.Bytes > st.MaxBytes {
		t.Fatalf("over the limit: %d > %d", st.Bytes, st.MaxBytes)
	}
	if _, ok := small.Get("a"); ok {
		t.Fatal("the earliest was not evicted")
	}
	if _, ok := small.Get("e"); !ok {
		t.Fatal("the newest was evicted")
	}
}

// Without a key that will match later, do not store -- it would never be found again, just
// wasting space.
func TestNoKeysNoStore(t *testing.T) {
	c := New(1<<20, time.Hour)
	o := NewObserver()
	o.reason.WriteString(reasoning)
	if n, k := o.Commit(c); n != 0 || k != 0 {
		t.Fatalf("stored even without a key: %d bytes / %d keys", n, k)
	}
	if c.Stats().Entries != 0 {
		t.Fatal("something extra appeared in the cache")
	}
}

func TestContinuationOriginOnlyWhileToolLoopIsOpen(t *testing.T) {
	c := New(1<<20, time.Hour)
	origin := Origin{Profile: "ark", Provider: "ark", Model: "ark-code-latest"}
	c.PutOrigin([]string{"call_a", "call_b"}, origin)

	anthropic := []byte(`{"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"call_a"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_a"}]}
		]}`)
	if got, ok := c.ContinuationOrigin(anthropic); !ok || got != origin {
		t.Fatalf("wrong Anthropic tool loop origin: ok=%v got=%+v", ok, got)
	}

	openAI := []byte(`{"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_b"}]},
			{"role":"tool","tool_call_id":"call_b","content":"ok"}
		]}`)
	if got, ok := c.ContinuationOrigin(openAI); !ok || got != origin {
		t.Fatalf("wrong OpenAI tool loop origin: ok=%v got=%+v", ok, got)
	}

	newTurn := []byte(`{"messages":[
			{"role":"tool","tool_call_id":"call_b","content":"ok"},
			{"role":"user","content":"new question"}
		]}`)
	if _, ok := c.ContinuationOrigin(newTurn); ok {
		t.Fatal("a new plain user turn must not keep pinning the old provider")
	}

	// Responses dialect (the one Codex uses): the conversation sits in top-level input[], and
	// tool output is a function_call_output item. Without this branch, Codex's tool loop does
	// not count as unclosed by this criterion -- cross-upstream migration never fires, and
	// DeepSeek answers 400 when it takes over another vendor's unclosed reasoning/tool state.
	responses := []byte(`{"model":"normal","input":[
			{"type":"function_call","call_id":"call_b","name":"exec","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_b","output":"ok"}
		]}`)
	if got, ok := c.ContinuationOrigin(responses); !ok || got != origin {
		t.Fatalf("wrong Responses tool loop origin: ok=%v got=%+v", ok, got)
	}

	// Parallel calls: two outputs in a row at the end; both count for this turn.
	parallel := []byte(`{"input":[
			{"type":"function_call_output","call_id":"call_a","output":"1"},
			{"type":"function_call_output","call_id":"call_b","output":"2"}
		]}`)
	if _, ok := c.ContinuationOrigin(parallel); !ok {
		t.Fatal("two function_call_output in a row is still an unclosed turn")
	}

	// The tail is no longer function_call_output → this turn is closed (the client is waiting
	// for new instructions).
	responsesClosed := []byte(`{"input":[
			{"type":"function_call_output","call_id":"call_b","output":"ok"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"keep going"}]}
		]}`)
	if _, ok := c.ContinuationOrigin(responsesClosed); ok {
		t.Fatal("a plain trailing message must not keep pinning the old provider")
	}
}

// Non-streaming responses must be observable too.
func TestObserveBody(t *testing.T) {
	o := NewObserver()
	o.ObserveBody([]byte(`{"choices":[{"message":{"role":"assistant",` +
		`"reasoning_content":"look at \"main.go\"\nthen look at src\\a","content":"ok",` +
		`"tool_calls":[{"id":"call_z"}]}}]}`))
	if got := o.Reasoning(); got != reasoning {
		t.Fatalf("want %q got %q", reasoning, got)
	}
	if k := o.Keys(); len(k) == 0 || k[0] != ToolKey("call_z") {
		t.Fatalf("wrong key: %v", k)
	}
}

// The Responses dialect (the one Codex uses) must be observable too.
//
// This chain once had a silent break point (2026-09-23): sseChunk's `delta` was declared as
// an object, while the delta of `response.reasoning_text.delta` is a **string** -- every
// reasoning delta event made the whole chunk fail to unmarshal, so the Codex path recorded
// not a single reasoning byte. Its behavior is identical to "the upstream really sent no
// reasoning" (0 reasoning bytes); only the bad-chunk count in the Wire line tells them apart.
//
// Also pins down dedup: the same reasoning appears once in the delta events and once in
// output_item.done; collecting both makes the next round's backfill duplicated content.
func TestObserverResponsesDialect(t *testing.T) {
	stream := `event: response.created` + "\n" + `data: {"type":"response.created"}` + "\n\n" +
		`event: response.output_item.added` + "\n" +
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}` + "\n\n" +
		`event: response.reasoning_text.delta` + "\n" +
		`data: {"type":"response.reasoning_text.delta","output_index":0,"item_id":"rs_1","delta":"look at \"main.go\"\n"}` + "\n\n" +
		`event: response.reasoning_text.delta` + "\n" +
		`data: {"type":"response.reasoning_text.delta","output_index":0,"item_id":"rs_1","delta":"then look at src\\a"}` + "\n\n" +
		// The closing event carries the **same** full text: it must not be counted twice.
		`event: response.output_item.done` + "\n" +
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"look at \"main.go\"\nthen look at src\\a"}]}}` + "\n\n" +
		`event: response.output_item.done` + "\n" +
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"item_1","call_id":"call_00_abc","name":"exec","arguments":"{}"}}` + "\n\n"

	o := NewObserver()
	// Feed in chunks: the rule that event boundaries are unrelated to read boundaries applies
	// to every dialect alike.
	for i := 0; i < len(stream); i += 7 {
		j := i + 7
		if j > len(stream) {
			j = len(stream)
		}
		o.Write([]byte(stream[i:j]))
	}
	if o.badChunks != 0 {
		t.Fatalf("%d chunks cannot be parsed - is delta declared as an object again?", o.badChunks)
	}
	if got := o.Reasoning(); got != reasoning {
		t.Fatalf("wrong reasoning content (counted twice or missed)\n want %q\n got  %q", reasoning, got)
	}
	keys := o.Keys()
	if len(keys) == 0 || keys[0] != ToolKey("call_00_abc") {
		t.Fatalf("the key should hang on call_id (not the item id): %v", keys)
	}
	if !strings.Contains(o.Wire(), "responses") {
		t.Fatalf("Wire should recognize this as the responses dialect: %s", o.Wire())
	}
}

// A non-streaming Responses response (one response object, same shape as the streaming close).
func TestObserveResponsesBody(t *testing.T) {
	o := NewObserver()
	o.ObserveBody([]byte(`{"id":"resp_1","object":"response","output":[` +
		`{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"look at \"main.go\"\nthen look at src\\a"}]},` +
		`{"type":"function_call","id":"item_1","call_id":"call_00_abc","name":"exec","arguments":"{}"}]}`))
	if got := o.Reasoning(); got != reasoning {
		t.Fatalf("want %q got %q", reasoning, got)
	}
	if k := o.Keys(); len(k) == 0 || k[0] != ToolKey("call_00_abc") {
		t.Fatalf("wrong key: %v", k)
	}
}

// The Responses summary is not "the original text required back"; not a byte of it may be
// kept.
//
// Field evidence: the upstream's reasoning item carries both content[] (the original text)
// and summary[] (a summary for humans). Backfilling the summary as if it were the original is
// fabricating content -- exactly what the "never fabricate" rule in st-reasoning.go guards
// against.
func TestObserverResponsesIgnoresSummary(t *testing.T) {
	o := NewObserver()
	o.ObserveBody([]byte(`{"output":[{"type":"reasoning","id":"rs_1",` +
		`"summary":[{"type":"summary_text","text":"a short summary"}],"content":[]}]}`))
	if got := o.Reasoning(); got != "" {
		t.Fatalf("summary must not be treated as the reasoning text, but %q was collected", got)
	}
}

// TestObserverNotesStreamFailure pins down that "the upstream stuffed the failure into the
// stream" is caught.
//
// Why it must exist: with stream:true the Responses dialect opens the stream with 200 first,
// then pushes the failure into the event stream (response.failed). Only the Observer reads
// this stream, so the fact "the upstream rejected this request" can **only** be asked here --
// if this link does not connect, the data plane's learnQuirks (which looks only at
// status>=400) never learns it, and what the user sees is "always reproduces, then fixes
// itself after a while". The line "stream disconnected before completion" reported on
// 2026-09-22 is exactly this shape.
func TestObserverNotesStreamFailure(t *testing.T) {
	o := NewObserver()
	if _, failed := o.StreamFailure(); failed {
		t.Fatal("a normal stream must not report a failure")
	}
	o.Write([]byte(`event: response.created` + "\n" +
		`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}` + "\n\n"))
	o.Write([]byte(`event: response.failed` + "\n" +
		`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","output":[],` +
		`"error":{"code":"invalid_request_error","message":"该模型始终思考，不支持关闭思考；请使用 low、high 或 max。"}}}` + "\n\n"))
	msg, failed := o.StreamFailure()
	if !failed {
		t.Fatal("the response.failed in the stream was not recorded")
	}
	// The original text stays byte for byte: it is the **match input** of the quirk signature
	// (judged by bytes.Contains); changing one byte means the signature never matches again.
	if !strings.Contains(msg, "始终思考") {
		t.Fatalf("the original text was lost: %q", msg)
	}
}

// TestObserverStreamFailureTakesTheFirstOnly records **only the first**.
//
// When one stream reports two failures, the first sentence is the cause and the later ones
// are its echo (the upstream sometimes adds an aborted/error summary). Taking the first also
// means later normal events cannot overwrite it.
func TestObserverStreamFailureTakesTheFirstOnly(t *testing.T) {
	o := NewObserver()
	for _, m := range []string{"the first reason", "a later echo"} {
		o.Write([]byte(`data: {"type":"response.failed","response":{"error":{"message":"` + m + `"}}}` + "\n\n"))
	}
	if msg, _ := o.StreamFailure(); msg != "the first reason" {
		t.Fatalf("the first one should be kept, got %q", msg)
	}
}

// TestObserverStreamFailureWithoutResponseObject recognizes the other envelope.
//
// Measured two kinds: sometimes the failure sentence is in `response.error.message`, sometimes
// there is only a top-level `error` object. **The fact that the upstream rejected this
// request** matters more than the sentence -- when the original text is unavailable, keep a
// fallback line, but do not treat it as if nothing happened.
func TestObserverStreamFailureWithoutResponseObject(t *testing.T) {
	o := NewObserver()
	o.Write([]byte(`data: {"type":"response.failed","error":{"code":"server_error","message":"upstream exploded"}}` + "\n\n"))
	msg, failed := o.StreamFailure()
	if !failed {
		t.Fatal("a variant with a top-level error was not recognized")
	}
	if !strings.Contains(msg, "upstream exploded") {
		t.Fatalf("the fallback message should carry the original text, got %q", msg)
	}
}
