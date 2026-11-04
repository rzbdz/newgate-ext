package hermes

import (
	"encoding/json"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
)

// 本文件是 hermes 交给 web 界面的那一面：**它跑哪个档位**，外加网关那两件事
// （清单同步、要不要重启）。形状与 codex 的档位卡对齐（同一套 Kind、同一个位置），
// 因为它们是同一件事：「这个客户端此刻走哪个档位」。

const conceptID = "hermes.model"

func profileOf() agentapi.AgentProfile { return agentapi.AgentProfile{AgentID: ID} }

func registerView(v view.Service) (modules.Release, error) {
	return v.Register("hermes",
		view.Title(func() string { return i18n.T("Hermes", nil) }).
			In(func() string { return i18n.T("Clients", nil) }).
			Of(view.FieldClients),
		func() ([]view.Concept, error) { return []view.Concept{modelConcept()}, nil })
}

func modelConcept() view.Concept {
	a := Agent()
	_, bad := slotsOf().Read()
	items := make([]view.ToggleItem, 0, len(a.Slots)+1)
	items = append(items, profileOf().ProfileItem())
	for _, s := range a.Slots {
		why := s.Desc
		if note := slotsOf().Note(s.Name, s.Tier); note != "" {
			why += " · " + note
		}
		items = append(items, view.ToggleItem{
			ID: s.Name, Label: s.Name, Kind: view.ToggleSelect,
			Value:   agentapi.TierOf(facts{}, s),
			Options: slotsOf().AllowedFor(s.Name),
			Why:     why,
		})
	}
	file := "state.json"
	if len(bad) > 0 {
		file = i18n.T("state.json — {key} could not be read, the defaults are in use", i18n.A{"key": SlotsKey})
	}
	c := view.Concept{
		ID: conceptID, Kind: view.KindToggles,
		Title: i18n.T("Hermes model", nil),
		Data:  view.Toggles{File: file, Items: items},
		Order: 20,
		Apply: applyModel,
		// 没装 hermes 的机器上整张锁灰（facts{} 必须传：它比 PATH 多认 ~/.local/bin，
		// 见 facts.go）。
		Locked: agentapi.NotInstalled(a, facts{}),
	}
	if (Takeover{}).IsTakenOver(ConfigPath()) {
		tone := view.ToneOK
		if restartPending() {
			tone = view.ToneWarn
		}
		c.Note = &view.Note{Text: statusText(false), Tone: tone}
		c.Actions = []view.Action{{
			ID:    "sync",
			Label: func() string { return i18n.T("sync /model list", nil) },
			Run: func() (string, error) {
				_, err := syncCatalog()
				return "", err
			},
		}, {
			ID:    "restart",
			Label: func() string { return i18n.T("restart hermes gateway", nil) },
			Run:   func() (string, error) { return "", restartGateway(true) },
		}}
	}
	return c
}

// applyModel 写链头与槽位映射，然后**当场重新接管一次**（已接管时）。
//
// 与 codex 卡片不同的一处：codex 的档位要等下一次 `newgate on codex` 才写进文件，
// 那是它的历史行为；hermes 这里按下保存就写——槽位的意义就是 config.yaml 里那几格，
// 只记在 state.json 里等于什么都没发生（与 codex 的 switchMode 同一条规矩）。
func applyModel(edit json.RawMessage, _ string) (string, error) {
	var patch map[string]string
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the slot map in this request is not readable: {err}", i18n.A{"err": err})
	}
	profile, patch := agentapi.SplitProfile(patch)
	known := map[string]bool{}
	for _, s := range Agent().Slots {
		known[s.Name] = true
	}
	next := map[string]string{}
	for slot, tier := range patch {
		if known[slot] {
			next[slot] = tier
		}
	}
	if err := profileOf().Write(profile); err != nil {
		return "", err
	}
	if err := slotsOf().Write(next); err != nil {
		return "", err
	}
	if (Takeover{}).IsTakenOver(ConfigPath()) {
		if _, err := (Takeover{}).Apply(proxyPort()); err != nil {
			return "", err
		}
	}
	return "", nil
}
