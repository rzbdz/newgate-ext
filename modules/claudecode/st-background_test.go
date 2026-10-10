package claudecode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/gateway/special"
	thinkingmodule "github.com/rzbdz/newgate/modules/thinking"
)

type testThinking struct{}

func (testThinking) BestEffortDisable(body []byte, request *special.Request) ([]byte, []string, error) {
	return thinkingmodule.BestEffortDisableThink(body, request)
}

func TestBackgroundIsClientLocal(t *testing.T) {
	plugin := background{}
	for _, test := range []struct {
		agent  string
		stream bool
		want   bool
	}{
		{ID, false, true},
		{ID, true, false},
		{"opencode", false, false},
		{"", false, false},
	} {
		if got := plugin.Match(&special.Request{Agent: test.agent, Stream: test.stream}); got != test.want {
			t.Fatalf("Match(%q, %v) = %v, want %v", test.agent, test.stream, got, test.want)
		}
	}
}

func TestBackgroundDisablesImplicitThinking(t *testing.T) {
	body := []byte(`{"model":"mid","messages":[{"role":"user","content":"classify"}]}`)
	out, notes, err := (background{thinking: testThinking{}}).Apply(body, &special.Request{
		Agent: ID, Model: "mid",
	})
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]interface{}
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	thinking, _ := root["thinking"].(map[string]interface{})
	if thinking["type"] != "disabled" || len(notes) == 0 {
		t.Fatalf("thinking=%v notes=%v", thinking, notes)
	}
}

func TestBackgroundOwnsRouteAndObservability(t *testing.T) {
	body := []byte(`{"system":"You are a security monitor for autonomous AI coding agents"}`)
	override := domain.Binding{Provider: "provider", Model: "fast-model"}
	raw, _ := json.Marshal(override)
	state := &domain.State{ModuleConfig: map[string][]byte{"classifier_override": raw}}
	decision, ok := (background{}).Route(body, &special.Request{Agent: ID}, state)
	if !ok || decision.Tier != "light" || decision.Head == nil || *decision.Head != override {
		t.Fatalf("decision=%+v matched=%v", decision, ok)
	}
	items := (background{}).Status(state)
	if len(items) != 1 || !strings.Contains(items[0].Value, override.String()) {
		t.Fatalf("status=%+v", items)
	}
	if len((background{}).Metrics()) == 0 || len((background{}).Bindings(state)) != 1 {
		t.Fatal("module did not publish metric/binding capabilities")
	}
}

// TestClassifierIsRecognisedByEveryGate：Claude Code 的**四个 gate** 发的
// 分类器请求都必须被认出来，判据见 isClassifier 的注释（system 标记，四个 gate
// 共用一句；**或**顶层 stop_sequences——分类器独有的结构判据）。表里的形状按
// 2.1.295 反混淆出来的四种 flavour 摆。
//
// 下半张表是反向的，而且比上半张重要：compact 总结、起标题、以及**会话正文里
// 引用了分类器话术**的请求，一个都不许认。认错的后果不是「多短路一次」，是
// compact 的答复会被换成一句 `<block>no</block>`——那等于把会话历史换掉。
func TestClassifierIsRecognisedByEveryGate(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"Bash gate（block 那套）",
			`{"system":"You are a security monitor for autonomous AI coding agents","stop_sequences":["</block>"],"max_tokens":2112}`, true},
		{"沙箱网络 gate（severity 那套）",
			`{"system":"You are a security monitor for autonomous AI coding agents","stop_sequences":["</severity>"],"max_tokens":2112}`, true},
		{"fast flavour（不设 stop_sequences）",
			`{"system":"You are a security monitor for autonomous AI coding agents","max_tokens":2304}`, true},
		{"两段式 stage 2（不设 stop_sequences）",
			`{"system":"You are a security monitor for autonomous AI coding agents","max_tokens":10240}`, true},
		{"提示词换了措辞，终止符还在",
			`{"system":"You monitor an autonomous agent","stop_sequences":["</severity>"],"max_tokens":2112}`, true},
		{"compact 总结",
			`{"system":"You are a helpful AI assistant tasked with summarizing conversations","max_tokens":64000,"messages":[{"role":"user","content":"…"}]}`, false},
		{"起标题",
			`{"system":"You are coming up with a title and a git branch name","max_tokens":64,"messages":[{"role":"user","content":"fix the parser"}]}`, false},
		{"会话正文引用了分类器话术，壳上却没有标记",
			`{"system":"You are Claude Code","max_tokens":8192,"messages":[{"role":"user","content":"the prompt begins You are a security monitor and Stage 1 does NOT apply user intent"}]}`, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := isClassifier([]byte(tt.body)); got != tt.want {
				t.Fatalf("isClassifier = %v, want %v（body=%s）", got, tt.want, tt.body)
			}
		})
	}
}
