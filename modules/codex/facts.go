package codex

import agentapi "github.com/rzbdz/newgate/modules/confighook"

// facts 是本模块交给内核的运行期事实（见 confighook.AgentFacts）。
//
// 两份都走内核的通用判据：装没装看 PATH 上的 `codex`（跳过我们自己的 shim 目录，
// 见 Agent.OnPath），槽位走共享的那张覆盖表（confighook.SlotOverrides）。
// **仍然注册**而不是省的：这一格的意义是「这家客户端有没有自己的话要说」，
// 将来加自定义探测时改动只落在本文件，内核一个字不动。
type facts struct{}

var _ agentapi.AgentFacts = facts{}

func (facts) Installed() bool { return Agent().OnPath() }

// SlotTier 这个槽位此刻走哪个档位：用户改过就用改的，没改返回空串（内核据此回落到
// 描述符里的缺省）。**不在这里兜底**——「没改过就用缺省」只有一份实现（内核的
// confighook.TierOf）。
//
// 它同时被两处问：接管写 config.toml 那一刻（takeover.go 的 tierOf），以及界面
// 上那张卡显示的此刻值。同一个实现保证了两边不会分家。
func (facts) SlotTier(s agentapi.Slot) string { return slotsOf().Tier(s) }
