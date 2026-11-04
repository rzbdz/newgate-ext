package hermes

import (
	"strings"
	"testing"
)

// 字节手术这一族测的是：该改的改对、别的逐字节不动、改两次和改一次一样。
//
// sampleYAML 是 hermes 0.13 自己落盘的形状（2026-11-04 本机 ~/.hermes/config.yaml
// 的节选，密钥换成了占位）：块风格、两格缩进、空映射写成 `{}`、列表缩进在父键下。
const sampleYAML = `model:
  provider: custom
  model: claude-opus-5
  context_length: 1000000
  default: claude-opus-5
  base_url: https://api.example.com:60000/
  api_key: sk-placeholder
  api_mode: anthropic_messages
providers: {}
fallback_providers: []
toolsets:
  - hermes-cli
agent:
  max_turns: 90
  personalities:
    helpful: You are a helpful, friendly AI assistant.
    pirate: "Arrr! Ye be talkin' to Captain Hermes: yo ho ho!"
auxiliary:
  vision:
    provider: smt-vision
    model: gpt-4o
    base_url: ""
    api_key: ""
    timeout: 120
    extra_body: {}
  compression:
    provider: auto
    model: ""
    timeout: 120
delegation:
  model: claude-sonnet-4.6
  provider: ""
  max_iterations: 50
  default_toolsets:
    - terminal
    - file
smart_model_routing:
  enabled: true
  cheap_model:
    provider: smt-codex
    model: gemini-3-flash-preview
custom_providers:
  - name: smt-claude
    base_url: https://api.example.com:60000/
    models:
      claude-opus-5:
        context_length: 1000000
`

func apply(t *testing.T, src string, edits ...edit) string {
	t.Helper()
	d := parseDoc(src)
	for _, e := range edits {
		if _, err := d.put(e); err != nil {
			t.Fatalf("put %v: %v", e.Path, err)
		}
	}
	return d.String()
}

func TestPutScalarTouchesOnlyThatLine(t *testing.T) {
	out := apply(t, sampleYAML, edit{Path: []string{"model", "default"}, Value: "normal"})
	want := strings.Replace(sampleYAML, "  default: claude-opus-5\n", "  default: normal\n", 1)
	if out != want {
		t.Errorf("只该改一行\n--- got ---\n%s", out)
	}
}

// 子映射里同名的键（auxiliary.vision.model 与顶层 model.model）不许认错。
func TestPutNestedDoesNotConfuseSameNamedKeys(t *testing.T) {
	out := apply(t, sampleYAML, edit{Path: []string{"auxiliary", "vision", "model"}, Value: "vision"})
	if !strings.Contains(out, "  model: claude-opus-5\n") {
		t.Errorf("顶层 model.model 不该动\n%s", out)
	}
	if !strings.Contains(out, "    model: vision\n") || strings.Contains(out, "gpt-4o") {
		t.Errorf("auxiliary.vision.model 该改\n%s", out)
	}
	if !strings.Contains(out, "  compression:\n    provider: auto\n") {
		t.Errorf("兄弟块不该动\n%s", out)
	}
}

// `providers: {}` 是 hermes 写空映射的方式：往里补键要先把它变成块。
func TestPutIntoEmptyFlowMapping(t *testing.T) {
	out := apply(t, sampleYAML, edit{Path: []string{"providers", "newgate"},
		Value: omap{{"name", "newgate"}, {"base_url", "http://127.0.0.1:8899/a/hermes/v1"}}})
	if !strings.Contains(out, "providers:\n  newgate:\n    name: newgate\n    base_url: \"http://127.0.0.1:8899/a/hermes/v1\"\nfallback_providers: []\n") {
		t.Errorf("provider 块该插在 providers 下\n%s", out)
	}
}

// 原来是块、现在写成标量：整块换掉，不留孤儿子行。
func TestPutReplacesWholeBlock(t *testing.T) {
	out := apply(t, sampleYAML, edit{Path: []string{"smart_model_routing", "cheap_model"},
		Value: omap{{"provider", "newgate"}, {"model", "light"}}})
	if strings.Contains(out, "gemini-3-flash-preview") || strings.Count(out, "cheap_model:") != 1 {
		t.Errorf("旧块该整块换掉\n%s", out)
	}
	if !strings.Contains(out, "  cheap_model:\n    provider: newgate\n    model: light\ncustom_providers:") {
		t.Errorf("新块的位置/缩进不对\n%s", out)
	}
}

func TestPutMissingParentsAreCreated(t *testing.T) {
	out := apply(t, "a: 1\n", edit{Path: []string{"x", "y", "z"}, Value: 3})
	if out != "a: 1\nx:\n  y:\n    z: 3\n" {
		t.Errorf("got\n%s", out)
	}
}

func TestPutIsIdempotent(t *testing.T) {
	edits := []edit{
		{Path: []string{"model", "default"}, Value: "normal"},
		{Path: []string{"model", "base_url"}, Value: "http://127.0.0.1:8899/a/hermes/v1"},
		{Path: []string{"providers", "newgate"}, Value: omap{{"models", omap{{"normal", omap{{"context_length", 200000}}}}}}},
		{Path: []string{"delegation", "default_toolsets"}, Value: []any{"terminal"}},
	}
	once := apply(t, sampleYAML, edits...)
	d := parseDoc(once)
	for _, e := range edits {
		changed, err := d.put(e)
		if err != nil || changed {
			t.Errorf("第二次 put %v 不该再改（changed=%v err=%v）", e.Path, changed, err)
		}
	}
	if d.String() != once {
		t.Errorf("两次结果不同")
	}
}

// 一个非空标量不能被当成映射往里写——那是丢别人的值。
func TestPutRefusesToTurnAValueIntoAMapping(t *testing.T) {
	d := parseDoc("model: claude-opus-5\n")
	if _, err := d.put(edit{Path: []string{"model", "default"}, Value: "normal"}); err == nil {
		t.Errorf("model 是一个字符串（hermes 的旧写法），往里塞子键该拒绝")
	}
}

func TestRemove(t *testing.T) {
	out := apply(t, sampleYAML, edit{Path: []string{"model", "api_key"}})
	if strings.Contains(out, "api_key: sk-placeholder") {
		t.Errorf("没删掉\n%s", out)
	}
	if !strings.Contains(out, "  base_url: https://api.example.com:60000/\n  api_mode:") {
		t.Errorf("邻居该原样接上\n%s", out)
	}
}

func TestLitQuotesWhatYAMLWouldReinterpret(t *testing.T) {
	for in, want := range map[string]string{
		"normal":                     "normal",
		"newgate-ds":                 "newgate-ds",
		"smt-claude/claude-opus-5-5": "smt-claude/claude-opus-5-5",
		"http://127.0.0.1:8899/v1":   `"http://127.0.0.1:8899/v1"`,
		"yes":                        `"yes"`,
		"123":                        `"123"`,
		"":                           `""`,
		"a: b":                       `"a: b"`,
	} {
		if got := lit(in); got != want {
			t.Errorf("lit(%q) = %s，想要 %s", in, got, want)
		}
	}
}

func TestChildrenAndGet(t *testing.T) {
	d := parseDoc(sampleYAML)
	if got := strings.Join(d.children("auxiliary"), ","); got != "vision,compression" {
		t.Errorf("children = %s", got)
	}
	if v, _ := d.get("agent", "personalities", "pirate"); !strings.HasPrefix(v, "Arrr! Ye be talkin'") {
		t.Errorf("带引号的值该去掉引号读回：%q", v)
	}
}
