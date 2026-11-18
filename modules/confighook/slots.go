package confighook

import (
	"encoding/json"
	"slices"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate/lib/i18n"
)

// 本文件是**每个客户端都要有的那张「槽位 → 档位」覆盖表**。
//
// # 为什么它是机制
//
// 槽位映射说的是「这个客户端的哪一个槽位归到哪个语义档位」，而它同时是两件事的
// 交汇：客户端的**槽位身份**（ANTHROPIC_DEFAULT_SONNET_MODEL 是谁）与产品的
// **档位阶梯**（normal 比 mid 强）。
//
//	前者是客户端的知识 —— 归那个模块，写死在它的 agent.go 里，不该可配；
//	后者是**用户的取舍** —— 归用户。「Bash 分类器我想让它走 normal，省得 Sonnet
//	一挂就断」「subagent 全部降一档」这类事，改代码改不了。
//
// 这段逻辑 2026-09-21 之前长在发行版的 modules/claudecode 里（当时只有它一家有
// 槽位映射）。codex 也要一张同样的表——它的 `model` 槽位要能像 claude 那样改档位
// ——于是留在那里的代价只能是抄一遍，而这份东西里有几处是**错了不会红**的：
// 「只留与缺省不同的那些」「坏值滤掉但不让接管失败」「不认识的槽位名只给档位」。
//
// 所以它住内核，客户端只提供两件本家的知识：**state.json 里用哪个键**、以及
// **我有哪些槽位**。

// SlotOverrides 是一个客户端的覆盖表。
//
// 每家一个实例，住在自己模块的包级变量里（见 modules/claudecode 的 slotsOf）。
type SlotOverrides struct {
	// Key 是 state.json 的 `module_config` 里的键（`claude_slots` / `codex_slots`）。
	// 一家一个，互不干扰——与 language 那边同一个位置、同一个理由：state.json 的
	// ModuleConfig 就是「core 不认识的顶层键原样存着」的那一格（见 domain.State）。
	Key string
	// AgentID 是这位客户端的 id，用在给人看的那句话里（「下一次 `newgate on X`
	// 才生效」）——那句话必须点名，否则用户不知道该让谁重新接管。
	AgentID string
	// Slots 是这个客户端的槽位表。
	//
	// 一份**值**而不是一个取它的函数：构造这个值的人（模块自己的 slotsOf）每次
	// 都是现取 `Agent().Slots` 的，所以「表变了」表现为「重新构造一次」——再包一层
	// 闭包只会让读的人多跳一层，而跳的那一层里没有任何东西可替换。
	Slots []Slot
}

// Read 读用户改过的槽位映射（槽位名 → 档位）。没配过返回空表。
//
// 读的时候要**筛**：这个文件是给人手改的，写错一个档位名（`hevy`）会一路注入成
// 模型名发到上游，症状是「某些请求突然 400」，而那跟配置之间隔着好几层。滤掉的
// 条目由调用方报出来（见 Note），不静默吞掉。
func (o SlotOverrides) Read() (ok map[string]string, bad map[string]string) {
	raw := store.LoadState().ModuleConfig[o.Key]
	if len(raw) == 0 {
		return nil, nil
	}
	var all map[string]string
	if err := json.Unmarshal(raw, &all); err != nil {
		// 解析不了 = 整份都不可信。报出来（界面那张卡会说明），但**不**让接管失败：
		// 一个改坏了的映射不该让客户端起不来，缺省值仍然是对的。
		return nil, map[string]string{"": string(raw)}
	}
	ok = make(map[string]string, len(all))
	for slot, tier := range all {
		if slices.Contains(o.AllowedFor(slot), tier) {
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

// AllowedFor 是这个槽位收得下的取值：语义档位 + 这个槽位自己声明的例外
// （见 Slot.Also，比如 Claude Code 的 `inherit`）。
//
// 不认识的槽位名只给档位：那种条目本来就该被丢掉（多半是界面手里那份快照旧了），
// 给它开例外等于替一个不存在的槽位背书。
func (o SlotOverrides) AllowedFor(slot string) []string {
	for _, s := range o.Slots {
		if s.Name == slot {
			return append(append([]string(nil), domain.Roles...), s.Also...)
		}
	}
	return domain.Roles
}

// Default 说出厂设置里这个槽位归哪一档；不认识的槽位返回 false。
func (o SlotOverrides) Default(slot string) (string, bool) {
	for _, s := range o.Slots {
		if s.Name == slot {
			return s.Tier, true
		}
	}
	return "", false
}

// Write 存槽位映射。空表 = 清掉这个键（回到出厂缺省）。
//
// 值必须是已知档位：写进来的东西会被原样注入成模型名，一个打错的档位名在上游那
// 边是一个不存在的模型——那是最难往回追的一种故障（配置看着像模像样）。
func (o SlotOverrides) Write(m map[string]string) error {
	for slot, tier := range m {
		if !slices.Contains(o.AllowedFor(slot), tier) {
			return i18n.E("{slot}: {tier} is not one of the values this slot takes — pick one of {known}",
				i18n.A{"slot": slot, "tier": tier, "known": o.AllowedFor(slot)})
		}
	}
	// 只留与缺省不同的那些：留下一份「和缺省一样」的条目，会让以后改缺省的人
	// 发现自己的改动对一部分用户不生效——而那些用户从没配过任何东西。
	diff := map[string]string{}
	for slot, tier := range m {
		if def, known := o.Default(slot); known && def != tier {
			diff[slot] = tier
		}
	}
	s := store.LoadState()
	// 传进来就是空的、与**减完之后**是空的，是同一件事：这个客户端此刻全走缺省。
	// 两条都得删键——只判前一条的话，把最后一行改回缺省会在 state.json 里留下一个
	// `{}`（实测踩到：codex 只有一个槽位，所以它一路都是这个形状）。那正是上面这句
	// 要避免的「同值记录」，只是发生在文件层而不是条目层。
	if len(diff) == 0 {
		delete(s.ModuleConfig, o.Key)
		return store.SaveState(s)
	}
	raw, err := json.Marshal(diff)
	if err != nil {
		return i18n.Ef(err, "cannot encode the slot map: {err}", nil)
	}
	if s.ModuleConfig == nil {
		s.ModuleConfig = map[string][]byte{}
	}
	s.ModuleConfig[o.Key] = raw
	return store.SaveState(s)
}

// Tier 是交给内核的那个答案（见 AgentFacts.SlotTier）：这个槽位此刻走哪儿。
//
// 返回空串 = 没改过，用缺省。**不在这里兜底**：缺省归 TierOf 管，两处都兜一遍的话
// 「到底是谁决定的」就又成了要看两个地方才知道的事。
func (o SlotOverrides) Tier(s Slot) string {
	ok, _ := o.Read()
	return ok[s.Name]
}

// Note 说这个槽位有没有被改过、改成了什么；没改返回空串。
func (o SlotOverrides) Note(name, def string) string {
	ok, bad := o.Read()
	if v, isBad := bad[name]; isBad {
		return i18n.T("the config says {tier}, which is not a tier — the default is in use",
			i18n.A{"tier": v})
	}
	if v := ok[name]; v != "" && v != def {
		// 「下一次接管才生效」必须写在这里：注入的环境变量是**启动时**给的、
		// 配置文件是**接管时**写的，改完映射之后跑着的会话一个字节都不会变
		// （见 docs/07-clients-runtime.md）。不说这句，用户看到的是一次
		// 「点了没反应」。
		return i18n.T("set in the config: {tier} (the default is {def}) — "+
			"takes effect the next time {agent} is taken over (newgate on {agent})",
			i18n.A{"tier": v, "def": def, "agent": o.AgentID})
	}
	return ""
}

// NotInstalled 是「这家客户端没装在这台机器上」那句**锁灰的理由**；装了返回空串。
//
// 判据只有一条：**这台机器上有没有它**。没有的话，「槽位走哪个档位」写得再对也
// 一个字节都不会生效——那个命令根本不存在。界面据此把整张卡锁灰（见 Concept.Locked），
// 用户就不会改完才发现白改。
//
// 判据**由客户端自己交上来**（facts.Installed），没交才回落到 a.OnPath()。2026-09-22
// 之前这里直接调 a.OnPath()，于是「客户端有自己的知识」这条通道在这张卡上被绕过去了：
// codex 住在 nvm 的版本 bin 目录里，而 daemon 的 PATH 里没有那个目录，卡片就报了
// 一句「这台机器上没有装 codex」——**对 daemon 的 PATH 是真话，对这台机器是假话**，
// 而用户看到的只有后半句。`registry.Installed`（见 module.go）从一开始就是「事实优先」，
// 这里漏掉那一步是两处判据分了家，症状正是两个界面给出不同答案。
//
// 用 a.OnPath() 而不是自己去查 PATH：那是**唯一**一份通用判据（扣掉我们自己的
// shim 目录，见 OnPath），另写一份就会多出第三种答案。
func NotInstalled(a *Agent, facts AgentFacts) string {
	if a == nil || InstalledDefault(a, facts) {
		return ""
	}
	return i18n.T("{agent} is not installed on this machine — nothing on this card takes effect yet. "+
		"Install it with `newgate {agent} -y`.", i18n.A{"agent": a.ID})
}
