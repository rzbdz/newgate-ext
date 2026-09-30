package home

import (
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
	configapi "github.com/rzbdz/newgate/modules/config"
)

// 本文件是交给界面的那一面：**一屏看到我这些链**。

// registerView 把这一节挂上去。没有 web 界面时什么都不做（见 module.go）。
//
// 登记时**一个文件都不读**：产出函数 `concepts` 要等到真的有人来看界面才跑。
// 这一条在这里格外要紧——链的结论要重读并重新解析每一份 profile（见
// configapi.Config.AllChains 与 lib/view 的包注释），而每一条 `newgate …` 命令
// 都会跑到这里，其中绝大多数没有人会打开界面。
func registerView(v view.Service, cfg configapi.Config, deps overviewDeps) (modules.Release, error) {
	// `.Landing()`：首屏落在这一节上。多个人声明时按 Source 取最小的那个
	// （见 lib/view 的 Sections），所以它不是一个「抢座位」的动作——多一份声明
	// 不会让这一屏变成随机的，只会按 Source 分胜负。
	//
	// **不归组**（曾经是 `.In("Overview")`）：侧栏把**声明了落点的那一栏**排在目录
	// 第一位、且不给它画分组标题（见 Sidebar.svelte 的 rows）。归组的话它会连同
	// 标题一起被卷到中间去，而「凭什么它最上面」这个问题就答不上来了——它自己在
	// 第一位时，位置本身就是那句话，与「不归组的那几栏排最前面」是同一条理由。
	section := view.Title(func() string { return i18n.T("Home", nil) }).Landing()
	// 这一节上只挂**一颗**动作：fallback 那个状态开关（见 overview.go 的
	// fallbackAction）。曾经还有一颗「全都探一遍」，2026-09-29 按用户的话去掉了
	// （原话：「主页全部探一遍那个按钮也是傻啊，直接移除掉吧」）——探某一份是卡片头
	// 上那颗的事，而「全都探一遍」是一次要打十几个上游、几十秒才有结果的批量操作，
	// 放在一屏拿来扫的界面上，点它的人多半只想看看某一家的延迟。
	// fallback 链总开关，与探活并排挂在右上角（判据同：没装网关就不挂——链都没有，
	// 关一个不存在的开关没有意义）。
	if a, ok := fallbackAction(deps.gateway); ok {
		section = section.Does(a)
	}
	return v.Register(Name, section, concepts(cfg, deps))
}

// concepts 是「此刻这些链长什么样」。每次被调用都重新读盘——CLI 刚建的档位、
// 刚改的 providers.json 会在下一次快照里出现（见 view 包注释里那段）。
//
// 读不出来时**整节报错**（而不是画一屏空的）：AllChains 的错误只有两种——配置
// 目录整个读不了，或者一份 profile 都读不出来，两种都不是「这台机器没有链」那个
// 意思。画成空屏会让用户以为配置丢了，而真正的原因（权限、路径）一个字都看不到。
// 这与 config 那边逐张卡片给 Broken 的做法不冲突：那里是**一份文件**坏了，别的
// 照常；这里失败的是「所有链」这件事本身。
//
// # 两概念共用一次 AllChains
//
// 这一节有两张卡（首屏那张总览 + 逐条读的链卡），它们摆的必须是**同一个时刻**的
// 配置。各自问一次 AllChains 的代价不止是双倍（那一位要重读并重新解析每一份
// profile），更糟的是两次调用之间盘可能被人改了一笔——于是两张卡说的是两份不同的
// 配置，而屏幕上完全看不出异样。所以链只取一次，往下传（见 chains.go 的文件头）。
func concepts(cfg configapi.Config, deps overviewDeps) view.Contributor {
	return func() ([]view.Concept, error) {
		all, err := cfg.AllChains()
		if err != nil {
			return nil, err
		}
		// **只有一张卡**。曾经这里还有一张 `home.chains`（逐条读的那一屏：每一档
		// 解析成什么、谁被跳过、为什么），2026-09-29 按用户的话删掉了——原话是
		// 「这个 tab、卡片就是没用的，直接删除：候选链」。它讲的每一件事，首屏那张
		// 卡的时刻表上都有一份（摊开那张卡就是同样的结论），而它多买到的只有一个
		// 标签条。那句话在这一屏上尤其贵：主页要的是「一屏看完」，多一条标签就多
		// 一层要点。
		return []view.Concept{overviewConcept(all, deps)}, nil
	}
}
