package confighook

import (
	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate/lib/i18n"
)

// 本文件是 confighook 交给**各客户端模块的 view.go** 的那一件东西：把「这个客户端
// 从哪条链起步」拼成界面上的一格。
//
// # 为什么它有单独一个文件，而不是塞进 profile.go
//
// profile.go 是**数据**（怎么读、怎么写、空串是什么意思），这里是把数据摆成
// `view.ToggleItem` 那个形状。与 modules/config/view.go 里 `stateView` 的分工一样：
// 形状归契约包（lib/view），内容归拥有者，而「拥有者的 UI 那一面」集中在一个
// view.go 里，读的人一眼看得出这个模块往界面上贡献了什么。
//
// 方向上是允许的：`lib/view` 是**叶子契约**（见它的包注释：箭头是
// `modules/* → lib/view → component`），业务模块直接 import 它正是设计里的事。
// 它拖进来的只有 component 与 i18n，没有 HTTP、没有前端。

// ProfileItem 造出「链头」那一格。
//
// 三家客户端（claude / codex / opencode）都插这一格，所以它必须**只有一份实现**：
// 「空串 = 回到全局缺省」这条语义在每一家的 Apply 里都要一模一样，实现成
// 「写一个空字符串」的话，state.json 里会留下一个 `"claude": ""` 的条目——
// 那正是槽位表那边花了力气避免的「同值记录」。
func (p AgentProfile) ProfileItem() view.ToggleItem {
	why := i18n.T("Which chain this client starts from. "+
		"The chain decides what each tier resolves to; changing it takes effect the next "+
		"time this client is taken over (newgate on {agent}).",
		i18n.A{"agent": p.AgentID})
	if n := p.Note(); n != "" {
		why += " · " + n
	}
	active := p.Active()
	return view.ToggleItem{
		ID:   ProfileItemID,
		Kind: view.ToggleSelect,
		// 取值是**存下来的那个**，不是解析之后的（见 AgentProfile.Stored）：
		// 这一格要回答的是「我单独定过没有」，而 `Active()` 把「跟随缺省到 ds」
		// 与「我就是要 ds」压成同一个字符串——两种状态在界面上长得一样，用户就
		// 没法知道改全局默认时这个客户端会不会跟着走。
		Label: i18n.T("Profile", nil),
		Value: p.Stored(),
		// 空串排在最前：它是「跟随全局缺省」，不是一个没得选的空档。
		Options: append([]string{""}, p.Options()...),
		// 那一档的说法要**点名跟着谁**：用户看到的是「跟随缺省」，而「缺省是谁」
		// 只有后端知道。括号不会与 profile 名撞——那是个文件名。
		OptionLabels: map[string]string{
			"": i18n.T("{profile} (the global default)", i18n.A{"profile": active}),
		},
		Why: why,
	}
}

// SplitProfile 把界面交回来的那一坨拆成「链头」与**其余格子**。
//
// 界面一次交全部格子（见 kinds/Toggles.svelte），而同一张卡上既有链头、又有槽位
// 映射——两者落到完全不同的地方（state.json 的 `active` 表 vs 各家的槽位表）。
// 拆包这一步三家都要做，而它有一条**做错了不会红**的规矩：链头必须从 rest 里
// **摘掉**，不能留在里面。留着的话它会被当成一个槽位名交给槽位表——那边对不认识
// 的名字是「丢掉」，所以表现是「链头改了但没保存」，而槽位那边一切正常。
func SplitProfile(patch map[string]string) (profile string, rest map[string]string) {
	profile = patch[ProfileItemID]
	rest = make(map[string]string, len(patch))
	for k, v := range patch {
		if k != ProfileItemID {
			rest[k] = v
		}
	}
	return profile, rest
}
