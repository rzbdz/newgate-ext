package home

import (
	"sort"
	"strings"
	"sync"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
	breakerapi "github.com/rzbdz/newgate/modules/breaker"
	configapi "github.com/rzbdz/newgate/modules/config"
	"github.com/rzbdz/newgate/modules/config/store"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
	gatewayapi "github.com/rzbdz/newgate/modules/gateway"
)

// 本文件是首屏那一张卡：**让哪个客户端走哪一份档位**。
//
// # 它和 chains.go 的分工
//
// chains.go 把 config 的链结论摆成一叠卡（纯配置，谁看都一样，`newgate tier` 要的
// 是同一份结论）。这里多两样东西，而且两样都**只能在后端合**：
//
//  1. **健康**。链是哪条由 config 算（纯函数），每条 binding 快不快由 breaker 记
//     （它自己的账本）。首屏要的是「这条链走起来会怎样」，那就得把两本账摆在一起。
//     让前端按 `provider/model` 去 join 也确实画得出来，代价是**两件只有拥有者
//     知道的事被抄进了浏览器**：链卡上哪一格算链头、多少毫秒算慢（阈值在
//     breaker/status，只有一处）。两个抄件都会在各自的模块里改，而前端那份没有
//     任何东西会红。所以 join 在这里发生。
//  2. **动作**。「就用它」要写 state.json 里那个客户端的链头——那是核心里
//     `confighook.AgentProfile` 的事，浏览器不该知道那个键长什么样。
//
// # 一次快照，一屏一个时刻
//
// 与 chains.go 同一条：AllChains 只问一次，health 只 Snapshot 一次。分开问会拼起
// 两个时刻的配置，而界面上看不出任何异样。健康那一份先被摊成一张 map（键是
// binding），而不是每站线性找一遍——十几份 profile × 五档 × 几站是几百次查找。
//
// # 依赖方向
//
// 对内核的 import 全是**公开契约**（configapi / agentapi / breakerapi / gatewayapi）
// 与共享叶子（store）。没有一个内部实现包。四个能力里只有 config 是硬依赖（没有链
// 就没有这张页面），其余三个都是 Optional：健康表没装就没有延迟那一格、网关没装就
// 没有探活按钮、客户端目录没装就没有上面那排标签——**少一样都照常出页面**，
// 而不是整张卡消失（见 module.go 的装配）。

// overviewID 是首屏那一张卡的稳定身份。
const overviewID = "home.overview"

// overviewDeps 是这张页面用得上的那几个**可选**端口。
//
// 零值（全 nil）是合法状态：那种装配下页面就是「一份 profile 一张卡、链照常列、
// 没有延迟也没有按钮」。判据是「没有它，这一屏还能不能说真话」，能，就 Optional。
type overviewDeps struct {
	agents  agentapi.AgentCatalog
	health  breakerapi.Breaker
	gateway gatewayapi.Gateway
}

// overviewConcept 把链结论 + 健康 + 客户端目录合成首屏那一张卡。
//
// all 是**调用方已经取好的那一份**：同一节里的两概念（这张与 chains）必须摆的是
// 同一个时刻的配置。各自再问一次 AllChains 会读到两个时刻的盘，而屏幕上看不出
// 任何异样（见 chains.go 的文件头与 view.go 的 concepts）。
func overviewConcept(all []*configapi.Chains, d overviewDeps) view.Concept {
	health := healthByBinding(d.health)
	agents := overviewAgents(d.agents)
	cards := make([]view.OverviewCard, 0, len(all))
	for _, one := range all {
		if one == nil {
			continue
		}
		cards = append(cards, overviewCard(one, agents, health, d.gateway))
	}
	c := view.Concept{
		ID: overviewID, Kind: view.KindOverview,
		Title: i18n.T("Routes", nil),
		Data:  view.Overview{Agents: agents, Cards: cards},
		// Order 0：与 home.chains 同住一节，这一张排前面（它才是首屏要的那一屏，
		// 见 view.Concept.Order）；chains 给自己一个更大的数。
		Order: 0,
		// 没有 Apply：这一屏上的改动全部走**动作**（「就用它」「探一下」），
		// 没有「整张卡交回来」这种东西——它是算出来的结论，不是一份文档。
		//
		// 没有 Live：它要重读并重新解析每一份 profile（view.Concept.Live 的判据
		// 是「读一次贵不贵」，不是「数据会不会变」）。探活的结果靠动作跑完之后
		// 那次重读带回来，不靠轮询。
	}
	// fallback 关着时，这一屏画的那些「后面还垫着几站」**一站都不会走**——链还在
	// 配置里，只是转发侧被一刀切到链头了。不说出来的话，这一屏上每一张卡都在讲一个
	// 此刻不成立的承诺：用户看着 +7 的角标，以为断了会有人接上。
	//
	// 所以它不是一句装饰，是这个屏幕上**最要紧的那个事实**——语气也就给重的那个。
	if d.gateway != nil && !d.gateway.FallbackOn() {
		c.Note = &view.Note{
			Text: i18n.T("the fallback chain is off — every request stops at its chain head", nil),
			Tone: view.ToneBad,
		}
	}
	return c
}

// overviewAgents 列出这台机器认识的客户端，外加它们各自的链头。
//
// 顺序按 id 排（机器标记）：标签会跳动的页面，用户每次都要重新找一遍。
//
// **末尾那一档空的「全部客户端」是必须的，哪怕目录里列着一堆客户端**：全局默认
// （`state.default_profile`）是每个没单配过的客户端实际走的那条链，而改它的唯一
// 手段是 `use:global:<profile>`——那一族动作按契约只在**当前这一档是全局**时画出来
// （见 web 侧 Overview.svelte 的 actions()：它按当前档的 id 拼 `use:<who>:<profile>`）。
// 一档空的都没有时，那个 id 永远拼不出来，于是「全局默认」在这一屏上变成只读的
// ——而它恰恰是「所有客户端一起换链」唯一的那一下。
//
// 排在最后而不是最前：具体客户端是这一屏的主体，全局那一档是它们的兜底，摆在
// 末尾读起来是「还有一个总的」。默认选中的那一档也因此在**装了**的客户端里挑。
func overviewAgents(cat agentapi.AgentCatalog) []view.OverviewAgent {
	st := store.LoadState()
	var out []view.OverviewAgent
	if cat != nil {
		names := cat.Names()
		sort.Strings(names)
		for _, id := range names {
			ap := agentapi.AgentProfile{AgentID: id}
			out = append(out, view.OverviewAgent{
				ID:   id,
				Name: id, // 客户端名是**机器取值**（claude / codex），它本来就是给人看的字。
				// ActiveFor 而不是 Active[id]：没单配过时它返回全局默认，而那句才是
				// 「这个客户端的请求落到哪条链上」的真话（confighook.AgentProfile 里
				// Active 与 Stored 的分工见那里：界面上的**取值**用 Stored，这里说的
				// 是它此刻实际走谁，用 Active）。
				Profile: ap.Active(),
				// Own 说的是**这一家自己设过没有**。它与 Profile 是两码事，而界面
				// 必须能分开：「给 claude 固定选 ds」与「claude 跟着全局、全局恰好是
				// ds」路由结果一样，但改掉全局之后一个跟着变、一个不变。只说 Profile
				// 等于把这件事藏起来（判据就是 confighook 的 IsOverride，不另立一套）。
				Own:   ap.IsOverride(),
				Ready: cat.Installed(id),
			})
		}
	}
	// Ready 恒 true：它不是「装了没有」，而是「这一档能不能点」——空这一档永远能点。
	// Own 恒 true：这一档**就是**全局默认本身，不存在「它跟着谁」。
	return append(out, view.OverviewAgent{
		ID: "", Name: i18n.T("all clients", nil), Ready: true, Own: true,
		Profile: st.ActiveFor(""),
	})
}

// overviewCard 是一份 profile 在这一屏上的那张卡。
func overviewCard(one *configapi.Chains, agents []view.OverviewAgent, health map[string]breakerapi.Status, g gatewayapi.Gateway) view.OverviewCard {
	card := view.OverviewCard{
		Profile: one.Profile,
		File:    profileFile(one.Profile),
		Default: one.Default,
		Roles:   make([]view.OverviewRow, 0, len(one.Keys)),
		Actions: cardActions(agents, one.Profile, one.Keys, g),
	}
	for _, chain := range one.Keys {
		card.Roles = append(card.Roles, overviewRow(one.Profile, chain, health))
	}
	return card
}

// healthByBinding 把健康表摊成 `provider/model → Status`。
//
// 键的拼法与健康表自己的行 ID 同一个函数（breaker.BindingKey）：两边各拼一次就必须
// 拼成同一个串，而拼错的症状是「延迟那一格永远空着」——不报错，只是没有数据。
func healthByBinding(b breakerapi.Breaker) map[string]breakerapi.Status {
	if b == nil {
		return nil
	}
	rows := b.Snapshot()
	out := make(map[string]breakerapi.Status, len(rows))
	for _, r := range rows {
		out[breakerapi.BindingKey(r.Provider, r.Model)] = r
	}
	return out
}

// overviewRow 是这一档的链 + 每一站的健康。
func overviewRow(profile string, chain configapi.Chain, health map[string]breakerapi.Status) view.OverviewRow {
	row := view.OverviewRow{
		// 行 ID 与链卡那边**同一个拼法**（`profile/tier`）：界面拿它做 key，跨快照
		// 稳定（见 view.OverviewRow.ID）。
		ID:   profile + "/" + chain.Key,
		Tier: chain.Key,
		Note: skipNote(chain.Skips),
	}
	if len(chain.Steps) > 0 {
		row.Head = chain.Steps[0].Binding.String()
	} else {
		// 空链是 bad：这一档此刻没得走（与 chains.go 的 rowOf 同一条）。
		row.Tone = view.ToneBad
	}
	row.Steps = overviewSteps(chain.Steps, health)
	return row
}

// overviewSteps 是链上的每一站，外加它在健康表里的那一格。
func overviewSteps(list []configapi.Step, health map[string]breakerapi.Status) []view.OverviewStep {
	if len(list) == 0 {
		return nil
	}
	out := make([]view.OverviewStep, 0, len(list))
	for _, s := range list {
		step := view.OverviewStep{
			Provider: s.Binding.Provider,
			Model:    s.Binding.Model,
			Profile:  s.Profile,
		}
		if h, ok := health[breakerapi.BindingKey(s.Binding.Provider, s.Binding.Model)]; ok {
			if ms, tone, sampled := breakerapi.LatencySample(h); sampled {
				step.LatencyMs, step.LatencyTone = ms, tone
			}
			// Grade 是机器标记（fluent / laggy / …），原样出去：它与 `newgate probe`
			// 的输出、与健康表那一列是同一套词。翻了它，两边就对不上了。
			if s := strings.TrimSpace(string(h.Grade)); s != "" {
				step.Probe = s
			}
		}
		out = append(out, step)
	}
	return out
}

// ---------- 动作 ----------

// useActionPrefix 是「就用它」那一族动作 ID 的前缀。
//
// 整个 ID 是 `use:<agent>:<profile>`：`<agent>` 是客户端机器标记（`global` 是
// 「所有客户端都用它」那一档），`<profile>` 是这张卡说的是哪一份。
//
// 两段都必要，理由写在 view.OverviewCard.Actions 上（一句话：同一个 agent 的动作在
// 十几张卡上各有一个，而账本按 ID 找人取的是第一个匹配——不带 profile 就会点到别人
// 身上）。界面按它挑按钮：当前那一栏是哪个 agent 就挑哪个前缀。
const useActionPrefix = "use:"

// useGlobal 是「所有客户端都用它」那一档的 agent 段。
//
// 它不是某个真实客户端：那个动作写的是**全局默认**（`state.json` 的
// `default_profile`），而不是任何一行的覆盖。用 `global` 这个词而不是空串，是因为
// 空串拼出来的 `use::production` 读起来像拼错了。
const useGlobal = "global"

// useAutoPrefix 是「改回自动」那一族动作 ID 的前缀，整串是 `auto:<agent>`。
//
// **为什么另起一族而不是 `use:<agent>:auto`**：`<profile>` 那一段装的是磁盘上的
// 档位名，而档位名是用户起的——`auto` 是一个完全合法的档位名。挤进同一族的话，
// 一台真的有一份 `auto.kv` 的机器上，那个「改回自动」的按钮会和一个「固定用 auto
// 这一份」的按钮**拼成同一个 ID**，而账本按 ID 取的是第一个匹配：用户点「固定用
// auto」，实际执行的是松开——静默地改错东西，界面上一点异样都没有。
//
// 两个前缀也就不共用一套解析：`use:` 后面必须有**两**段，`auto:` 后面只有一段。
const useAutoPrefix = "auto:"

// cardActions 是长在这张卡头上的按钮：「就用它」+「整张卡探一遍」。
func cardActions(agents []view.OverviewAgent, profile string, chains []configapi.Chain, g gatewayapi.Gateway) []view.Action {
	out := useActions(agents, profile)
	if g != nil {
		if targets := probeTargetsOf(chains); len(targets) > 0 {
			out = append(out, view.Action{
				ID:    "probe",
				Label: func() string { return "⚡ " + i18n.T("Test this profile", nil) },
				Run:   func() (string, error) { return "", probeSet(g, targets) },
			})
		}
	}
	return out
}

// useActions 是这张卡上的「用它」按钮，外加那张**正固定着它的卡**上的「改回自动」。
//
// 四条取舍：
//
//   - **固定用它**的判据是「这一家**自己**设过没有」，不是「它此刻解析成哪一份」。
//     这两件事必须分开：「给 claude 固定选 ds」与「claude 跟着全局、全局恰好是 ds」
//     是**两个状态**——路由结果此刻一样，但改掉全局之后一个跟着变、一个不变。拿解析
//     结果去比，前一版的症状正是「用户想固定住，界面却说它已经在用了、不给按钮」，
//     于是他永远没法把那一步做出来。
//   - **改回自动**只出现在「这一家固定在这一份上」的那张卡上：那是这件事唯一说得通
//     的地方（别处没有「你本来固定着，现在想松开」这个意思）。它反着走
//     `setAgentProfile(id, "")`，落点是 confighook 那句「空串 = 回到跟随全局默认」。
//   - **没装的客户端不出两种按钮**：这台机器上改了也不会有任何东西经过它。而「哪些
//     装了」是客户端目录的判据（它自己的事实优先、其次找 PATH），不是这里猜的。
//   - **全局那一档永远在**：一个客户端都没装时，它是唯一能把链换掉的手段。
func useActions(agents []view.OverviewAgent, profile string) []view.Action {
	st := store.LoadState()
	out := make([]view.Action, 0, len(agents)+1)
	if st.DefaultProfile != profile {
		out = append(out, view.Action{
			ID:    useActionID(useGlobal, profile),
			Label: func() string { return i18n.T("use for every client", nil) },
			// SetDefaultProfile(name, false)：**不动**那些单配过的客户端。把它们一起
			// 收敛是另一个决定（配置那节的 `apply to all` 才是那个），而这一屏说的是
			// 「这份档位应用起来」。
			Run: func() (string, error) { return "", setAgentProfile("", profile) },
		})
	}
	for _, a := range agents {
		if a.ID == "" || !a.Ready {
			continue
		}
		id, name := a.ID, a.Name
		pinned := a.Own && a.Profile == profile
		if !pinned {
			out = append(out, view.Action{
				ID: useActionID(id, profile),
				Label: func() string {
					return i18n.T("use for {client}", i18n.A{"client": name})
				},
				Run: func() (string, error) { return "", setAgentProfile(id, profile) },
			})
		}
		if pinned {
			out = append(out, view.Action{
				ID:    useAutoPrefix + id,
				Label: func() string { return i18n.T("follow the default", nil) },
				Run:   func() (string, error) { return "", setAgentProfile(id, "") },
			})
		}
	}
	return out
}

// useActionID 拼一个「用它」动作的 ID（见 useActionPrefix）。
func useActionID(agent, profile string) string {
	return useActionPrefix + agent + ":" + profile
}

// setAgentProfile 改某个客户端的链头；空 agent = 改全局默认。
//
// 两条路都落在 config 的**共享叶子**（store）上，而不是自己拼 state.json：那个文件
// 的键与校验规则（「这个 profile 真的存在」）是 config 的知识，抄一遍的代价是这个
// 界面能写出一个打错名字的链头——而那个客户端的每一次请求都会起不来。
func setAgentProfile(agent, profile string) error {
	if agent == "" {
		return store.SetDefaultProfile(profile, false)
	}
	return agentapi.AgentProfile{AgentID: agent}.Write(profile)
}

// probeAllAction 是右上角那一个「全都探一遍」。
//
// # 为什么它长在**节**上而不是某一张卡上
//
// 它要探的是这一屏**所有** profile 的链站——那个集合不属于任何一张卡（卡片级的
// 探活在 cardActions 里）。节的动作用户永远够得着，不管正看着哪一张卡。
//
// # 为什么它要在 Run 里现读一次
//
// 节的动作用户登记时构造一次就定了（见 view.Section.Actions），而这里是**今天**
// 有哪些链——登记那一刻读出来的那份到点击时早就过期了。所以闭包捕获的是 cfg 与
// 端口，链在 Run 里现取。
// fallbackAction 是右上角那个 fallback 链总开关。
//
// # 它关掉的到底是什么（说清楚，因为界面上那个词有歧义）
//
// **整条链**，不分「本 profile 内的」与「从别的 profile 借来的」。转发侧的落地是
// `steps = steps[:1]`，而 `steps` 是 resolve.BuildChain 展开出来的**一条平表**——
// 它按 profile 优先级把所有 profile 的候选展开进同一个列表，每一步身上带着来源
// （就是界面上的 `ds` / `cheap` 那些小药丸）。开关没用那个来源。
//
// 所以「只禁跨 profile、保留自己这一份里的 fallback」今天**做不到**：那要先让链在
// 截断处认得出自己是哪一段来的（改 resolve 与 forward），是另一件事。界面按能做
// 到的那件事写文案，不按想做到的那件写——写成「只禁跨 profile」就是在骗人。
//
// # 为什么按下的那一刻才取状态
//
// `on` 是**构造这个闭包时**读到的那一刻的值（快照会重读，所以每次都是新的）。
// 翻的是它的反面，而不是「设成某个固定值」：按钮上写的是此刻要做的那个动作。
func fallbackAction(g gatewayapi.Gateway) (view.Action, bool) {
	if g == nil {
		return view.Action{}, false
	}
	on := g.FallbackOn()
	return view.Action{
		ID: "fallback",
		Label: func() string {
			if on {
				return i18n.T("stop at the chain head", nil)
			}
			return i18n.T("enable the fallback chain", nil)
		},
		Run: func() (string, error) { return "", g.SetFallback(!on) },
	}, true
}

// probeAllAction 是右上角那个「全都探一遍」。
func probeAllAction(cfg configapi.Config, g gatewayapi.Gateway) (view.Action, bool) {
	if g == nil {
		return view.Action{}, false
	}
	return view.Action{
		ID:    "probe-all",
		Label: func() string { return "⚡ " + i18n.T("Test everything", nil) },
		Run: func() (string, error) {
			all, err := cfg.AllChains()
			if err != nil {
				return "", err
			}
			targets := probeAllTargets(all)
			if len(targets) == 0 {
				return "", nil
			}
			return "", probeSet(g, targets)
		},
	}, true
}

// ---------- 探活的扇出 ----------

// probeTarget 是一次探活的目标。key 是给人看的那个拼法（`provider/model`）。
type probeTarget struct {
	provider, model, key string
}

// probeSet 是「一次探一批 binding」的共用实现，返回一句给用户看的话（可能为空）。
//
// # 为什么只做到**批**这一层，没有单条的探活动作
//
// 单条探活今天只有一处入口：健康表（`breaker.health`）每一行上那个按钮——那才是
// 「这条 binding 现在通不通」该问的地方。首屏这一屏上的探活是**按卡**（这张 profile
// 的链此刻怎么样）与**按屏**（全都刷一遍）的，两种都是这一批的路子。给每一站再挂
// 一个单条按钮要两层身份（哪一张卡的哪一档），而它买到的东西与健康表那一行完全
// 重合——多出来的是一排按钮，而这一屏上最常用的那个（「就用它」）会被它们淹掉。
//
// # 为什么要限并发、还要限总时长
//
// 单条探活最坏要等一个首字节超时（默认 12s，见 store 的 Timeouts）。一条一条探
// 十几条就是几分钟——而这是**一次 HTTP 请求**，浏览器与反代都不会等那么久。所以：
// 并发探，并且**总共只等 probeBudget**，超了就如实说「还有几条在飞」。
//
// 超时那几条**不会白探**：它们在后台照常跑完、结论照常进策略层（ProbeBinding 自己
// 会灌），用户下一次刷新就看见了。这也是这一屏没有 Live 却不别扭的原因。
//
// 返回的 error 是**横幅上那句话**。走错误那条路（而不是静默成功）的理由：这一发里
// 确实有东西没通、或者没等到，而按这个按钮的人要的答案正是这个。咽下去的话，界面上
// 只剩「延迟那一格还是旧的」——看起来像按钮没生效。
// probeSet 是「一次探一批 binding」的入口：收一个网关，探这一批。
func probeSet(g gatewayapi.Gateway, targets []probeTarget) error {
	if g == nil || len(targets) == 0 {
		return nil
	}
	return probeBatch(g.ProbeBinding, targets)
}

// probeBatch 是扇出的那一半，也是**可以被单测的那一半**：它只要一个「探一条」
// 的函数，而不是整个网关端口。
//
// 为什么要这条缝：探活的扇出有它自己的坑（并发、去重、没等完的那些），而网关端口
// 有十几个方法——为一条测试假造一整个网关，代价比被测的东西还大，而假得不像的那种
// 替身又会让测试验的是替身的行为。收一个函数就够了：真正要问的只有「并发对不对、
// 失败有没有说出来」。
func probeBatch(probe func(provider, model string) (gatewayapi.ProbeOutcome, error), targets []probeTarget) error {
	type result struct {
		target probeTarget
		err    error
	}
	ch := make(chan result, len(targets))
	sem := make(chan struct{}, probeFanout)
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(t probeTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			_, err := probe(t.provider, t.model)
			ch <- result{target: t, err: err}
		}(t)
	}
	go func() { wg.Wait(); close(ch) }()

	deadline := time.After(probeBudget)
	var failed []string
	done := 0
	for done < len(targets) {
		select {
		case r := <-ch:
			done++
			if r.err != nil {
				failed = append(failed, r.target.key)
			}
		case <-deadline:
			// 没等完：把还没回来的算「在飞」，不再等。
			return probeReport(len(targets), failed, len(targets)-done)
		}
	}
	return probeReport(len(targets), failed, 0)
}

// probeReport 是这一批探活的结果那句话；全都通了就返回 nil（横幅不出现）。
func probeReport(total int, failed []string, inFlight int) error {
	if len(failed) == 0 && inFlight == 0 {
		return nil
	}
	parts := []string{i18n.N("probed {n} binding", "probed {n} bindings", total,
		i18n.A{"n": total})}
	if len(failed) > 0 {
		sort.Strings(failed)
		parts = append(parts, i18n.N("{n} not answering ({list})",
			"{n} not answering ({list})", len(failed),
			i18n.A{"n": len(failed), "list": joinLimit(failed, 6)}))
	}
	if inFlight > 0 {
		parts = append(parts, i18n.N("{n} still in flight — refresh in a moment",
			"{n} still in flight — refresh in a moment", inFlight, i18n.A{"n": inFlight}))
	}
	return i18n.E("{report}", i18n.A{"report": strings.Join(parts, " · ")})
}

// joinLimit 列出前 n 条，多出来的收成「还有 N 条」。
//
// 十几条不通时全铺出来会把横幅变成一堵墙，而用户要的第一句话是「哪几家不通」。
func joinLimit(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	rest := len(items) - n
	return strings.Join(items[:n], ", ") + " · " +
		i18n.N("and {n} more", "and {n} more", rest, i18n.A{"n": rest})
}

// probeTargetsOf 是这几条链上的去重目标（一次探活不该把同一条 binding 打两遍——
// 同一个 provider/model 在一条链上重复出现是常态，跨 profile 的链更是）。
func probeTargetsOf(chains []configapi.Chain) []probeTarget {
	var out []probeTarget
	seen := map[string]bool{}
	for _, c := range chains {
		for _, s := range c.Steps {
			key := breakerapi.BindingKey(s.Binding.Provider, s.Binding.Model)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, probeTarget{provider: s.Binding.Provider, model: s.Binding.Model, key: key})
		}
	}
	return out
}

// probeAllTargets 是这一屏上所有链站的去重目标（右上角那个按钮用）。
func probeAllTargets(all []*configapi.Chains) []probeTarget {
	var out []probeTarget
	seen := map[string]bool{}
	for _, one := range all {
		if one == nil {
			continue
		}
		for _, t := range probeTargetsOf(one.Keys) {
			if seen[t.key] {
				continue
			}
			seen[t.key] = true
			out = append(out, t)
		}
	}
	return out
}

// probeFanout 是一次最多同时探几条。8 与 `newgate probe` 那条命令同一个数：同一个
// 上游在同一个时刻被我们打两发，没有理由在这里比命令行更凶。
const probeFanout = 8

// probeBudget 是一次探活动作最多等多久。
//
// 15s 是「几站 + 一点余量」：单条最坏 12s（首字节超时），并发 8 路时十几条通常一轮
// 就回来了。定一个上限是为了不让一次点击变成一分钟的转圈——超时的那些在后台照常
// 跑完，结论照样进账本。
const probeBudget = 15 * time.Second
