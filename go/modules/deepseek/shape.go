package deepseek

import (
	"bytes"

	breakerapi "github.com/rzbdz/newgate/go/modules/breaker"
)

var _ breakerapi.ShapeDetector = (*reasoningShape)(nil)

// reasoningShape 认出 DeepSeek 思考模式那两句 400：
//
//	The `reasoning_content` in the thinking mode must be passed back to the API.
//	The `content[].thinking` in the thinking mode must be passed back to the API.
//
// 这两句是**请求形状**问题，不是这家上游「能不能用」的问题：同一份 body 换哪个
// provider 都会以同样的方式被拒（校验规则来自模型侧，不是渠道侧），所以它不该
// 记在任何一个 provider 的可用性账本上。认出来之后健康表只计数、永不摘牌，数据
// 面则据此打 [shape-400] 专属日志并存一份不参与滚动清理的证据。
//
// 为什么这条判据放在 deepseek 而不是 core（2026-09-17 搬过来）：文案是 DeepSeek
// 的方言。「must be passed back」这个措辞、以及它点名的两个字段名，都是这一家
// 的口径；别的上游哪天报同样的话也未必是同一件事。判据待在知道真相的模块里，
// 它才能被这个模块自己测试、自己演进，而 core 里不再出现任何上游专有字符串。
//
// 判据故意**宽进严出**：
//   - 必须真的带 "must be passed"（不是只看字段名出现）——只有字段名的话，
//     一句「reasoning_content 字段格式错误」也会被算成形状错误，那是一次真正的
//     请求问题被放过；
//   - 必须点名两个字段之一——只有 "must be passed" 的话，任何
//     "the value must be passed as header" 都会被误认。
//
// 两条同时成立才认。宁可漏认（少一个计数、400 照样不记在任何人头上），不可
// 误认（把真正的可用性故障放过）。
type reasoningShape struct{}

// ShapeDetector 的名字进日志、指标和 `newgate st` 的说明。
func (reasoningShape) Name() string { return "deepseek" }

func (reasoningShape) Match(status int, body []byte) bool {
	if status != 400 || len(body) == 0 {
		return false
	}
	if !bytes.Contains(body, []byte("must be passed back")) &&
		!bytes.Contains(body, []byte("must be passed")) {
		return false
	}
	// 两种方言各点名一个字段：OpenAI 方言说 reasoning_content，Anthropic 方言
	// 说 content[].thinking。都要认——同一个上游的两个端点，报错措辞不同。
	return bytes.Contains(body, []byte("reasoning_content")) ||
		bytes.Contains(body, []byte("content[].thinking"))
}
