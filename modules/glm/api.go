package glm

import modules "github.com/rzbdz/newgate/go/component"

// Model 是 GLM 模型家族对外的判定端口。
// 将匹配逻辑留在模型 owner 内，客户端交叉组件只消费结论。
type Model struct {
	MatchTarget func(model, provider, baseURL string) bool
}

// Capability 标识唯一的 GLM 模型家族判定器。
var Capability = modules.NewCapability[Model]("model-family.glm")
