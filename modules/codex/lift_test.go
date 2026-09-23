package codex

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/modules/gateway/special"
)

func init() {
	special.Register(ToolsLift{})
}

// reqToolsLift 构造一个最小可用的 special.Request；ToolsLift.Apply 只读 Agent。
func reqToolsLift() *special.Request { return &special.Request{Agent: ID} }

// toolFactory 造一条工具声明——形状照 Codex 0.155.1 实际发出来的写。
// 三种 type 各一条，用来钉叶子与容器的分别。
func toolFactory(kind, name string) []byte {
	switch kind {
	case "function":
		return []byte(`{"type":"function","name":"` + name + `","description":"d","parameters":{"type":"object"}}`)
	case "custom":
		return []byte(`{"type":"custom","name":"` + name + `","description":"d"}`)
	case "web_search":
		return []byte(`{"type":"web_search","name":"` + name + `"}`)
	default:
		panic("unknown tool kind: " + kind)
	}
}

// nestTools 造一份 `input[0].additional_tools` 数组（裸数组，给 FlattenToolTree 测）。
func nestTools(tools [][]byte) []byte {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, t := range tools {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(t)
	}
	buf.WriteByte(']')
	return buf.Bytes()
}

// nestInsideNamespace 把若干工具包进一个 `namespace` 容器，用来钉住递归。
func nestInsideNamespace(name string, tools [][]byte) []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"type":"namespace","name":"`)
	buf.WriteString(name)
	buf.WriteString(`","tools":`)
	buf.Write(nestTools(tools))
	buf.WriteByte('}')
	return buf.Bytes()
}

// TestFlattenToolTreeNamespaceFlattened 钉「namespace 拍平」的核心。
//
// `namespace` 是 Codex 客户端的分组（functions / collaboration），上游没有对
// 应概念——拍平成一个 tools 数组，叶子字节不动。这条规矩的来由见 lift.go。
func TestFlattenToolTreeNamespaceFlattened(t *testing.T) {
	leaves := [][]byte{
		toolFactory("function", "get_weather"),
		toolFactory("custom", "exec"),
		toolFactory("web_search", "search"),
	}
	arr := nestTools([][]byte{
		nestInsideNamespace("functions", leaves[:2]),
		leaves[2],
	})
	out, n := FlattenToolTree(arr)
	if n != len(leaves) {
		t.Fatalf("leaf count: got %d, want %d", n, len(leaves))
	}
	// 叶子字节必须逐字保留——这是「请求体不做 JSON 往返」在 lift 上的落点。
	// 用 string.Contains 而不是 JSON 解析，因为目的是验证**字节保真**。
	got := string(out)
	for _, want := range leaves {
		if !strings.Contains(got, string(want)) {
			t.Fatalf("lost a leaf: %s\nin output: %s", string(want), got)
		}
	}
}

// TestFlattenToolTreeUnknownKindPassThrough 钉「未知 type 不动手」。
//
// Codex 哪天加一种新的工具 type，抬上去的是它写的字节原文，不是我们猜的形
// 状。把 function/custom/web_search 之外的 `type` 当叶子、字节不动。
func TestFlattenToolTreeUnknownKindPassThrough(t *testing.T) {
	custom := []byte(`{"type":"future_thing","name":"x","custom_field":[1,2,3]}`)
	out, n := FlattenToolTree(nestTools([][]byte{custom}))
	if n != 1 {
		t.Fatalf("leaf count: got %d, want 1", n)
	}
	if !strings.Contains(string(out), string(custom)) {
		t.Fatalf("future_thing must be byte-identical\n got: %s\nwant sub: %s", string(out), string(custom))
	}
}

// TestFlattenToolTreeEmptyInput 不算「已抬」——与 leafCount=0 的契约配套。
func TestFlattenToolTreeEmptyInput(t *testing.T) {
	out, n := FlattenToolTree([]byte(`[]`))
	if n != 0 || out != nil {
		t.Fatalf("empty input: got (%s, %d), want (nil, 0)", string(out), n)
	}
}

// TestToolsInInputFindsItem 钉「input[].additional_tools 项是哪一个」。
//
// 顺序：Codex 的 `input[]` 不一定把 additional_tools 放在 index 0；它可能跟
// 在一条 user 消息后面。该当按 type 字段去找，不按 index 猜。
func TestToolsInInputFindsItem(t *testing.T) {
	body := []byte(`{"model":"x","input":[
		{"type":"message","role":"user","content":"hi"},
		{"type":"additional_tools","tools":[
			{"type":"function","name":"f","description":"d"}
		]}
	]}`)
	arr, ok := ToolsInInput(body)
	if !ok || arr == nil {
		t.Fatal("additional_tools item must be found regardless of index")
	}
	if !strings.Contains(string(arr), `"name":"f"`) {
		t.Fatalf("tool not lifted: %s", string(arr))
	}
}

// TestToolsInInputNoItem 钉「找不到就是 false，不算错误」。
func TestToolsInInputNoItem(t *testing.T) {
	body := []byte(`{"model":"x","input":[{"type":"message","role":"user"}],"tools":[{"type":"function","name":"f"}]}`)
	if arr, ok := ToolsInInput(body); ok || arr != nil {
		t.Fatalf("no additional_tools: got (%s, %v)", string(arr), ok)
	}
}

// TestHasUsableTopLevelTools 钉「顶 tools 是新版的非空数组」与「空/null/缺席
// 都视为可抬」之间的界线。
//
// 真值表的来由（lift.go 注释里有说）：Codex 0.155 在带 additional_tools 项的请求
// 里把 `tools` 写成 `null` 或 `[]`，那种情况应当按"我要用嵌套那份"处理。
func TestHasUsableTopLevelTools(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{`{"tools":[{"type":"function","name":"f"}]}`, true},
		{`{"tools":[]}`, false},
		{`{"tools":null}`, false},
		{`{"model":"x"}`, false},    // 缺席：没有「已存在的顶层 tools」可用
		{`{"tools":"oops"}`, false}, // 不是数组
	}
	for _, c := range cases {
		if got := HasUsableTopLevelTools([]byte(c.body)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.body, got, c.want)
		}
	}
}

// TestSetTopLevelTools 三态区分：缺席/空 → Insert；非空 → Replace。
func TestSetTopLevelTools(t *testing.T) {
	body := []byte(`{"model":"x"}`)
	tools := []byte(`[{"type":"function","name":"f"}]`)
	out, err := SetTopLevelTools(body, tools)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"tools":[`) {
		t.Fatalf("missing top-level tools: %s", string(out))
	}
	// 再设一次——此时 `tools` 已是数组，应当走 Replace 而不是再 Insert。
	out2, err := SetTopLevelTools(out, tools)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	_ = json.Unmarshal(out2, &m)
	// tools 字段必须是**一个**数组，不是两个
	if bytes.Count(out2, []byte(`"tools"`)) != 1 {
		t.Fatalf("`tools` appears more than once — Insert didn't detect existing value\n%s", string(out2))
	}
}

// TestApplyLiftFromInput0 钉「Apply 的完整路径」——这是真对外测接口。
//
// 输入有 `additional_tools`、顶层 `tools` 是 null → Apply 应抬；输入是新版形状
// （顶层非空数组）→ Apply 不动。
func TestApplyLiftFromInput0(t *testing.T) {
	body := []byte(`{"model":"x","input":[
		{"type":"additional_tools","tools":[
			{"type":"function","name":"f","description":"d"}
		]}
	],"tools":null}`)
	out, notes, err := ToolsLift{}.Apply(body, reqToolsLift())
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) == 0 {
		t.Fatalf("must report a note when it lifted something\n%s", string(out))
	}
	if !strings.Contains(string(out), `"tools":[{"type":"function","name":"f"`) {
		t.Fatalf("must have written top-level tools\n%s", string(out))
	}
	// input[0] 那一项必须原样保留——实测上游对额外的 additional_tools 项不
	// 反对，删它就是无依据的改动。
	if !strings.Contains(string(out), `"additional_tools"`) {
		t.Fatalf("must preserve input[0] (the additional_tools item)\n%s", string(out))
	}

	// 新版形状：顶层非空 tools → Apply 不动。
	new := []byte(`{"model":"x","tools":[{"type":"function","name":"f"}]}`)
	out2, notes2, err := ToolsLift{}.Apply(new, reqToolsLift())
	if err != nil {
		t.Fatal(err)
	}
	if string(out2) != string(new) || len(notes2) != 0 {
		t.Fatalf("新版形状必须不动: out=%s notes=%v", string(out2), notes2)
	}
}

// TestApplyEmptyArrayCountsNoLeaves 钉「空数组 → 原样转发」。
//
// `additional_tools.tools` 是 `[]` 也是「找不到可抬的」——叶子数 0 就当
// nothing，notes 不报、不替换顶层 tools。
func TestApplyEmptyArrayCountsNoLeaves(t *testing.T) {
	body := []byte(`{"model":"x","input":[
		{"type":"additional_tools","tools":[]}
	],"tools":null}`)
	out, notes, err := ToolsLift{}.Apply(body, reqToolsLift())
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(body) || len(notes) != 0 {
		t.Fatalf("empty array: must pass through\n out=%s\n notes=%v", string(out), notes)
	}
}

// TestApplyWrongAgent 是防回归——Match 只对 codex 客户端生效。
//
// 顺手验 Apply 的失败模式：错客户端时 Match 应当 return false，但 Apply 本身
// 是幂等的——调用者（special.Apply）在 Match 不通过时根本不会叫 Apply；这里
// 是直接测 Apply 在错的 Agent 下也安全（不会越权去抬一份非 codex 的嵌套形状）。
func TestApplyWrongAgent(t *testing.T) {
	body := []byte(`{"model":"x","input":[{"type":"additional_tools","tools":[]}]}`)
	r := &special.Request{Agent: "claude"} // 别的客户端
	out, notes, err := ToolsLift{}.Apply(body, r)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(body) || len(notes) != 0 {
		t.Fatalf("wrong agent must not lift anything\n out=%s\n notes=%v", string(out), notes)
	}
}
