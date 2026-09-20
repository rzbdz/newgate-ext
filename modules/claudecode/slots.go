package claudecode

import (
	agentapi "github.com/rzbdz/newgate/modules/confighook"
)

// 槽位 → 档位的映射，从 2026-09-21 起可配。
//
// # 为什么本来就该可配
//
// 这个映射说的是「Claude Code 的哪一个槽位归到哪个语义档位」，而它同时是两件事的
// 交汇：客户端的**槽位身份**（ANTHROPIC_DEFAULT_SONNET_MODEL 是谁）与产品的
// **档位阶梯**（normal 比 mid 强）。
//
//	前者是客户端的知识 —— 归本模块，写死在 agent.go 里，不该可配；
//	后者是**用户的取舍** —— 归用户。「Bash 分类器我想让它走 normal，
//	省得 Sonnet 一挂就断」「subagent 全部降一档」这类事，改代码改不了。
//
// 所以缺省值留在 Go 里（那是产品的出厂设置，也是没配过的人拿到的东西），
// 用户改过的住在配置里。
//
// # 实现 2026-09-21 搬去了内核
//
// 读、写、筛、以及「只留与缺省不同的那些」这套逻辑现在住在
// `confighook.SlotOverrides`（见那边的说明：codex 也要一张同样的表，留在本模块
// 里的代价只能是抄一遍，而里面几处错了不会红）。本文件只剩**本家的两件知识**：
// state.json 里用哪个键、本模块有哪些槽位。
//
// SlotsKey 是 state.json 的 `module_config` 里的键。
const SlotsKey = "claude_slots"

// slotsOf 是本模块那张覆盖表。包级一份：它没有状态，只有两个常量。
func slotsOf() agentapi.SlotOverrides {
	return agentapi.SlotOverrides{
		Key:     SlotsKey,
		AgentID: ID,
		Slots:   func() []agentapi.Slot { return Agent().Slots },
	}
}

// ReadSlotTiers / WriteSlotTiers / allowedFor / defaultTier / slotTier / slotNote
// 是本文件里对内核那几件的叫法，保留短名字：调用点很多（界面、命令、注入），
// 而写成 agentapi.X 会把这一屏塞满包名。
func ReadSlotTiers() (ok, bad map[string]string) { return slotsOf().Read() }

func WriteSlotTiers(m map[string]string) error { return slotsOf().Write(m) }

func allowedFor(slot string) []string { return slotsOf().AllowedFor(slot) }

func defaultTier(slot string) (string, bool) { return slotsOf().Default(slot) }

// facts 是本模块交给内核的**运行时事实**（见 confighookapi.AgentFacts）。
//
// # 为什么是一个类型，而不是描述符上的两个函数字段
//
// 描述符（Agent）说的是「这个客户端是什么」：二进制名、方言、槽位表、环境变量名
// ——那些是不变的、可以打印给人看的。而「装没装」「某个槽位此刻走哪个档位」是
// **此刻怎么样**，随磁盘与配置变。塞进同一个 struct 的话，依赖注入那一侧就没了：
// 内核只是捡到一个恰好被赋了值的函数指针，谁提供、能不能替换、测试里怎么塞一个
// 假的，全都无从谈起。
//
// 端口这一侧是完整的：RegisterAgentFacts 拿得到 Release（Stop 时自动撤销）、
// 没注册时内核有一份写死的缺省（见 confighookapi.InstalledDefault）。
type facts struct{}

var _ agentapi.AgentFacts = facts{}

// Installed 这台机器上有没有 claude。
//
// 走内核那份通用判据（在 PATH 上找 Bin 里的名字，**已经跳过我们自己的 shim 目录**，
// 见 confighook.Agent.OnPath），**不必自己写**：本客户端没有什么特别的知识。
func (facts) Installed() bool { return Agent().OnPath() }

// SlotTier 这个槽位此刻走哪个档位：用户改过就用改的，没改返回空串（内核据此回落到
// 描述符里的缺省）。**不在这里兜底**——「没改过就用缺省」只有一份实现（内核的
// confighookapi.TierOf），两处各兜一遍的话，那个问题就又要看两个地方才知道答案。
func (facts) SlotTier(s agentapi.Slot) string { return slotsOf().Tier(s) }

// slotNote 说这个槽位有没有被改过、改成了什么；没改返回空串。
func slotNote(name, def string) string { return slotsOf().Note(name, def) }

// lockReason 说这两张卡此刻有没有意义；有意义返回空串（见 confighook.NotInstalled）。
func lockReason() string { return agentapi.NotInstalled(Agent()) }
