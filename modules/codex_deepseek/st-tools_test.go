package codex_deepseek

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/modules/gateway/special"

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

// codexRequest 是一份**照抄真实现场**的最小请求：工具树挂在 input[0] 的
// additional_tools 里，两层 namespace，一个 custom（exec）+ 一个 function（wait）。
// 真实现的形状见 /tmp/codexcap 的抓包（2026-09-21）。
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

// 抬上来之后：顶层有 tools、custom 变成了 function、原生 function 一字不动、
// input[0] 那条还在（实测上游对多出来的那条不报错，删它没有依据）。
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
	if len(notes) != 1 || !strings.Contains(notes[0], "lifted 1 tool") {
		t.Fatalf("the rewrite must be reported (不静默): %v", notes)
	}
}

// 没有 additional_tools 的请求一个字节都不动，也不报 note——
// 「不归我管」与「动了手」必须能分开。
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

// 顶层已经有 tools 时走替换，不是插入（Codex 两份形状都发过：缺席与 null）。
func TestApplyReplacesExistingToolsKey(t *testing.T) {
	body := []byte(`{"model":"normal","tools":null,"input":[{"type":"additional_tools","tools":[` +
		`{"type":"custom","name":"exec","description":"d"}]}]}`)
	out, _, err := testPlugin().Apply(body, &special.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(out), `"tools"`) != 2 { // 顶层一个 + additional_tools 里那一个
		t.Fatalf("tools key must appear exactly twice, no duplicates:\n%s", out)
	}
	var doc struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(out, &doc); err != nil || len(doc.Tools) != 1 {
		t.Fatalf("top-level tools was not replaced: %v %s", err, out)
	}
}

// apply_patch 是上游原生认的 custom 工具，不许降级。
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

// 响应侧：被降级过的工具，其 function_call 事件要整套改回 custom_tool_call。
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

// 真现场的那条路：参数是**几十个 delta** 吐出来的，而 output_item.done 上那份
// arguments 是**空的**（实测 2026-09-21，deepseek-flash 一条 exec 调用发了 53 个
// delta，收尾那条 item 的 arguments 是 ""）。
//
// 少了这一步的后果不是「报错」，是**静默地交出一段空代码**：codex 拿空字符串去
// eval，报一个与真正原因毫无关系的错。所以这条测试盯的是 output_item.done 上的
// input 必须是**攒起来的那份原文**。
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
	// 收尾那条 item 的 arguments 是空的——上游就是这么发的。
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

// 别的工具（原生 function）的调用事件一个字节都不许动。
func TestEgressLeavesOtherToolsAlone(t *testing.T) {
	plugin := testPlugin()
	e := plugin.Egress(nil, []byte(codexRequest))
	added := []byte("data: " + `{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","name":"wait","call_id":"c2"}}` + "\n\n")
	if out := e.Egress(added); out != nil {
		t.Fatalf("a native function call must not be touched: %s", out)
	}
	// 而且它不该被记成「我们的」，所以后续的 arguments 事件也不动。
	delta := []byte("data: " + `{"type":"response.function_call_arguments.delta","delta":"x","item_id":"i2","output_index":2}` + "\n\n")
	if out := e.Egress(delta); out != nil {
		t.Fatalf("the delta of a native function must not be touched: %s", out)
	}
}

// completed 汇总里那几条也要改回 custom——codex 在流结束后还会读它一次。
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
	// 载荷前面是 "data: "，剥掉再解析。
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

// 非 JSON 的载荷、以及没有 data 行的事件，一律不碰（注释行、[DONE]、keepalive）。
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

// 拆壳：上游回的是 {"input": "<原文>"}，codex 要的是里面那串原文。
// 拆不出来时原样返回（绝不编一段它没写过的代码）。
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
