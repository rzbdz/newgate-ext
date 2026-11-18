package thinking

import (
	"testing"

	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
)

// TestLearnRecognizesEverySignature locks each criterion this module registers, one by one:
// one missing = that upstream's ailment is never learned, every request hits the 400 again.
//
// Moved from modules/gateway/quirk (2026-09-18): the table was hard-coded in gateway then;
// now the criteria belong to the **module owning the patch** (see signatures.go), so the test
// moved too - the lock lives where the criterion lives, or the editor thinks it lives elsewhere.
func TestLearnRecognizesEverySignature(t *testing.T) {
	table := quirk.NewTable()
	for _, sig := range signatures() {
		if _, err := table.RegisterSignature(sig); err != nil {
			t.Fatal(err)
		}
	}
	// Every one comes from a real dump, none made up.
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
				"before 2026-09-17 this wording matched not a single signature, so it could never be learned",
		},
		{
			"deepseek：cannot be disabled",
			`{"error":{"message":"deepseek thinking options type cannot be disabled"}}`,
			"wording variant",
		},
		{
			"does not support disabling thinking",
			`{"error":{"message":"this model does not support disabling thinking"}}`,
			"wording variant",
		},
		{
			"thinking cannot be turned off",
			`{"error":{"message":"Thinking cannot be turned off for this model"}}`,
			"wording variant; case-insensitive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table.Reset()
			got := table.Learn("prov", "model-x", 400, []byte(tt.body))
			// Asserts Label's **source-language text**: the criteria are the upstream error strings
			// in Any, and those are not translated (see signatures.go).
			if len(got) != 1 || got[0] != "this model always thinks" {
				t.Fatalf("not learned (%s): %v", tt.reason, got)
			}
			if !table.Has("prov", "model-x", quirk.NoThinkingDisable) {
				t.Fatal("learned it but did not set the flag")
			}
			// Idempotent: the same ailment is not reported twice.
			if again := table.Learn("prov", "model-x", 400, []byte(tt.body)); again != nil {
				t.Fatalf("learned it twice: %v", again)
			}
		})
	}
}
