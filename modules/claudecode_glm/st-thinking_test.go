package claudecode_glm

import (
	"encoding/json"
	"strings"
	"testing"

	claudeapi "github.com/rzbdz/newgate-ext/modules/claudecode"
	"github.com/rzbdz/newgate-ext/modules/glm"
	glmapi "github.com/rzbdz/newgate-ext/modules/glm"
	"github.com/rzbdz/newgate/modules/gateway/special"
	thinkingmodule "github.com/rzbdz/newgate/modules/thinking"
)

func testPlugin() thinking {
	return thinking{
		client: claudeapi.Client{AgentID: "claude"},
		model:  glmapi.Model{MatchTarget: glm.MatchTarget},
	}
}

func req(model, provider, baseURL string) *special.Request {
	return &special.Request{
		Model: model, Provider: provider, BaseURL: baseURL,
		Protocol: "anthropic", Path: "/messages", Agent: "claude",
	}
}

func TestGlmMatch(t *testing.T) {
	cases := []struct {
		name string
		r    *special.Request
		want bool
	}{
		{"模型名 glm-5.3", req("glm-5.3", "smt-glm", "https://gw.example.com/v1"), true},
		{"模型名 glm-4.5-air", req("glm-4.5-air", "gw", "https://gw.example.com/v1"), true},
		{"provider 名", req("v5", "smt-glm", "https://gw.example.com/v1"), true},
		{"endpoint", req("v5", "gw", "https://open.bigmodel.cn/v1"), false}, // 无 "glm" 串
		{"deepseek 不归我管", req("deepseek-chat", "smt-deepseek", "https://gw.example.com/v1"), false},
		{"官方端点不碰", req("glm-5.3", "anth", "https://api.anthropic.com/v1"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := testPlugin().Match(c.r); got != c.want {
			t.Errorf("%s: Match=%v want %v", c.name, got, c.want)
		}
	}
}

// TestGlmApply 现场机制：glm 把「没写 thinking」当默认开。Claude Code 没写
// 就补显式 disabled；写了（真要思考）一个字节不动。
func TestGlmApply(t *testing.T) {
	r := req("glm-5.3", "smt-glm", "https://gw.example.com/v1")
	r.Agent = "claude"

	t.Run("没写 thinking 就补 disabled", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.3","max_tokens":64,"messages":[{"role":"user","content":"go"}]}`)
		out, notes, err := testPlugin().Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("改完不是合法 JSON: %v", err)
		}
		if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "disabled" {
			t.Fatalf("thinking 应为 disabled，实际 %v", m["thinking"])
		}
		if len(notes) == 0 || !strings.Contains(notes[0], "GLM") {
			t.Fatalf("改了就得有 note，实际 %v", notes)
		}
	})

	t.Run("客户端写了 thinking 就不动（真要思考）", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.3","thinking":{"type":"adaptive"},"messages":[]}`)
		out, notes, err := testPlugin().Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(body) || len(notes) != 0 {
			t.Fatalf("显式 thinking 不该被动: out=%s notes=%v", out, notes)
		}
	})

	t.Run("没有 messages 不动", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.3"}`)
		out, notes, err := testPlugin().Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(body) || len(notes) != 0 {
			t.Fatalf("没 messages 没东西可补: out=%s notes=%v", out, notes)
		}
	})
}

// TestGlmApply_OpencodeUntouched 现场回归（2026-09-15，与 st-deepseek 同款）：
// opencode 走 OpenAI 方言，没有 thinking 这个字段可写，它不发 thinking 不是
// 「不想思考」。替它补 disabled 就是把用户给模型配的行为改掉。
func TestGlmApply_OpencodeUntouched(t *testing.T) {
	r := &special.Request{Model: "glm-5.3", Provider: "smt-glm",
		BaseURL: "https://gw.example.com/v1", Protocol: "openai",
		Path: "/chat/completions"}

	body := []byte(`{"model":"glm-5.3","max_tokens":64,"messages":[{"role":"user","content":"go"}]}`)
	out, notes, err := testPlugin().Apply(body, r)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(body) || len(notes) != 0 {
		t.Fatalf("opencode 的请求被补了 thinking（现场 bug）: out=%s notes=%v", out, notes)
	}
	if testPlugin().Match(r) {
		t.Fatal("组合模块不该认领 opencode × GLM")
	}
}

// TestClaudeBgThenGlmCompose 组合语义：claude-bg 先把后台请求的 thinking
// 定为 disabled，glm/deepseek 看到已写就不动——两层各司其职，不重复注入。
func TestClaudeBgThenGlmCompose(t *testing.T) {
	r := req("glm-5.3", "smt-glm", "https://gw.example.com/v1")
	r.Agent, r.Stream, r.Tier = "claude", false, "mid"
	body := []byte(`{"model":"mid","messages":[]}`)

	out1, _, err := thinkingmodule.BestEffortDisableThink(body, r)
	if err != nil {
		t.Fatal(err)
	}
	out2, notes, err := testPlugin().Apply(out1, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("claude-bg 已写 disabled，glm 不该再动: %v", notes)
	}
	if string(out1) != string(out2) {
		t.Fatalf("组合后 body 不该变化:\n%s\n%s", out1, out2)
	}
}
