package claudecode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	thinkingmodule "github.com/rzbdz/newgate-ext/modules/thinking"
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

// TestClassifierIsRecognisedByEveryGate: classifier requests from Claude Code's **four gates**
// must all be recognized; the test is in the comment on isClassifier (the system marker, one
// sentence shared by the four gates; **or** the top-level stop_sequences -- the classifier's unique
// structural test). The shapes in the table follow the four flavours deobfuscated out of 2.1.295.
//
// The lower half of the table is the reverse, and more important than the upper half:
// compact summaries, title generation, and requests whose **session body quotes the
// classifier's wording** must not be recognized at all. The consequence of a false
// positive is not "one extra short-circuit", it is that the compact's answer becomes
// a `<block>no</block>` -- which is to say, the session history gets replaced.
func TestClassifierIsRecognisedByEveryGate(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"Bash gate (the block kind)",
			`{"system":"You are a security monitor for autonomous AI coding agents","stop_sequences":["</block>"],"max_tokens":2112}`, true},
		{"sandbox network gate (the severity kind)",
			`{"system":"You are a security monitor for autonomous AI coding agents","stop_sequences":["</severity>"],"max_tokens":2112}`, true},
		{"fast flavour (no stop_sequences)",
			`{"system":"You are a security monitor for autonomous AI coding agents","max_tokens":2304}`, true},
		{"two-stage stage 2 (no stop_sequences)",
			`{"system":"You are a security monitor for autonomous AI coding agents","max_tokens":10240}`, true},
		{"the prompt was reworded, the terminator is still there",
			`{"system":"You monitor an autonomous agent","stop_sequences":["</severity>"],"max_tokens":2112}`, true},
		{"compact summary",
			`{"system":"You are a helpful AI assistant tasked with summarizing conversations","max_tokens":64000,"messages":[{"role":"user","content":"…"}]}`, false},
		{"generate a title",
			`{"system":"You are coming up with a title and a git branch name","max_tokens":64,"messages":[{"role":"user","content":"fix the parser"}]}`, false},
		{"the session body quotes the classifier wording, but the shell has no marker",
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
