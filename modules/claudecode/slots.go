package claudecode

import (
	"encoding/json"
	"slices"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/store"
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
// # 为什么存在 state.json 的 ModuleConfig 里
//
// 与 classifier_override 同一个位置、同一个理由：state.json 的 ModuleConfig 就是
// 「core 不认识的顶层键原样存着」的那一格（见 domain.State）。为它单开一个文件会
// 多出一份要备份、要迁移、要解释的东西，而这里只有五行数据。
const SlotsKey = "claude_slots"

// ReadSlotTiers 读用户改过的槽位映射（槽位名 → 档位）。没配过返回空表。
//
// 读的时候要**筛**：这个文件是给人手改的，写错一个档位名（`hevy`）会一路注入成
// 模型名发到上游，症状是「某些请求突然 400」，而那跟配置之间隔着好几层。滤掉的
// 条目由调用方报出来（见 slotTier / SlotsView），不静默吞掉。
func ReadSlotTiers() (ok map[string]string, bad map[string]string) {
	raw := store.LoadState().ModuleConfig[SlotsKey]
	if len(raw) == 0 {
		return nil, nil
	}
	var all map[string]string
	if err := json.Unmarshal(raw, &all); err != nil {
		// 解析不了 = 整份都不可信。报出来（下面那张表会说明），但**不**让接管失败：
		// 一个改坏了的映射不该让 claude 起不来，缺省值仍然是对的。
		return nil, map[string]string{"": string(raw)}
	}
	ok = make(map[string]string, len(all))
	for slot, tier := range all {
		if slices.Contains(allowedFor(slot), tier) {
			ok[slot] = tier
			continue
		}
		if bad == nil {
			bad = map[string]string{}
		}
		bad[slot] = tier
	}
	return ok, bad
}

// allowedFor 是这个槽位收得下的取值：语义档位 + 这个槽位自己声明的例外
// （见 agentapi.Slot.Also，比如 Claude Code 的 `inherit`）。
//
// 不认识的槽位名只给档位：那种条目本来就该被丢掉（多半是界面手里那份快照旧了），
// 给它开例外等于替一个不存在的槽位背书。
func allowedFor(slot string) []string {
	for _, s := range Agent().Slots {
		if s.Name == slot {
			return append(append([]string(nil), domain.Roles...), s.Also...)
		}
	}
	return domain.Roles
}

// WriteSlotTiers 存槽位映射。空表 = 清掉这个键（回到出厂缺省）。
//
// 值必须是已知档位：写进来的东西会被原样注入成模型名，一个打错的档位名在上游那
// 边是一个不存在的模型——那是最难往回追的一种故障（配置看着像模像样）。
func WriteSlotTiers(m map[string]string) error {
	for slot, tier := range m {
		if !slices.Contains(allowedFor(slot), tier) {
			return i18n.E("{slot}: {tier} is not one of the values this slot takes — pick one of {known}",
				i18n.A{"slot": slot, "tier": tier, "known": allowedFor(slot)})
		}
	}
	s := store.LoadState()
	if len(m) == 0 {
		delete(s.ModuleConfig, SlotsKey)
		return store.SaveState(s)
	}
	// 只留与缺省不同的那些：留下一份「和缺省一样」的条目，会让以后改缺省的人
	// 发现自己的改动对一部分用户不生效——而那些用户从没配过任何东西。
	diff := map[string]string{}
	for slot, tier := range m {
		if def, known := defaultTier(slot); known && def != tier {
			diff[slot] = tier
		}
	}
	raw, err := json.Marshal(diff)
	if err != nil {
		return i18n.Ef(err, "cannot encode the slot map: {err}", nil)
	}
	if s.ModuleConfig == nil {
		s.ModuleConfig = map[string][]byte{}
	}
	s.ModuleConfig[SlotsKey] = raw
	return store.SaveState(s)
}

// defaultTier 说出厂设置里这个槽位归哪一档；不认识的槽位返回 false。
func defaultTier(slot string) (string, bool) {
	for _, s := range Agent().Slots {
		if s.Name == slot {
			return s.Tier, true
		}
	}
	return "", false
}

// slotTier 是交给内核的那个回调（见 agentapi.Agent.SlotTier）：这个槽位此刻走哪儿。
//
// 返回空串 = 没改过，用缺省。**不在这里兜底**：缺省归内核那边的 TierOf 管，
// 两处都兜一遍的话「到底是谁决定的」就又成了要看两个地方才知道的事。
func slotTier(s agentapi.Slot) string {
	ok, _ := ReadSlotTiers()
	return ok[s.Name]
}

// slotNote 说这个槽位有没有被改过、改成了什么；没改返回空串。
func slotNote(name, def string) string {
	ok, bad := ReadSlotTiers()
	if v, isBad := bad[name]; isBad {
		return i18n.T("the config says {tier}, which is not a tier — the default is in use",
			i18n.A{"tier": v})
	}
	if v := ok[name]; v != "" && v != def {
		// 「下一次接管才生效」必须写在这里：env 是**启动时注入**的，改完映射之后
		// 跑着的会话一个字节都不会变（见 docs/07-clients-runtime.md）。不说这句，
		// 用户看到的是一次「点了没反应」。
		return i18n.T("set in the config: {tier} (the default is {def}) — "+
			"takes effect the next time claude is taken over (newgate on claude)",
			i18n.A{"tier": v, "def": def})
	}
	return ""
}

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
func (facts) SlotTier(s agentapi.Slot) string {
	ok, _ := ReadSlotTiers()
	return ok[s.Name]
}

// lockReason 说这两张卡此刻有没有意义；有意义返回空串。
//
// 判据就一条：**这台机器上有没有 claude**。没有的话，「槽位走哪个档位」「分类器
// 裸不裸奔」写得再对也一个字节都不会生效——那个命令根本不存在。界面据此把整张卡
// 锁灰（见 lib/view 的 Concept.Locked），用户就不会改完才发现白改。
//
// 用 Agent().OnPath() 而不是自己去查 PATH：那是**唯一**一份「装没装」的判据
// （扣掉我们自己的 shim 目录），另写一份就会在两个界面上给出不同答案。
func lockReason() string {
	if Agent().OnPath() {
		return ""
	}
	return i18n.T("claude is not installed on this machine — "+
		"nothing on this card takes effect yet. Install it with `newgate claude -y`.", nil)
}
