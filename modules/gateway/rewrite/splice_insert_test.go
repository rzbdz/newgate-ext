package rewrite

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestInsertTopLevelRaw_KeepsEveryOtherByte: inserting a new key may only add the inserted
// span; the other bytes (indentation, key order, big integer literals, Unicode escapes) must
// stay as they were.
func TestInsertTopLevelRaw_KeepsEveryOtherByte(t *testing.T) {
	body := []byte(`{
  "model": "deepseek-chat",
  "big": 9007199254740993,
  "esc": "hello",
  "messages": [{"role":"user","content":"hi"}]
}`)
	out, err := InsertTopLevelRaw(body, "thinking", []byte(`{"type":"disabled"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"thinking":{"type":"disabled"},
  "model": "deepseek-chat",
  "big": 9007199254740993,
  "esc": "hello",
  "messages": [{"role":"user","content":"hi"}]
}`
	if string(out) != want {
		t.Fatalf("bytes differ\n got: %s\nwant: %s", out, want)
	}
	var v map[string]interface{}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("the result is not valid JSON: %v", err)
	}
}

func TestInsertTopLevelRaw_EmptyObjectNeedsNoComma(t *testing.T) {
	out, err := InsertTopLevelRaw([]byte(`{}`), "thinking", []byte(`{"type":"disabled"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"thinking":{"type":"disabled"}}` {
		t.Fatalf("got %s", out)
	}
}

func TestInsertTopLevelRaw_RefusesDuplicate(t *testing.T) {
	if _, err := InsertTopLevelRaw([]byte(`{"thinking":{"type":"enabled"}}`),
		"thinking", []byte(`{"type":"disabled"}`)); err == nil {
		t.Fatal("an existing key of the same name must error, never produce a duplicate key")
	}
}

func TestInsertTopLevelRaw_NotObject(t *testing.T) {
	if _, err := InsertTopLevelRaw([]byte(`[1,2]`), "a", []byte(`1`)); err == nil {
		t.Fatal("a non-object top level should error")
	}
}

// TestEnsureArrayItemField_OnlyMatchingItems adds only to elements that match and lack the
// field.
func TestEnsureArrayItemField_OnlyMatchingItems(t *testing.T) {
	body := []byte(`{"model":"x","messages":[` +
		`{"role":"user","content":"a"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1"}]},` +
		`{"role":"user","content":"b"},` +
		`{"role":"assistant","reasoning_content":"already has it","content":"c"},` +
		`{"role":"assistant","content":"d"}` +
		`]}`)

	out, n, err := EnsureArrayItemField(body, "messages", "reasoning_content",
		[]byte(`""`), func(item []byte) bool {
			r, _ := TopLevelString(item, "role")
			return r == "assistant"
		})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("should add it to 2 entries (the 3rd already has the field), got %d", n)
	}
	want := `{"model":"x","messages":[` +
		`{"role":"user","content":"a"},` +
		`{"reasoning_content":"",` + `"role":"assistant","content":[{"type":"tool_use","id":"t1"}]},` +
		`{"role":"user","content":"b"},` +
		`{"role":"assistant","reasoning_content":"already has it","content":"c"},` +
		`{"reasoning_content":"",` + `"role":"assistant","content":"d"}` +
		`]}`
	if string(out) != want {
		t.Fatalf("bytes differ\n got: %s\nwant: %s", out, want)
	}
	var v struct {
		Messages []map[string]interface{} `json:"messages"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("the result is not valid JSON: %v", err)
	}
	for i, m := range v.Messages {
		if m["role"] != "assistant" {
			if _, has := m["reasoning_content"]; has {
				t.Fatalf("entry %d is not an assistant but was given the field", i)
			}
			continue
		}
		if m["reasoning_content"] == nil {
			t.Fatalf("assistant entry %d has no reasoning_content", i)
		}
	}
	if v.Messages[3]["reasoning_content"] != "already has it" {
		t.Fatal("the existing reasoning_content was overwritten")
	}
}

// TestEnsureArrayItemField_NoopKeepsBytes: with nothing to add it must return the original
// slice and n=0.
func TestEnsureArrayItemField_NoopKeepsBytes(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"a"}]}`)
	out, n, err := EnsureArrayItemField(body, "messages", "reasoning_content",
		[]byte(`""`), func(item []byte) bool {
			r, _ := TopLevelString(item, "role")
			return r == "assistant"
		})
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if string(out) != string(body) {
		t.Fatalf("no change should mean no byte change: %s", out)
	}
}

// TestEnsureArrayItemField_NestedRoleNotConfused: a "role" nested inside content must not be
// taken for the message's role, and a nested object must not be taken for an array element.
func TestEnsureArrayItemField_NestedRoleNotConfused(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"content":[{"role":"assistant","text":"text inside the content"}],"role":"user"}` +
		`]}`)
	out, n, err := EnsureArrayItemField(body, "messages", "reasoning_content",
		[]byte(`""`), func(item []byte) bool {
			r, _ := TopLevelString(item, "role")
			return r == "assistant"
		})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("this is a user entry and must not get the field, but %d places were patched: %s", n, out)
	}
}

// TestEnsureArrayItemField_WhitespaceAndEmptyItems: indentation, empty object elements,
// non-object elements.
func TestEnsureArrayItemField_WhitespaceAndEmptyItems(t *testing.T) {
	body := []byte("{\n  \"messages\": [\n    {},\n    \"bare string\",\n    { \"role\": \"assistant\" }\n  ]\n}")
	out, n, err := EnsureArrayItemField(body, "messages", "reasoning_content",
		[]byte(`""`), func(item []byte) bool {
			r, _ := TopLevelString(item, "role")
			return r == "assistant"
		})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("there is only one assistant, got %d: %s", n, out)
	}
	if !strings.Contains(string(out), `{"reasoning_content":"" "role": "assistant" }`) &&
		!strings.Contains(string(out), `{"reasoning_content":"", "role": "assistant" }`) {
		t.Fatalf("wrong insertion position: %s", out)
	}
	var v map[string]interface{}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("the result is not valid JSON: %v\n%s", err, out)
	}
}

func TestEnsureArrayItemField_MissingKey(t *testing.T) {
	body := []byte(`{"model":"x"}`)
	out, n, err := EnsureArrayItemField(body, "messages", "reasoning_content",
		[]byte(`""`), nil)
	if err == nil {
		t.Fatal("missing messages should error, and the caller decides whether that matters")
	}
	if n != 0 || string(out) != string(body) {
		t.Fatal("on error the body must not change")
	}
}

func TestEnsureArrayItemField_NotArray(t *testing.T) {
	if _, _, err := EnsureArrayItemField([]byte(`{"messages":"nope"}`),
		"messages", "x", []byte(`""`), nil); err == nil {
		t.Fatal("a non-array messages should error")
	}
}
