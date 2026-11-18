package claudecode_glm

import (
	"encoding/json"
	"strings"
	"testing"

	claudeapi "github.com/rzbdz/newgate-ext/modules/claudecode"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	"github.com/rzbdz/newgate-ext/modules/glm"
	glmapi "github.com/rzbdz/newgate-ext/modules/glm"
	thinkingmodule "github.com/rzbdz/newgate-ext/modules/thinking"
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
		{"model name glm-5.3", req("glm-5.3", "smt-glm", "https://gw.example.com/v1"), true},
		{"model name glm-4.5-air", req("glm-4.5-air", "gw", "https://gw.example.com/v1"), true},
		{"provider name", req("v5", "smt-glm", "https://gw.example.com/v1"), true},
		{"endpoint", req("v5", "gw", "https://open.bigmodel.cn/v1"), false}, // 无 "glm" 串
		{"deepseek is not mine", req("deepseek-chat", "smt-deepseek", "https://gw.example.com/v1"), false},
		{"leave the official endpoint alone", req("glm-5.3", "anth", "https://api.anthropic.com/v1"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := testPlugin().Match(c.r); got != c.want {
			t.Errorf("%s: Match=%v want %v", c.name, got, c.want)
		}
	}
}

// TestGlmApply, the on-site mechanism: glm treats "thinking unwritten"
// as on by default. If Claude Code did not write it, add an explicit
// disabled; if written (really wanting to think), not one byte is touched.
func TestGlmApply(t *testing.T) {
	r := req("glm-5.3", "smt-glm", "https://gw.example.com/v1")
	r.Agent = "claude"

	t.Run("with no thinking field, fill in disabled", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.3","max_tokens":64,"messages":[{"role":"user","content":"go"}]}`)
		out, notes, err := testPlugin().Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("after the change it is not valid JSON: %v", err)
		}
		if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "disabled" {
			t.Fatalf("thinking should be disabled, actual %v", m["thinking"])
		}
		if len(notes) == 0 || !strings.Contains(notes[0], `thinking:{"type":"disabled"}`) {
			t.Fatalf("if it changed there must be a note saying what was injected, actual %v", notes)
		}
	})

	t.Run("if the client wrote thinking, leave it alone (it really wants to think)", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.3","thinking":{"type":"adaptive"},"messages":[]}`)
		out, notes, err := testPlugin().Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(body) || len(notes) != 0 {
			t.Fatalf("an explicit thinking must not be touched: out=%s notes=%v", out, notes)
		}
	})

	t.Run("with no messages, leave it alone", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.3"}`)
		out, notes, err := testPlugin().Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(body) || len(notes) != 0 {
			t.Fatalf("no messages means nothing to fill in: out=%s notes=%v", out, notes)
		}
	})
}

// TestGlmApply_OpencodeUntouched, the on-site regression (2026-09-15, same as
// st-deepseek): opencode speaks the OpenAI dialect and has no thinking field
// to write; not sending thinking is not "does not want to think". Filling in
// disabled for it would change the behavior the user configured for the model.
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
		t.Fatalf("the opencode request had thinking filled in (the live bug): out=%s notes=%v", out, notes)
	}
	if testPlugin().Match(r) {
		t.Fatal("the composite module must not claim opencode x GLM")
	}
}

// TestClaudeBgThenGlmCompose, composition semantics: claude-bg first sets
// a background request's thinking to disabled, and glm/deepseek see it is written
// and leave it alone -- each layer does its own job, with no duplicate injection.
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
		t.Fatalf("claude-bg already writes disabled, glm must not touch it: %v", notes)
	}
	if string(out1) != string(out2) {
		t.Fatalf("after combining, the body must not change:\n%s\n%s", out1, out2)
	}
}
