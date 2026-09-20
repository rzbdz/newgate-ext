package claudecode

import (
	"encoding/json"
	"strings"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/store"
	"github.com/rzbdz/newgate/modules/gateway/gatewaystate"
)

// classifierConcept 是这个模块给 web 界面的一面：Bash 分类器此刻被怎么对待。
//
// # 为什么这张卡比别的卡片更硬
//
// `naked forever` 是**永久短路**安全分类器的开关：开着的时候，分类器请求一律
// 直接批准、不再问上游。它在今天的产品里只出现在 `newgate status` 的一行里——
// 而那一行是网关**代转**的插件状态（special.StatusProvider）。关掉 cli 的那份
// 发行版（dist-dashboard）里 status 与 `newgate naked off` 都不存在，于是那个
// 状态在那份构建里**既看不见、也没有命令能关**，唯一的路是手工去 state.json
// 里改那个键。一张卡片就是「编辑器」与「一个 JSON 文件」的差别。
//
// # 为什么自己解析，而不是问插件
//
// 插件那条路（special.Statuses）在**整层关掉**时返回空——而「层关着、键还留着
// forever」恰恰是最该被看见的局面：把层一开回来，那扇门就又开了。所以这里读的是
// **磁盘上的配置**（ParseNakedConfig 一处判据，含懒过期），再单独说明它此刻是否
// 真的在生效。这也与 CLI 的 status 行同源：同一句话、同一个判据。
//
// # 为什么只读
//
// `newgate naked` 的写入带时限语义（`on` 与 `forever` 是两件事，见 classifier-
// naked.go），而这一版界面没有选时限的控件。给它一个 bool 开关，等于把「永久」
// 悄悄降级成「一段时间」——比不给更糟（同 pluginmanager 把 footgun 留成只读）。
func classifierConcept() view.Concept {
	st := store.LoadState()
	cfg, active := ParseNakedConfig(st.ModuleConfig[NakedConfigKey])
	naked := ""
	if active {
		if cfg.Mode == "forever" {
			naked = "forever"
		} else {
			naked = "on"
		}
	}
	override := ""
	if o, err := classifierOverride(st); err == nil && o != nil {
		override = o.String()
	}
	items := []clToggle{
		{
			ID: "naked", Label: i18n.T("Naked", nil), Kind: "select",
			Value:   naked,
			Options: []string{"", "on", "forever"},
			Why:     nakedWhy(st, cfg, active),
		},
		{
			ID: "classifier_override", Label: i18n.T("Classifier override", nil), Kind: "text",
			Value: override,
			Why:   overrideWhy(st),
		},
	}
	return view.Concept{
		ID: "claudecode.classifier", Kind: view.KindToggles,
		Title: i18n.T("Claude Code Bash classifier", nil),
		Data:  clData{File: "state.json", Items: items},
		// Live：限时窗口那句话写的是「还有多久自动关闭」——页面开着不动，那个数字
		// 就是错的（而它说的是安全门什么时候关上）。整张卡读的是内存里的
		// state.json，常问不亏。
		Live:  true,
		Apply: applyClassifier,
	}
}

// clToggle 一行一个控件，形状与 config 的 toggles 一致。
type clToggle struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Kind    string   `json:"kind"`
	Value   string   `json:"value,omitempty"`
	Options []string `json:"options,omitempty"`
	Why     string   `json:"why,omitempty"`
}

type clData struct {
	File  string     `json:"file"`
	Items []clToggle `json:"items"`
}

// nakedWhy 把「现在是什么状态」与「这一刻算不算数」写在一句话里。
func nakedWhy(st *domain.State, cfg NakedConfig, active bool) string {
	state := i18n.T("off — the Bash classifier is asked as usual", nil)
	switch {
	case active && cfg.Mode == "forever":
		state = i18n.T("on (forever) — the classifier is short-circuited and every request logs [naked]", nil)
	case active:
		state = i18n.T("on — it turns itself off in {left}",
			i18n.A{"left": time.Until(cfg.ExpiresAt).Round(time.Second)})
	}
	// 「配置里写着什么」与「它此刻算不算数」是两件事：这一层（或这枚插件）被关掉
	// 时，配置留着、门是关的，把层一开回来门就又开了——不说出来，用户会照着上面
	// 那句去关一个此刻根本没生效的东西。
	if note := layerNote(st); note != "" {
		state += " · " + note
	}
	return state
}

func overrideWhy(st *domain.State) string {
	if _, err := classifierOverride(st); err != nil {
		return i18n.T("classifier_override is not valid: {err}", i18n.A{"err": err})
	}
	return i18n.T("provider/model, or @key to follow another tier. Empty = the Bash classifier rides the light tier.", nil)
}

// layerNote 说「这一层此刻算不算数」；算数时返回空串（没什么可说的）。
func layerNote(st *domain.State) string {
	off := []string{}
	if gatewaystate.PluginOff(st, nakedPluginName) {
		off = append(off, nakedPluginName)
	}
	if gatewaystate.PluginOff(st, backgroundPluginName) {
		off = append(off, backgroundPluginName)
	}
	enabled := gatewaystate.SpecialEnabled(st)
	if !enabled {
		return i18n.T("the whole special_treatment layer is off — none of this counts until it is switched back on", nil)
	}
	if len(off) > 0 {
		return i18n.T("{names}: switched off individually, so that setting counts for nothing right now",
			i18n.A{"names": strings.Join(off, ", ")})
	}
	return ""
}

// applyClassifier 写这两格。裸奔的三种状态与 CLI 一一对应（off / 60s 窗口 /
// forever），**不把 forever 降级成窗口**——那正是这张卡以前只读的理由，现在把三态
// 都摆出来，理由就不成立了。
func applyClassifier(edit json.RawMessage, _ string) (string, error) {
	var patch map[string]string
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the classifier settings in this request are not readable: {err}",
			i18n.A{"err": err})
	}
	if v, ok := patch["naked"]; ok {
		switch strings.TrimSpace(v) {
		case "":
			if err := saveNakedConfig(NakedConfig{}); err != nil {
				return "", err
			}
			// Mode 为空串时 ParseNakedConfig 判为「没开」，但那会把一个空对象写进
			// state.json；直接删掉这个键更干净（与 `newgate naked off` 同一条）。
			if err := deleteNakedConfig(); err != nil {
				return "", err
			}
		case "on":
			if err := saveNakedConfig(NakedConfig{
				Mode: "on", ExpiresAt: time.Now().Add(nakedDefaultTTL),
			}); err != nil {
				return "", err
			}
		case "forever":
			if err := saveNakedConfig(NakedConfig{Mode: "forever"}); err != nil {
				return "", err
			}
		default:
			return "", i18n.E("naked must be off, on or forever, got {value}", i18n.A{"value": v})
		}
	}
	if v, ok := patch["classifier_override"]; ok {
		v = strings.TrimSpace(v)
		s := store.LoadState()
		if v == "" {
			delete(s.ModuleConfig, "classifier_override")
		} else {
			b, err := domain.ParseBindingString(v)
			if err != nil {
				return "", err
			}
			raw, err := json.Marshal(b)
			if err != nil {
				return "", err
			}
			if s.ModuleConfig == nil {
				s.ModuleConfig = map[string][]byte{}
			}
			s.ModuleConfig["classifier_override"] = raw
		}
		if err := store.SaveState(s); err != nil {
			return "", err
		}
	}
	return "", nil
}

// deleteNakedConfig 清掉裸奔那个键（关掉它）。
func deleteNakedConfig() error {
	s := store.LoadState()
	delete(s.ModuleConfig, NakedConfigKey)
	return store.SaveState(s)
}

// 插件名取自插件自己（`classifierNaked{}.Name()`），不是抄一份字面量：抄的那份
// 会漂移，而漂移的后果是 PluginOff 问了一个不存在的插件——**失败方向是 fail-open**，
// 那句提醒会静默地永远不出现。
var (
	nakedPluginName      = classifierNaked{}.Name()
	backgroundPluginName = background{}.Name()
)

// slotsConcept 是这张卡：**Claude Code 的哪个槽位归哪个档位**。
//
// # 为什么它该有一张卡
//
// 映射的缺省值住在 agent.go 里（那是出厂设置），而 2026-09-21 之前它**只**住在
// 那里——想「让 Bash 分类器走 normal，免得 Sonnet 一挂整场断」或者「subagent 全
// 降一档省点钱」，只有改代码重编一条路。用户的原话是「目前看 go 代码就是他妈的
// 写死的吧」。
//
// 它是配置，不是代码：改完之后**下一次接管**就生效（env 是启动时注入的，跑着的
// 会话要 `newgate on claude` 重来一次——这句写在 why 里，不写的话用户会以为点了
// 没生效）。
//
// # 为什么不做成「每个档位一张卡」
//
// 一档一行就是这张卡：五行，一眼看完，改哪一行都只有一个下拉。做成五张卡的话，
// 「我想让 sonnet 走重档」这件事要在五个地方里找出对的那一个。
func slotsConcept() view.Concept {
	a := Agent()
	_, bad := ReadSlotTiers()
	items := make([]clToggle, 0, len(a.Slots))
	for _, s := range a.Slots {
		why := s.Desc
		if note := slotNote(s.Name, s.Tier); note != "" {
			why += " · " + note
		}
		items = append(items, clToggle{
			ID: s.Name, Label: s.Name, Kind: "select",
			Value:   a.TierOf(s),
			Options: domain.Roles,
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
		ID: "claudecode.slots", Kind: view.KindToggles,
		Title: i18n.T("Claude Code slots", nil),
		Data:  clData{File: file, Items: items},
		// Order 20：排在分类器那张卡后面。分类器是「此刻安全门关没关」（会变、要
		// 看），这里是「装完调一次的归属」（很少动）——把会变的放前面。
		Order: 20,
		Apply: applySlots,
	}
}

// applySlots 写槽位映射。
//
// 载荷是**全部槽位**（见 kinds/Toggles.svelte：一次交全部，不是增量）。所以这里
// 拿到的是一份完整的表，直接交给 WriteSlotTiers——它会把「与缺省相同」的那些丢掉，
// 于是把某一行改回缺省 = 那一行从配置里消失，而不是留下一条多余的同值记录。
func applySlots(edit json.RawMessage, _ string) (string, error) {
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
	if err := WriteSlotTiers(next); err != nil {
		return "", err
	}
	return "", nil
}
