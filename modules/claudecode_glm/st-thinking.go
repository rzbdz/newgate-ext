package claudecode_glm

import (
	claudeapi "github.com/rzbdz/newgate-ext/modules/claudecode"
	"github.com/rzbdz/newgate-ext/modules/gateway/rewrite"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	glmapi "github.com/rzbdz/newgate-ext/modules/glm"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

type thinking struct {
	client claudeapi.Client
	model  glmapi.Model
}

var _ special.Plugin = (*thinking)(nil)

func Treatments(client claudeapi.Client, model glmapi.Model) []special.Plugin {
	return []special.Plugin{thinking{client: client, model: model}}
}

// glm fixes the semantic pit of GLM-family models
// "thinking by default when thinking is not written".
//
// The scene is the same as st-claude-bg: glm-5.3 thinks anyway on requests
// without a thinking field (captured: a bare request's response has non-empty
// reasoning_content). In the Anthropic protocol, "unwritten" means do not think, and
// the Claude family implements it that way; GLM treats the default as on. So requests
// where "the client did not ask to think" get dragged into 15-30 seconds of thinking.
//
// The fix is the same as move 1 of st-deepseek: only add an explicit disabled when
// Claude Code **did not write thinking**; when written (enabled/adaptive), not one
// byte is touched -- that is the client really wanting to think. The split from
// claude-bg: that one claims by request shape (claude's background non-streaming),
// this one by upstream (glm family), complementary and not duplicative.
//
// Why only fill it in for Claude Code (claudeCode, see special.go): OpenAI-dialect clients
// (opencode) have no Anthropic thinking field to write at all, so not sending thinking is a
// matter of expressiveness, not the intent "do not want to think". Filling in disabled for
// it would change the default behavior the user configured for this model -- the scene on
// 2026-09-15: when opencode went through DeepSeek, every single request had thinking turned
// off, and the user reported "deepseek has stopped thinking"; narrowed the same day. GLM
// treats the default as thinking, which is exactly what a client that wants to think wants.
//
// Removal condition: GLM changes the default to not thinking (or the gateway does it instead),
// which `newgate st off glm` verifies; once confirmed unnecessary, the whole file can be deleted.
func (thinking) Name() string { return "glm" }

// Before/After: see the same comment in the distribution's modules/deepseek: product
// plugins declare their own ordering edges (the kernel does not know the name glm).
// The division of labor is claude-bg (kernel, reroute background non-streaming first)
// -> this plugin (write thinking) -> always-thinks (kernel, backstop translation).
func (thinking) Before() []string { return []string{"always-thinks"} }
func (thinking) After() []string  { return []string{"claude-bg"} }

func (thinking) Why() string {
	return i18n.T("GLM models treat a missing thinking field as thinking on (Anthropic semantics say off) "+
		"→ requests that never asked to think get dragged into ten-plus seconds\n"+
		"on the Claude Code path an explicit disabled is filled in when it is absent (present is left alone); "+
		"OpenAI-dialect clients (opencode) have no such field to write, so they are not touched", nil)
}

// Match only recognizes GLM: glm appears in any of the model name, provider
// name, or base URL (an aggregating gateway may rename models, and the trace
// usually remains in the provider name or endpoint). Anthropic's official
// endpoint is never touched (the same reverse backstop as st-deepseek).
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
		return body, nil, nil // the client set it: respect that, not one byte changed
	}

	if _, effort := rewrite.TopLevelRaw(body, "reasoning_effort"); effort {
		return body, nil, nil // reasoning effort given = the client explicitly wants thinking
	}
	if _, has := rewrite.TopLevelRaw(body, "messages"); !has {
		return body, nil, nil // /v1/models and friends: nothing to backfill
	}
	nb, err := rewrite.InsertTopLevelRaw(body, "thinking",
		[]byte(`{"type":"disabled"}`))
	if err != nil {
		return nil, nil, i18n.Ef(err, "failed to inject thinking: {err}", nil)
	}
	return nb, []string{i18n.T("injected {patch} (GLM treats an absent field as thinking on)",
		i18n.A{"patch": `thinking:{"type":"disabled"}`})}, nil
}
