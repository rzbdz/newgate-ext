// Package claudecode_deepseek owns behavior that exists only at the
// intersection of Claude Code and DeepSeek. Neither base module imports it.
package claudecode_deepseek

import (
	claudeapi "github.com/rzbdz/newgate-ext/modules/claudecode"
	deepseekapi "github.com/rzbdz/newgate-ext/modules/deepseek"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/gateway/rewrite"
	"github.com/rzbdz/newgate/modules/gateway/special"
	"github.com/rzbdz/newgate/modules/pluginmanager"
)

type thinking struct {
	client claudeapi.Client
	model  deepseekapi.Model
}

var _ special.Plugin = (*thinking)(nil)

func Treatments(client claudeapi.Client, model deepseekapi.Model) []special.Plugin {
	return []special.Plugin{thinking{client: client, model: model}}
}

func (thinking) Name() string { return "claudecode-deepseek" }

func (thinking) Before() []string { return []string{"deepseek", "always-thinks"} }
func (thinking) After() []string  { return nil }

func (thinking) Why() string {
	return i18n.T("Claude Code strips third-party DeepSeek thinking blocks; turn DeepSeek thinking off "+
		"unless it was explicitly asked for, so the next round does not 400 for a block that must be passed back", nil)
}

func (t thinking) Match(request *special.Request) bool {
	return request != nil && request.Agent == t.client.AgentID &&
		t.model.MatchTarget != nil &&
		t.model.MatchTarget(request.Model, request.Provider, request.BaseURL)
}

func (thinking) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	// 这一手可以被单独关掉（`newgate plugin claudecode_deepseek.inject-thinking off`）。
	// 关掉之后 Claude Code 的请求会按缺省进 DeepSeek 思考模式，下一轮就是
	// 「thinking 块没回传」的 400——所以它是 quirk 级而不是 safe 级。
	if pluginmanager.Off(stateOf(r), SwitchInjectThinking) {
		return body, nil, nil
	}
	if _, has := rewrite.TopLevelRaw(body, "thinking"); has {
		return body, nil, nil
	}
	if _, effort := rewrite.TopLevelRaw(body, "reasoning_effort"); effort {
		return body, nil, nil
	}
	if _, has := rewrite.TopLevelRaw(body, "messages"); !has {
		return body, nil, nil
	}
	out, err := rewrite.InsertTopLevelRaw(body, "thinking", []byte(`{"type":"disabled"}`))
	if err != nil {
		return nil, nil, i18n.Ef(err, "failed to inject thinking: {err}", nil)
	}
	return out, []string{i18n.T("injected {patch} (Claude Code will not pass DeepSeek thinking blocks back)",
		i18n.A{"patch": `thinking:{"type":"disabled"}`})}, nil
}
