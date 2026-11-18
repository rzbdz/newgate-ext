package rewrite

import (
	"encoding/json"
	"strings"
	"testing"
)

// 插入点必须在子数组**开头**，且除了插进去的那段，其余每个字节原样保留。
func TestEnsureArrayItemArrayHead(t *testing.T) {
	body := []byte("{\n  \"messages\": [\n" +
		"    {\"role\": \"user\", \"content\": [{\"type\":\"text\",\"text\":\"a\"}]},\n" +
		"    {\"role\": \"assistant\", \"content\": [{\"type\":\"text\",\"text\":\"b\"}]}\n" +
		"  ],\n  \"max_tokens\": 1e10\n}")

	isAsst := func(item []byte) bool { r, _ := TopLevelString(item, "role"); return r == "assistant" }

	out, n, err := EnsureArrayItemArrayHead(body, "messages", "content",
		[]byte(`{"type":"thinking","thinking":""}`), isAsst, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 1 {
		t.Fatalf("插了 %d 条，想要 1", n)
	}
	if !json.Valid(out) {
		t.Fatalf("结果不是合法 JSON:\n%s", out)
	}
	if !strings.Contains(string(out),
		`"content": [{"type":"thinking","thinking":""},{"type":"text","text":"b"}]`) {
		t.Fatalf("没插在开头:\n%s", out)
	}
	// user 那条与顶层其余字节不动：缩进、key 顺序、1e10 的字面量都在
	if !strings.Contains(string(out), `{"role": "user", "content": [{"type":"text","text":"a"}]}`) {
		t.Fatalf("user 消息被动了:\n%s", out)
	}
	if !strings.Contains(string(out), `"max_tokens": 1e10`) {
		t.Fatalf("数字字面量被改写了:\n%s", out)
	}
}

// 空数组不需要补逗号；need 返回 false 的一律不碰，且返回原字节。
func TestEnsureArrayItemArrayHeadEdges(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":[]}]}`)
	out, n, err := EnsureArrayItemArrayHead(body, "messages", "content",
		[]byte(`{"type":"thinking"}`), nil, nil)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if string(out) != `{"messages":[{"role":"assistant","content":[{"type":"thinking"}]}]}` {
		t.Fatalf("空数组插错了:\n%s", out)
	}

	never := func([]byte) bool { return false }
	out, n, err = EnsureArrayItemArrayHead(body, "messages", "content",
		[]byte(`{"type":"thinking"}`), nil, never)
	if err != nil || n != 0 || string(out) != string(body) {
		t.Fatalf("need=false 时不该动: n=%d err=%v\n%s", n, err, out)
	}
}

func TestArrayItems(t *testing.T) {
	items, ok := ArrayItems([]byte(`[ {"a":[1,2]} , "s" , 3 ]`))
	if !ok {
		t.Fatal("扫不动")
	}
	want := []string{`{"a":[1,2]}`, `"s"`, `3`}
	if len(items) != len(want) {
		t.Fatalf("切出 %d 个，想要 %d", len(items), len(want))
	}
	for i, w := range want {
		if string(items[i]) != w {
			t.Fatalf("第 %d 个 = %s，想要 %s", i, items[i], w)
		}
	}
	if _, ok := ArrayItems([]byte(`{"not":"array"}`)); ok {
		t.Fatal("对象被当成数组了")
	}
}

func TestAppendLastArrayItemArray(t *testing.T) {
	body := []byte("{\n  \"messages\":[" +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1"}]}` +
		`],"max_tokens":9007199254740993}` + "\n")
	isUser := func(item []byte) bool {
		role, _ := TopLevelString(item, "role")
		return role == "user"
	}
	block := []byte(`{"type":"text","text":"continue"}`)
	out, changed, err := AppendLastArrayItemArray(body, "messages", "content", block, isUser)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !json.Valid(out) {
		t.Fatalf("结果不是合法 JSON:\n%s", out)
	}
	if !strings.Contains(string(out),
		`{"type":"tool_result","tool_use_id":"t1"},{"type":"text","text":"continue"}`) {
		t.Fatalf("没有追加到最后一条消息的 content 尾部:\n%s", out)
	}
	if !strings.Contains(string(out), `"max_tokens":9007199254740993`) {
		t.Fatalf("无关的大整数被改写:\n%s", out)
	}
}

// AppendArrayItemArrayAt 按下标挑元素。存在的理由见函数注释：要追加的那条消息
// **不是**数组的最后一项（Claude Code 会在 tool_result 之后追加一条 role:"system"
// 的插话，现场 dump/err-400-req000464）。
func TestAppendArrayItemArrayAt(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1"}]},` +
		`{"role":"system","content":[{"type":"text","text":"插话"}]}` +
		`],"max_tokens":9007199254740993}`)
	block := []byte(`{"type":"text","text":"continue"}`)

	out, changed, err := AppendArrayItemArrayAt(body, "messages", "content", 0, block)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !json.Valid(out) {
		t.Fatalf("结果不是合法 JSON:\n%s", out)
	}
	// 追加在第 0 项（user 那条）的 content 尾部，system 仍在它后面。
	if !strings.Contains(string(out),
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1"},{"type":"text","text":"continue"}]}`) {
		t.Fatalf("没有追加到第 0 项的 content 尾部:\n%s", out)
	}
	if !strings.Contains(string(out), `{"role":"system","content":[{"type":"text","text":"插话"}]}`) {
		t.Fatalf("后面那条 system 被动了:\n%s", out)
	}
	if !strings.Contains(string(out), `"max_tokens":9007199254740993`) {
		t.Fatalf("无关的大整数被改写:\n%s", out)
	}

	// 下标越界、元素不是对象：什么都不做，且**返回原字节**（不是拷贝）。
	for _, tc := range []struct {
		name string
		idx  int
	}{
		{"越界（正）", 2}, {"越界（负）", -1},
	} {
		out, changed, err := AppendArrayItemArrayAt(body, "messages", "content", tc.idx, block)
		if err != nil || changed || string(out) != string(body) {
			t.Fatalf("%s：changed=%v err=%v\n%s", tc.name, changed, err, out)
		}
	}

	// 元素不是对象（顶层数组里混了标量）时跳过，不 panic 也不改字节。
	scalar := []byte(`{"messages":[1,{"role":"user","content":[]}]}`)
	out, changed, err = AppendArrayItemArrayAt(scalar, "messages", "content", 0, block)
	if err != nil || changed || string(out) != string(scalar) {
		t.Fatalf("标量元素：changed=%v err=%v\n%s", changed, err, out)
	}

	// content 是字符串（没有子数组可追加）：按「形状不认识」报 errNotArray。
	strContent := []byte(`{"messages":[{"role":"user","content":"纯文本"}]}`)
	if _, _, err := AppendArrayItemArrayAt(strContent, "messages", "content", 0, block); err == nil {
		t.Fatal("content 是字符串时应当报 errNotArray")
	}

	// 空 content 数组：不加前导逗号。
	empty := []byte(`{"messages":[{"role":"user","content":[]}]}`)
	out, changed, err = AppendArrayItemArrayAt(empty, "messages", "content", 0, block)
	if err != nil || !changed {
		t.Fatalf("空数组：changed=%v err=%v", changed, err)
	}
	if string(out) != `{"messages":[{"role":"user","content":[{"type":"text","text":"continue"}]}]}` {
		t.Fatalf("空数组追加错了:\n%s", out)
	}
}
