package claudecode_deepseek

import (
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/deepseek"
	deepseekapi "github.com/rzbdz/newgate-ext/modules/deepseek"
	"github.com/rzbdz/newgate/modules/claudecode"
	claudeapi "github.com/rzbdz/newgate/modules/claudecode"
	"github.com/rzbdz/newgate/modules/gateway/special"
)

func testPlugin() thinking {
	return thinking{
		client: claudeapi.Client{AgentID: claudecode.ID},
		model:  deepseekapi.Model{MatchTarget: deepseek.MatchTarget},
	}
}

func TestThinkingOnlyMatchesClaudeCodeDeepSeekIntersection(t *testing.T) {
	plugin := testPlugin()
	for _, test := range []struct {
		agent string
		model string
		want  bool
	}{
		{claudecode.ID, "deepseek-chat", true},
		{"opencode", "deepseek-chat", false},
		{claudecode.ID, "glm-5.3", false},
	} {
		request := &special.Request{Agent: test.agent, Model: test.model}
		if got := plugin.Match(request); got != test.want {
			t.Fatalf("Match(%q, %q) = %v, want %v", test.agent, test.model, got, test.want)
		}
	}
}

func TestThinkingDisablesImplicitDeepSeekThinking(t *testing.T) {
	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"assistant","content":"a"}]}`)
	out, notes, err := testPlugin().Apply(body, &special.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"thinking":{"type":"disabled"}`) ||
		!strings.Contains(strings.Join(notes, "\n"), "注入 thinking") {
		t.Fatalf("out=%s notes=%v", out, notes)
	}
}
