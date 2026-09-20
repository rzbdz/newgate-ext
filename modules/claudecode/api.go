package claudecode

import modules "github.com/rzbdz/newgate/component"

// Client 是 Claude Code 家族暴露给交叉组件的稳定身份。
// 这里只传递匹配所需的 AgentID，不泄漏客户端模块的插件实现。
type Client struct {
	AgentID string
}

// Capability 标识唯一的 Claude Code 客户端家族。
var Capability = modules.NewCapability[Client]("client-family.claudecode")
