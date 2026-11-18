package codex

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/gateway/special"
)

func init() {
	special.Register(ToolsLift{})
}

// reqToolsLift builds a minimal usable special.Request; ToolsLift.Apply only reads Agent.
func reqToolsLift() *special.Request { return &special.Request{Agent: ID} }

// toolFactory builds one tool declaration -- the shape follows what Codex 0.155.1 actually
// sends. One each of the three types, to pin the difference between leaves and containers.
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

// nestTools builds an `input[0].additional_tools` array (a bare array, for FlattenToolTree tests).
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

// nestInsideNamespace wraps several tools in a `namespace` container, to pin the recursion.
func nestInsideNamespace(name string, tools [][]byte) []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"type":"namespace","name":"`)
	buf.WriteString(name)
	buf.WriteString(`","tools":`)
	buf.Write(nestTools(tools))
	buf.WriteByte('}')
	return buf.Bytes()
}

// TestFlattenToolTreeNamespaceFlattened pins the core of "namespace flattening".
//
// `namespace` is the Codex client's grouping (functions / collaboration),
// with no corresponding concept upstream -- flatten it into one tools array,
// leaves untouched byte for byte. For where this rule comes from, see lift.go.
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
	// Leaf bytes must be preserved verbatim -- this is where "the request body
	// does not do a JSON round trip" lands on lift. string.Contains rather
	// than JSON parsing, because the point is to verify **byte fidelity**.
	got := string(out)
	for _, want := range leaves {
		if !strings.Contains(got, string(want)) {
			t.Fatalf("lost a leaf: %s\nin output: %s", string(want), got)
		}
	}
}

// TestFlattenToolTreeUnknownKindPassThrough pins "do not touch an unknown type".
//
// The day Codex adds a new tool type, what is lifted is the raw bytes it wrote, not a shape we
// guessed. Any `type` other than function/custom/web_search is treated as a leaf, bytes untouched.
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

// TestFlattenToolTreeEmptyInput is not "already lifted" -- paired with the leafCount=0 contract.
func TestFlattenToolTreeEmptyInput(t *testing.T) {
	out, n := FlattenToolTree([]byte(`[]`))
	if n != 0 || out != nil {
		t.Fatalf("empty input: got (%s, %d), want (nil, 0)", string(out), n)
	}
}

// TestToolsInInputFindsItem pins "which item is the input[].additional_tools one".
//
// Order: Codex's `input[]` does not necessarily put additional_tools at index 0; it
// may follow a user message. Find it by the type field, not by guessing the index.
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

// TestToolsInInputNoItem pins "not found means false, not an error".
func TestToolsInInputNoItem(t *testing.T) {
	body := []byte(`{"model":"x","input":[{"type":"message","role":"user"}],"tools":[{"type":"function","name":"f"}]}`)
	if arr, ok := ToolsInInput(body); ok || arr != nil {
		t.Fatalf("no additional_tools: got (%s, %v)", string(arr), ok)
	}
}

// TestHasUsableTopLevelTools pins the dividing line between "the top tools is
// the new-shape non-empty array" and "empty/null/absent all count as liftable".
//
// Where the truth table comes from (explained in the lift.go comment): Codex
// 0.155 writes `tools` as `null` or `[]` on requests carrying an additional_tools
// item, and that case should be handled as "I want to use the nested one".
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

// TestSetTopLevelTools distinguishes three states: absent/empty -> Insert; non-empty -> Replace.
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
	// Set it once more -- `tools` is now an array, so it should go Replace rather than Insert again.
	out2, err := SetTopLevelTools(out, tools)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	_ = json.Unmarshal(out2, &m)
	// The tools field must be **one** array, not two
	if bytes.Count(out2, []byte(`"tools"`)) != 1 {
		t.Fatalf("`tools` appears more than once — Insert didn't detect existing value\n%s", string(out2))
	}
}

// TestApplyLiftFromInput0 pins "Apply's full path" -- this is the real outward-facing interface.
//
// Input has `additional_tools` and the top-level `tools` is null -> Apply should
// lift; input is the new shape (top-level non-empty array) -> Apply does nothing.
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
	// The input[0] item must be preserved as is -- measured, the upstream does not object
	// to an extra additional_tools item, so deleting it would be a change with no basis.
	if !strings.Contains(string(out), `"additional_tools"`) {
		t.Fatalf("must preserve input[0] (the additional_tools item)\n%s", string(out))
	}

	// New shape: top-level non-empty tools -> Apply does nothing.
	new := []byte(`{"model":"x","tools":[{"type":"function","name":"f"}]}`)
	out2, notes2, err := ToolsLift{}.Apply(new, reqToolsLift())
	if err != nil {
		t.Fatal(err)
	}
	if string(out2) != string(new) || len(notes2) != 0 {
		t.Fatalf("the new-style shape must stay untouched: out=%s notes=%v", string(out2), notes2)
	}
}

// TestApplyEmptyArrayCountsNoLeaves pins "empty array -> forward as is".
//
// `additional_tools.tools` being `[]` also means "nothing to lift" -- a leaf count of 0
// is treated as nothing, notes are not reported, and the top-level tools is not replaced.
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

// TestApplyWrongAgent is a regression guard -- Match only applies to the codex client.
//
// It also checks Apply's failure mode: with the wrong client Match should return
// false, but Apply itself is idempotent -- the caller (special.Apply) never calls
// Apply at all when Match does not pass; here we test directly that Apply is also
// safe under the wrong Agent (it will not overstep and lift a non-codex nested shape).
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
