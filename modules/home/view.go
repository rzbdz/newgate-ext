package home

import (
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
	configapi "github.com/rzbdz/newgate/modules/config"
)

// 本文件是交给界面的那一面：**一屏看到我这些链**。

// conceptID 是这一节的稳定身份（前端排序、草稿、行动作回传都按它走）。
const conceptID = "home.chains"

// registerView 把这一节挂上去。没有 web 界面时什么都不做（见 module.go）。
//
// 登记时**一个文件都不读**：产出函数 `concepts` 要等到真的有人来看界面才跑。
// 这一条在这里格外要紧——链的结论要重读并重新解析每一份 profile（见
// configapi.Config.AllChains 与 lib/view 的包注释），而每一条 `newgate …` 命令
// 都会跑到这里，其中绝大多数没有人会打开界面。
func registerView(v view.Service, cfg configapi.Config) (modules.Release, error) {
	// `.Landing()`：首屏落在这一节上。多个人声明时按 Source 取最小的那个
	// （见 lib/view 的 Sections），所以它不是一个「抢座位」的动作——多一份声明
	// 不会让这一屏变成随机的，只会按 Source 分胜负。
	//
	// `.In(i18n.T("Overview"))` 而不是不分组：不分组的那几栏**排在最前面**
	// （见 Sidebar.svelte 的 rows），而首屏本来就要落在这一节上——它自己跑到最上面
	// 之后，那一栏反而没有位置可站，读的人也就不知道它凭什么在最上面。给它一个
	// 名字，是让「为什么它在第一位」变成看得见的（与 Section.Group 那条注释同源）。
	return v.Register(Name,
		view.Title(func() string { return i18n.T("Home", nil) }).
			Landing().
			In(func() string { return i18n.T("Overview", nil) }),
		concepts(cfg))
}

// concepts 是「此刻这些链长什么样」。每次被调用都重新读盘——CLI 刚建的档位、
// 刚改的 providers.json 会在下一次快照里出现（见 view 包注释里那段）。
//
// 读不出来时**整节报错**（而不是画一屏空的）：AllChains 的错误只有两种——配置
// 目录整个读不了，或者一份 profile 都读不出来，两种都不是「这台机器没有链」那个
// 意思。画成空屏会让用户以为配置丢了，而真正的原因（权限、路径）一个字都看不到。
// 这与 config 那边逐张卡片给 Broken 的做法不冲突：那里是**一份文件**坏了，别的
// 照常；这里失败的是「所有链」这件事本身。
func concepts(cfg configapi.Config) view.Contributor {
	return func() ([]view.Concept, error) {
		all, err := cfg.AllChains()
		if err != nil {
			return nil, err
		}
		return []view.Concept{{
			ID: conceptID, Kind: view.KindChains,
			Title: i18n.T("Chains", nil),
			Data:  Chains(all),
			// Order 0：这一节今天只有这一张卡（Order 只在**同一节里**分先后，
			// 见 view.Concept.Order），写 0 是把它放在默认位置上，将来这一节长出
			// 第二张卡时新卡只需给自己一个更大的数。
			//
			// 没有 Apply：这一屏上的改动走**行上的动作**（「把这一档换成 X」，
			// 见 configapi.Chain.Actions）。整张卡级的写回没有意义——链是算出来的
			// 结论，不是一个可以整份交回来的文档。
			//
			// 没有 Live：这一面读盘、不读内存态（见 lib/view 的 Concept.Live——
			// 声明的判据是「读一次贵不贵」，不是「数据会不会变」）。
			Order: 0,
		}}, nil
	}
}
