package claudecode_glm

import (
	"fmt"

	glmapi "github.com/rzbdz/newgate-modules-ext/go/modules/glm"
	claudeapi "github.com/rzbdz/newgate/go/modules/claudecode"
	"github.com/rzbdz/newgate/go/modules/gateway/rewrite"
	"github.com/rzbdz/newgate/go/modules/gateway/special"
)

type thinking struct {
	client claudeapi.Client
	model  glmapi.Model
}

var _ special.Plugin = (*thinking)(nil)

func Treatments(client claudeapi.Client, model glmapi.Model) []special.Plugin {
	return []special.Plugin{thinking{client: client, model: model}}
}

// glm 修 GLM 系模型「没写 thinking 就默认思考」的语义坑。
//
// 现场同 st-claude-bg：glm-5.3 对不带 thinking 字段的请求照样思考（实抓：
// 裸请求响应里 reasoning_content 非空）。Anthropic 协议里「没写」的语义是
// 不思考，Claude 家族也是这么实现的；GLM 把缺省当成了开。于是「客户端
// 没要求思考」的请求被拖进 15-30 秒的思考。
//
// 修法与 st-deepseek 第 1 手同款：只在 Claude Code **没写 thinking 时**补
// 显式 disabled；写了（enabled/adaptive）就一个字节不动——那是客户端真要
// 思考。与 claude-bg 的分工：那边按请求形态（claude 的后台非流式）认领、
// 这边按上游（glm 系），互补不重复。
//
// 为什么只给 Claude Code 补（claudeCode，见 special.go）：OpenAI 方言的
// 客户端（opencode）压根没有 thinking 这个 Anthropic 字段可写，它不发
// thinking 是表达能力问题、不是「不想思考」的意图。替它补 disabled 就是
// 把用户给这个模型配的默认行为改掉——2026-09-15 的现场：opencode 走
// DeepSeek 时条条请求被关思考，用户报「deepseek 不思考了」，同一天收窄。
// GLM 把缺省当默认思考，对本来就要思考的客户端来说正是它想要的。
//
// 摘除条件：GLM 把缺省改成不思考（或网关替它改了），
// `newgate st off glm` 即可验证；确认不需要了整文件可删。
func (thinking) Name() string { return "glm" }

func (thinking) Why() string {
	return "GLM 系模型把「没写 thinking」当默认开思考（Anthropic 语义是关）" +
		"→ 没要求思考的请求被拖进十几秒\n" +
		"Claude Code 那条路没写就补显式 disabled（写了不动）；" +
		"OpenAI 方言的客户端（opencode）没有这个字段可写，不碰"
}

// Match 只认 GLM：模型名、provider 名、base URL 任一处出现 glm
// （聚合网关可能改模型名，provider 名或 endpoint 里通常仍留着痕迹）。
// Anthropic 官方端点一律不碰（同 st-deepseek 的反向兜底）。
func (t thinking) Match(r *special.Request) bool {
	return r != nil && r.Agent == t.client.AgentID &&
		t.model.MatchTarget != nil &&
		t.model.MatchTarget(r.Model, r.Provider, r.BaseURL)
}

func (t thinking) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	if !t.Match(r) {
		return body, nil, nil
	}
	if _, has := rewrite.TopLevelRaw(body, "thinking"); has {
		return body, nil, nil // 客户端写了：尊重，一个字节不动
	}

	if _, effort := rewrite.TopLevelRaw(body, "reasoning_effort"); effort {
		return body, nil, nil // 设了推理强度 = 明确要思考
	}
	if _, has := rewrite.TopLevelRaw(body, "messages"); !has {
		return body, nil, nil // /v1/models 之类，没东西可补
	}
	nb, err := rewrite.InsertTopLevelRaw(body, "thinking",
		[]byte(`{"type":"disabled"}`))
	if err != nil {
		return nil, nil, fmt.Errorf("注入 thinking 失败: %w", err)
	}
	return nb, []string{`注入 thinking:{"type":"disabled"}（GLM 把缺省当默认思考）`}, nil
}
