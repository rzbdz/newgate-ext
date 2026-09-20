package codex

import agentapi "github.com/rzbdz/newgate/modules/confighook"

// facts 是本模块交给内核的运行期事实（见 confighook.AgentFacts）。
//
// 今天两份都走内核的通用判据：codex 没装没装看 PATH 上的 `codex`（跳过我们自己的
// shim 目录），槽位表还没有（模型走 config.toml，见 agent.go 的 Notes）。**仍然
// 注册**而不是省的：这一格的意义是「这家客户端有没有自己的话要说」，将来加槽位、
// 加自定义探测时改动只落在本文件，内核一个字不动。
type facts struct{}

var _ agentapi.AgentFacts = facts{}

func (facts) Installed() bool { return Agent().OnPath() }

func (facts) SlotTier(agentapi.Slot) string { return "" }
