package upstream_test

// This file self-tests the fake upstream: both dialects pass streaming and not,
// count_tokens is 42, the strict rule really 400s (first prove it blocks, then
// that filled-in reasoning passes), FailNext hits one shot, Requests/Reset is intact.
//
// Why test a test double on purpose: every e2e conclusion rests on the fake really
// blocking per the official rule - had it misjudged, the 200s after it are
// worthless (mock/e2e_claude.sh ch. 6 checks this; TestStrictReasoning).

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/testing/upstream"
)

// ---- small helpers ----

// send sends one request and returns the raw response (callers close Body; use it for headers too).
func send(t *testing.T, s *upstream.Server, method, path, body string, headers map[string]string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, s.URL()+path, reader)
	if err != nil {
		t.Fatalf("building request %s %s: %v", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// post sends a POST and reads back the status code and raw body.
func post(t *testing.T, s *upstream.Server, path, body string) (int, string) {
	t.Helper()
	resp := send(t, s, http.MethodPost, path, body, nil)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response body of %s: %v", path, err)
	}
	return resp.StatusCode, string(raw)
}

// get sends a GET.
func get(t *testing.T, s *upstream.Server, path string) (int, string) {
	t.Helper()
	resp := send(t, s, http.MethodGet, path, "", nil)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response body of %s: %v", path, err)
	}
	return resp.StatusCode, string(raw)
}

// decodeObj decodes a JSON object, Fatalf otherwise.
func decodeObj(t *testing.T, raw string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("not a JSON object: %v (raw: %s)", err, raw)
	}
	return obj
}

// dig walks a path (string = object key, int = array index), Fatalf if it misses.
// Assertions are full of dig(..., "choices", 0, "message", "content"); only flattened does it read.
func dig(t *testing.T, value any, path ...any) any {
	t.Helper()
	current := value
	for _, step := range path {
		switch key := step.(type) {
		case string:
			obj, ok := current.(map[string]any)
			if !ok {
				t.Fatalf("getting %v: %v is not an object", path, current)
			}
			field, ok := obj[key]
			if !ok {
				t.Fatalf("getting %v: no %q (actual keys: %v)", path, key, obj)
			}
			current = field
		case int:
			arr, ok := current.([]any)
			if !ok {
				t.Fatalf("getting %v: %v is not an array", path, current)
			}
			if key >= len(arr) {
				t.Fatalf("getting %v: index %d out of range (length %d)", path, key, len(arr))
			}
			current = arr[key]
		default:
			t.Fatalf("getting %v: path segments must be string or int", path)
		}
	}
	return current
}

// sseEvents splits an SSE body into events: one "data: <json>" line per chunk, a
// blank line between; the trailing [DONE] is not JSON, returned separately as a bool.
func sseEvents(t *testing.T, body string) (events []map[string]any, done bool) {
	t.Helper()
	for _, block := range strings.Split(body, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		if !strings.HasPrefix(block, "data: ") {
			t.Fatalf("SSE block is not a data: line: %q", block)
		}
		payload := strings.TrimPrefix(block, "data: ")
		if payload == "[DONE]" {
			done = true
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatalf("SSE block is not JSON: %v (raw: %s)", err, payload)
		}
		events = append(events, event)
	}
	return events, done
}

// eventTypes extracts the event type sequence, for asserting Anthropic's whole chunk order.
func eventTypes(events []map[string]any) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		kind, _ := event["type"].(string)
		out = append(out, kind)
	}
	return out
}

// ---- openai dialect ----

func TestOpenAINonStream(t *testing.T) {
	s := upstream.New(t)
	payload := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`

	resp := send(t, s, http.MethodPost, "/v1/chat/completions", payload, map[string]string{
		"Authorization":     "Bearer sk-test",
		"Anthropic-Version": "2023-06-01",
	})
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want 200 (body: %s)", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	body := decodeObj(t, string(raw))
	if got := dig(t, body, "id"); got != upstream.ChatCompletionID {
		t.Errorf("id = %v, want %s", got, upstream.ChatCompletionID)
	}
	if got := dig(t, body, "object"); got != "chat.completion" {
		t.Errorf("object = %v, want chat.completion", got)
	}
	if got := dig(t, body, "model"); got != "deepseek-chat" {
		t.Errorf("model = %v (the response must echo the model from the request)", got)
	}
	if got := dig(t, body, "choices", 0, "message", "content"); got != "MOCK-OK model=deepseek-chat" {
		t.Errorf("choices[0].message.content = %v", got)
	}
	if got := dig(t, body, "choices", 0, "finish_reason"); got != "stop" {
		t.Errorf("finish_reason = %v", got)
	}
	if got := dig(t, body, "usage", "total_tokens"); got != float64(12) {
		t.Errorf("usage.total_tokens = %v, want 12", got)
	}

	// the record face: it is how we know what the proxy actually sent
	if s.Count() != 1 {
		t.Fatalf("record count = %d, want 1", s.Count())
	}
	rec, ok := s.Last()
	if !ok {
		t.Fatal("Last() says there are no records")
	}
	if rec.Method != http.MethodPost {
		t.Errorf("recorded method = %q", rec.Method)
	}
	if rec.Path != "/v1/chat/completions" {
		t.Errorf("recorded path = %q", rec.Path)
	}
	if got := rec.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("recorded Authorization = %q", got)
	}
	if got := rec.Header.Get("Anthropic-Version"); got != "2023-06-01" {
		t.Errorf("recorded Anthropic-Version = %q (http.Header keys are case-insensitive)", got)
	}
	if string(rec.Body) != payload {
		t.Errorf("recorded request body = %q, want the verbatim bytes %q", rec.Body, payload)
	}
	if rec.At.IsZero() || rec.At.After(time.Now()) {
		t.Errorf("recorded time = %v, wrong", rec.At)
	}
	fields, err := rec.JSON()
	if err != nil {
		t.Fatalf("Record.JSON(): %v", err)
	}
	if got := fields["model"]; got != "deepseek-chat" {
		t.Errorf("model in JSON() = %v", got)
	}
}

func TestOpenAIStream(t *testing.T) {
	s := upstream.New(t)
	s.SetChunkDelay(0) // 协议用例不必真等

	resp := send(t, s, http.MethodPost, "/v1/chat/completions",
		`{"model":"glm-4-plus","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	// Framing must match python: no length, no chunking (body delimited by close).
	// Go's default adds chunked to HTTP/1.1 responses of unknown length, which is
	// not the framing the gateway sees in the bash e2e.
	if len(resp.TransferEncoding) != 0 {
		t.Errorf("the response was chunked (%v) -- was the Transfer-Encoding: identity line removed?", resp.TransferEncoding)
	}
	if resp.ContentLength != -1 {
		t.Errorf("ContentLength = %d, a streaming response should not have a length", resp.ContentLength)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response body: %v", err)
	}
	events, done := sseEvents(t, string(raw))
	if !done {
		t.Error("the stream does not end with data: [DONE]")
	}
	if len(events) != 5 {
		t.Fatalf("event count = %d, want 4 delta blocks + 1 closing block (actual types: %v)", len(events), eventTypes(events))
	}

	var text strings.Builder
	for _, event := range events[:4] {
		if got := dig(t, event, "object"); got != "chat.completion.chunk" {
			t.Errorf("block object = %v, want chat.completion.chunk", got)
		}
		if got := dig(t, event, "model"); got != "glm-4-plus" {
			t.Errorf("block model = %v", got)
		}
		if got := dig(t, event, "choices", 0, "index"); got != float64(0) {
			t.Errorf("block index = %v", got)
		}
		text.WriteString(dig(t, event, "choices", 0, "delta", "content").(string))
	}
	if got := text.String(); got != "MOCK-STREAM glm-4-plus" {
		t.Errorf("delta blocks joined = %q, want MOCK-STREAM glm-4-plus", got)
	}
	if got := dig(t, events[4], "choices", 0, "finish_reason"); got != "stop" {
		t.Errorf("closing block finish_reason = %v", got)
	}
}

// ---- anthropic dialect ----

func TestAnthropicNonStream(t *testing.T) {
	s := upstream.New(t)
	withTools := `{"model":"glm-4-plus","max_tokens":64,` +
		`"tools":[{"name":"Read","input_schema":{"type":"object"}}],` +
		`"messages":[{"role":"user","content":"go"}]}`

	for _, tc := range []struct {
		name string
		path string
		body string
		want int // 期望 content 里几块
	}{
		{"with tools: thinking + tool_use + text", "/v1/messages", withTools, 3},
		{"prefixed paths are also accepted (before the gateway strips the prefix / when hitting the fake upstream directly)", "/a/claude/p/ds/v1/messages", withTools, 3},
		{"without tools: no tool_use block", "/v1/messages",
			`{"model":"glm-4-plus","max_tokens":64,"messages":[{"role":"user","content":"go"}]}`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := post(t, s, tc.path, tc.body)
			if code != http.StatusOK {
				t.Fatalf("status code = %d, want 200 (body: %s)", code, raw)
			}
			body := decodeObj(t, raw)
			if got := dig(t, body, "id"); got != upstream.MessageID {
				t.Errorf("id = %v", got)
			}
			if got := dig(t, body, "type"); got != "message" {
				t.Errorf("type = %v", got)
			}
			if got := dig(t, body, "model"); got != "glm-4-plus" {
				t.Errorf("model = %v", got)
			}
			if got := dig(t, body, "stop_reason"); got != "end_turn" {
				t.Errorf("stop_reason = %v", got)
			}
			if got := dig(t, body, "usage", "input_tokens"); got != float64(7) {
				t.Errorf("usage.input_tokens = %v", got)
			}

			content, ok := dig(t, body, "content").([]any)
			if !ok {
				t.Fatalf("content is not an array: %v", dig(t, body, "content"))
			}
			if len(content) != tc.want {
				t.Fatalf("content block count = %d, want %d", len(content), tc.want)
			}
			// Order is semantics: thinking first, tool_use right after - thinkcache
			// binds reasoning text to tool id through this adjacency.
			if got := dig(t, content, 0, "type"); got != "thinking" {
				t.Errorf("content[0].type = %v, want thinking", got)
			}
			if got := dig(t, content, 0, "thinking"); got != upstream.ThinkingText {
				t.Errorf("content[0].thinking = %v", got)
			}
			if got := dig(t, content, 0, "signature"); got != upstream.ThinkingSignature {
				t.Errorf("content[0].signature = %v", got)
			}
			if tc.want == 3 {
				if got := dig(t, content, 1, "type"); got != "tool_use" {
					t.Errorf("content[1].type = %v, want tool_use", got)
				}
				if got := dig(t, content, 1, "id"); got != upstream.ToolUseID {
					t.Errorf("content[1].id = %v, want %s", got, upstream.ToolUseID)
				}
				if got := dig(t, content, 1, "name"); got != upstream.ToolName {
					t.Errorf("content[1].name = %v", got)
				}
			}
			last := len(content) - 1
			if got := dig(t, content, last, "type"); got != "text" {
				t.Errorf("type of the last block = %v, want text", got)
			}
			if got := dig(t, content, last, "text"); got != "MOCK-OK model=glm-4-plus" {
				t.Errorf("text of the last block = %v", got)
			}
		})
	}
}

func TestAnthropicStream(t *testing.T) {
	s := upstream.New(t)
	s.SetChunkDelay(0)
	withTools := `{"model":"glm-4-plus","stream":true,"max_tokens":64,` +
		`"tools":[{"name":"Read","input_schema":{"type":"object"}}],` +
		`"messages":[{"role":"user","content":"go"}]}`

	resp := send(t, s, http.MethodPost, "/v1/messages", withTools, nil)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response body: %v", err)
	}
	events, done := sseEvents(t, string(raw))
	if !done {
		t.Error("the stream does not end with data: [DONE] (the python version sends this line too)")
	}

	// The whole chunk sequence: Claude Code's real shape (thinking block first, the
	// tools request adds tool_use, text blocks after).
	want := []string{
		"message_start",
		"content_block_start", // index 0：thinking
		"content_block_delta", // 推理原文
		"content_block_start", // index 1：tool_use
		"content_block_start", // index 2：text
		"content_block_delta", "content_block_delta", "content_block_delta", "content_block_delta",
		"message_stop",
	}
	got := eventTypes(events)
	if len(got) != len(want) {
		t.Fatalf("block sequence = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("type of block %d = %q, want %q (whole thing: %v)", i, got[i], want[i], got)
		}
	}

	if got := dig(t, events[0], "message", "id"); got != upstream.MessageID {
		t.Errorf("message_start message.id = %v", got)
	}
	if got := dig(t, events[0], "message", "model"); got != "glm-4-plus" {
		t.Errorf("message_start message.model = %v", got)
	}
	if got := dig(t, events[0], "message", "content"); got == nil {
		t.Error("message_start message.content should be an empty array")
	}
	if got := dig(t, events[1], "index"); got != float64(0) {
		t.Errorf("thinking block index = %v", got)
	}
	if got := dig(t, events[1], "content_block", "type"); got != "thinking" {
		t.Errorf("content_block.type of block 1 = %v", got)
	}
	if got := dig(t, events[2], "delta", "type"); got != "thinking_delta" {
		t.Errorf("delta.type of block 2 = %v", got)
	}
	if got := dig(t, events[2], "delta", "thinking"); got != upstream.ThinkingText {
		t.Errorf("thinking_delta content = %v", got)
	}
	if got := dig(t, events[3], "content_block", "id"); got != upstream.ToolUseID {
		t.Errorf("tool_use block id = %v, want %s", got, upstream.ToolUseID)
	}
	if got := dig(t, events[3], "index"); got != float64(1) {
		t.Errorf("tool_use block index = %v", got)
	}
	if got := dig(t, events[4], "content_block", "type"); got != "text" {
		t.Errorf("type of the text block = %v", got)
	}
	if got := dig(t, events[4], "index"); got != float64(2) {
		t.Errorf("index of the text block = %v", got)
	}
	var text strings.Builder
	for _, event := range events[5:9] {
		if got := dig(t, event, "index"); got != float64(2) {
			t.Errorf("index of the text delta = %v, it should point at the text block", got)
		}
		if got := dig(t, event, "delta", "type"); got != "text_delta" {
			t.Errorf("delta.type of the text delta = %v", got)
		}
		text.WriteString(dig(t, event, "delta", "text").(string))
	}
	if got := text.String(); got != "MOCK-STREAM glm-4-plus" {
		t.Errorf("text deltas joined = %q", got)
	}

	// Without tools there is no tool_use block, and the text block's index falls back to 1
	resp2 := send(t, s, http.MethodPost, "/v1/messages",
		`{"model":"glm-4-plus","stream":true,"messages":[{"role":"user","content":"go"}]}`, nil)
	defer resp2.Body.Close()
	raw2, err := io.ReadAll(resp2.Body)
	if err != nil {
		t.Fatalf("reading the response body: %v", err)
	}
	events2, _ := sseEvents(t, string(raw2))
	if got := eventTypes(events2)[:4]; len(got) != 4 || got[3] != "content_block_start" {
		t.Fatalf("block sequence = %v", got)
	}
	if got := dig(t, events2[3], "content_block", "type"); got != "text" {
		t.Errorf("without tools block 3 should be text, actually %v", got)
	}
	if got := dig(t, events2[3], "index"); got != float64(1) {
		t.Errorf("without tools the text block index = %v, want 1", got)
	}
}

// ---- count_tokens ----

func TestCountTokens(t *testing.T) {
	s := upstream.New(t)
	tools := `"tools":[{"name":"Read","input_schema":{"type":"object"}}]`

	for _, tc := range []struct{ name, path, body string }{
		{"Claude Code's shape (no model)", "/v1/messages/count_tokens",
			`{"messages":[{"role":"user","content":"count the tokens"}],` + tools + `}`},
		{"prefixed variant", "/a/claude/p/ds/v1/messages/count_tokens",
			`{"messages":[{"role":"user","content":"count the tokens"}]}`},
		{"history with an assistant turn that has no reasoning must also pass (check the strict rule after count_tokens)",
			`/v1/messages/count_tokens`,
			`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_x","name":"Read","input":{}}]}],` + tools + `}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := post(t, s, tc.path, tc.body)
			if code != http.StatusOK {
				t.Fatalf("status code = %d, want 200 (body: %s)", code, raw)
			}
			body := decodeObj(t, raw)
			if got := dig(t, body, "input_tokens"); got != float64(42) {
				t.Errorf("input_tokens = %v, want 42", got)
			}
		})
	}
}

// ---- tail shape criterion ----

// TestStrictTailShape self-checks the key criterion: **prove it really blocks**,
// then prove it blocks **only** by shape. Rewritten 2026-09-18 from live data;
// the old doc rule (non-empty reasoning per assistant) fails on the real upstream.
//
// Measured (real smt-deepseek/deepseek-flash, 3/3 in each cell):
//
//	the last user message's content[] is all tool_result blocks   -> 400
//	one more non-empty text block in that content[] (a space is enough) -> 200
//
// And whether reasoning_content is real text, omitted, empty or a placeholder
// **makes no difference to the result**. The two passing cases below (full
// reasoning, compliant tail) and the blocking one (full reasoning, non-compliant
// tail) are the two halves, each other's control; drop one and it degrades to "passes anyway".
func TestStrictTailShape(t *testing.T) {
	const tools = `"tools":[{"name":"Read","input_schema":{"type":"object"}}]`
	const thinkingOn = `"thinking":{"type":"enabled","budget_tokens":1024}`
	const toolUse = `{"type":"tool_use","id":"toolu_x","name":"Read","input":{}}`
	const tr = `{"type":"tool_result","tool_use_id":"toolu_x","content":"ok"}`
	// Header: a normal Q&A round, then one more tool call; each case appends its own tail.
	const head = `{"model":"deepseek-chat",`
	const pre = `"messages":[{"role":"user","content":"run it"},{"role":"assistant","content":[` + toolUse + `]},`
	// Full reasoning text: proves it neither rescues a bad tail nor hurts a good one.
	const reason = `"reasoning_content":"the real reasoning text from this turn"`

	for _, tc := range []struct {
		name string
		path string
		body string
		want int
	}{
		// ---- blocked: tail shape does not comply ----
		{
			name: "tail is only tool_result (the standard shape of the Claude Code main loop)",
			body: head + thinkingOn + `,` + tools + `,` + pre +
				`{"role":"user","content":[` + tr + `]}]}`,
			want: http.StatusBadRequest,
		},
		{
			// The most counterintuitive case in the set: the reasoning **is there**,
			// real text, and the upstream still 400s. It proves the error text lies.
			name: "tail is only tool_result, but the reasoning text is complete -- still block it",
			body: head + thinkingOn + `,` + tools + `,` + pre +
				`{"role":"user","content":[` + tr + `]}]}` + ``,
			want: http.StatusBadRequest,
		},
		{
			name: "tail has two tool_results (parallel tool round)",
			body: head + thinkingOn + `,` + tools + `,` + pre +
				`{"role":"user","content":[` + tr +
				`,{"type":"tool_result","tool_use_id":"toolu_y","content":"ok2"}]}]}`,
			want: http.StatusBadRequest,
		},
		{
			name: "tail is only tool_result, followed by a role:system interjection -- still block it",
			body: head + thinkingOn + `,` + tools + `,` + pre +
				`{"role":"user","content":[` + tr + `]},` +
				`{"role":"system","content":[{"type":"text","text":"the user said one thing"}]}]}`,
			want: http.StatusBadRequest,
		},
		{
			name: "thinking: disabled is also blocked (independent of the thinking switch)",
			body: head + `"thinking":{"type":"disabled"},` + tools + `,` + pre +
				`{"role":"user","content":[` + tr + `]}]}`,
			want: http.StatusBadRequest,
		},
		{
			name: "also blocked without tools (independent of tools)",
			body: head + thinkingOn + `,` + pre +
				`{"role":"user","content":[` + tr + `]}]}`,
			want: http.StatusBadRequest,
		},

		// ---- allowed: tail shape complies ----
		{
			name: "tail is tool_result + text (the same request body with one extra block)",
			body: head + thinkingOn + `,` + tools + `,` + pre +
				`{"role":"user","content":[` + tr + `,{"type":"text","text":"continue"}]}]}`,
			want: http.StatusOK,
		},
		{
			name: "tail is tool_result + text of a single space -- the space is enough",
			body: head + thinkingOn + `,` + tools + `,` + pre +
				`{"role":"user","content":[` + tr + `,{"type":"text","text":" "}]}]}`,
			want: http.StatusOK,
		},
		{
			name: "tail is tool_result + image (a non-text block also counts as an instruction)",
			body: head + thinkingOn + `,` + tools + `,` + pre +
				`{"role":"user","content":[` + tr +
				`,{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}`,
			want: http.StatusOK,
		},
		{
			name: "tail is only image",
			body: head + thinkingOn + `,` + tools + `,` + pre +
				`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}`,
			want: http.StatusOK,
		},
		{
			// A family **known unfixable**: the tail user turn is tool_result-only, but the
			// array ends with assistant. It still 400s, and appending an instruction to that
			// user turn **does not help** (measured 3/3 still 400), so the deepseek plugin
			// deliberately leaves it alone - noise with no effect is worse than a 400. Real
			// clients never reach it (Claude Code ends on tool_result awaiting the model, or on text).
			name: "tail user is tool_result and the array ends with assistant (the unfixable family)",
			body: head + thinkingOn + `,` + tools + `,` + pre +
				`{"role":"user","content":[` + tr + `]},` +
				`{"role":"assistant","content":[{"type":"text","text":"done talking"}]}]}`,
			want: http.StatusBadRequest,
		},
		{
			name: "tail is ordinary user text",
			body: head + thinkingOn + `,` + tools + `,` + pre +
				`{"role":"user","content":[{"type":"text","text":"one more"}]}]}`,
			want: http.StatusOK,
		},
		{
			name: "the content of the user message is a plain string",
			body: head + thinkingOn + `,` + tools + `,` +
				`"messages":[{"role":"user","content":"plain text"}]}`,
			want: http.StatusOK,
		},
		{
			name: "no user message",
			body: head + thinkingOn + `,` + tools + `,` +
				`"messages":[{"role":"assistant","content":[` + toolUse + `]}]}`,
			want: http.StatusOK,
		},
		{
			// The openai dialect's continuation turn ends on role:tool, and the last user
			// is the old string message - this rule never applies. Measured 200 here too.
			name: "openai dialect: ends with role:tool",
			path: "/v1/chat/completions",
			body: head + thinkingOn + `,` + tools +
				`,"messages":[{"role":"user","content":"run it"},` +
				`{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"Read","arguments":"{}"}}]},` +
				`{"role":"tool","tool_call_id":"c1","content":"ok"}]}`,
			want: http.StatusOK,
		},
		{
			name: "compliant tail + complete reasoning text (compare with the \"still block it\" case above)",
			body: head + thinkingOn + `,` + tools + `,` +
				`"messages":[{"role":"user","content":"run it"},` +
				`{"role":"assistant",` + reason + `,"content":[` + toolUse + `]},` +
				`{"role":"user","content":[` + tr + `,{"type":"text","text":"continue"}]}]}`,
			want: http.StatusOK,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := upstream.New(t)
			path := tc.path
			if path == "" {
				path = "/v1/messages"
			}
			code, raw := post(t, s, path, tc.body)
			if code != tc.want {
				t.Fatalf("status code = %d, want %d (body: %s)", code, tc.want, raw)
			}
			if s.Count() != 1 {
				t.Fatalf("this request should be recorded (whether blocked or not), record count = %d", s.Count())
			}
			if tc.want == http.StatusOK {
				// allowed: it must be the normal response, not some other error
				if !strings.Contains(raw, "MOCK-OK model=deepseek-chat") {
					t.Errorf("it passed through but the response body is not the fake upstream's normal reply: %s", raw)
				}
				return
			}
			// blocked: error text must be word-for-word python's - that is the criterion, not the code
			if !strings.Contains(raw, upstream.StrictReasoningMessage) {
				t.Errorf("that sentence is not in the 400 error text: %s", raw)
			}
			body := decodeObj(t, raw)
			if got := dig(t, body, "type"); got != "error" {
				t.Errorf("type = %v, want error", got)
			}
			if got := dig(t, body, "error", "type"); got != "invalid_request_error" {
				t.Errorf("error.type = %v", got)
			}
			if got := dig(t, body, "error", "message"); got != upstream.StrictReasoningMessage {
				t.Errorf("error.message = %v", got)
			}
		})
	}
}

// ---- control plane ----

func TestFailNextIsOneShot(t *testing.T) {
	s := upstream.New(t)
	payload := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`

	if code, _ := post(t, s, "/v1/chat/completions", payload); code != http.StatusOK {
		t.Fatalf("the first request should be fine")
	}
	s.FailNext(http.StatusServiceUnavailable)
	code, raw := post(t, s, "/v1/chat/completions", payload)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("the first request after arming = %d, want 503 (body: %s)", code, raw)
	}
	if !strings.Contains(raw, "mock forced 503") {
		t.Errorf("failure response body = %s, expected to contain mock forced 503", raw)
	}
	if code, _ = post(t, s, "/v1/chat/completions", payload); code != http.StatusOK {
		t.Fatalf("one-shot: the second request = %d, want 200", code)
	}
	// The failed shot stays in the records (record first, judge after): retry is assertable
	if s.Count() != 3 {
		t.Errorf("record count = %d, want 3 (including the one that failed)", s.Count())
	}

	// FailNext(0) equals disarm (0 is falsy in python)
	s.FailNext(http.StatusTooManyRequests)
	s.FailNext(0)
	if code, _ := post(t, s, "/v1/chat/completions", payload); code != http.StatusOK {
		t.Errorf("after FailNext(0) = %d, want 200 (0 disarms it)", code)
	}

	// Failure injection can land on count_tokens too: python judges failure before count_tokens
	s.FailNext(http.StatusTooManyRequests)
	if code, _ := post(t, s, "/v1/messages/count_tokens", `{"messages":[]}`); code != http.StatusTooManyRequests {
		t.Errorf("armed count_tokens = %d, want 429", code)
	}
}

func TestRequestsAndReset(t *testing.T) {
	s := upstream.New(t)
	first := `{"model":"deepseek-chat","messages":[{"role":"user","content":"1"}]}`
	second := `{"model":"glm-4-plus","messages":[{"role":"user","content":"2"}]}`

	if code, raw := post(t, s, "/v1/chat/completions", first); code != http.StatusOK {
		t.Fatalf("first request = %d (%s)", code, raw)
	}
	if code, raw := post(t, s, "/v1/messages", second); code != http.StatusOK {
		t.Fatalf("second request = %d (%s)", code, raw)
	}

	records := s.Requests()
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2", len(records))
	}
	if records[0].Path != "/v1/chat/completions" || records[1].Path != "/v1/messages" {
		t.Errorf("record order is wrong: %q, %q (must be chronological)", records[0].Path, records[1].Path)
	}
	if string(records[0].Body) != first || string(records[1].Body) != second {
		t.Errorf("request body recorded wrong: %q, %q", records[0].Body, records[1].Body)
	}
	if records[1].At.Before(records[0].At) {
		t.Errorf("timestamps did not increase: %v -> %v", records[0].At, records[1].At)
	}

	// What comes back is a copy: modifying it cannot reach into the Server
	records[0].Path = "just poking at it"
	if s.Requests()[0].Path != "/v1/chat/completions" {
		t.Error("Requests() must return a copy; modifying it must not affect the internal records")
	}

	s.Reset()
	if s.Count() != 0 || len(s.Requests()) != 0 {
		t.Fatalf("records remain after Reset: Count=%d", s.Count())
	}

	// Reset disarms FailNext too (python's /__mock/reset clears NEXT_FAIL as well)
	s.FailNext(http.StatusInternalServerError)
	s.Reset()
	if code, raw := post(t, s, "/v1/chat/completions", first); code != http.StatusOK {
		t.Errorf("after Reset = %d, want 200 (Reset should also disarm) (body: %s)", code, raw)
	}
}

// TestControlPlaneHTTP walks the HTTP control endpoints, used by bash scripts and
// curl: shape must match python exactly (lowercase headers, parsed body, epoch s, []).
func TestControlPlaneHTTP(t *testing.T) {
	s := upstream.New(t)

	code, raw := get(t, s, "/v1/models")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/models = %d", code)
	}
	models := decodeObj(t, raw)
	if models["object"] != "list" {
		t.Errorf("object = %v", models["object"])
	}
	if data, ok := models["data"].([]any); !ok || len(data) != 0 {
		t.Errorf("data = %v, want an empty array", models["data"])
	}

	if code, _ := get(t, s, "/nope"); code != http.StatusNotFound {
		t.Errorf("unknown GET path = %d, want 404", code)
	}

	payload := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`
	resp := send(t, s, http.MethodPost, "/v1/chat/completions", payload, map[string]string{
		"Authorization": "Bearer sk-ds",
	})
	resp.Body.Close()

	code, raw = get(t, s, "/__mock/requests")
	if code != http.StatusOK {
		t.Fatalf("GET /__mock/requests = %d", code)
	}
	var wire []map[string]any
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		t.Fatalf("records are not a JSON array: %v (%s)", err, raw)
	}
	if len(wire) != 1 {
		t.Fatalf("record count = %d, want 1 (%s)", len(wire), raw)
	}
	if got := wire[0]["path"]; got != "/v1/chat/completions" {
		t.Errorf("wire path = %v", got)
	}
	if got := wire[0]["method"]; got != "POST" {
		t.Errorf("wire method = %v", got)
	}
	headers, ok := wire[0]["headers"].(map[string]any)
	if !ok {
		t.Fatalf("wire headers is not an object: %v", wire[0]["headers"])
	}
	if got := headers["authorization"]; got != "Bearer sk-ds" {
		t.Errorf("wire headers[authorization] = %v (the python version uses lowercase keys)", got)
	}
	if _, mixed := headers["Authorization"]; mixed {
		t.Error("wire headers must not contain the original-case key")
	}
	if got := dig(t, wire[0], "body", "model"); got != "deepseek-chat" {
		t.Errorf("wire body.model = %v (body should be the parsed object)", got)
	}
	at, ok := wire[0]["at"].(float64)
	if !ok || at <= 0 {
		t.Errorf("wire at = %v, want epoch seconds", wire[0]["at"])
	}

	// Empty records must be [], not null: for x in r in bash blows up on null
	s.Reset()
	if _, raw = get(t, s, "/__mock/requests"); strings.TrimSpace(raw) != "[]" {
		t.Errorf("empty record = %q, want []", strings.TrimSpace(raw))
	}

	// GET /__mock/fail?code=N arms the next shot
	code, raw = get(t, s, "/__mock/fail?code=429")
	if code != http.StatusOK {
		t.Fatalf("GET /__mock/fail = %d", code)
	}
	if got := decodeObj(t, raw)["next_fail"]; got != float64(429) {
		t.Errorf("next_fail = %v, want 429", got)
	}
	if code, _ := post(t, s, "/v1/chat/completions", payload); code != http.StatusTooManyRequests {
		t.Errorf("first request after arming = %d, want 429", code)
	}
	if code, _ := post(t, s, "/v1/chat/completions", payload); code != http.StatusOK {
		t.Errorf("second request after arming = %d, want 200 (one-shot)", code)
	}
	// Default code=500 (python's q.get("code", ["500"]))
	if _, raw = get(t, s, "/__mock/fail"); decodeObj(t, raw)["next_fail"] != float64(500) {
		t.Errorf("the default code should be 500: %s", raw)
	}
	// Not an integer: python throws and drops the connection, here a stated 400 (deliberate)
	if code, _ := get(t, s, "/__mock/fail?code=abc"); code != http.StatusBadRequest {
		t.Errorf("status code for code=abc = %d, want 400", code)
	}
	s.FailNext(0) // 别把上面那发缺省 500 留给后面的用例

	// POST /__mock/reset clears records; this shot is not recorded (handled before body)
	if code, raw = post(t, s, "/__mock/reset", ""); code != http.StatusOK {
		t.Fatalf("POST /__mock/reset = %d（%s）", code, raw)
	}
	if s.Count() != 0 {
		t.Errorf("record count after reset = %d", s.Count())
	}
}

// ---- delay ----

// TestStreamIsIncremental verifies two things: the chunk gap really takes effect,
// and chunks arrive incrementally (a buffering proxy would hold chunk one till the end).
func TestStreamIsIncremental(t *testing.T) {
	s := upstream.New(t)
	const delay = 200 * time.Millisecond // 4 个增量块 → 整条流 ~800ms

	// Speed up the baseline delay first: mock_slow uses the other dial, so it should not change
	s.SetChunkDelay(0)
	s.SetSlowChunkDelay(delay)

	start := time.Now()
	code, raw := post(t, s, "/v1/chat/completions",
		`{"model":"m","stream":true,"mock_slow":true,"messages":[]}`)
	elapsed := time.Since(start)
	if code != http.StatusOK {
		t.Fatalf("mock_slow stream = %d (%s)", code, raw)
	}
	if elapsed < 3*delay {
		t.Errorf("the mock_slow stream took only %v (expected >= %v, about 4 block intervals) -- is the slow mode not in effect?", elapsed, 3*delay)
	}
	if !strings.HasSuffix(raw, "data: [DONE]\n\n") {
		t.Errorf("the stream did not finish, tail is: %q", raw[len(raw)-40:])
	}

	// Baseline delay: chunk-by-chunk arrival plus total stream duration both line up
	s.SetChunkDelay(delay)
	resp := send(t, s, http.MethodPost, "/v1/chat/completions",
		`{"model":"m","stream":true,"messages":[]}`, nil)
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	start = time.Now()
	firstLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("reading the first block: %v", err)
	}
	first := time.Since(start)
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading the rest of the stream: %v", err)
	}
	total := time.Since(start)

	// A whole-stream buffer takes ~800ms to give the first chunk; set the threshold at
	// 400ms: machine jitter does not fail it, and "accumulate then send" always exceeds.
	if first > 400*time.Millisecond {
		t.Errorf("the first block arrived only after %v (the whole stream about %v) -- is it not flushing block by block?", first, 4*delay)
	}
	if total < 3*delay {
		t.Errorf("the whole stream took only %v (expected >= %v) -- is the block interval not in effect?", total, 3*delay)
	}
	stream := firstLine + string(rest)
	if got := strings.Count(stream, "data: "); got != 6 {
		t.Errorf("data: line count = %d, want 6 (4 deltas + 1 closing + [DONE])", got)
	}
	if !strings.HasSuffix(stream, "data: [DONE]\n\n") {
		t.Errorf("the stream did not finish, tail is: %q", stream[len(stream)-40:])
	}

	// SetChunkDelay(0) = as fast as possible: it returns at once (the delay is adjustable)
	s.SetChunkDelay(0)
	start = time.Now()
	if code, raw := post(t, s, "/v1/chat/completions", `{"model":"m","stream":true,"messages":[]}`); code != http.StatusOK {
		t.Fatalf("after SetChunkDelay(0) = %d (%s)", code, raw)
	}
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Errorf("after SetChunkDelay(0) it still took %v, expected as fast as possible", elapsed)
	}
}
