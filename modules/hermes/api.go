package hermes

import modules "github.com/rzbdz/newgate/component"

// Client 是 Hermes 家族暴露给交叉组件的稳定身份。
//
// 与 codex / opencode 那条同款：交叉行为（上游怪癖补丁、客户端×模型的语义）只需要
// AgentID，不该依赖接管实现或 config.yaml 的结构。
type Client struct {
	AgentID string
}

// Capability 标识唯一的 Hermes 客户端家族。
var Capability = modules.NewCapability[Client]("client-family.hermes")
