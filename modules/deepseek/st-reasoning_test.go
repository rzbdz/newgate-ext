package deepseek

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	"github.com/rzbdz/newgate-ext/modules/gateway/thinkcache"
)

// claudeReq is a Claude Code-shaped context: /a/claude/ + /v1/messages.
//
// The deepseek tests in this package all assume "Claude Code calling a DeepSeek gateway"
// - that is what the plugin is for. opencode's path (empty Agent + /chat/completions)
// stands alone in TestDeepSeekOpencodeKeepsThinking.
func claudeReq(model string) *special.Request {
	return &special.Request{Model: model, Provider: "gw", BaseURL: "https://gw.example.com/v1",
		Protocol: "anthropic", Path: "/messages", Agent: "claude"}
}

// seedCache inserts "the upstream really gave this last round" reasoning text into thinkcache.
//
// Since 2026-09-18 the presence of the text is the whole line: fill when present, and
// touch not a byte when absent (a placeholder is wrong; see the st-reasoning.go header).
// So any test of "fill it back" must build the real source here first.
func seedCache(toolID, text string) {
	thinkcache.Default.Put([]string{thinkcache.ToolKey(toolID)}, []byte(text))
}

// With real text, the assistant's content[] must carry a thinking block first - the
// client (Claude Code) strips these for non-official endpoints, so only we can add them.
func TestDeepSeekBackfillsThinkingBlockWhenThinkingOn(t *testing.T) {
	seedCache("t1", "the real reasoning text from the previous turn")
	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled","budget_tokens":1024},` +
		`"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"hi"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"n":9007199254740993}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}` +
		`]}`)

	out, notes, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(notes) == 0 {
		t.Fatal("changed something but did not report notes -- violates \"never silent\"")
	}

	var got struct {
		Thinking json.RawMessage `json:"thinking"`
		Messages []struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("after the change it is not valid JSON: %v\n%s", err, out)
	}

	// The client explicitly asked for thinking mode; we must not quietly turn it off.
	if !strings.Contains(string(got.Thinking), "enabled") {
		t.Fatalf("the client's thinking was changed: %s", got.Thinking)
	}

	// The assistant's thinking block must come **first**: the protocol puts it before tool_use.
	asst := got.Messages[1]
	if len(asst.Content) != 2 {
		t.Fatalf("assistant content block count = %d, want 2", len(asst.Content))
	}
	if !strings.Contains(string(asst.Content[0]), `"type":"thinking"`) {
		t.Fatalf("the first block is not thinking: %s", asst.Content[0])
	}
	if !strings.Contains(string(asst.Content[1]), "tool_use") {
		t.Fatalf("the original tool_use block was lost: %s", asst.Content[1])
	}

	// Not one byte of the user message should be touched.
	if strings.Contains(string(got.Messages[0].Content[0]), "thinking") {
		t.Fatal("a thinking block was added to the user message")
	}

	// Big integers must stay verbatim: a JSON round trip turns them into ...92.
	if !strings.Contains(string(out), "9007199254740993") {
		t.Fatalf("a big integer got corrupted:\n%s", out)
	}
}

// Inserting thinking blocks with thinking mode off gets rejected upstream ("off yet blocks").
func TestDeepSeekNoThinkingBlockWhenDisabled(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"model":"deepseek-chat","thinking":{"type":"disabled"},"messages":[{"role":"assistant","content":[{"type":"text","text":"hi"}]}]}`),
	} {
		out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if strings.Contains(string(out), `"type":"thinking"`) {
			t.Fatalf("thinking is off yet a thinking block was added:\n%s", out)
		}
	}
}

// Messages that already carry reasoning are untouched - including redacted_thinking.
func TestDeepSeekLeavesExistingThinkingAlone(t *testing.T) {
	for _, blk := range []string{
		`{"type":"thinking","thinking":"I am thinking","signature":"abc"}`,
		`{"type":"redacted_thinking","data":"xxx"}`,
	} {
		body := []byte(`{"thinking":{"type":"enabled"},"messages":[` +
			`{"role":"assistant","content":[` + blk + `,{"type":"text","text":"hi"}]}]}`)
		out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if strings.Count(string(out), `"type":"thinking"`)+
			strings.Count(string(out), `"type":"redacted_thinking"`) != 1 {
			t.Fatalf("added another block to a message that already has reasoning text:\n%s", out)
		}
	}
}

// Plain-string content (allowed by Anthropic) has no block to insert; skip, not error.
func TestDeepSeekStringContentSkipped(t *testing.T) {
	body := []byte(`{"thinking":{"type":"enabled"},"messages":[{"role":"assistant","content":"hi"}]}`)
	out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(string(out), `"type":"thinking"`) {
		t.Fatalf("inserted a block into string content:\n%s", out)
	}
	if !json.Valid(out) {
		t.Fatalf("after the change it is not valid JSON:\n%s", out)
	}
}

// reasoning_effort and thinking:disabled are mutually exclusive; upstream answers
// "thinking options type cannot be disabled when reasoning_effort is set". With an
// effort set, do not turn thinking off - fill in blocks instead.
func TestDeepSeekRespectsReasoningEffort(t *testing.T) {
	thinkcache.Default.Put([]string{thinkcache.TextKey("hi")}, []byte("the real reasoning text from the previous turn"))
	body := []byte(`{"model":"deepseek-chat","reasoning_effort":"high",` +
		`"messages":[{"role":"assistant","content":[{"type":"text","text":"hi"}]}]}`)
	out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(string(out), `"type":"disabled"`) {
		t.Fatalf("reasoning_effort is set yet it still turned thinking off:\n%s", out)
	}
	if !strings.Contains(string(out), `"type":"thinking"`) {
		t.Fatalf("no thinking block was added:\n%s", out)
	}
	if strings.Contains(string(out), `"thinking":""`) {
		t.Fatalf("added an empty thinking block -- a strict upstream returns 400 anyway:\n%s", out)
	}
}

// No text means **not a byte is filled in** - the placeholder path was disproved by
// measurement (see the st-reasoning.go header). This pins: the field stays absent.
//
// Field: 3494 placeholders across the store; checking each tool id against "recorded
// this round's reasoning" - **not one hit**, so the upstream never gave us them. And
// measurement (real upstream, 3/3 per cell) shows reasoning_content presence has no
// effect on that 400: field omitted + clean tail -> 200. Pure invention.
func TestDeepSeekSkipsMessageWithoutReasoningSource(t *testing.T) {
	// The tail carries plain text: this tests the "fill reasoning" half; the tail-shape
	// half is covered by TestTailShape*. A bare tool_result tail would make pass 4 add
	// an instruction by design, and then it is unclear who changed what.
	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled","budget_tokens":1024},` +
		`"messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t-never-cached-0001","name":"Read","input":{"n":9007199254740993}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t-never-cached-0001","content":"ok"},{"type":"text","text":"keep going"}]}` +
		`]}`)

	out, notes, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Not a byte may move: no reasoning_content, no thinking block.
	if string(out) != string(body) {
		t.Fatalf("it changed the request without the original text (fabricating content):\nbefore %s\nafter %s", body, out)
	}
	if strings.Contains(string(out), "reasoning_content") {
		t.Fatalf("filled in reasoning_content out of thin air:\n%s", out)
	}
	if strings.Contains(string(out), `"type":"thinking"`) {
		t.Fatalf("inserted a thinking block out of thin air:\n%s", out)
	}
	// Big integers must stay verbatim: a JSON round trip turns them into ...92.
	if !strings.Contains(string(out), "9007199254740993") {
		t.Fatalf("a big integer got corrupted:\n%s", out)
	}
	// No changes means no notes - "never silent" governs **changes**, not pass-throughs.
	if len(notes) != 0 {
		t.Fatalf("reported notes without changing anything: %v", notes)
	}
}

// The client's own thinking block is the reasoning text: reasoning_content uses its
// text directly (no cache dependency), and no block is inserted twice.
func TestDeepSeekUsesClientThinkingBlock(t *testing.T) {
	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled"},` +
		`"messages":[{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"let me think","signature":"sig"},` +
		`{"type":"text","text":"answer"}]}]}`)

	out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var got struct {
		Messages []struct {
			Role             string `json:"role"`
			ReasoningContent string `json:"reasoning_content"`
			Content          []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("after the change it is not valid JSON: %v\n%s", err, out)
	}

	a := got.Messages[0]
	if a.ReasoningContent != "let me think" {
		t.Fatalf("reasoning_content should take the client's thinking block text, actual %q", a.ReasoningContent)
	}
	if len(a.Content) != 2 {
		t.Fatalf("added another block to a message that already has a thinking block:\n%s", out)
	}
}

// thinkcache hit: both reasoning_content and the thinking block get the real text.
func TestDeepSeekUsesCacheReasoning(t *testing.T) {
	const toolID = "t-cache-hit-0002"
	thinkcache.Default.Put([]string{thinkcache.ToolKey(toolID)}, []byte("the real reasoning from this turn"))

	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled"},` +
		`"messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"` + toolID + `","name":"Bash","input":{}}]}` +
		`]}`)

	out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var got struct {
		Messages []struct {
			ReasoningContent string `json:"reasoning_content"`
			Content          []struct {
				Type     string `json:"type"`
				Thinking string `json:"thinking"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("after the change it is not valid JSON: %v\n%s", err, out)
	}

	a := got.Messages[0]
	if a.ReasoningContent != "the real reasoning from this turn" {
		t.Fatalf("reasoning_content should take the cached text, actual %q", a.ReasoningContent)
	}
	if len(a.Content) == 0 || a.Content[0].Thinking != "the real reasoning from this turn" {
		t.Fatalf("the thinking block should take the cached text:\n%s", out)
	}
}

// Counts must be honest: skips **must report their cause**, never as "used real text".
//
// Why: the user asked whether it is the cache or the request, and those need opposite
// handling - a count alone says nothing. This request's tool id never entered
// thinkcache, so it is necessarily nocache, and the log must print the id as well for
// matching against the "recorded this round's reasoning" line.
func TestDeepSeekSkipCauseReportedHonestly(t *testing.T) {
	const toolID = "t-never-cached-0003"
	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled"},` +
		`"messages":[{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"` + toolID + `","name":"Read","input":{}}]}]}`)

	out, notes, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("it changed the request without the original text:\n%s", out)
	}
	all := strings.Join(notes, "\n")
	// **Neither field has text => return early, not a single note.** Deliberate: no
	// action means no log spam (thousands of placeholders a day are the main noise).
	// The classification can only be reported if we **did act**; see the mixed case.
	if len(notes) != 0 {
		t.Fatalf("added nothing yet reported notes:\n%s", all)
	}
}

// Mixed: one with text, one without. Having acted, both sides must be reported honestly
// - the one with text says "used real text", the other says "skipped" with its cause.
func TestDeepSeekSkipCauseReportedWhenPartlyRestored(t *testing.T) {
	const hitID, missID = "t-hit-0004", "t-miss-0004"
	seedCache(hitID, "the real reasoning from this turn")

	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled"},` +
		`"messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"` + hitID + `","name":"Read","input":{}}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"` + missID + `","name":"Read","input":{}}]}` +
		`]}`)

	out, notes, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(string(out), "the real reasoning from this turn") {
		t.Fatalf("cache hit but it was not filled back:\n%s", out)
	}
	if strings.Contains(string(out), missID+`","name":"Read","input":{}}],"reasoning_content"`) {
		t.Fatalf("it also filled in the entry that did not hit the cache:\n%s", out)
	}
	// The assertion is on the **English source + machine markers** (class words like
	// nocache, tool ids), not the rendered text: translations change (i18n rule).
	all := strings.Join(notes, "\n")
	for _, want := range []string{"1 used the real reasoning text",
		"1 had no original text to backfill", "nocache", missID} {
		if !strings.Contains(all, want) {
			t.Fatalf("notes should contain %q (never silent + traceable):\n%s", want, all)
		}
	}
	if strings.Contains(all, "backfilled thinking block on 2 assistant messages: "+
		"2 used the real reasoning text") {
		t.Fatalf("what was skipped was reported as real reasoning text:\n%s", all)
	}
}

// ---- on opencode's path (bare /v1 + OpenAI dialect), thinking must stay on ----

// Field (2026-09-15): opencode speaks the OpenAI dialect and never writes the Anthropic
// thinking field, so every request was read as "client wants no thinking" and got
// disabled - the user saw "deepseek somehow stopped thinking".
//
// After the fix: turning thinking off applies to Claude Code only (Agent=="claude").
// Here Agent is empty and the path is /chat/completions, opencode's shape: thinking
// must not move a byte, but reasoning is still sent back for it (upstream wants it on).
func TestDeepSeekOpencodeKeepsThinking(t *testing.T) {
	seedCache("call_oa_1", "the real reasoning text from the previous turn")
	body := []byte(`{"model":"deepseek-flash","stream":true,"messages":[` +
		`{"role":"user","content":"look at this file"},` +
		`{"role":"assistant","content":"looked at it","tool_calls":[` +
		`{"id":"call_oa_1","type":"function","function":{"name":"Read","arguments":"{}"}}]}` +
		`]}`)

	r := &special.Request{Model: "deepseek-flash", Provider: "smt-deepseek",
		BaseURL: "https://gw.example.com/v1", Protocol: "openai",
		Path: "/chat/completions"}
	out, notes, err := reasoning{}.Apply(body, r)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(string(out), `"thinking"`) {
		t.Fatalf("the opencode request had thinking turned off (the live bug):\n%s", out)
	}

	var got struct {
		Messages []struct {
			ReasoningContent string `json:"reasoning_content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("after the change it is not valid JSON: %v\n%s", err, out)
	}
	if got.Messages[1].ReasoningContent != "the real reasoning text from the previous turn" {
		t.Fatalf("the cache has the text yet it was not used to fill back, actual %q:\n%s",
			got.Messages[1].ReasoningContent, out)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "reasoning_content") {
		t.Fatalf("changed reasoning_content but did not report it (violates \"never silent\"): %v", notes)
	}
}

// Pass 3 (thinking block at the start of content[]) is an Anthropic dialect requirement;
// in the OpenAI dialect the carrier is reasoning_content, and putting an unknown block
// type into someone else's content[] only earns a 400.
func TestDeepSeekNoThinkingBlockInOpenAIDialect(t *testing.T) {
	seedCache("t-oa-0001", "the real reasoning text from the previous turn")
	body := []byte(`{"model":"deepseek-flash","thinking":{"type":"enabled"},` +
		`"messages":[{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"t-oa-0001","name":"Read","input":{}}]}]}`)

	out, _, err := reasoning{}.Apply(body, &special.Request{Model: "deepseek-flash",
		Provider: "smt-deepseek", BaseURL: "https://gw.example.com/v1",
		Path: "/chat/completions"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(string(out), `"type":"thinking"`) {
		t.Fatalf("stuffed a thinking block into the OpenAI dialect content[]:\n%s", out)
	}
	if !strings.Contains(string(out), `"reasoning_content"`) {
		t.Fatalf("reasoning_content should be filled in as usual:\n%s", out)
	}
}

func TestDeepSeekToolLoopMigrationIsScopedAndRebased(t *testing.T) {
	ds := reasoning{}
	candidate := &special.Request{
		Model: "deepseek-flash", Provider: "smt-deepseek",
		BaseURL: "https://gw.example.com/v1", Path: "/messages",
	}
	if needed, _ := ds.NeedsToolLoopRebase("ark", "ark-code-latest", candidate); !needed {
		t.Fatal("DeepSeek taking over an Ark tool loop should ask for a rebase")
	}
	if needed, _ := ds.NeedsToolLoopRebase(
		"smt-deepseek", "deepseek-flash", candidate); needed {
		t.Fatal("DeepSeek continuing its own tool loop should not rebase")
	}
	ark := &special.Request{Model: "ark-code-latest", Provider: "ark",
		BaseURL: "https://ark.example.com", Path: "/messages"}
	if needed, _ := ds.NeedsToolLoopRebase("smt-deepseek", "deepseek-flash", ark); needed {
		t.Fatal("deepseek special must not constrain moving out to Ark")
	}

	body := []byte(`{"model":"deepseek-flash","messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}` +
		`]}`)
	out, note, err := ds.RebaseToolLoop(body, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), toolLoopRebasePrompt) {
		t.Fatalf("no rebase instruction was appended:\n%s", out)
	}
	if !strings.Contains(note, "lossily rebuilt a foreign tool loop") {
		t.Fatalf("the rebase was not reported explicitly: %q", note)
	}
}

// TestTailShapeRepairOnToolResultOnlyTail pins pass 4: filling a tool_result-only tail.
//
// Why this pass is indispensable (the 2026-09-18 lesson from deleting it): without it
// the log shows **no 400** - a 400 fails over along the chain and the client gets a 200.
// "Looks fine" is really "every request silently downgraded".
func TestTailShapeRepairOnToolResultOnlyTail(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"ends with a user turn", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"let's start"}]},` +
			`{"role":"assistant","content":[{"type":"thinking","thinking":"think about it"},{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`},
		{"after the user turn there is also a system interjection", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"let's start"}]},` +
			`{"role":"assistant","content":[{"type":"thinking","thinking":"think about it"},{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},` +
			`{"role":"system","content":[{"type":"text","text":"The user sent a new message while you were working: keep going."}]}]}`},
		{"parallel tool round: two tool_results", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"let's start"}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"t1"},{"type":"tool_use","id":"t2"}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a"},{"type":"tool_result","tool_use_id":"t2","content":"b"}]}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, notes, err := reasoning{}.Apply([]byte(tt.body), claudeReq("deepseek-flash"))
			if err != nil {
				t.Fatalf("Apply errored: %v", err)
			}
			s := string(out)
			if !strings.Contains(s, `"text":`+mustJSON(toolLoopRebasePrompt)) {
				t.Fatalf("the continue instruction was not appended to the tail:\n%s", s)
			}
			// Not a byte of the original content may be lost: tool_result stays, history untouched.
			if !strings.Contains(s, `"tool_use_id":"t1"`) ||
				!strings.Contains(s, `"text":"let's start"`) {
				t.Fatalf("appending the tail instruction modified existing content:\n%s", s)
			}
			// The instruction must land in the **last user message**, not the array's end:
			// a trailing system remark must stay after it (upstream ignores system).
			if i := strings.Index(s, mustJSON(toolLoopRebasePrompt)); i >= 0 {
				if k := strings.Index(s, "while you were working"); k >= 0 &&
					strings.Index(s, mustJSON(toolLoopRebasePrompt)) > k {
					t.Fatalf("the continue instruction landed after a trailing system interjection (the upstream does not accept instructions in system):\n%s", s)
				}
			}
			if !containsNote(notes, "trailing user turn holds only tool_result") {
				t.Fatalf("notes were not reported (never silent is a hard requirement): %v", notes)
			}
		})
	}
}

// TestTailShapeLeavesNormalTailsAlone: these tails already pass; not a byte may move.
//
// The criterion is "content[] is **entirely** tool_result blocks", not "has no text
// block" - the latter is too wide and would alter [image] / [tool_result, image], which
// measured 3/3 passing (against real smt-deepseek/deepseek-flash, 3/3).
func TestTailShapeLeavesNormalTailsAlone(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"tail has text", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"hi"}]}]}`},
		{"tail is assistant (another rule handles it, so appending an instruction proves nothing)", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},` +
			`{"role":"assistant","content":[{"type":"text","text":"done talking"}]}]}`},
		{"only image blocks", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}`},
		{"tool_result + image", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"},` +
			`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}`},
		{"tool_result + text (already passes)", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"},` +
			`{"type":"text","text":"continue"}]}]}`},
		{"a tool_result-only turn is not the last user turn", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},` +
			`{"role":"assistant","content":[{"type":"text","text":"done"}]},` +
			`{"role":"user","content":[{"type":"text","text":"one more"}]}]}`},
		{"content is a string", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":"plain text"}]}`},
		{"OpenAI dialect: tail is role:tool", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"Bash","arguments":"{}"}}]},` +
			`{"role":"tool","tool_call_id":"c1","content":"ok"}]}`},
		{"no messages", `{"thinking":{"type":"adaptive"}}`},
		{"messages is empty", `{"thinking":{"type":"adaptive"},"messages":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, notes, err := reasoning{}.Apply([]byte(tt.body),
				claudeReq("deepseek-flash"))
			if err != nil {
				t.Fatalf("Apply errored: %v", err)
			}
			// Count occurrences rather than Contains: the fixture may itself carry a
			// "continue" (that is what a passing tail looks like), and Contains would misfire.
			before := strings.Count(tt.body, mustJSON(toolLoopRebasePrompt))
			after := strings.Count(string(out), mustJSON(toolLoopRebasePrompt))
			if after != before {
				t.Fatalf("a tail that must not be touched was touched (%d added):\n%s", after-before, out)
			}
			if containsNote(notes, "trailing user turn holds only tool_result") {
				t.Fatalf("a tail that must not be touched reported a tail note: %v", notes)
			}
		})
	}
}

// TestTailShapeInjectionDependsOnToolCallOrigin pins the third criterion (provenance).
//
// The measured table in docs/06-reasoning.md §1.1 (3/3 per cell, real
// smt-deepseek/deepseek-flash): bare tail + all aggregator-produced `call_NN_...` ->
// 200, must not be rewritten; any foreign id mixed in (`call_<hex>` / `toolu_...`) ->
// 400, must be filled. The regression point for "healthy requests got rewritten": the
// criterion once had only tail shape and anchor, so the normal path got a sentence.
//
// The self-produced family must accept **two-digit serials** (00/01/...): the aggregator
// numbers parallel tool calls 00, 01, ... in one turn (2026-09-22 real traffic: call_00_
// 2917, call_01_ 705, call_02_ 16, call_03_ 8, call_04_ 2). Accepting only 00 would call
// the common two-tool turn foreign, and "healthy path gets injected" would return.
func TestTailShapeInjectionDependsOnToolCallOrigin(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		inject bool
	}{
		{"all self-produced call_00_ + bare tail -> inject nothing", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"let's start"}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"call_00_abc123","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_00_abc123","content":"ok"}]}]}`, false},
		{"parallel second round call_01_ + bare tail -> inject nothing", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"let's start"}]},` +
			`{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"call_00_abc123","name":"Bash","input":{}},` +
			`{"type":"tool_use","id":"call_01_def456","name":"Read","input":{}}]},` +
			`{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"call_00_abc123","content":"ok"},` +
			`{"type":"tool_result","tool_use_id":"call_01_def456","content":"ok"}]}]}`, false},
		{"two-or-more-digit ordinal call_42_ + bare tail -> inject nothing", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"let's start"}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"call_42_abc123","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_42_abc123","content":"ok"}]}]}`, false},
		{"single-digit ordinal call_0_ (not the self-produced shape) + bare tail -> inject", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"let's start"}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"call_0_abc123","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_0_abc123","content":"ok"}]}]}`, true},
		{"foreign call_<hex> + bare tail -> inject", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"let's start"}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"call_faedac561213487ebeea7731","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_faedac561213487ebeea7731","content":"ok"}]}]}`, true},
		{"foreign toolu_... + bare tail -> inject", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"let's start"}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01ABCDEF","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01ABCDEF","content":"ok"}]}]}`, true},
		{"self-produced mixed with foreign + bare tail -> inject", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"let's start"}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"call_00_abc123","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_00_abc123","content":"ok"}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"call_faedac561213487ebeea7731","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_faedac561213487ebeea7731","content":"ok"}]}]}`, true},
		{"a tail that already has text -> never touch (even if the id is foreign)", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"call_faedac561213487ebeea7731","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_faedac561213487ebeea7731","content":"ok"},` +
			`{"type":"text","text":"continue"}]}]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, notes, err := reasoning{}.Apply([]byte(tt.body), claudeReq("deepseek-flash"))
			if err != nil {
				t.Fatalf("Apply errored: %v", err)
			}
			before := strings.Count(tt.body, mustJSON(toolLoopRebasePrompt))
			after := strings.Count(string(out), mustJSON(toolLoopRebasePrompt))
			if got := after > before; got != tt.inject {
				t.Fatalf("inject = %v, want %v (before %d after %d):\n%s", got, tt.inject, before, after, out)
			}
			if got := containsNote(notes, "trailing user turn holds only tool_result"); got != tt.inject {
				t.Fatalf("tail note = %v, want %v: %v", got, tt.inject, notes)
			}
		})
	}
}

// TestTailShapeRepairedWhenThinkingOff: the repair happens with thinking off too.
//
// This check is **independent of the thinking switch**. Measured 3/3: the same
// tool_result-only tail, with thinking:{"type":"disabled"} and no tools, still answers
// "reasoning_content must be passed back".
func TestTailShapeRepairedWhenThinkingOff(t *testing.T) {
	body := []byte(`{"thinking":{"type":"disabled"},"messages":[` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`)
	out, notes, err := reasoning{}.Apply(body, claudeReq("deepseek-flash"))
	if err != nil {
		t.Fatalf("Apply errored: %v", err)
	}
	if !strings.Contains(string(out), mustJSON(toolLoopRebasePrompt)) {
		t.Fatalf("with thinking off the tail instruction was not appended (measured: this check is not affected by thinking):\n%s", out)
	}
	if !containsNote(notes, "trailing user turn holds only tool_result") {
		t.Fatalf("notes were not reported: %v", notes)
	}
}

// mustJSON asserts the inserted text is **exactly** toolLoopRebasePrompt (not longer).
// Wording is where this path goes wrong most easily: it lives on in the user's history.
func mustJSON(s string) string {
	q, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(q)
}

func containsNote(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// TestResponsesTailShapeRepair pins pass 5: in the Responses dialect (/v1/responses),
// when input[] ends with a **foreign** function_call_output, append a plain user
// instruction at the end.
//
// Basis: measured 2026-09-22 (a probe against real smt-deepseek/deepseek-flash, 3/3
// per cell): self-produced `call_NN_...` call_id + [function_call, function_call_output]
// ending -> 200, must not be rewritten; foreign `call_faedac...` call_id + same ending
// -> 400 `The reasoning_text ... must be passed back`, and one plain user message
// appended -> 200.
func TestResponsesTailShapeRepair(t *testing.T) {
	newBody := func(callID string) string {
		return `{"model":"deepseek-flash","input":[` +
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]},` +
			`{"type":"function_call","call_id":"` + callID + `","name":"bash","arguments":"{}"},` +
			`{"type":"function_call_output","call_id":"` + callID + `","output":"ok"}]}`
	}
	// The appended item must be verbatim this one: type/role/content[].type are all
	// Responses dialect protocol fields, and the text uses json.Marshal(toolLoopRebasePrompt).
	injected := `{"type":"message","role":"user","content":[{"type":"input_text","text":` +
		mustJSON(toolLoopRebasePrompt) + `}]}`
	responsesReq := func() *special.Request {
		return &special.Request{Model: "deepseek-flash", Provider: "smt-deepseek",
			BaseURL: "https://gw.example.com/v1", Protocol: "openai",
			Path: "/responses", Agent: "codex"}
	}

	tests := []struct {
		name   string
		callID string
		want   bool
	}{
		{"self-produced call_00_... -> inject nothing", "call_00_abc123", false},
		{"self-produced call_01_... (second in parallel) -> inject nothing", "call_01_def456", false},
		{"foreign call_<hex> -> inject", "call_faedac561213487ebeea7731", true},
		{"foreign toolu_... -> inject", "toolu_01ABCDEF", true},
		{"single-digit ordinal call_0_... (unrecognized) -> inject", "call_0_abc123", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := newBody(tt.callID)
			out, notes, err := reasoning{}.Apply([]byte(body), responsesReq())
			if err != nil {
				t.Fatalf("Apply errored: %v", err)
			}
			s := string(out)
			if got := strings.Contains(s, injected); got != tt.want {
				t.Fatalf("inject = %v, want %v:\n%s", got, tt.want, s)
			}
			if !tt.want {
				if s != body {
					t.Fatalf("a body that must not be touched was touched:\n%s", s)
				}
				if len(notes) != 0 {
					t.Fatalf("reported notes without changing anything: %v", notes)
				}
				return
			}
			// It must be appended at the **end** of input[]: the shape criterion looks at the last entry.
			if strings.Index(s, injected) < strings.Index(s, `"type":"function_call_output"`) {
				t.Fatalf("the instruction was not appended at the end of input[]:\n%s", s)
			}
			if !json.Valid(out) {
				t.Fatalf("after the change it is not valid JSON:\n%s", out)
			}
			if !containsNote(notes, "Responses input[] ends on a function_call_output") {
				t.Fatalf("changed something but did not report the 5th-pass notes: %v", notes)
			}
		})
	}
}

// TestResponsesTailShapeLeavesOtherShapesAlone: shapes that do not match are untouched.
func TestResponsesTailShapeLeavesOtherShapesAlone(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"the last item is not function_call_output", `{"model":"deepseek-flash","input":[` +
			`{"type":"function_call","call_id":"call_faedac561213487ebeea7731","name":"bash","arguments":"{}"}]}`},
		{"foreign call_id but an assistant message still follows", `{"model":"deepseek-flash","input":[` +
			`{"type":"function_call_output","call_id":"call_faedac561213487ebeea7731","output":"ok"},` +
			`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`},
		{"no input array (messages dialect)", `{"model":"deepseek-flash","messages":[` +
			`{"role":"user","content":[{"type":"text","text":"hi"}]}]}`},
		{"input is an empty array", `{"model":"deepseek-flash","input":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &special.Request{Model: "deepseek-flash", Provider: "smt-deepseek",
				BaseURL: "https://gw.example.com/v1", Protocol: "openai",
				Path: "/responses", Agent: "codex"}
			out, notes, err := reasoning{}.Apply([]byte(tt.body), r)
			if err != nil {
				t.Fatalf("Apply errored: %v", err)
			}
			if string(out) != tt.body {
				t.Fatalf("a body that must not be touched was touched:\n%s", out)
			}
			if containsNote(notes, "Responses input[] ends on a function_call_output") {
				t.Fatalf("the shape does not match yet it reported a 5th-pass note: %v", notes)
			}
		})
	}
}

// TestImagesSurviveTheDeepSeekPatch: an image request through the DeepSeek reasoning
// patch must keep image blocks and their base64 **byte for byte**.
//
// This settles part of "does newgate support images". The forwarding layer is a pure
// byte passthrough (only swaps model, optionally fixes tools) and cannot break images;
// what might lose them is the **upstream patch** (AppendLastArrayItemArray) that inserts
// thinking blocks into the assistant's content[] - only there, never in user images.
func TestImagesSurviveTheDeepSeekPatch(t *testing.T) {
	seedCache("t1", "the real reasoning text from the previous turn")
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNgYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	imageBlock := `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + png + `"}}`
	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled","budget_tokens":1024},` +
		`"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"look at the image"},` + imageBlock + `]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"n":1}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}` +
		`]}`)

	out, notes, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(notes) == 0 {
		t.Fatal("changed something but did not report notes")
	}
	if !bytes.Contains(out, []byte(imageBlock)) {
		t.Fatalf("the image block did not reach the upstream as is (or was rewritten):\n%s", out)
	}
	if bytes.Contains(out, []byte(png)) && !bytes.Contains(out, []byte(`"data":"`+png)) {
		t.Fatal("the base64 content was modified")
	}

	// The image in the user message must stay where it was: the patch may touch assistant only.
	var got struct {
		Messages []struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("after the change it is not valid JSON: %v", err)
	}
	if len(got.Messages[0].Content) != 2 {
		t.Fatalf("the user message content block count changed (was 2, now %d) -- the patch touched what it must not", len(got.Messages[0].Content))
	}
	if !strings.Contains(string(got.Messages[0].Content[1]), png) {
		t.Fatal("the image in the user message changed")
	}
}

// TestRebaseToolLoopResponsesDialect: cross-upstream migration in the responses dialect.
//
// Field (2026-09-23): Codex keeps the conversation in top-level input[] with **no
// messages**, so on this migration path AppendLastArrayItemArray finds no messages and
// returns changed=false - a silent no-op, and exactly what RebaseToolLoop exists for
// (migrating an unclosed tool loop to DeepSeek 400s, see the A/B in this file). The
// added branch appends a plain user message item at the end of input[].
//
// Two things pinned, neither optional:
//   - the responses shape really changed (not a no-op), and the item is the **verbatim**
//     dialect shape (type/role/content[].type all responses names, not a text block);
//   - a shape with no input (say /v1/models) stays untouched, with no note.
func TestRebaseToolLoopResponsesDialect(t *testing.T) {
	responsesReq := func() *special.Request {
		return &special.Request{
			InModel: "normal", Tier: "normal", Model: "deepseek-chat",
			Provider: "ds", BaseURL: "https://api.deepseek.com", Protocol: "openai",
			Path: "/responses", Agent: "codex"}
	}

	body := `{"model":"normal","stream":false,"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},` +
		`{"type":"function_call","call_id":"call_00_abc","name":"exec","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_00_abc","output":"done"}]}`
	out, note, err := reasoning{}.RebaseToolLoop([]byte(body), responsesReq())
	if err != nil {
		t.Fatalf("RebaseToolLoop: %v", err)
	}
	if note == "" {
		t.Fatal("it changed the request but not a single note (never silent is a hard requirement)")
	}
	if string(out) == body {
		t.Fatal("nothing changed in the responses shape -- the migration is a silent no-op, exactly what this change is meant to fix")
	}
	var got struct {
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("cannot decode the output: %v\n%s", err, out)
	}
	if len(got.Input) != 4 {
		t.Fatalf("input should have one more item (3 -> 4), got %d: %s", len(got.Input), out)
	}
	last := got.Input[3]
	if last.Type != "message" || last.Role != "user" {
		t.Errorf("the appended item should be a plain user message, got type=%q role=%q", last.Type, last.Role)
	}
	if len(last.Content) != 1 || last.Content[0].Type != "input_text" ||
		last.Content[0].Text != toolLoopRebasePrompt {
		t.Errorf("the content shape of the appended item is wrong (responses needs input_text, not text): %+v", last.Content)
	}
	// Old bytes must not move: the first three items stay as-is.
	if !strings.Contains(string(out), `"call_id":"call_00_abc","output":"done"}`) {
		t.Errorf("the original function_call_output was modified: %s", out)
	}

	// A shape with no input: untouched, and no note.
	other := `{"model":"normal","input_text":"hi"}`
	out2, note2, err := reasoning{}.RebaseToolLoop([]byte(other), responsesReq())
	if err != nil {
		t.Fatalf("RebaseToolLoop(non-responses): %v", err)
	}
	if note2 != "" {
		t.Errorf("it did nothing yet reported a note: %q", note2)
	}
	_ = out2
}
