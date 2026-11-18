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
    {"role":"user","content":"带 \"引号\" 和 \\ 反斜杠 和 中文"}
  ],
  "tools": [
    {"type":"function","function":{
       "name":"background_cancel",
       "parameters":{"type":"object","properties":{
          "model":{"type":"string","description":"嵌套的 model 键，绝不能被动"},
          "flags":{"type":"array","items":{"type":"string"},"default":[]}
       },"required":[],"additionalProperties":false}
    }}
  ],
  "a_last_key": "值里也有 \"model\": \"fake\" 这种字符串，也不能被动"
}`

func TestSpliceOnlyTouchesTopLevelModel(t *testing.T) {
	out, err := ReplaceTopLevelString([]byte(raw), "model", "model-large")
	if err != nil {
		t.Fatalf("splice 失败: %v", err)
	}

	// 期望：把原文里那一处 `"heavy"` 换成 `"model-large"`，别的字节全不变
	want := strings.Replace(raw, `"model": "heavy"`, `"model": "model-large"`, 1)
	if string(out) != want {
		t.Errorf("不是逐字节只改了 model\n--- 期望 ---\n%s\n--- 实际 ---\n%s", want, out)
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
		t.Error("model 之前的字节被改动了")
	}
	if !strings.HasSuffix(string(out), post) {
		t.Error("model 之后的字节被改动了")
	}
	// 嵌套的同名键必须原封不动
	for _, must := range []string{
		`"model":{"type":"string","description":"嵌套的 model 键，绝不能被动"}`,
		`"big": 9007199254740993`,
		`"sci": 1e10`,
		`"required":[]`,
		`"cache_control":{"type":"ephemeral"}`,
		`"值里也有 \"model\": \"fake\" 这种字符串，也不能被动"`,
	} {
		if !strings.Contains(string(out), must) {
			t.Errorf("这段应原样保留但没了: %s", must)
		}
	}
}

func TestSpliceKeyOrderAndWhitespaceIntact(t *testing.T) {
	out, _ := ReplaceTopLevelString([]byte(raw), "model", "m")
	// 顶层第一个键仍是 stream（Marshal 会把它排到 a_last_key 后面）
	i0 := strings.Index(string(out), `"stream"`)
	i1 := strings.Index(string(out), `"a_last_key"`)
	if i0 < 0 || i1 < 0 || i0 > i1 {
		t.Error("顶层 key 顺序变了")
	}
	if !strings.Contains(string(out), "{\n  \"stream\": true,\n") {
		t.Error("缩进/空白被压掉了")
	}
}

func TestTopLevelReaders(t *testing.T) {
	if m, ok := TopLevelString([]byte(raw), "model"); !ok || m != "heavy" {
		t.Errorf("TopLevelString = %q %v", m, ok)
	}
	if !TopLevelBool([]byte(raw), "stream") {
		t.Error("stream 该是 true")
	}
	if TopLevelBool([]byte(raw), "nope") {
		t.Error("不存在的键该是 false")
	}
	// 嵌套的 model 不该被读到顶层
	nested := `{"tools":[{"model":"inner"}],"model":"outer"}`
	if m, _ := TopLevelString([]byte(nested), "model"); m != "outer" {
		t.Errorf("读到了嵌套的 model: %q", m)
	}
}

func TestSpliceErrors(t *testing.T) {
	if _, err := ReplaceTopLevelString([]byte(`[1,2]`), "model", "x"); err == nil {
		t.Error("顶层不是对象该报错")
	}
	if _, err := ReplaceTopLevelString([]byte(`{"a":1}`), "model", "x"); err == nil {
		t.Error("没有该键该报错")
	}
	if _, err := ReplaceTopLevelString([]byte(`{"model":123}`), "model", "x"); err == nil {
		t.Error("值不是字符串该报错")
	}
}
