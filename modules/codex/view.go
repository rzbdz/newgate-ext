package codex

import (
	"encoding/json"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
)

// 本文件是 codex 交给 web 界面的那一面：**它跑哪个档位**。
//
// 形状与 claudecode 的槽位卡对齐（同一套 Kind、同一套取值），因为它们是同一件事：
// 「这个客户端此刻走哪个档位」。差别只有一处——codex 只有一个槽位（它的 `model`
// 就是全部），而且那个值**写进 config.toml**，不是注入环境变量（见 agent.go）。
//
// 为什么这张卡值得有：codex 的档位住在 `~/.codex/config.toml` 的 `model = "…"` 里，
// 而那份文件是**接管时**写的。也就是说「改档位」这件事在用户那边要经过
// `newgate on codex`，而在此之前他没有任何地方能看见自己现在跑的是哪一档——
// `newgate agents` 要在终端里敲，而它在浏览器里没有对应物。

// SlotsKey 是 state.json 的 `module_config` 里的键（本模块的覆盖表）。
const SlotsKey = "codex_slots"

// slotsOf 是本模块那张覆盖表（实现见 confighook.SlotOverrides）。
func slotsOf() agentapi.SlotOverrides {
	return agentapi.SlotOverrides{
		Key:     SlotsKey,
		AgentID: ID,
		Slots:   func() []agentapi.Slot { return Agent().Slots },
	}
}

// conceptID 是这张卡的稳定身份。
const conceptID = "codex.model"

// registerView 把这张卡挂上去。没有 web 界面时什么都不做。
func registerView(v view.Service) (modules.Release, error) {
	return v.Register("codex",
		view.Title(func() string { return i18n.T("Codex", nil) }),
		concepts)
}

func concepts() ([]view.Concept, error) {
	return []view.Concept{modelConcept()}, nil
}

func modelConcept() view.Concept {
	a := Agent()
	_, bad := slotsOf().Read()

	items := make([]view.ToggleItem, 0, len(a.Slots))
	for _, s := range a.Slots {
		why := s.Desc
		if note := slotsOf().Note(s.Name, s.Tier); note != "" {
			why += " · " + note
		}
		items = append(items, view.ToggleItem{
			ID: s.Name, Label: s.Name, Kind: view.ToggleSelect,
			// 此刻走哪儿：本模块交给内核的那份事实（见 facts.go），与接管写 TOML
			// 时问的是同一个实现——界面显示的和真正写进文件的不会分家。
			Value: agentapi.TierOf(facts{}, s),
			// 档位 + 这个槽位自己认的例外（见 agentapi.Slot.Also）：下拉里没有的
			// 取值，用户就没法设——而说明里写着可以。
			Options: slotsOf().AllowedFor(s.Name),
			Why:     why,
		})
	}

	// 解析不了的那一份要单独说：那是一个**改坏了的文件**，而它此刻静静地不起作用
	// （缺省值仍然是对的）。不报的话，用户会以为自己的改动生效了。
	file := "state.json"
	if len(bad) > 0 {
		file = i18n.T("state.json — {key} could not be read, the defaults are in use",
			i18n.A{"key": SlotsKey})
	}
	return view.Concept{
		ID: conceptID, Kind: view.KindToggles,
		Title: i18n.T("Codex model", nil),
		Data:  view.Toggles{File: file, Items: items},
		// Order 20：与 claudecode 的槽位卡同一个位置。这一节里它是「装完调一次的
		// 归属」，不是「此刻变没变」，所以排在会变的东西后面。
		Order: 20,
		Apply: applyModel,
		// 没装 codex 的机器上这张卡整张锁灰：改档位写得再对，接管也不会发生——
		// 那份 config.toml 根本不存在（判据见 confighook.NotInstalled）。
		Locked: agentapi.NotInstalled(a),
	}
}

// applyModel 写槽位映射。
//
// 载荷是**全部槽位**（见 kinds/Toggles.svelte：一次交全部，不是增量），所以这里是
// 一份完整的表，直接交给共享的那份 Write——它会把「与缺省相同」的那些丢掉，于是
// 把这一行改回缺省 = 那个键从配置里消失，而不是留下一条多余的同值记录。
//
// 没有 CAS 基线：这份表的家是 state.json 的 module_config（一个模块一格），而
// 它**没有**对应的原文卡（不像 claude 的槽位表那样与 config 的文件卡配对）。
// 两个标签页同时改这一张卡时，后写的赢——代价是那一格的取舍，不是丢一份文件。
func applyModel(edit json.RawMessage, _ string) (string, error) {
	var patch map[string]string
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the slot map in this request is not readable: {err}",
			i18n.A{"err": err})
	}
	known := map[string]bool{}
	for _, s := range Agent().Slots {
		known[s.Name] = true
	}
	next := map[string]string{}
	for slot, tier := range patch {
		// 不认识的槽位名丢掉：那多半是**界面手里那份快照旧了**（模块升级后槽位变了），
		// 把它写进去只会在 state.json 里留一条谁也不认识的记录。
		if known[slot] {
			next[slot] = tier
		}
	}
	if err := slotsOf().Write(next); err != nil {
		return "", err
	}
	return "", nil
}
