package deepseek

import modules "github.com/rzbdz/newgate/component"

// Model is the DeepSeek model family's public detection port.
// MatchTarget belongs to the model module, so others need not copy heuristics.
type Model struct {
	MatchTarget func(model, provider, baseURL string) bool
}

// Capability identifies the single DeepSeek model-family detector.
var Capability = modules.NewCapability[Model]("model-family.deepseek")
