package rewrite

import (
	"encoding/json"
	"strings"
	"testing"
)

const probeUserItem = `{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}`

// Non-empty array: insert before the closing ], keeping every other byte as is
// (indentation, key order, number literals, and the existing item itself).
func TestAppendTopLevelArrayItemNonEmpty(t *testing.T) {
	body := []byte("{\n  \"model\": \"deepseek-flash\",\n  \"input\": [\n" +
		"    {\"type\": \"function_call_output\", \"call_id\": \"call_faedac\", \"output\": \"x\"}\n" +
		"  ],\n  \"max_output_tokens\": 1e10\n}")

	out, changed, err := AppendTopLevelArrayItem(body, "input", []byte(probeUserItem))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !changed {
		t.Fatalf("the place to insert was not touched")
	}
	if !json.Valid(out) {
		t.Fatalf("the result is not valid JSON:\n%s", out)
	}
	var parsed struct {
		Model string            `json:"model"`
		Input []json.RawMessage `json:"input"`
		Max   json.RawMessage   `json:"max_output_tokens"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("parse failed: %v\n%s", err, out)
	}
	if len(parsed.Input) != 2 {
		t.Fatalf("input should have 2 items, got %d:\n%s", len(parsed.Input), out)
	}
	if string(parsed.Input[1]) != probeUserItem {
		t.Fatalf("what was added is not that item:\n%s", parsed.Input[1])
	}
	// Preserved verbatim: the existing item, indentation, the 1e10 literal, top-level key
	// order
	if !strings.Contains(string(out), `{"type": "function_call_output", "call_id": "call_faedac", "output": "x"}`) {
		t.Fatalf("the existing items were touched:\n%s", out)
	}
	if !strings.Contains(string(out), "\n    {") && !strings.Contains(string(out), "    {") {
		t.Fatalf("the original indentation was lost:\n%s", out)
	}
	if !strings.Contains(string(out), `"max_output_tokens": 1e10`) {
		t.Fatalf("a numeric literal was rewritten:\n%s", out)
	}
	if strings.Index(string(out), `"model"`) > strings.Index(string(out), `"input"`) {
		t.Fatalf("the top-level key order changed:\n%s", out)
	}
}

// Neither an empty array nor a whitespace-only array gets a comma (it would yield `[,x]`,
// not valid JSON); a non-array, a missing key or a non-object is **left alone** and reported
// truthfully, for the caller to handle fail-open.
func TestAppendTopLevelArrayItemEdges(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantErr  bool
		wantMove bool
	}{
		{"empty array", `{"input":[]}`, false, true},
		{"whitespace only", `{"input":[  ]}`, false, true},
		{"not an array", `{"input":{}}`, true, false},
		{"missing this key", `{"input2":[]}`, true, false},
		{"top level is not an object", `[]`, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, changed, err := AppendTopLevelArrayItem([]byte(tc.body), "input", []byte(probeUserItem))
			if tc.wantErr && err == nil {
				t.Fatalf("an error was expected but none was raised")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("should not error: %v", err)
			}
			if changed != tc.wantMove {
				t.Fatalf("changed=%v, want %v", changed, tc.wantMove)
			}
			if !changed {
				if string(out) != tc.body {
					t.Fatalf("when nothing changes the input must come back as is:\n%s", out)
				}
				return
			}
			if !json.Valid(out) {
				t.Fatalf("the result is not valid JSON:\n%s", out)
			}
			var parsed struct {
				Input []json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(out, &parsed); err != nil {
				t.Fatalf("parse failed: %v\n%s", err, out)
			}
			if len(parsed.Input) != 1 || string(parsed.Input[0]) != probeUserItem {
				t.Fatalf("an empty array should get only the new item:\n%s", out)
			}
		})
	}
}
