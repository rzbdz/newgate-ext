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
		if slices.Contains(domain.Roles, tier) {
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

// WriteSlotTiers 存槽位映射。空表 = 清掉这个键（回到出厂缺省）。
//
// 值必须是已知档位：写进来的东西会被原样注入成模型名，一个打错的档位名在上游那
// 边是一个不存在的模型——那是最难往回追的一种故障（配置看着像模像样）。
func WriteSlotTiers(m map[string]string) error {
	for slot, tier := range m {
		if !slices.Contains(domain.Roles, tier) {
			return i18n.E("{slot}: {tier} is not a tier — pick one of {known}",
				i18n.A{"slot": slot, "tier": tier, "known": domain.Roles})
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
