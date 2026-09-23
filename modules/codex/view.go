package codex

import (
	"encoding/json"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/store"
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
		Slots:   Agent().Slots,
	}
}

// conceptID 是这张卡的稳定身份。
const conceptID = "codex.model"

// profileOf 是这个客户端的**链头**那一格（读写的实现在内核，见
// confighook.AgentProfile）：codex 整条链从哪条 profile 起步。
func profileOf() agentapi.AgentProfile { return agentapi.AgentProfile{AgentID: ID} }

// registerView 把这张卡挂上去。没有 web 界面时什么都不做。
//
// `In("Clients")`：与 claudecode / opencode 那两家归到同一档。漏了它的症状是
// **codex 那一栏掉到侧栏最上面、不属于任何分组**（2026-09-21 实测被用户抓到）
// ——分组是贡献者报的，界面只画拿到的标题，所以漏了不会有任何东西变红。
func registerView(v view.Service) (modules.Release, error) {
	return v.Register("codex",
		view.Title(func() string { return i18n.T("Codex", nil) }).
			In(func() string { return i18n.T("Clients", nil) }),
		concepts)
}

func concepts() ([]view.Concept, error) {
	return []view.Concept{modelConcept(), modelsConcept()}, nil
}

func modelConcept() view.Concept {
	a := Agent()
	_, bad := slotsOf().Read()

	// 第一格是**链头**，后面的才是各槽位走哪一档。顺序有意义：档位最终落到哪家
	// 模型上要先有链，先有链才有档。
	items := make([]view.ToggleItem, 0, len(a.Slots)+1)
	items = append(items, profileOf().ProfileItem())
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
		// 模式那一句与切换的按钮**不在这张卡上**：它们搬去了「Codex 模型名」那张
		// （见 modelsview.go）。理由：模式的全部意义就是「codex 的模型名算哪一档」，
		// 而那张表只在改名模式下真的生效——分开放的话，用户看着一张表却不知道它
		// 此刻算不算数。2026-09-21 用户的原话是「我用了改名模式，为什么没有映射表
		// 让用户填写啊」，那次反馈同时说明了两件事：表要有，而且它和模式是一件事。
		// 没装 codex 的机器上这张卡整张锁灰：改档位写得再对，接管也不会发生——
		// 那份 config.toml 根本不存在（判据见 confighook.NotInstalled）。
		// facts{} 必须传：本模块的 Installed 比通用 PATH 查找多认 nvm 的版本
		// bin 目录（见 facts.go：daemon 的 PATH 里没有它），漏传就退化成
		// 「daemon 的 PATH 上没有 codex」= 卡片对这台机器说谎。
		Locked: agentapi.NotInstalled(a, facts{}),
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
	// 同一张卡上有两件事：链头（一格）与槽位映射（若干格）。拆包这一步在内核
	// （见 confighook.SplitProfile）：链头必须从槽位那份里摘掉，否则它会被当成一个
	// 槽位名丢掉，症状是「链头改了但没保存」，而槽位那边一切正常。
	profile, patch := agentapi.SplitProfile(patch)

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
	// 先写链头再写槽位：链头写不进去（profile 不存在）时整个保存失败，而槽位那边
	// 一个字节都不该动——两份配置要么一起改，要么一起不改。
	if err := profileOf().Write(profile); err != nil {
		return "", err
	}
	if err := slotsOf().Write(next); err != nil {
		return "", err
	}
	return "", nil
}

// modeNote 说这一刻用的是哪种接管模式（见 models.go 的三个常量）。
//
// 语气是 ok 而不是 warn：两种模式都是**正当的选择**，没有哪一种「有问题」——
// warn 会让人以为配置出事了，然后去找一个并不存在的故障。它是陈述，不是告警。
func modeNote() *view.Note {
	text := i18n.T("takeover mode: one tier is written into config.toml", nil)
	if Mode() == ModeRename {
		text = i18n.T("rename mode: codex's own model names map to tiers", nil)
	}
	return &view.Note{Text: text, Tone: view.ToneOK}
}

// modeActions 是切模式的那个按钮——**只画「另一个」**。
//
// 与档位卡上那对 apply 同一条取舍（见 config/view.go 的 profileActions）：把当前
// 已经在生效的那一种也画成按钮，就是一个点了不会改变任何事的按钮，而它站在旁边
// 会把真会做事的那个淹掉。差别只有一处：那边是「做/不做」，这边是**两个对称的
// 选项**，所以按钮的字直接写模式的**名字**（`rename mode`），不写「切换到…」——
// 一台只有两个档的开关，标名字比标动作短，而且读的人不用在脑子里做一次取反。
func modeActions() []view.Action {
	if Mode() == ModeRename {
		return []view.Action{{
			ID:    "mode-takeover",
			Label: func() string { return i18n.T("takeover mode", nil) },
			Run:   func() (string, error) { return switchMode(ModeTakeover) },
		}}
	}
	return []view.Action{{
		ID:    "mode-rename",
		Label: func() string { return i18n.T("rename mode", nil) },
		Run:   func() (string, error) { return switchMode(ModeRename) },
	}}
}

// switchMode 换模式，并**当场按新模式重新接管一次**。
//
// # 为什么不能只记下模式、等下一次 `newgate on codex`
//
// 模式的全部意义就是**config.toml 里写什么**。只写状态不接管，等于什么都没发生，
// 而卡片上那句 Note 已经变了——用户会以为自己切过去了，然后在 codex 里换模型、
// 发现没用。这与档位卡上那个 apply 是同一条规矩：按下去就得当场生效。
//
// # 三次结局都要有说法
//
//   - 写状态失败 → 整个动作失败（界面把原因显示出来）；
//   - codex 没装 / 那份 config.toml 不在 → **模式照记，接管跳过**。这不是错：
//     用户可能先在界面上选好，之后再装 codex——`Apply` 给的 Skipped 就是这句话；
//   - 接管失败（文件写不进去）→ 报错。**但那时的状态已经写下了**，所以界面显示的
//     模式与文件里的不一致——这是刻意的：状态说的是「你选的是哪种」，而失败的是
//     这一次写入。重按一次按钮（或 `newgate on codex`）就补上了。
func switchMode(mode string) (string, error) {
	if err := SetMode(mode); err != nil {
		return "", err
	}
	if _, err := (Takeover{}).Apply(proxyPort()); err != nil {
		return "", err
	}
	return "", nil
}

// proxyPort 是接管要写进 base_url 的那个端口。与命令行走**同一个来源**
// （state.json 的 port，读不到退回缺省），否则界面切的模式与终端 `newgate on codex`
// 写出来的会指向两个不同的端口。
func proxyPort() int {
	if st := store.LoadState(); st != nil && st.Port != 0 {
		return st.Port
	}
	return domain.ProxyPort
}
