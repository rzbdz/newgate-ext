package home

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
	configapi "github.com/rzbdz/newgate/modules/config"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/store"
)

// 本文件把 config 的**读端口**结论（configapi.Config.AllChains）翻译成界面要的形状
// （view.Chains）。它是纯函数：进来一份结论、出去一份数据，不读盘（除了那一处取
// 「profile 落在哪份文件」的 stat）、不碰时钟。
//
// # 为什么这一层值得单独存在，而不是让 config 直接产出 view.Chains
//
// 因为它们是**两个问题**。config 回答的是「这一份档位此刻解析成什么」，与谁说、
// 在哪一屏上说是无关的（命令行 `newgate tier` 要的是同一份结论）；而「一屏怎么
// 摆、链尾那句话怎么写、哪几个动作挂在哪一行」是**产品取舍**，随发行版变——
// 内核里不该有 `home` 这个模块的名字，这一层就是那条接缝（见 core/CLAUDE.md §0）。
//
// # 一次快照，一屏一个时刻
//
// 上面的 all 是**一次** AllChains 拿到的（见 configapi.Config.AllChains 的注释）：
// 分开问十次会拼起十个时刻的配置，而界面上看不出任何异样。所以这里**不重新读盘**，
// 只把拿到的结论摆出来；唯一的一次磁盘访问是取「这份 profile 落在哪份文件」，
// 而那不是链的结论（它是文件的地址，不随解析变）。
//
// # 依赖方向
//
// 对内核的 import 只有三样：**公开契约**（configapi）、共享叶子（paths / store，
// core/CLAUDE.md 明说谁都能直接 import）、以及契约本身（lib/view、lib/i18n）。
// 链的**结论**一律走端口——这里不 import store/resolve 去自己算一条链，那种依赖会
// 在内核重构时断在另一个仓库里，而且那里没有测试会红。

// Chains 把每一份 profile 的链摆成一叠卡。
//
// 顺序**原样保留**：生效的那一份在最前、其余按名字（那是 AllChains 给的，也是
// 「一屏」那一屏的读法——第一眼要找的是「我现在站在哪一份上」，见 orderedProfiles）；
// 卡里的档位次序也原样保留（那是 domain.Roles 的能力序，命令行与 doctor 照同一条）。
// 这里重排一次就是把同一条产品取舍抄成两份，而抄来的那份会漂移。
func Chains(all []*configapi.Chains) view.Chains {
	cards := make([]view.ChainCard, 0, len(all))
	for _, one := range all {
		if one == nil {
			continue
		}
		card := view.ChainCard{
			Profile: one.Profile,
			File:    profileFile(one.Profile),
			Default: one.Default,
			Roles:   make([]view.ChainRow, 0, len(one.Keys)),
		}
		for _, chain := range one.Keys {
			card.Roles = append(card.Roles, rowOf(one.Profile, chain))
		}
		cards = append(cards, card)
	}
	return view.Chains{Cards: cards}
}

// rowOf 把一档的结论摆成一行。
func rowOf(profile string, chain configapi.Chain) view.ChainRow {
	head := ""
	if len(chain.Steps) > 0 {
		head = chain.Steps[0].Binding.String()
	}
	row := view.ChainRow{
		// ID = `profile/tier`：**行 ID 必须在整个概念里唯一**（见 view.ChainRow.ID
		// 与 view.RunRowAction）。档位名单独一个不够用——十份 profile 就有十个
		// `heavy`，而「把这一档换成 X」要动的是**某一份文件里的某一行**。
		ID:   profile + "/" + chain.Key,
		Tier: chain.Key,
		Head: head,
		// Actions **原样透传**：那些闭包由 config 构造（它才知道这一档写在哪份
		// 文件里、此刻的基线是什么、并发改到了怎么跟用户交代），在这里重造一份
		// 就是把这套知识抄进发行版。跨包传递是安全的——它们在同一个进程里等着
		// 被 view.RunRowAction 调（见 configapi.Chain.Actions）。
		Actions: chain.Actions,
	}
	row.Steps = steps(chain.Steps)
	if len(chain.Steps) == 0 {
		// 空链是 bad：这一档此刻没得走（与 configapi.Chain 的注释、`newgate tier`
		// 的「no usable candidate」同一件事）。有链时不着色——这一格只说
		// 「有没有可用的链头」，**不表达健康度**（那要探活，是另一本账，见
		// view.ChainRow.Tone）。
		row.Tone = view.ToneBad
	}
	row.Note = skipNote(chain.Skips)
	return row
}

// steps 把链上的站摆出来。
//
// Note 一律空着：某一站为什么没排更前，属于**整条链**那件事（见 view.ChainRow.Note
// 与 Step.Note 的分工），而今天 resolve 也没有逐站的说明可给（configapi.Step 上
// 只有 Profile 与 Binding）。留一个空字段比编一句话好。
func steps(list []configapi.Step) []view.ChainStep {
	if len(list) == 0 {
		return nil
	}
	out := make([]view.ChainStep, 0, len(list))
	for _, s := range list {
		out = append(out, view.ChainStep{
			Provider: s.Binding.Provider,
			Model:    s.Binding.Model,
			// Profile 要写出来：链会**跨 profile**（本 profile 的候选之后接着别人的），
			// 不写的话用户会去自己正看着的那份文件里找一个不存在的候选。
			// 界面拿它与卡头比，同名时不重复画（见 Chains.svelte 的 foreign）。
			Profile: s.Profile,
		})
	}
	return out
}

// ---------- 跳过：只给汇总，永不逐条铺开 ----------

// skipNote 是链尾那句「还有谁被跳过、为什么」。
//
// # 一条取舍（与 `newgate tier` 同源，措辞各写各的）
//
// **只给按类目的计数**。实测一份配置里 106 条跳过有 91 条是「与更靠前的候选重复」
// ——把必然发生的去重当结果一行行铺开，只会把真正的结论（走谁）淹掉，而这个页面
// 的结构本来就是「一行一条链」。想逐条看的人要的是另一个口子（`newgate tier <档位>`
// 会按类目分组并给每类一句「所以呢」），不是这里。
//
// 类目顺序固定（skipKindOrder）：同一份配置跑两次，这句话必须逐字相同——链卡
// 会被反复重算（每次快照都是一次），一个随 map 遍历序变的句子看起来像「配置又动了」。
func skipNote(skips []configapi.Skip) string {
	if len(skips) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, s := range skips {
		counts[skipKind(s)]++
	}
	parts := make([]string, 0, len(counts))
	for _, kind := range skipKindOrder {
		if n := counts[kind]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", skipLabel(kind), n))
		}
	}
	return i18n.N("{n} candidate did not make it: {reasons}",
		"{n} candidates did not make it: {reasons}", len(skips),
		i18n.A{"reasons": strings.Join(parts, " · ")})
}

// skipKindOrder 是类目的**固定次序**（只有列在这里的类目会被画出来）。
//
// 它同时是一张**契约表**：下面的 skipLabel 只认这几个标记，认不出来的（将来内核
// 新加一类）落进 skipOther 那一栏——数量照报，只是没有专属的名字。整张表漏掉某一类
// 的代价是那一类被静默吞掉，所以这里有 chains_test.go 的一条棘轮：真 skips 递进来
// 之后每一类都必须出现在那句话里。
//
// # 这几个标记是从哪儿来的
//
// 它们不是本模块的词汇，是 **resolve 与显示层之间的协议**：建链时就打好在
// `Skip.Kind` 上，`newgate tier` 用它分组计数、状态块尾行用它、这里也用它
// （见 resolve/chain.go 那组常量的注释：它们是机器标记，不随语言变）。
//
// 字面量在这里重写了一遍而不是 import resolve：链的结论一律走 configapi 端口
// （见 api.go——发行版自己 import resolve 去拼是内核点名要避免的那种依赖），而
// 这几个字符串是**协议**，不是实现细节。写错/内核改名由测试拦：chains_test.go 拿
// 真的 skips（沙箱里解析出来的）过一遍这里，对不上就红。
const (
	skipExcluded    = "excluded"
	skipUndefined   = "undefined"
	skipNoKey       = "no-key"
	skipUnavailable = "unavailable"
	skipDisabled    = "disabled"
	skipMaxSteps    = "max-attempts"
	skipDuplicate   = "duplicate"
	skipCycle       = "cycle"
	skipOther       = "other"
)

var skipKindOrder = []string{
	skipExcluded, skipUndefined, skipNoKey, skipUnavailable, skipDisabled,
	skipMaxSteps, skipDuplicate, skipCycle, skipOther,
}

// skipKind 取一条跳过的类目；没打标记的归「其他」（今天不会发生——建链时一定打，
// 这是给外面手搓的 Skip 留的兜底）。
func skipKind(s configapi.Skip) string {
	if s.Kind != "" {
		return s.Kind
	}
	return skipOther
}

// skipLabel 是类目在**这一屏上**的说法。
//
// 措辞是本模块自己的（与 `newgate tier` 那边各写各的：那边是终端里的一行统计，
// 这里是浏览器里链尾的一句话，读者与空间都不一样），但**类目是同一套**——两边
// 认的是同一批机器标记，所以同一份配置在两个界面上数出来的条数一定相同。
//
// 只有这里过 i18n：类目本身是机器标记，翻了它就没有判据了（同 resolve 那条注释）。
func skipLabel(kind string) string {
	switch kind {
	case skipExcluded:
		// 不翻：它是配置文件里那个标志名（`excluded=true`），用户拿去 grep 的就是它。
		return skipExcluded
	case skipUndefined:
		return i18n.T("not defined", nil)
	case skipNoKey:
		// 说全 `api_key` 而不是「没有 key」：用户要去加的那个字段就叫这个名字。
		return i18n.T("no api_key", nil)
	case skipUnavailable:
		return i18n.T("unavailable", nil)
	case skipDisabled:
		return i18n.T("disabled", nil)
	case skipMaxSteps:
		return i18n.T("past max attempts", nil)
	case skipDuplicate:
		return i18n.T("already on the chain", nil)
	case skipCycle:
		return i18n.T("a reference loop", nil)
	}
	return i18n.T("other", nil)
}

// ---------- 这一份 profile 落在哪份文件 ----------

// profileFile 是这一份 profile 的文件地址，**相对配置根**（空 = 取不到）。
//
// # 为什么这个字段要有
//
// 界面拿它把「控件」与「原文」两半配成一对并排（见 view.ChainCard.File 与 nav.ts
// 的 fileOf），而配对的判据是**字符串相等**——所以这里的写法必须与 config 自己
// 那几张卡一致：相对配置根（paths.RelToRoot）。这里写绝对路径的症状是右栏不出来，
// 而左栏一切正常，看起来只像「原文那一半没做」。
//
// # 为什么次序是「.kv 优先」
//
// 同一份 profile 可以同时有 .kv 与 .json，而**.kv 赢**（store.LoadProfile 的读取
// 次序）。这里按同一次序探测，探测不到就退回 .json——两边给出不同文件的话，用户在
// 界面上改的是一份、解析读的是另一份（改完没反应，而文件确实变了）。
//
// 先 store.LoadProfileRaw 读一遍再取路径：**读得出来才报地址**。读不出来的那份
// 根本不会出现在这张卡上（store.Load 会跳过坏文件），所以这一步今天是恒真的；把它
// 写出来是因为它才是「这份文件存在且可用」的判据，而 stat 只回答「有个同名的东西」。
func profileFile(profile string) string {
	if _, err := store.LoadProfileRaw(profile); err != nil {
		return ""
	}
	for _, ext := range []string{".kv", ".json"} {
		p := filepath.Join(paths.Mappings(), profile+ext)
		if _, err := os.Stat(p); err == nil {
			return paths.RelToRoot(p)
		}
	}
	return ""
}
