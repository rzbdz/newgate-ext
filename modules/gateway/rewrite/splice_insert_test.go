package rewrite

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestInsertTopLevelRaw_KeepsEveryOtherByte 插入新 key 只能多出插入的那一段，
// 其余字节（缩进、key 顺序、大整数字面量、Unicode 转义）必须原样。
func TestInsertTopLevelRaw_KeepsEveryOtherByte(t *testing.T) {
	body := []byte(`{
  "model": "deepseek-chat",
  "big": 9007199254740993,
  "esc": "你好",
  "messages": [{"role":"user","content":"hi"}]
}`)
	out, err := InsertTopLevelRaw(body, "thinking", []byte(`{"type":"disabled"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"thinking":{"type":"disabled"},
  "model": "deepseek-chat",
  "big": 9007199254740993,
  "esc": "你好",
  "messages": [{"role":"user","content":"hi"}]
}`
	if string(out) != want {
		t.Fatalf("字节不一致\n got: %s\nwant: %s", out, want)
	}
	var v map[string]interface{}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
}

func TestInsertTopLevelRaw_EmptyObjectNeedsNoComma(t *testing.T) {
	out, err := InsertTopLevelRaw([]byte(`{}`), "thinking", []byte(`{"type":"disabled"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"thinking":{"type":"disabled"}}` {
		t.Fatalf("got %s", out)
	}
}

func TestInsertTopLevelRaw_RefusesDuplicate(t *testing.T) {
	if _, err := InsertTopLevelRaw([]byte(`{"thinking":{"type":"enabled"}}`),
		"thinking", []byte(`{"type":"disabled"}`)); err == nil {
		t.Fatal("已有同名 key 时必须报错，绝不能产生重复 key")
	}
}

func TestInsertTopLevelRaw_NotObject(t *testing.T) {
	if _, err := InsertTopLevelRaw([]byte(`[1,2]`), "a", []byte(`1`)); err == nil {
		t.Fatal("顶层不是对象时应报错")
	}
}

// TestEnsureArrayItemField_OnlyMatchingItems 只补 match 中的、且还没有该字段的元素。
func TestEnsureArrayItemField_OnlyMatchingItems(t *testing.T) {
	body := []byte(`{"model":"x","messages":[` +
		`{"role":"user","content":"a"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1"}]},` +
		`{"role":"user","content":"b"},` +
		`{"role":"assistant","reasoning_content":"已经有了","content":"c"},` +
		`{"role":"assistant","content":"d"}` +
		`]}`)

	out, n, err := EnsureArrayItemField(body, "messages", "reasoning_content",
		[]byte(`""`), func(item []byte) bool {
			r, _ := TopLevelString(item, "role")
			return r == "assistant"
		})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("应该补 2 条（第 3 条已有该字段），实际 %d", n)
	}
	want := `{"model":"x","messages":[` +
		`{"role":"user","content":"a"},` +
		`{"reasoning_content":"",` + `"role":"assistant","content":[{"type":"tool_use","id":"t1"}]},` +
		`{"role":"user","content":"b"},` +
		`{"role":"assistant","reasoning_content":"已经有了","content":"c"},` +
		`{"reasoning_content":"",` + `"role":"assistant","content":"d"}` +
		`]}`
	if string(out) != want {
		t.Fatalf("字节不一致\n got: %s\nwant: %s", out, want)
	}
	var v struct {
		Messages []map[string]interface{} `json:"messages"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
	for i, m := range v.Messages {
		if m["role"] != "assistant" {
			if _, has := m["reasoning_content"]; has {
				t.Fatalf("第 %d 条不是 assistant 却被补了", i)
			}
			continue
		}
		if m["reasoning_content"] == nil {
			t.Fatalf("第 %d 条 assistant 没有 reasoning_content", i)
		}
	}
	if v.Messages[3]["reasoning_content"] != "已经有了" {
		t.Fatal("原有的 reasoning_content 被覆盖了")
	}
}

// TestEnsureArrayItemField_NoopKeepsBytes 没东西要补时必须返回原切片、n=0。
func TestEnsureArrayItemField_NoopKeepsBytes(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"a"}]}`)
	out, n, err := EnsureArrayItemField(body, "messages", "reasoning_content",
		[]byte(`""`), func(item []byte) bool {
			r, _ := TopLevelString(item, "role")
			return r == "assistant"
		})
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if string(out) != string(body) {
		t.Fatalf("没改动却动了字节: %s", out)
	}
}

// TestEnsureArrayItemField_NestedRoleNotConfused 嵌套在 content 里的 "role"
// 不能被当成消息的 role，嵌套对象也不能被当成数组元素。
func TestEnsureArrayItemField_NestedRoleNotConfused(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"content":[{"role":"assistant","text":"这是内容里的字"}],"role":"user"}` +
		`]}`)
	out, n, err := EnsureArrayItemField(body, "messages", "reasoning_content",
		[]byte(`""`), func(item []byte) bool {
			r, _ := TopLevelString(item, "role")
			return r == "assistant"
		})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("这条是 user，不该被补，实际补了 %d 处: %s", n, out)
	}
}

// TestEnsureArrayItemField_WhitespaceAndEmptyItems 缩进、空对象元素、非对象元素。
func TestEnsureArrayItemField_WhitespaceAndEmptyItems(t *testing.T) {
	body := []byte("{\n  \"messages\": [\n    {},\n    \"裸字符串\",\n    { \"role\": \"assistant\" }\n  ]\n}")
	out, n, err := EnsureArrayItemField(body, "messages", "reasoning_content",
		[]byte(`""`), func(item []byte) bool {
			r, _ := TopLevelString(item, "role")
			return r == "assistant"
		})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("只有一条 assistant，实际 %d: %s", n, out)
	}
	if !strings.Contains(string(out), `{"reasoning_content":"" "role": "assistant" }`) &&
		!strings.Contains(string(out), `{"reasoning_content":"", "role": "assistant" }`) {
		t.Fatalf("插入位置不对: %s", out)
	}
	var v map[string]interface{}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("结果不是合法 JSON: %v\n%s", err, out)
	}
}

func TestEnsureArrayItemField_MissingKey(t *testing.T) {
	body := []byte(`{"model":"x"}`)
	out, n, err := EnsureArrayItemField(body, "messages", "reasoning_content",
		[]byte(`""`), nil)
	if err == nil {
		t.Fatal("没有 messages 时应报错，由调用方决定这是否要紧")
	}
	if n != 0 || string(out) != string(body) {
		t.Fatal("报错时不能动 body")
	}
}

func TestEnsureArrayItemField_NotArray(t *testing.T) {
	if _, _, err := EnsureArrayItemField([]byte(`{"messages":"nope"}`),
		"messages", "x", []byte(`""`), nil); err == nil {
		t.Fatal("messages 不是数组时应报错")
	}
}
