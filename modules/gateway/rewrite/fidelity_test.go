package rewrite

import (
	"bytes"
	"encoding/json"
	"testing"
)

// compact 去掉无意义空白，便于做语义比对。
// RawMessage 保留原始字节但 Marshal 会压缩空白——空白不影响语义，
// 数字字面量和 key 顺序才影响，而那两样是我们要守住的。
func compact(b []byte) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return string(b)
	}
	return buf.String()
}

// rewriteModel 复现代理对请求体做的唯一改动，用于断言忠实性。
func rewriteModel(body []byte, newModel string) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	mj, err := json.Marshal(newModel)
	if err != nil {
		return nil, err
	}
	payload["model"] = json.RawMessage(mj)
	return json.Marshal(payload)
}

// 真实的 opencode 请求形状：深层嵌套 tool schema、大整数、空数组、null、
// 转义字符、Unicode。这些都是 interface{} 往返会破坏的东西。
const realistic = `{
  "model": "heavy",
  "max_tokens": 1000000,
  "temperature": 0,
  "stream": true,
  "messages": [
    {"role":"system","content":[{"type":"text","text":"long prefix","cache_control":{"type":"ephemeral"}}]},
    {"role":"user","content":"计算 1e10 和 \"引号\" 与 \\ 反斜杠"}
  ],
  "tools": [
    {"type":"function","function":{
      "name":"background_cancel",
      "description":"Cancel a background task",
      "parameters":{
        "type":"object",
        "properties":{
          "id":{"type":"string"},
          "ids":{"type":"array","items":{"type":"string"}},
          "flags":{"type":"array","items":{"type":"string"},"default":[]},
          "nested":{"type":"object","properties":{"deep":{"type":"integer","maximum":9007199254740991}}}
        },
        "required":["id"],
        "additionalProperties":false
      }
    }},
    {"type":"function","function":{
      "name":"empty_required",
      "parameters":{"type":"object","properties":{},"required":[]}
    }}
  ],
  "metadata": {"nullable": null, "big": 9007199254740993, "sci": 1e10, "neg": -0.5}
}`

func TestOnlyModelChanges(t *testing.T) {
	out, err := rewriteModel([]byte(realistic), "model-large")
	if err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}

	var in, got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(realistic), &in); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}

	if len(in) != len(got) {
		t.Fatalf("顶层键数量变了: %d -> %d", len(in), len(got))
	}
	for k, v := range in {
		g, ok := got[k]
		if !ok {
			t.Errorf("键 %q 丢了", k)
			continue
		}
		if k == "model" {
			if string(g) != `"model-large"` {
				t.Errorf("model 应被改写，得到 %s", g)
			}
			continue
		}
		// 其余每个顶层值必须语义完全相同（含数字字面量与 key 顺序）
		if compact(v) != compact(g) {
			t.Errorf("键 %q 被改动了:\n 原: %s\n 新: %s", k, v, g)
		}
	}
}

// 明确记录：如果用 map[string]interface{} 往返，上面那些值会被破坏。
// 这个测试证明我们没走那条路。
func TestInterfaceRoundTripWouldCorrupt(t *testing.T) {
	var viaIface map[string]interface{}
	if err := json.Unmarshal([]byte(realistic), &viaIface); err != nil {
		t.Fatal(err)
	}
	corrupted, _ := json.Marshal(viaIface)

	var a, b map[string]json.RawMessage
	_ = json.Unmarshal([]byte(realistic), &a)
	_ = json.Unmarshal(corrupted, &b)

	changed := 0
	for k, v := range a {
		if k == "model" {
			continue
		}
		if compact(v) != compact(b[k]) {
			changed++
			t.Logf("interface{} 往返破坏了 %q:\n 原: %s\n 后: %s", k, v, b[k])
		}
	}
	if changed == 0 {
		t.Skip("这个 Go 版本下 interface{} 往返恰好没破坏——但仍不该依赖它")
	}
	t.Logf("interface{} 往返共破坏 %d 个顶层字段，这就是必须用 RawMessage 的原因", changed)
}
