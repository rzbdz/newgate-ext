package deepseek

import "testing"

// Snapshots of real upstream response text (see forward's [shape-400] log and
// docs/11-troubleshooting.md §1). The criterion must hold on real evidence, so these
// are verbatim, not made-up strings.
const (
	reasoning400 = `{"type":"error","message":"The ` + "`reasoning_content`" +
		` in the thinking mode must be passed back to the API. (request id: 202609170712356080642648268d9d67Gf5hQWS)"}`
	thinking400 = `{"type":"error","message":"The ` + "`content[].thinking`" +
		` in the thinking mode must be passed back to the API."}`
	// The Responses dialect (/v1/responses) message. Caught verbatim on 2026-09-22 by a
	// probe against the real smt-deepseek/deepseek-flash (foreign call_id plus trailing
	// [function_call, function_call_output], 3/3 per cell).
	reasoningText400 = `{"type":"error","message":"The ` + "`reasoning_text`" +
		` in the thinking mode must be passed back to the API."}`
)

// TestReasoningShapeIsThePolicyGate is the **decision point** between request-shape
// errors and availability errors.
//
// This criterion decides which upstream feedback enters no provider's availability
// ledger. Changing it changes "what error is not blamed on this provider" and **must
// not** be changed by inference - every case below is real upstream text or its kin.
//
// Moved here from modules/breaker on 2026-09-17: the text is DeepSeek's dialect, and a
// criterion living where the truth is can be tested and evolved there; core holds no
// upstream-specific strings. Test cases unchanged.
func TestReasoningShapeIsThePolicyGate(t *testing.T) {
	tests := []struct {
		name string
		body string
		code int
		want bool
	}{
		// Must be claimed (a shape error: never trips, only counts) --
		{"openai dialect reasoning_content 400", reasoning400, 400, true},
		{"anthropic dialect content[].thinking 400", thinking400, 400, true},
		{"responses dialect reasoning_text 400", reasoningText400, 400, true},

		// Must **not** be claimed (a real availability problem, must be booked) --
		// 401 credentials: swallowing it makes you think upstream is broken; the key is wrong
		{"401 unauthorized", `{"error":"missing api key"}`, 401, false},
		// 403 permissions: as above
		{"403 forbidden", `{"error":"no quota"}`, 403, false},
		// 404 this provider lacks the model: another one will work
		{"404 model not found", `{"error":"model unknown"}`, 404, false},
		// 408/409/429 queued / rate limited / conflict: wait or switch
		{"408 request timeout", `{"error":"timed out"}`, 408, false},
		{"409 conflict", `{"error":"concurrent edit"}`, 409, false},
		{"429 rate limit", `{"error":"slow down"}`, 429, false},
		// 500/502/503/504 upstream is down: must trip the breaker
		{"500 server error", `{"error":"internal"}`, 500, false},
		{"502 bad gateway", `{"error":"upstream"}`, 502, false},
		// A 400 that is not reasoning: unclaimed, so it is neither a shape error (no
		// shape_skips bump) nor availability - 400s are never booked against upstream.
		// **Lenient-in, strict-out**: field-only and "must be passed"-only both misclaim.
		{"400 unrelated (no reasoning_content)", `{"error":"bad parameter foo"}`, 400, false},
		{"400 'must be passed' without reasoning_content",
			`{"error":"the value must be passed as header"}`, 400, false},
		// Reverse: the field is named but "must be passed" is absent - a format error
		// hint must not be let through as a shape error.
		{"400 reasoning_content mentioned but not 'must be passed'",
			`{"error":"reasoning_content must be a string"}`, 400, false},
		{"400 content[].thinking mentioned but not 'must be passed'",
			`{"error":"content[].thinking must be an array"}`, 400, false},
		{"400 reasoning_text mentioned but not 'must be passed'",
			`{"error":"reasoning_text must be a string"}`, 400, false},

		// Boundary: empty body, non-400
		{"empty body", ``, 400, false},
		{"400 with empty body", ``, 400, false},
		{"401 with reasoning_content in body (key phrase elsewhere)",
			`{"error":"unauthorized; see reasoning_content handling"}`, 401, false},
		{"500 with the exact reasoning phrasing (wrong status)",
			reasoning400, 500, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := (reasoningShape{}).Match(tt.code, []byte(tt.body))
			if got != tt.want {
				t.Errorf("Match(%q, %d) = %v, want %v", tt.body, tt.code, got, tt.want)
			}
		})
	}
}

// TestReasoningShapeNameIsStable: the name goes into logs (the "criterion X" in the
// [shape-400] line), metrics, the `newgate st` description, and the evidence dir name
// (dump/shape-400-<name>/). Renaming it moves old evidence dirs, so it is a contract.
func TestReasoningShapeNameIsStable(t *testing.T) {
	if got := (reasoningShape{}).Name(); got != "deepseek" {
		t.Fatalf("check name = %q, should be stable as deepseek", got)
	}
}
