package codex_deepseek

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	"github.com/rzbdz/newgate-ext/modules/pluginmanager"

	"github.com/rzbdz/newgate-ext/modules/codex"
	codexapi "github.com/rzbdz/newgate-ext/modules/codex"
	"github.com/rzbdz/newgate-ext/modules/deepseek"
	deepseekapi "github.com/rzbdz/newgate-ext/modules/deepseek"
)

func testPlugin() tools {
	return tools{
		client: codexapi.Client{AgentID: codex.ID},
		model:  deepseekapi.Model{MatchTarget: deepseek.MatchTarget},
	}
}

// reqWithOff builds a context with the given switch points turned off. Same as the deepseek one.
func reqWithOff(t *testing.T, off ...string) *special.Request {
	t.Helper()
	cfg := pluginmanager.Config{Off: map[string]pluginmanager.Entry{}}
	for _, path := range off {
		cfg.Off[path] = pluginmanager.Entry{}
	}
	raw, err := cfg.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &special.Request{State: &domain.State{
		ModuleConfig: map[string][]byte{pluginmanager.StateKey: raw}}}
}

// codexRequest is a minimal request **copied from the real scene**: the tool tree hangs
// in input[0]'s additional_tools, two levels of namespace, one custom (exec) + one
// function (wait). For the real shape see the capture in /tmp/codexcap (2026-09-21).
const codexRequest = `{"model":"normal","stream":true,"tool_choice":"auto",` +
	`"input":[{"type":"additional_tools","id":"at_1","role":"developer","tools":[` +
	`{"type":"namespace","name":"functions","description":"","tools":[` +
	`{"type":"custom","name":"exec","description":"Run JS"},` +
	`{"type":"function","name":"wait","description":"Wait","parameters":{"type":"object","properties":{"cell_id":{"type":"string"}},"required":["cell_id"]}}` +
	`]}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`

func TestMatchOnlyCodexDeepSeekIntersection(t *testing.T) {
	plugin := testPlugin()
	for _, test := range []struct {
		agent string
		model string
		want  bool
	}{
		{codex.ID, "deepseek-flash", true},
		{"claude", "deepseek-flash", false},
		{codex.ID, "glm-5.3", false},
	} {
		request := &special.Request{Agent: test.agent, Model: test.model}
		if got := plugin.Match(request); got != test.want {
			t.Fatalf("Match(%q, %q) = %v, want %v", test.agent, test.model, got, test.want)
		}
	}
}

// After the lift: the top level has tools, custom became function, the native
// function is untouched byte for byte, and the input[0] item is still there (measured,
// the upstream does not error on the extra one, so deleting it has no basis).
func TestApplyLiftsToolsAndDegradesCustom(t *testing.T) {
	out, notes, err := testPlugin().Apply([]byte(codexRequest), &special.Request{})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tools []map[string]any `json:"tools"`
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(doc.Tools) != 2 {
		t.Fatalf("want 2 lifted tools, got %d: %s", len(doc.Tools), out)
	}
	exec, wait := doc.Tools[0], doc.Tools[1]
	if exec["type"] != "function" || exec["name"] != "exec" {
		t.Fatalf("exec was not degraded to a function: %v", exec)
	}
	if _, ok := exec["parameters"].(map[string]any); !ok {
		t.Fatalf("a function tool must carry parameters: %v", exec)
	}
	if exec["description"] != "Run JS" {
		t.Fatalf("description must survive the rewrite: %v", exec)
	}
	if wait["type"] != "function" || wait["name"] != "wait" {
		t.Fatalf("the native function must survive untouched: %v", wait)
	}
	if len(doc.Input) != 2 || doc.Input[0]["type"] != Tools {
		t.Fatalf("the additional_tools item must stay in input: %v", doc.Input)
	}
	// Two notes: lifted 2 (leaf count, not downgrade count), of which 1 was
	// downgraded. See the "note is reported as two lines" passage in Apply.
	if len(notes) != 2 ||
		!strings.Contains(notes[0], "lifted 2 tool declarations") ||
		!strings.Contains(notes[1], "degraded 1 custom tool ") {
		t.Fatalf("the rewrite must be reported (never silent): %v", notes)
	}
}

// Even with not one custom (all native function) it **still reports how many were lifted**:
// the lift is the step that fixes the root cause, and here the downgrade count is 0.
// Reporting only the downgrade count would make the log say "lifted 0" while the request was
// plainly changed -- exactly the kind of lie the "do not be silent" rule exists to remove.
func TestApplyReportsTheLiftEvenWithNothingDegraded(t *testing.T) {
	body := []byte(`{"model":"normal","input":[{"type":"additional_tools","tools":[` +
		`{"type":"namespace","name":"functions","tools":[` +
		`{"type":"function","name":"wait","description":"w","parameters":{"type":"object","properties":{}}},` +
		`{"type":"function","name":"apply_patch_helpers","description":"h","parameters":{"type":"object","properties":{}}}]}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	out, notes, err := testPlugin().Apply(body, &special.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) == string(body) {
		t.Fatal("a tree of native functions must still be lifted")
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "lifted 2 tool declarations") {
		t.Fatalf("want exactly the lift note, got %v", notes)
	}
}

// A request without additional_tools is untouched byte for byte, and reports
// no note -- "not mine to handle" and "took action" must be distinguishable.
func TestApplyLeavesForeignRequestsAlone(t *testing.T) {
	body := []byte(`{"model":"normal","input":[{"type":"message","role":"user"}]}`)
	out, notes, err := testPlugin().Apply(body, &special.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(body) || len(notes) != 0 {
		t.Fatalf("body must be untouched: %s notes=%v", out, notes)
	}
}

// New Codex puts tools directly in the top-level tools (including namespace groups
// and web_search); there is no input[0] to lift here -- but the custom inside is still
// rejected by DeepSeek with `400 Unsupported custom tool` (measured 2026-09-21). So the
// top-level copy must be downgraded too: custom -> function, not one other byte touched.
func TestApplyDegradesCustomInTopLevelTools(t *testing.T) {
	body := []byte(`{"model":"normal","stream":true,"tools":[` +
		`{"type":"function","name":"exec_command","description":"run","parameters":{"type":"object","properties":{}}},` +
		`{"type":"namespace","name":"multi_agent_v1","description":"","tools":[` +
		`{"type":"function","name":"spawn_agent","description":"spawn","parameters":{"type":"object","properties":{}}}]},` +
		`{"type":"web_search","external_web_access":false},` +
		`{"type":"custom","name":"exec","description":"Run JS"}` +
		`],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	out, notes, err := testPlugin().Apply(body, &special.Request{})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(doc.Tools) != 4 {
		t.Fatalf("the array must keep its length, got %d", len(doc.Tools))
	}
	if doc.Tools[0]["type"] != "function" || doc.Tools[0]["name"] != "exec_command" {
		t.Fatalf("a native function must survive untouched: %v", doc.Tools[0])
	}
	if doc.Tools[1]["type"] != "namespace" {
		t.Fatalf("a namespace must survive untouched: %v", doc.Tools[1])
	}
	if doc.Tools[2]["type"] != "web_search" {
		t.Fatalf("web_search must survive untouched: %v", doc.Tools[2])
	}
	if doc.Tools[3]["type"] != "function" || doc.Tools[3]["name"] != "exec" {
		t.Fatalf("the custom tool was not degraded: %v", doc.Tools[3])
	}
	if _, ok := doc.Tools[3]["parameters"].(map[string]any); !ok {
		t.Fatalf("a degraded tool must carry parameters: %v", doc.Tools[3])
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "degraded 1 custom tool") {
		t.Fatalf("the rewrite must be reported (never silent): %v", notes)
	}
}

// Not one custom at the top level (Codex 0.155.1 under the default sandbox is exactly this:
// all function + one namespace + web_search) -> not one byte touched, and no note reported.
func TestApplyLeavesCleanTopLevelToolsAlone(t *testing.T) {
	body := []byte(`{"model":"normal","tools":[` +
		`{"type":"function","name":"exec_command","description":"run","parameters":{"type":"object","properties":{}}},` +
		`{"type":"namespace","name":"multi_agent_v1","tools":[` +
		`{"type":"function","name":"spawn_agent","description":"spawn","parameters":{"type":"object","properties":{}}}]},` +
		`{"type":"web_search","external_web_access":false}]}`)
	out, notes, err := testPlugin().Apply(body, &special.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(body) || len(notes) != 0 {
		t.Fatalf("body must be untouched: %s notes=%v", out, notes)
	}
}

// When the top level already has tools it is replacement, not
// insertion (Codex has sent both shapes: absent and null).
func TestApplyReplacesExistingToolsKey(t *testing.T) {
	body := []byte(`{"model":"normal","tools":null,"input":[{"type":"additional_tools","tools":[` +
		`{"type":"custom","name":"exec","description":"d"}]}]}`)
	out, _, err := testPlugin().Apply(body, &special.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(out), `"tools"`) != 2 { // one at top level + the one in additional_tools
		t.Fatalf("tools key must appear exactly twice, no duplicates:\n%s", out)
	}
	var doc struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(out, &doc); err != nil || len(doc.Tools) != 1 {
		t.Fatalf("top-level tools was not replaced: %v %s", err, out)
	}
}

// The switch point must **really be read**: once off, the request
// is untouched byte for byte and the response side claims nothing.
//
// This was added on 2026-09-21 because its absence was measured live: `newgate plugin
// codex-deepseek.lift-tools off` reported "off", while on live the top-level tools were still
// lifted -- a switch that reports a name nobody reads is a lie **worse than having no switch**.
//
// Both sides are checked together: reading only one side would let "the request was not changed but
// the response was changed to custom_tool_call" slip through, an asymmetry that breaks the client.
func TestSwitchOffStopsBothDirections(t *testing.T) {
	plugin := testPlugin()
	off := reqWithOff(t, SwitchDegradeTools)

	out, notes, err := plugin.Apply([]byte(codexRequest), off)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != codexRequest || len(notes) != 0 {
		t.Fatalf("with it off the request must stay byte-for-byte: %s notes=%v", out, notes)
	}
	if got := plugin.Egress(off, []byte(codexRequest)); got != nil {
		t.Fatalf("with it off the response side must not claim, got %v", got)
	}

	// The top-level route must be turned off as well -- the two routes are the same move.
	top := []byte(`{"model":"normal","tools":[{"type":"custom","name":"exec","description":"d"}]}`)
	out, notes, err = plugin.Apply(top, off)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(top) || len(notes) != 0 {
		t.Fatalf("with it off the top level must not change either: %s notes=%v", out, notes)
	}

	// The other way round: with the switch on (missing snapshot = not off) it
	// acts as usual, so the test above cannot pass by "never acting at all".
	if out, notes, _ := plugin.Apply([]byte(codexRequest), &special.Request{}); string(out) == codexRequest || len(notes) == 0 {
		t.Fatalf("when not off it must lift as usual: %s notes=%v", out, notes)
	}
}

// apply_patch is the custom tool the upstream natively recognizes; it must not be downgraded.
func TestApplyKeepsApplyPatchCustom(t *testing.T) {
	body := []byte(`{"model":"normal","input":[{"type":"additional_tools","tools":[` +
		`{"type":"custom","name":"apply_patch","description":"d"}]}]}`)
	out, _, err := testPlugin().Apply(body, &special.Request{})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(out, &doc); err != nil || doc.Tools[0]["type"] != "custom" {
		t.Fatalf("apply_patch must stay custom: %v %s", err, out)
	}
}

// A custom hidden **inside** a top-level namespace must be downgraded
// too -- this is the root cause of the upstream's `Currently custom
// tools are not allowed inside a namespace` (measured 2026-09-21).
//
// The namespace shell is untouched (DeepSeek really uses it as a group, and
// flattening would break it); only the custom inside is rewritten to function.
func TestApplyDegradesCustomInsideTopLevelNamespace(t *testing.T) {
	body := []byte(`{"model":"normal","tools":[` +
		`{"type":"function","name":"exec_command","description":"run","parameters":{"type":"object","properties":{}}},` +
		`{"type":"namespace","name":"functions","description":"","tools":[` +
		`{"type":"function","name":"wait","description":"w","parameters":{"type":"object","properties":{}}},` +
		`{"type":"custom","name":"exec","description":"Run JS"}]}]}`)
	out, notes, err := testPlugin().Apply(body, &special.Request{})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(doc.Tools) != 2 {
		t.Fatalf("top-level length must survive, got %d: %s", len(doc.Tools), out)
	}
	if doc.Tools[1]["type"] != "namespace" {
		t.Fatalf("the namespace shell must survive, got %v", doc.Tools[1])
	}
	inner, _ := doc.Tools[1]["tools"].([]any)
	if len(inner) != 2 {
		t.Fatalf("the nested array must keep its length, got %d: %v", len(inner), inner)
	}
	if inner[0].(map[string]any)["type"] != "function" {
		t.Fatalf("a nested function must survive untouched: %v", inner[0])
	}
	if inner[1].(map[string]any)["type"] != "function" ||
		inner[1].(map[string]any)["name"] != "exec" {
		t.Fatalf("the nested custom was not degraded: %v", inner[1])
	}
	if _, has := inner[1].(map[string]any)["parameters"]; !has {
		t.Fatalf("a degraded nested tool must carry parameters: %v", inner[1])
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "degraded 1 custom tool") {
		t.Fatalf("the rewrite must be reported: %v", notes)
	}
}

// A namespace **entirely** of native functions / no custom -> the whole namespace is untouched
// byte for byte. **This is the byte-surgery rule**: if it can be left alone, leave it alone.
func TestApplyLeavesNamespaceWithFunctionsAlone(t *testing.T) {
	body := []byte(`{"model":"normal","tools":[` +
		`{"type":"namespace","name":"multi_agent_v1","tools":[` +
		`{"type":"function","name":"spawn_agent","description":"s","parameters":{"type":"object","properties":{}}},` +
		`{"type":"function","name":"get_goal","description":"g","parameters":{"type":"object","properties":{}}}]}]}`)
	out, notes, err := testPlugin().Apply(body, &special.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(body) || len(notes) != 0 {
		t.Fatalf("a clean namespace must be untouched: %s notes=%v", out, notes)
	}
}

func TestEgressClaimsOnlyWhenSomethingWasDegraded(t *testing.T) {
	plugin := testPlugin()
	if got := plugin.Egress(nil, []byte(codexRequest)); got == nil {
		t.Fatal("a request with a degraded tool must be claimed")
	}
	plain := []byte(`{"model":"normal","input":[{"type":"additional_tools","tools":[` +
		`{"type":"custom","name":"apply_patch","description":"d"}]}]}`)
	if got := plugin.Egress(nil, plain); got != nil {
		t.Fatal("a request with nothing degraded must not be claimed")
	}
}

// A downgraded custom at the top level must be changed back to custom_tool_call
// on the response side too -- the two sources (lifted from input[0], already at the
// top level) are one thing to the response side, and the criterion must accept both.
func TestEgressClaimsForTopLevelDegradedTools(t *testing.T) {
	plugin := testPlugin()
	body := []byte(`{"model":"normal","tools":[` +
		`{"type":"function","name":"exec_command","description":"run","parameters":{"type":"object","properties":{}}},` +
		`{"type":"custom","name":"exec","description":"Run JS"}]}`)
	if got := plugin.Egress(nil, body); got == nil {
		t.Fatal("a top-level degraded custom tool must be claimed")
	}
	clean := []byte(`{"model":"normal","tools":[` +
		`{"type":"function","name":"exec_command","description":"run","parameters":{"type":"object","properties":{}}}]}`)
	if got := plugin.Egress(nil, clean); got != nil {
		t.Fatal("a top-level array with nothing degraded must not be claimed")
	}
}

// Response side: for a downgraded tool, its function_call
// event is changed back to custom_tool_call as a whole.
func TestEgressRewritesTheDegradedToolCall(t *testing.T) {
	plugin := testPlugin()
	e := plugin.Egress(nil, []byte(codexRequest))

	added := []byte("event: response.output_item.added\ndata: " +
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"i1","status":"in_progress","arguments":"","call_id":"c1","name":"exec"}}` + "\n\n")
	out := string(e.Egress(added))
	if !strings.Contains(out, `"type":"custom_tool_call"`) || !strings.Contains(out, `"name":"exec"`) {
		t.Fatalf("the item was not rewritten: %s", out)
	}
	if !strings.HasPrefix(out, "event: response.output_item.added\ndata: ") || !strings.HasSuffix(out, "\n\n") {
		t.Fatalf("the event framing must survive: %q", out)
	}

	delta := []byte("data: " + `{"type":"response.function_call_arguments.delta","delta":"{","item_id":"i1","output_index":1}` + "\n\n")
	if out := string(e.Egress(delta)); !strings.Contains(out, `"type":"response.custom_tool_call_input.delta"`) {
		t.Fatalf("the delta was not renamed: %s", out)
	}

	done := []byte("data: " + `{"type":"response.function_call_arguments.done","arguments":"{\"input\":\"const r = 1;\"}","item_id":"i1","output_index":1}` + "\n\n")
	out = string(e.Egress(done))
	if !strings.Contains(out, `"type":"response.custom_tool_call_input.done"`) ||
		!strings.Contains(out, `"input":"const r = 1;"`) {
		t.Fatalf("the done event was not unwrapped: %s", out)
	}
	if strings.Contains(out, `"arguments"`) {
		t.Fatalf("arguments must not survive into the custom event: %s", out)
	}

	itemDone := []byte("data: " + `{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"i1","status":"completed","arguments":"{\"input\":\"x\"}","call_id":"c1","name":"exec"}}` + "\n\n")
	if out := string(e.Egress(itemDone)); !strings.Contains(out, `"type":"custom_tool_call"`) {
		t.Fatalf("output_item.done must carry the rewritten item: %s", out)
	}
}

// The real scene's route: the parameters come out as **dozens of deltas**, while the
// arguments on output_item.done is **empty** (measured 2026-09-21, deepseek-flash
// sent 53 deltas for one exec call, and the arguments of the closing item was "").
//
// The consequence of missing this step is not "an error", it is **silently
// handing over an empty piece of code**: codex evals the empty string and
// reports an error with nothing to do with the real cause. So this test watches
// that the input on output_item.done must be **the accumulated source**.
func TestEgressAccumulatesDeltasForTheDoneItem(t *testing.T) {
	plugin := testPlugin()
	e := plugin.Egress(nil, []byte(codexRequest))

	added := []byte("data: " + `{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"i2","status":"in_progress","arguments":"","call_id":"c2","name":"exec"}}` + "\n\n")
	if out := e.Egress(added); out == nil {
		t.Fatal("the added event must be rewritten")
	}
	for _, chunk := range []string{`{"input": "const r =`, ` await tools.exec_`, `command({cmd: \"ls\"});"} `} {
		delta := []byte("data: " + `{"type":"response.function_call_arguments.delta","delta":` +
			mustJSON(chunk) + `,"item_id":"i2","output_index":2}` + "\n\n")
		if out := e.Egress(delta); out == nil {
			t.Fatalf("delta %q must be rewritten", chunk)
		}
	}
	// The arguments of the closing item is empty -- that is how the upstream sends it.
	itemDone := []byte("data: " + `{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"i2","status":"completed","arguments":"","call_id":"c2","name":"exec"}}` + "\n\n")
	out := string(e.Egress(itemDone))
	if out == "" {
		t.Fatal("the done item must be rewritten")
	}
	var doc struct {
		Item map[string]any `json:"item"`
	}
	payload := strings.TrimPrefix(strings.TrimSpace(out), "data: ")
	if err := json.Unmarshal([]byte(payload), &doc); err != nil {
		t.Fatalf("rewritten item is not JSON: %v\n%s", err, out)
	}
	want := `const r = await tools.exec_command({cmd: "ls"});`
	if doc.Item["input"] != want {
		t.Fatalf("the done item must carry the accumulated input\n got: %q\nwant: %q",
			doc.Item["input"], want)
	}
	if doc.Item["type"] != "custom_tool_call" {
		t.Fatalf("the done item must be a custom_tool_call: %v", doc.Item)
	}
	if _, has := doc.Item["arguments"]; has {
		t.Fatalf("arguments must not survive: %v", doc.Item)
	}
}

func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Call events of other tools (native functions) must not be touched byte for byte.
func TestEgressLeavesOtherToolsAlone(t *testing.T) {
	plugin := testPlugin()
	e := plugin.Egress(nil, []byte(codexRequest))
	added := []byte("data: " + `{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","name":"wait","call_id":"c2"}}` + "\n\n")
	if out := e.Egress(added); out != nil {
		t.Fatalf("a native function call must not be touched: %s", out)
	}
	// And it should not be recorded as "ours", so the
	// subsequent arguments events are not touched either.
	delta := []byte("data: " + `{"type":"response.function_call_arguments.delta","delta":"x","item_id":"i2","output_index":2}` + "\n\n")
	if out := e.Egress(delta); out != nil {
		t.Fatalf("the delta of a native function must not be touched: %s", out)
	}
}

// The few entries in the completed summary must be changed back
// to custom too -- codex reads it once more after the stream ends.
func TestEgressRewritesCompletedOutput(t *testing.T) {
	plugin := testPlugin()
	e := plugin.Egress(nil, []byte(codexRequest))
	completed := []byte("data: " + `{"type":"response.completed","response":{"output":[` +
		`{"type":"function_call","id":"i1","call_id":"c1","name":"exec","arguments":"{\"input\":\"go\"}"},` +
		`{"type":"function_call","id":"i2","call_id":"c2","name":"wait","arguments":"{}"}]}}` + "\n\n")
	out := e.Egress(completed)
	if out == nil {
		t.Fatal("completed must be rewritten when it carries a degraded call")
	}
	var doc struct {
		Response struct {
			Output []map[string]any `json:"output"`
		} `json:"response"`
	}
	// The payload is preceded by "data: "; strip it before parsing.
	payload := strings.TrimPrefix(strings.TrimSpace(string(out)), "data: ")
	if err := json.Unmarshal([]byte(payload), &doc); err != nil {
		t.Fatalf("rewritten completed is not JSON: %v\n%s", err, out)
	}
	if doc.Response.Output[0]["type"] != "custom_tool_call" || doc.Response.Output[0]["input"] != "go" {
		t.Fatalf("the degraded call was not rewritten: %v", doc.Response.Output[0])
	}
	if doc.Response.Output[1]["type"] != "function_call" {
		t.Fatalf("the native call must stay a function_call: %v", doc.Response.Output[1])
	}
}

// Non-JSON payloads and events with no data line are left alone (comment lines, [DONE], keepalive).
func TestEgressIgnoresNonJSONPayloads(t *testing.T) {
	plugin := testPlugin()
	e := plugin.Egress(nil, []byte(codexRequest))
	for _, event := range []string{
		"event: ping\ndata: [DONE]\n\n",
		": keepalive\n\n",
		"event: response.created\ndata: {not json\n\n",
	} {
		if out := e.Egress([]byte(event)); out != nil {
			t.Fatalf("event %q must be left alone, got %s", event, out)
		}
	}
}

// Unwrap: the upstream replies {"input": "<source>"}, and codex wants the source inside.
// When it cannot be unwrapped, return as is (never make up a piece of code it never wrote).
func TestUnwrapInput(t *testing.T) {
	if got := unwrapInput(`{"input":"const r = 1;"}`); got != "const r = 1;" {
		t.Fatalf("unwrapInput = %q", got)
	}
	if got := unwrapInput("const r = 1;"); got != "const r = 1;" {
		t.Fatalf("plain text must pass through: %q", got)
	}
	if got := unwrapInput(`{"cmd":"ls"}`); got != `{"cmd":"ls"}` {
		t.Fatalf("a foreign shape must pass through: %q", got)
	}
}
