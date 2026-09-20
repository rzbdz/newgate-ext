package deepseek

import "testing"

// 真上游响应文案的快照（见 forward 的 [shape-400] 日志与
// docs/11-troubleshooting.md §1）。判据必须能在真现场成立，所以这里用的是
// 原文，不是自造的字符串。
const (
	reasoning400 = `{"type":"error","message":"The ` + "`reasoning_content`" +
		` in the thinking mode must be passed back to the API. (request id: 202609170712356080642648268d9d67Gf5hQWS)"}`
	thinking400 = `{"type":"error","message":"The ` + "`content[].thinking`" +
		` in the thinking mode must be passed back to the API."}`
)

// TestReasoningShapeIsThePolicyGate 是请求形状错误 vs 可用性错误的**行权点**
// 回归测试。
//
// 这条判据决定「哪类上游反馈不进任何 provider 的可用性账本」。改它等于改了
// 「什么错误不该算在这家头上」，**不能**靠推断改——下面的每条都是真上游响应
// 文案的快照或同族。
//
// 2026-09-17 从 modules/breaker 搬到这里：文案是 DeepSeek 的方言，判据待在知道
// 真相的模块里才能被它自己测试和演进，core 里不再出现任何上游专有字符串。
// 用例一条没改。
func TestReasoningShapeIsThePolicyGate(t *testing.T) {
	tests := []struct {
		name string
		body string
		code int
		want bool
	}{
		// 必须认领（形状错误，永不摘牌，只计数）——
		{"openai dialect reasoning_content 400", reasoning400, 400, true},
		{"anthropic dialect content[].thinking 400", thinking400, 400, true},

		// 必须**不**认领（真可用性问题，要记账）——
		// 401 凭证：绕过去会以为是上游坏，其实是 key 错
		{"401 unauthorized", `{"error":"missing api key"}`, 401, false},
		// 403 权限：同上
		{"403 forbidden", `{"error":"no quota"}`, 403, false},
		// 404 该 provider 没这个模型：换一个能成
		{"404 model not found", `{"error":"model unknown"}`, 404, false},
		// 408/409/429 排队 / 限流 / 冲突：等一下或换一家
		{"408 request timeout", `{"error":"timed out"}`, 408, false},
		{"409 conflict", `{"error":"concurrent edit"}`, 409, false},
		{"429 rate limit", `{"error":"slow down"}`, 429, false},
		// 500/502/503/504 上游挂了：必须熔断
		{"500 server error", `{"error":"internal"}`, 500, false},
		{"502 bad gateway", `{"error":"upstream"}`, 502, false},
		// 400 但不是 reasoning：不认领，于是它既不算形状错（不涨 shape_skips）
		// 也不进可用性账本——400 一律不记上游的账。这两条是**宽进严出**的
		// 边界：只看字段名、或只看 "must be passed"，都会把它们误认。
		{"400 unrelated (no reasoning_content)", `{"error":"bad parameter foo"}`, 400, false},
		{"400 'must be passed' without reasoning_content",
			`{"error":"the value must be passed as header"}`, 400, false},
		// 反向：点名了字段但没有 "must be passed"——一句格式错误提示不该被
		// 当成形状错误放过。
		{"400 reasoning_content mentioned but not 'must be passed'",
			`{"error":"reasoning_content must be a string"}`, 400, false},

		// 边界：空 body、非 400
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

// TestReasoningShapeNameIsStable：名字进日志（[shape-400] 那行的「判据 X」）、
// 指标、`newgate st` 的说明，以及证据目录名（dump/shape-400-<名字>/）。
// 改名字会让老证据目录换地方，所以它是一条对外契约。
func TestReasoningShapeNameIsStable(t *testing.T) {
	if got := (reasoningShape{}).Name(); got != "deepseek" {
		t.Fatalf("判据名 = %q，应稳定为 deepseek", got)
	}
}
