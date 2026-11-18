package thinking

import (
	"testing"

	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
)

// TestLearnRecognizesEverySignature 逐个锁住本模块注册的每一条判据：少一条 =
// 那条上游的毛病永远学不到，每次请求都重新撞一遍 400。
//
// 从 modules/gateway/quirk 搬来（2026-09-18）：签名表那时候硬编码在 gateway 里，
// 现在判据归**拥有补丁的模块**（见 signatures.go），锁它的测试也就跟着搬——判据
// 待在哪，锁它的测试就在哪，否则改签名的人会以为测试还在别处。
func TestLearnRecognizesEverySignature(t *testing.T) {
	table := quirk.NewTable()
	for _, sig := range signatures() {
		if _, err := table.RegisterSignature(sig); err != nil {
			t.Fatal(err)
		}
	}
	// 每条都来自真实 dump，不是编的。
	tests := []struct {
		name   string
		body   string
		reason string
	}{
		{
			"GLM 1210",
			`{"error":{"message":"[1210][该模型始终思考，不支持关闭思考；请使用 low、high 或 max。]"}}`,
			"dump/err-400-req000082、req000117（route: mid -> smt-glm/glm-5.3-flash）",
		},
		{
			"kimi：only type=enabled is allowed",
			`{"error":{"type":"<nil>","message":"invalid thinking: only type=enabled is allowed for this model (request id: 202609170742120)"}}`,
			"dump/err-400-req000213、req000230（route: mid -> kimi/kimi-k2.7-code）——" +
				"2026-09-17 之前这条措辞一条签名都不匹配，所以永远学不到",
		},
		{
			"deepseek：cannot be disabled",
			`{"error":{"message":"deepseek thinking options type cannot be disabled"}}`,
			"措辞变体",
		},
		{
			"does not support disabling thinking",
			`{"error":{"message":"this model does not support disabling thinking"}}`,
			"措辞变体",
		},
		{
			"thinking cannot be turned off",
			`{"error":{"message":"Thinking cannot be turned off for this model"}}`,
			"措辞变体；大小写不敏感",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table.Reset()
			got := table.Learn("prov", "model-x", 400, []byte(tt.body))
			// 断言的是 Label 的**源语言原文**：判据本身是 Any 里那些上游报错原文，
			// 它们不翻译（见 signatures.go）。
			if len(got) != 1 || got[0] != "this model always thinks" {
				t.Fatalf("没学到（%s）: %v", tt.reason, got)
			}
			if !table.Has("prov", "model-x", quirk.NoThinkingDisable) {
				t.Fatal("学到了却没打上 flag")
			}
			// 幂等：同一个毛病第二次不再重复报。
			if again := table.Learn("prov", "model-x", 400, []byte(tt.body)); again != nil {
				t.Fatalf("重复学了一次: %v", again)
			}
		})
	}
}
