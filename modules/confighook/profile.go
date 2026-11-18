package confighook

import (
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate/lib/i18n"
)

// 本文件是**一个客户端「从哪条链起步」的那一格**。
//
// # 它为什么是机制
//
// 链头（profile）本来只有全局一个，per-agent 那张表（`state.json` 的 `active`，
// 见 domain.State.Active）从 M1 起就在——`claude` 用贵的、`opencode` 用便宜的，
// 各切各的。但那条路一直**只有 CLI 有**（`newgate --set-profile <名> --agent <x>`），
// 界面上一个字都看不到：用户在浏览器里能改这个客户端的每一个槽位走哪一档，却改不了
// 「它整条链从哪儿开始」——而后者更靠前，它是那些档位最终落到哪家模型上的前提。
//
// 每一家客户端都要这一格（claude / codex / opencode），所以它住内核：留在某一个
// 模块里，另外两家只能各抄一遍，而里面有两条规矩是**抄错了不会红**的：
// 「空 = 跟随全局缺省」（而不是一份写死的 profile 名），以及写之前要验那个 profile
// 真的存在（打错一个名字的后果是这个客户端的每一次请求都起不来）。

// AgentProfile 是某一个客户端的链头。
type AgentProfile struct {
	// AgentID 是这个客户端的 id（`claude` / `codex` / …）。
	AgentID string
}

// Active 是这个客户端此刻**实际走**的 profile（解析之后的那一个）。
//
// **它总是有一个值**：没单独设过时返回全局默认。它回答的是「这个客户端的请求
// 落到哪条链上」，所以写给人看的那句话（Note）用它。
//
// 界面上的**取值**不要用它——用 Stored。两者的差别是这一格存在的全部理由。
func (p AgentProfile) Active() string {
	return store.LoadState().ActiveFor(p.AgentID)
}

// Stored 是这个客户端**存下来的**那个选择；空串 = 没单独设过、跟随全局默认。
//
// 为什么界面要的是它、不是 Active：两者在「用户选了 ds」与「用户什么都没选、
// 而此刻全局默认正好是 ds」这两种情形下**返回同一个字符串**（`ds`），而这两件事
// 配置上完全不同（一个在 state.json 里记着一条，一个没有；行为差别是「以后改全局
// 默认时它跟不跟着走」）。
//
// 这一条是实测踩出来的（2026-09-21）：第一版把 Active 当取值端出去，于是那一格
// 永远显示 `ds`——「跟随缺省」那个标签一次都没露过面，用户的原话是「我点击 apply
// 之后，还是显示没有缺省的啊」。
func (p AgentProfile) Stored() string {
	return store.LoadState().Active[p.AgentID]
}

// IsOverride 说这个客户端是不是**单独设过**（而不是跟着全局默认走）。
//
// 界面拿它说一句「跟随全局缺省」：不说的话，用户看到下拉里有一个值，会以为那是
// 自己设的——而他改全局默认时发现这个客户端没跟着变，就会以为哪儿坏了。
//
// # 判据是「有没有一条记录」，不是「它跟缺省一不一样」
//
// 与 Write 那边同一条（见那里的长注释）。用户明确选了 ds、而此刻全局默认**也正好
// 是 ds** 时，这两件事在配置上完全不同：一条在 state.json 里记着、以后改全局默认
// 它**不会**跟着走，另一条什么都没记、改全局它跟着走。
//
// 用「跟缺省一不一样」当判据的症状实测过（2026-09-29）：在网页首屏上给某一家点
// 「就用 ds」，而自动恰好也是 ds——那一下**明明写进了一条记录**，界面却把这一家
// 报成「跟随自动」，高亮于是从这张卡跳到「自动」那张上。用户的原话是「我点 ds，
// 怎么跳到选中自动了」。Write 那边 2026-09-21 就为同一个理由改对了，这一条漏了。
func (p AgentProfile) IsOverride() bool {
	st := store.LoadState()
	v, ok := st.Active[p.AgentID]
	return ok && v != ""
}

// Options 是可选的 profile 名。
//
// 读不出来时返回空表（没有 mappings 目录、目录读不了）：界面拿到一个空下拉，
// 而不是一个报错的卡片——一个读不了的目录不该让整张卡消失（同 lib/view 里
// Concept.Broken 那条：坏消息要显示出来，不是把东西藏起来）。
func (p AgentProfile) Options() []string {
	names, err := store.ListProfiles()
	if err != nil {
		return nil
	}
	return names
}

// Write 改这个客户端的链头。**空串 = 回到全局默认**（那个键消失）。
//
// 与槽位表那条同规矩：留下一份「和缺省一样」的记录，会让以后改缺省的人发现自己的
// 改动对一部分用户不生效——而那些用户从没配过任何东西。
func (p AgentProfile) Write(profile string) error {
	// 空串 = 回到「跟随全局默认」（那个键消失）。
	if profile == "" {
		return store.ClearActiveProfile(p.AgentID)
	}
	// 别的值一律**记下来**，哪怕它恰好等于此刻的全局默认。
	//
	// 曾经这里写的是「与全局默认同名 = 没设过」（与槽位表那条同规矩）。那是错的，
	// 因为这一格的取值是**存下来的那个**（见 Stored）：用户明确选了 `ds` 之后，
	// 那一格会显示回 `ds（缺省）`——他想固定住的东西看起来像没固定，而界面上
	// 再没有任何办法表达「我就是要它」。
	//
	// 槽位表那边不一样：那边的取值本来就是「此刻实际走哪一档」，没有第二种读法。
	return store.SetActiveProfile(p.AgentID, profile)
}

// Note 是给界面的一句话：这个客户端此刻是跟着全局默认，还是单独设过。
func (p AgentProfile) Note() string {
	if p.IsOverride() {
		return i18n.T("set for this client only (other clients follow the global default)", nil)
	}
	return i18n.T("following the global default ({profile})", i18n.A{"profile": p.Active()})
}

// ProfileItemID 是那一格在界面载荷里的键（形状见 view.go 的 ProfileItem）。
//
// 它必须与槽位名**不可能**撞上：界面一次交全部格子（见 kinds/Toggles.svelte），
// 而槽位表那边是按名字取值的——撞上就会把一个槽位的档位当成 profile 写下去。
// 槽位名是客户端自己的模型键（`model` / `sonnet`），所以一个带前缀的词就够。
const ProfileItemID = "profile"
