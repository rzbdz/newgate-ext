package rewrite

import (
	"encoding/json"
	"strings"
	"testing"
)

const probeUserItem = `{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}`

// 非空数组：补在收尾 ] 之前，其余每一个字节原样保留（缩进、key 顺序、
// 数字字面量、以及那条已有 item 自己）。
func TestAppendTopLevelArrayItemNonEmpty(t *testing.T) {
	body := []byte("{\n  \"model\": \"deepseek-flash\",\n  \"input\": [\n" +
		"    {\"type\": \"function_call_output\", \"call_id\": \"call_faedac\", \"output\": \"x\"}\n" +
		"  ],\n  \"max_output_tokens\": 1e10\n}")

	out, changed, err := AppendTopLevelArrayItem(body, "input", []byte(probeUserItem))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !changed {
		t.Fatalf("该插的地方没插")
	}
	if !json.Valid(out) {
		t.Fatalf("结果不是合法 JSON:\n%s", out)
	}
	var parsed struct {
		Model string            `json:"model"`
		Input []json.RawMessage `json:"input"`
		Max   json.RawMessage   `json:"max_output_tokens"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("解析失败: %v\n%s", err, out)
	}
	if len(parsed.Input) != 2 {
		t.Fatalf("input 应该有 2 项，得到 %d:\n%s", len(parsed.Input), out)
	}
	if string(parsed.Input[1]) != probeUserItem {
		t.Fatalf("补进去的不是那条 item:\n%s", parsed.Input[1])
	}
	// 逐字保留：原有 item、缩进、1e10 的字面量、顶层 key 顺序
	if !strings.Contains(string(out), `{"type": "function_call_output", "call_id": "call_faedac", "output": "x"}`) {
		t.Fatalf("原有 item 被动了:\n%s", out)
	}
	if !strings.Contains(string(out), "\n    {") && !strings.Contains(string(out), "    {") {
		t.Fatalf("原来的缩进丢了:\n%s", out)
	}
	if !strings.Contains(string(out), `"max_output_tokens": 1e10`) {
		t.Fatalf("数字字面量被改写了:\n%s", out)
	}
	if strings.Index(string(out), `"model"`) > strings.Index(string(out), `"input"`) {
		t.Fatalf("顶层 key 顺序变了:\n%s", out)
	}
}

// 空数组与「只有空白」的数组都不补逗号（补出来是 `[,x]`，不是合法 JSON）；
// 非数组、缺 key、不是对象一律**不动**且如实报错，交给调用方按 fail-open 处理。
func TestAppendTopLevelArrayItemEdges(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantErr  bool
		wantMove bool
	}{
		{"空数组", `{"input":[]}`, false, true},
		{"只有空白", `{"input":[  ]}`, false, true},
		{"不是数组", `{"input":{}}`, true, false},
		{"缺这个 key", `{"input2":[]}`, true, false},
		{"顶层不是对象", `[]`, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, changed, err := AppendTopLevelArrayItem([]byte(tc.body), "input", []byte(probeUserItem))
			if tc.wantErr && err == nil {
				t.Fatalf("该报错没报")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("不该报错: %v", err)
			}
			if changed != tc.wantMove {
				t.Fatalf("changed=%v，想要 %v", changed, tc.wantMove)
			}
			if !changed {
				if string(out) != tc.body {
					t.Fatalf("不动的时候必须原样返回:\n%s", out)
				}
				return
			}
			if !json.Valid(out) {
				t.Fatalf("结果不是合法 JSON:\n%s", out)
			}
			var parsed struct {
				Input []json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(out, &parsed); err != nil {
				t.Fatalf("解析失败: %v\n%s", err, out)
			}
			if len(parsed.Input) != 1 || string(parsed.Input[0]) != probeUserItem {
				t.Fatalf("空数组该只补出新那一项:\n%s", out)
			}
		})
	}
}
