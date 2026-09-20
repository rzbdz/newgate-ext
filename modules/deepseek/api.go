package deepseek

import modules "github.com/rzbdz/newgate/component"

// Model 是 DeepSeek 模型家族对外的判定端口。
// MatchTarget 由模型模块拥有，交叉组件无需复制 provider/base URL 启发式规则。
type Model struct {
	MatchTarget func(model, provider, baseURL string) bool
}

// Capability 标识唯一的 DeepSeek 模型家族判定器。
var Capability = modules.NewCapability[Model]("model-family.deepseek")
