package thinking

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/gateway/special"
)

// emptyDump is the shape of the 2026-11-19 field dump (err-400-req002560), trimmed to what
// matters: two assistant turns whose thinking block holds one space and an empty signature,
// one turn with real reasoning, and the untouched user turns.
const emptyDump = `{"model":"claude-opus-5-5","messages":[` +
	`{"role":"user","content":[{"type":"text","text":"run echo hi"}]},` +
	`{"role":"assistant","content":[{"type":"thinking","thinking":" ","signature":""},` +
	`{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"echo hi"}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"hi"}]},` +
	`{"role":"assistant","content":[{"type":"thinking","thinking":"I should run it.","signature":""},` +
	`{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"echo hi"}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"hi"}]}` +
	`],"max_tokens":64}`

func anthropicRequest() *special.Request {
	return &special.Request{Path: "/messages", Model: "claude-opus-5-5", Provider: "smt-claude"}
}

// The whitespace-only block is dropped and the real one is kept: 78 of the 85 thinking blocks
// in the field dump carry text with no signature, and they are fine - the criterion must not
// reach them.
func TestEmptyThinkingDropsOnlyWhitespaceBlocks(t *testing.T) {
	p := emptyThinking{}
	if !p.Match(anthropicRequest()) {
		t.Fatal("the Anthropic dialect must be claimed")
	}
	out, notes, err := p.Apply([]byte(emptyDump), anthropicRequest())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(notes) == 0 {
		t.Fatal("dropping a block must be reported (never silent)")
	}
	if !strings.Contains(notes[0], "1 empty thinking block") {
		t.Fatalf("the note must say how many were dropped: %q", notes[0])
	}
	if !json.Valid(out) {
		t.Fatalf("not valid JSON:\n%s", out)
	}
	if strings.Contains(string(out), `"thinking":" "`) || strings.Contains(string(out), `"thinking": " "`) {
		t.Fatalf("the whitespace block is still there:\n%s", out)
	}
	// The turn that really reasoned keeps its text **and its position** (first block).
	if !strings.Contains(string(out), `{"type":"thinking","thinking":"I should run it.","signature":""}`) {
		t.Fatalf("real reasoning was touched:\n%s", out)
	}
	// Both tool_use blocks survive: dropping a block must not drop its sibling.
	if strings.Count(string(out), `"type":"tool_use"`) != 2 {
		t.Fatalf("a tool_use block was lost:\n%s", out)
	}
}

// Nothing to drop: the body comes back as is and no note is emitted (the framework reads "no
// notes" as "nothing changed", which keeps pass-through byte for byte).
func TestEmptyThinkingNoopKeepsBytes(t *testing.T) {
	clean := `{"model":"m","messages":[{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"real","signature":""}]}]}`
	out, notes, err := emptyThinking{}.Apply([]byte(clean), anthropicRequest())
	if err != nil || len(notes) != 0 {
		t.Fatalf("err=%v notes=%v", err, notes)
	}
	if string(out) != clean {
		t.Fatalf("bytes changed:\n%s", out)
	}
}

// The OpenAI dialect has no thinking blocks to drop - not claimed at all.
func TestEmptyThinkingIgnoresOpenAIDialect(t *testing.T) {
	r := &special.Request{Path: "/chat/completions"}
	if (emptyThinking{}).Match(r) {
		t.Fatal("an OpenAI-dialect request must not be claimed")
	}
	if (emptyThinking{}).Match(nil) {
		t.Fatal("nil must not be claimed")
	}
}

// Dropping must never empty a content[]: a turn whose only block is the empty thinking one is
// left alone, and the upstream's own error is preferable to a shape we manufactured.
func TestEmptyThinkingLeavesEmptyContentAlone(t *testing.T) {
	body := `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":" "}]}]}`
	out, notes, err := emptyThinking{}.Apply([]byte(body), anthropicRequest())
	if err != nil || len(notes) != 0 || string(out) != body {
		t.Fatalf("err=%v notes=%v\n%s", err, notes, out)
	}
}
