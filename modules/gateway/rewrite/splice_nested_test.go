package rewrite

import (
	"encoding/json"
	"strings"
	"testing"
)

// The insertion point must be at the **start** of the sub-array, and apart from the inserted
// span every other byte stays as is.
func TestEnsureArrayItemArrayHead(t *testing.T) {
	body := []byte("{\n  \"messages\": [\n" +
		"    {\"role\": \"user\", \"content\": [{\"type\":\"text\",\"text\":\"a\"}]},\n" +
		"    {\"role\": \"assistant\", \"content\": [{\"type\":\"text\",\"text\":\"b\"}]}\n" +
		"  ],\n  \"max_tokens\": 1e10\n}")

	isAsst := func(item []byte) bool { r, _ := TopLevelString(item, "role"); return r == "assistant" }

	out, n, err := EnsureArrayItemArrayHead(body, "messages", "content",
		[]byte(`{"type":"thinking","thinking":""}`), isAsst, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 1 {
		t.Fatalf("inserted %d entries, want 1", n)
	}
	if !json.Valid(out) {
		t.Fatalf("the result is not valid JSON:\n%s", out)
	}
	if !strings.Contains(string(out),
		`"content": [{"type":"thinking","thinking":""},{"type":"text","text":"b"}]`) {
		t.Fatalf("not inserted at the head:\n%s", out)
	}
	// The user entry and the rest of the top level stay put: indentation, key order and the
	// 1e10 literal are all still there
	if !strings.Contains(string(out), `{"role": "user", "content": [{"type":"text","text":"a"}]}`) {
		t.Fatalf("the user message was touched:\n%s", out)
	}
	if !strings.Contains(string(out), `"max_tokens": 1e10`) {
		t.Fatalf("a numeric literal was rewritten:\n%s", out)
	}
}

// An empty array needs no comma; anything need returns false for is left alone and the
// original bytes come back.
func TestEnsureArrayItemArrayHeadEdges(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":[]}]}`)
	out, n, err := EnsureArrayItemArrayHead(body, "messages", "content",
		[]byte(`{"type":"thinking"}`), nil, nil)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if string(out) != `{"messages":[{"role":"assistant","content":[{"type":"thinking"}]}]}` {
		t.Fatalf("the empty array was inserted wrong:\n%s", out)
	}

	never := func([]byte) bool { return false }
	out, n, err = EnsureArrayItemArrayHead(body, "messages", "content",
		[]byte(`{"type":"thinking"}`), nil, never)
	if err != nil || n != 0 || string(out) != string(body) {
		t.Fatalf("with need=false it must not touch anything: n=%d err=%v\n%s", n, err, out)
	}
}

func TestArrayItems(t *testing.T) {
	items, ok := ArrayItems([]byte(`[ {"a":[1,2]} , "s" , 3 ]`))
	if !ok {
		t.Fatal("cannot scan")
	}
	want := []string{`{"a":[1,2]}`, `"s"`, `3`}
	if len(items) != len(want) {
		t.Fatalf("split into %d, want %d", len(items), len(want))
	}
	for i, w := range want {
		if string(items[i]) != w {
			t.Fatalf("entry %d = %s, want %s", i, items[i], w)
		}
	}
	if _, ok := ArrayItems([]byte(`{"not":"array"}`)); ok {
		t.Fatal("object was treated as an array")
	}
}

func TestAppendLastArrayItemArray(t *testing.T) {
	body := []byte("{\n  \"messages\":[" +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1"}]}` +
		`],"max_tokens":9007199254740993}` + "\n")
	isUser := func(item []byte) bool {
		role, _ := TopLevelString(item, "role")
		return role == "user"
	}
	block := []byte(`{"type":"text","text":"continue"}`)
	out, changed, err := AppendLastArrayItemArray(body, "messages", "content", block, isUser)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !json.Valid(out) {
		t.Fatalf("the result is not valid JSON:\n%s", out)
	}
	if !strings.Contains(string(out),
		`{"type":"tool_result","tool_use_id":"t1"},{"type":"text","text":"continue"}`) {
		t.Fatalf("not appended to the content of the last message:\n%s", out)
	}
	if !strings.Contains(string(out), `"max_tokens":9007199254740993`) {
		t.Fatalf("an unrelated big integer was rewritten:\n%s", out)
	}
}

// AppendArrayItemArrayAt picks the element by index. Why it exists: see the function note -
// the message to append to is **not** the array's last entry (Claude Code appends a
// role:"system" remark after a tool_result; field dump dump/err-400-req000464).
func TestAppendArrayItemArrayAt(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1"}]},` +
		`{"role":"system","content":[{"type":"text","text":"aside"}]}` +
		`],"max_tokens":9007199254740993}`)
	block := []byte(`{"type":"text","text":"continue"}`)

	out, changed, err := AppendArrayItemArrayAt(body, "messages", "content", 0, block)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !json.Valid(out) {
		t.Fatalf("the result is not valid JSON:\n%s", out)
	}
	// Appended at the tail of entry 0's (the user one) content; system is still after it.
	if !strings.Contains(string(out),
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1"},{"type":"text","text":"continue"}]}`) {
		t.Fatalf("not appended to the content of item 0:\n%s", out)
	}
	if !strings.Contains(string(out), `{"role":"system","content":[{"type":"text","text":"aside"}]}`) {
		t.Fatalf("the system message after it was touched:\n%s", out)
	}
	if !strings.Contains(string(out), `"max_tokens":9007199254740993`) {
		t.Fatalf("an unrelated big integer was rewritten:\n%s", out)
	}

	// Index out of range, element not an object: do nothing and **return the original
	// bytes** (not a copy).
	for _, tc := range []struct {
		name string
		idx  int
	}{
		{"out of range (positive)", 2}, {"out of range (negative)", -1},
	} {
		out, changed, err := AppendArrayItemArrayAt(body, "messages", "content", tc.idx, block)
		if err != nil || changed || string(out) != string(body) {
			t.Fatalf("%s：changed=%v err=%v\n%s", tc.name, changed, err, out)
		}
	}

	// Skip when the element is not an object (scalars mixed into the top-level array):
	// no panic, no byte changed.
	scalar := []byte(`{"messages":[1,{"role":"user","content":[]}]}`)
	out, changed, err = AppendArrayItemArrayAt(scalar, "messages", "content", 0, block)
	if err != nil || changed || string(out) != string(scalar) {
		t.Fatalf("scalar element: changed=%v err=%v\n%s", changed, err, out)
	}

	// content is a string (no sub-array to append to): report errNotArray as "shape
	// unrecognized".
	strContent := []byte(`{"messages":[{"role":"user","content":"plain text"}]}`)
	if _, _, err := AppendArrayItemArrayAt(strContent, "messages", "content", 0, block); err == nil {
		t.Fatal("a string content should raise errNotArray")
	}

	// Empty content array: no leading comma.
	empty := []byte(`{"messages":[{"role":"user","content":[]}]}`)
	out, changed, err = AppendArrayItemArrayAt(empty, "messages", "content", 0, block)
	if err != nil || !changed {
		t.Fatalf("empty array: changed=%v err=%v", changed, err)
	}
	if string(out) != `{"messages":[{"role":"user","content":[{"type":"text","text":"continue"}]}]}` {
		t.Fatalf("the empty-array append is wrong:\n%s", out)
	}
}
