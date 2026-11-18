package rewrite

import (
	"strings"
	"testing"
)

// 真实形状：奇怪的缩进、非字母序的 key、嵌套 tools 里也有 "model" 键、
// 大整数、科学计数、null、空数组、转义引号、Unicode、cache_control。
const raw = `{
  "stream": true,
  "model": "heavy",
  "max_tokens": 1000000,
  "metadata": {"big": 9007199254740993, "sci": 1e10, "nil": null, "neg": -0.5},
  "messages": [
    {"role":"system","content":[{"type":"text","text":"prefix","cache_control":{"type":"ephemeral"}}]},
    {"role":"user","content":"with \"quotes\" and \\ backslashes and CJK"}
  ],
  "tools": [
    {"type":"function","function":{
       "name":"background_cancel",
       "parameters":{"type":"object","properties":{
          "model":{"type":"string","description":"a nested model key, must never be touched"},
          "flags":{"type":"array","items":{"type":"string"},"default":[]}
       },"required":[],"additionalProperties":false}
    }}
  ],
  "a_last_key": "the value also contains \"model\": \"fake\" and it must not be touched either"
}`

func TestSpliceOnlyTouchesTopLevelModel(t *testing.T) {
	out, err := ReplaceTopLevelString([]byte(raw), "model", "model-large")
	if err != nil {
		t.Fatalf("splice failed: %v", err)
	}

	// 期望：把原文里那一处 `"heavy"` 换成 `"model-large"`，别的字节全不变
	want := strings.Replace(raw, `"model": "heavy"`, `"model": "model-large"`, 1)
	if string(out) != want {
		t.Errorf("not a byte-exact change of model only\n--- want ---\n%s\n--- got ---\n%s", want, out)
	}
}

func TestSplicePreservesEverythingElseByteForByte(t *testing.T) {
	out, err := ReplaceTopLevelString([]byte(raw), "model", "x")
	if err != nil {
		t.Fatal(err)
	}
	// 逐字节核对：除了 model 值那一段，前后缀必须完全一致
	pre := raw[:strings.Index(raw, `"heavy"`)]
	post := raw[strings.Index(raw, `"heavy"`)+len(`"heavy"`):]
	if !strings.HasPrefix(string(out), pre) {
		t.Error("bytes before model were changed")
	}
	if !strings.HasSuffix(string(out), post) {
		t.Error("bytes after model were changed")
	}
	// 嵌套的同名键必须原封不动
	for _, must := range []string{
		`"model":{"type":"string","description":"a nested model key, must never be touched"}`,
		`"big": 9007199254740993`,
		`"sci": 1e10`,
		`"required":[]`,
		`"cache_control":{"type":"ephemeral"}`,
		`"the value also contains \"model\": \"fake\" and it must not be touched either"`,
	} {
		if !strings.Contains(string(out), must) {
			t.Errorf("this span should be preserved as is but is gone: %s", must)
		}
	}
}

func TestSpliceKeyOrderAndWhitespaceIntact(t *testing.T) {
	out, _ := ReplaceTopLevelString([]byte(raw), "model", "m")
	// 顶层第一个键仍是 stream（Marshal 会把它排到 a_last_key 后面）
	i0 := strings.Index(string(out), `"stream"`)
	i1 := strings.Index(string(out), `"a_last_key"`)
	if i0 < 0 || i1 < 0 || i0 > i1 {
		t.Error("the top-level key order changed")
	}
	if !strings.Contains(string(out), "{\n  \"stream\": true,\n") {
		t.Error("indentation/whitespace was collapsed")
	}
}

func TestTopLevelReaders(t *testing.T) {
	if m, ok := TopLevelString([]byte(raw), "model"); !ok || m != "heavy" {
		t.Errorf("TopLevelString = %q %v", m, ok)
	}
	if !TopLevelBool([]byte(raw), "stream") {
		t.Error("stream should be true")
	}
	if TopLevelBool([]byte(raw), "nope") {
		t.Error("a missing key should be false")
	}
	// 嵌套的 model 不该被读到顶层
	nested := `{"tools":[{"model":"inner"}],"model":"outer"}`
	if m, _ := TopLevelString([]byte(nested), "model"); m != "outer" {
		t.Errorf("read a nested model: %q", m)
	}
}

func TestSpliceErrors(t *testing.T) {
	if _, err := ReplaceTopLevelString([]byte(`[1,2]`), "model", "x"); err == nil {
		t.Error("a non-object top level should error")
	}
	if _, err := ReplaceTopLevelString([]byte(`{"a":1}`), "model", "x"); err == nil {
		t.Error("a missing key should error")
	}
	if _, err := ReplaceTopLevelString([]byte(`{"model":123}`), "model", "x"); err == nil {
		t.Error("a non-string value should error")
	}
}
