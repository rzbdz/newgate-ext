package forward

import "strings"

// The native Gemini API puts the model **in the path**, not in the body:
//
//	POST /v1beta/models/<model>:generateContent
//	POST /v1beta/models/<model>:streamGenerateContent?alt=sse
//	POST /v1beta/models/<model>:countTokens
//
// Everywhere else newgate routes from the body's top-level `model`; here there is none.
// These three helpers are the whole dialect translation, and they stay pure functions so
// the fragile part ("the model we route on is the one we rewrite") is unit-testable
// without a network or a config.

// geminiCall is the parsed shape of one native-Gemini call.
type geminiCall struct {
	Model  string // the path segment between `models/` and `:` (or the end)
	Action string // generateContent | streamGenerateContent | countTokens | ""
}

// parseGeminiPath recognizes `/v1beta/models/<model>[:<action>]`.
//
// ok=false for every other shape (model listing, an embedding endpoint, junk): those are
// not things this layer routes, and guessing at them would change requests it does not
// understand.
func parseGeminiPath(suffix string) (geminiCall, bool) {
	const head = "/v1beta/models/"
	if !strings.HasPrefix(suffix, head) {
		return geminiCall{}, false
	}
	rest := suffix[len(head):]
	// A trailing path after the model ("…/models/<model>/…") is not a shape we know.
	if rest == "" || strings.Contains(rest, "/") {
		return geminiCall{}, false
	}
	model, action, _ := strings.Cut(rest, ":")
	if model == "" {
		return geminiCall{}, false
	}
	return geminiCall{Model: model, Action: action}, true
}

// withGeminiModel returns the suffix with the model segment replaced.
//
// Why rewrite at all: the client names a **tier** (GEMINI_MODEL=heavy), and the upstream
// only knows concrete names. The chain step's model is the one that was resolved, so the
// path must carry it -- exactly the same rule as replacing the body's top-level model.
func withGeminiModel(suffix, model string) string {
	if model == "" {
		return suffix
	}
	call, ok := parseGeminiPath(suffix)
	if !ok {
		return suffix
	}
	const head = "/v1beta/models/"
	rest := suffix[len(head):]
	_, action, hasAction := strings.Cut(rest, ":")
	out := head + model
	if hasAction {
		out += ":" + action
	}
	_ = call
	return out
}

// geminiStreams reports whether this call is a streaming one.
//
// Two spellings both stream, and both must be recognized or the shim's 150s total
// timeout would cut a long answer: the action name (`streamGenerateContent`) and the
// `alt=sse` query (the same endpoint can also answer a JSON array).
func geminiStreams(call geminiCall, rawQuery string) bool {
	if call.Action == "streamGenerateContent" {
		return true
	}
	for _, kv := range strings.Split(rawQuery, "&") {
		if kv == "alt=sse" {
			return true
		}
	}
	return false
}
