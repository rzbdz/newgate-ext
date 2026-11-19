package rewrite

import (
	"encoding/json"
	"strings"
	"testing"
)

func isAsst(t *testing.T) func([]byte) bool {
	t.Helper()
	return func(item []byte) bool { r, _ := TopLevelString(item, "role"); return r == "assistant" }
}

// The block being dropped is the 2026-11-19 shape verbatim: whitespace text, empty signature.
// Apart from that one span, every byte stays - including the other blocks' order and the
// indentation the client sent.
func TestDropArrayItemBlocks(t *testing.T) {
	body := []byte("{\n  \"messages\": [\n" +
		"    {\"role\": \"user\", \"content\": [{\"type\":\"text\",\"text\":\"a\"}]},\n" +
		"    {\"role\": \"assistant\", \"content\": [\n" +
		"      {\"type\": \"thinking\", \"thinking\": \" \", \"signature\": \"\"},\n" +
		"      {\"type\": \"tool_use\", \"id\": \"t1\", \"name\": \"Bash\", \"input\": {}}\n" +
		"    ]}\n" +
		"  ],\n  \"max_tokens\": 1e10\n}")

	out, n, err := DropArrayItemBlocks(body, "messages", "content", isAsst(t), func(b []byte) bool {
		typ, _ := TopLevelString(b, "type")
		return typ == "thinking"
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 1 {
		t.Fatalf("dropped %d blocks, want 1", n)
	}
	if !json.Valid(out) {
		t.Fatalf("the result is not valid JSON:\n%s", out)
	}
	if strings.Contains(string(out), `"thinking"`) {
		t.Fatalf("the block is still there:\n%s", out)
	}
	// The user message, the key order and a numeric literal survive byte for byte
	for _, want := range []string{
		`{"role": "user", "content": [{"type":"text","text":"a"}]}`,
		`"max_tokens": 1e10`,
	} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("%q was lost:\n%s", want, out)
		}
	}
	if !strings.Contains(string(out), `{"type": "tool_use", "id": "t1", "name": "Bash", "input": {}}`) {
		t.Fatalf("the surviving block was rewritten:\n%s", out)
	}
}

// A content[] whose every block would be dropped is left alone: content:[] is a shape most
// upstreams reject, so a repair that empties the array and one that breaks the request are the
// same thing.
func TestDropArrayItemBlocksNeverEmptiesTheArray(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":" "}]}]}`)
	out, n, err := DropArrayItemBlocks(body, "messages", "content", isAsst(t),
		func([]byte) bool { return true })
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if string(out) != string(body) {
		t.Fatalf("nothing may be touched:\n%s", out)
	}
}

// Several drops in one array keep valid JSON: exactly one comma between the survivors, and the
// trailing whitespace the client sent is preserved.
func TestDropArrayItemBlocksSeveral(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":[
  {"type":"thinking","thinking":" "},
  {"type":"text","text":"keep1"},
  {"type":"thinking","thinking":"\n\t"},
  {"type":"text","text":"keep2"},
  {"type":"thinking","thinking":"  "}
]}]}`)
	out, n, err := DropArrayItemBlocks(body, "messages", "content", isAsst(t),
		func(b []byte) bool { typ, _ := TopLevelString(b, "type"); return typ == "thinking" })
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if !json.Valid(out) {
		t.Fatalf("not valid JSON:\n%s", out)
	}
	// Read back: exactly the two text blocks, in order
	var parsed struct {
		Messages []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	got := parsed.Messages[0].Content
	if len(got) != 2 || got[0].Text != "keep1" || got[1].Text != "keep2" {
		t.Fatalf("wrong survivors: %+v\n%s", got, out)
	}
	if strings.Contains(string(out), ",,") || strings.Contains(string(out), "[,") {
		t.Fatalf("broken separators:\n%s", out)
	}
}

// Unrecognized shapes are skipped, not errors: no messages, not an array, content a string.
func TestDropArrayItemBlocksUnrecognized(t *testing.T) {
	for _, body := range []string{
		`{"model":"m"}`,
		`{"messages":"nope"}`,
		`{"messages":[{"role":"assistant","content":"plain string"}]}`,
		`{"messages":[1,2,3]}`,
		`not json at all`,
	} {
		out, n, err := DropArrayItemBlocks([]byte(body), "messages", "content", isAsst(t),
			func([]byte) bool { return true })
		if n != 0 || string(out) != body {
			t.Errorf("unrecognized body %q must come back untouched: n=%d out=%q err=%v",
				body, n, out, err)
		}
	}
}

// Only the matched elements are examined: a user message's blocks are never candidates, even
// when they look droppable.
func TestDropArrayItemBlocksRespectsMatch(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"thinking","thinking":" "}]}]}`)
	out, n, err := DropArrayItemBlocks(body, "messages", "content", isAsst(t),
		func([]byte) bool { return true })
	if err != nil || n != 0 || string(out) != string(body) {
		t.Fatalf("the user message must not be touched: n=%d err=%v\n%s", n, err, out)
	}
}
