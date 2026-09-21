package codex_deepseek

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/gateway/special"
	"github.com/rzbdz/newgate/modules/pluginmanager"

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

// reqWithOff 造一个上下文，并把给定的开关点关掉。与 deepseek 那份同款。
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
	// 两条 note：抬了 2 条（叶子数，不是降级数），其中 1 条被降级。见 Apply 里
	// 「note 分两条报」那段。
	if len(notes) != 2 ||
		!strings.Contains(notes[0], "lifted 2 tool declarations") ||
		!strings.Contains(notes[1], "degraded 1 custom tool ") {
		t.Fatalf("the rewrite must be reported (不静默): %v", notes)
	}
}

// 一个 custom 都没有（全是原生 function）时**照样要报抬了几条**：抬才是修根因
// 的那一步，而这时降级数是 0。note 只报降级数会让日志说「抬了 0 条」而请求明明
// 被改了——这正是「不静默」那条规矩要消掉的那种谎。
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

// 新版 Codex 把工具直接放在顶层 tools 里（含 namespace 分组与 web_search），
// 此时没有 input[0] 可抬——但里面那条 custom 仍会被 DeepSeek 以
// `400 Unsupported custom tool` 拒掉（实测 2026-09-21）。所以顶层那份也要降级：
// custom → function，别的一个字节不动。
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
		t.Fatalf("the rewrite must be reported (不静默): %v", notes)
	}
}

// 顶层一个 custom 都没有（Codex 0.155.1 在默认沙箱下就是这种：全是 function +
// 一个 namespace + web_search）→ 一个字节都不动，也不报 note。
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

// 开关点必须**真的被读**：关掉之后请求一个字节都不动、响应侧也不认领。
//
// 这条是 2026-09-21 补的，因为现场实测到了它的缺席：`newgate plugin
// codex-deepseek.lift-tools off` 报了「已关闭」，而 live 上顶层 tools 照样被抬
// 上来——开关点报了名却没人读，那是**比没有开关更糟**的一种谎。
//
// 两侧一起验：只读一侧的话，「请求没改、响应却被改成 custom_tool_call」这种
// 把客户端弄坏的不对称会漏过去。
func TestSwitchOffStopsBothDirections(t *testing.T) {
	plugin := testPlugin()
	off := reqWithOff(t, SwitchLiftTools)

	out, notes, err := plugin.Apply([]byte(codexRequest), off)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != codexRequest || len(notes) != 0 {
		t.Fatalf("关掉之后请求必须逐字不动：%s notes=%v", out, notes)
	}
	if got := plugin.Egress(off, []byte(codexRequest)); got != nil {
		t.Fatalf("关掉之后响应侧不该认领，got %v", got)
	}

	// 顶层那条路线也要一起关掉——两条路线是同一手。
	top := []byte(`{"model":"normal","tools":[{"type":"custom","name":"exec","description":"d"}]}`)
	out, notes, err = plugin.Apply(top, off)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(top) || len(notes) != 0 {
		t.Fatalf("关掉之后顶层也不该动：%s notes=%v", out, notes)
	}

	// 反向：开关开着（快照缺席 = 没关）时照常动手，免得上面那条测试变成
	// 「怎么都不动」也能过。
	if out, notes, _ := plugin.Apply([]byte(codexRequest), &special.Request{}); string(out) == codexRequest || len(notes) == 0 {
		t.Fatalf("没关的时候必须照常抬：%s notes=%v", out, notes)
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

// 顶层 namespace **里面**藏着的 custom 也得降级——这是上游那句 `Currently
// custom tools are not allowed inside a namespace` 的根因（实测 2026-09-21）。
//
// namespace 的外壳不动（DeepSeek 真的把它当分组用，扁平化会破坏它），只把里面
// 那条 custom 改写成 function。
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

// namespace 里面**全是** native function / 没有 custom → namespace 整段逐字
// 不动。**这是字节手术的规矩**：能不动就不动。
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

// 顶层那份被降级的 custom 同样要在响应侧被改回 custom_tool_call——两条来源
// （input[0] 抬上来的、顶层本来就有的）对响应侧是同一件事，判据必须都认。
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
