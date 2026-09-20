package codex

import modules "github.com/rzbdz/newgate/component"

// Client 是 Codex 家族暴露给交叉组件的稳定身份。
//
// 与 opencode 那条同款：交叉行为（上游怪癖补丁、客户端×模型的语义）只需要 AgentID，
// 不该依赖接管实现或配置文件的结构。
type Client struct {
	AgentID string
}

// Capability 标识唯一的 Codex 客户端家族。
var Capability = modules.NewCapability[Client]("client-family.codex")
