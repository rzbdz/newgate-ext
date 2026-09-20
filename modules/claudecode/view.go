package claudecode

import (
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
	rows := []map[string]view.Cell{
		nakedRow(st),
		overrideRow(st),
	}
	// 这一层不生效时，**把这件事说一次**，而不是往两个单元格里各塞一句长话：
	// 上面那两行说的是「配置里写着什么」，这一行说的是「它们此刻算不算数」。
	// 少了它，两行会各说各话——配置写着 forever 而实际上那扇门是关着的，
	// 用户没法从这张表里看出来（详见下面的 layerRow）。
	if row := layerRow(st); row != nil {
		rows = append(rows, row)
	}
	return view.Concept{
		ID: "claudecode.classifier", Kind: view.KindTable,
		Title: i18n.T("Claude Code Bash classifier", nil),
		// Live：限时窗口那一格写的是「还有多久自动关闭」——页面开着不动，那个数字
		// 就是错的（而它说的是安全门什么时候关上）。整张卡读的是内存里的
		// state.json，常问不亏。
		Live: true,
		Data: view.Table{
			Columns: []view.Column{
				{ID: "switch", Label: i18n.T("Switch", nil)},
				{ID: "state", Label: i18n.T("State", nil)},
				{ID: "detail", Label: i18n.T("What it means", nil)},
			},
			Rows: rows,
		},
	}
}

// nakedRow 是裸奔那一行。三态：没开 / 限时开着 / 永久开着——**永久**是唯一
// 画成 bad 的那一个：另外两种自己会结束，这个不会。
func nakedRow(st *domain.State) map[string]view.Cell {
	cfg, active := ParseNakedConfig(st.ModuleConfig[NakedConfigKey])
	row := map[string]view.Cell{"switch": {Text: i18n.T("Naked", nil)}}
	switch {
	case !active:
		row["state"] = view.Cell{Text: i18n.T("off", nil), Tone: view.ToneOK}
		row["detail"] = view.Cell{Text: i18n.T("the Bash classifier is asked as usual", nil)}
	case cfg.Mode == "forever":
		row["state"] = view.Cell{Text: i18n.T("on (forever)", nil), Tone: view.ToneBad}
		row["detail"] = view.Cell{Text: i18n.T("the classifier is short-circuited and every request logs [naked]", nil)}
	default:
		row["state"] = view.Cell{
			Text: i18n.T("on — it turns itself off in {left}", i18n.A{"left": time.Until(cfg.ExpiresAt).Round(time.Second)}),
			Tone: view.ToneWarn,
		}
		row["detail"] = view.Cell{Text: i18n.T("the classifier is short-circuited and every request logs [naked]", nil)}
	}
	return row
}

// overrideRow 是分类器改道那一行：把 Bash 分类器整个换成一条固定链。
//
// 与插件那条 status 行同样的话（见 st-background.go 的 Status）：它优先于任何
// 档位——用户排查「我的分类请求怎么走到那个模型去了」时，答案在这一行里。
func overrideRow(st *domain.State) map[string]view.Cell {
	row := map[string]view.Cell{"switch": {Text: i18n.T("Classifier override", nil)}}
	override, err := classifierOverride(st)
	switch {
	case err != nil:
		// 坏配置是**要说出来的**：它会让整条改道静默失效（见 st-background.go）。
		row["state"] = view.Cell{Text: i18n.T("invalid", nil), Tone: view.ToneBad}
		row["detail"] = view.Cell{Text: i18n.T("classifier_override is not valid: {err}", i18n.A{"err": err})}
	case override != nil:
		row["state"] = view.Cell{Text: override.String(), Tone: view.ToneWarn}
		row["detail"] = view.Cell{Text: i18n.T("the highest priority globally, ahead of any profile", nil)}
	default:
		row["state"] = view.Cell{Text: i18n.T("none", nil), Tone: view.ToneOK}
		// 复用插件自己那句话（st-background.go 的 Status）：同一件事在两处各写
		// 一遍措辞，翻出来的两份译文迟早会分叉。
		row["detail"] = view.Cell{Text: i18n.T("Claude Code Bash classifier → the light-tier chain", nil)}
	}
	return row
}

// layerRow 是「这一层此刻算不算数」那一行；层开着时不出现（没什么可说的）。
//
// **为什么值得单独一行**：上面两行答的是「配置里写着什么」，而这一层关掉时，
// 那两个答案都不作数——配置留着，门是关的，把层一开回来门就又开了。两行各自
// 去解释这件事的话（只解释一行更糟），同一张表里就会出现两种说法。
func layerRow(st *domain.State) map[string]view.Cell {
	// 两种「不生效」：整层关掉，或者这个插件被单独关掉。前者说的是这一层，
	// 后者说的是某一枚插件——而这张卡上的两行各属一枚插件，所以分开说。
	off := []string{}
	if gatewaystate.PluginOff(st, nakedPluginName) {
		off = append(off, nakedPluginName)
	}
	if gatewaystate.PluginOff(st, backgroundPluginName) {
		off = append(off, backgroundPluginName)
	}
	enabled := gatewaystate.SpecialEnabled(st)
	if enabled && len(off) == 0 {
		return nil
	}
	row := map[string]view.Cell{
		"switch": {Text: i18n.T("special_treatment layer", nil)},
		"state":  {Text: i18n.T("off", nil), Tone: view.ToneWarn},
	}
	if !enabled {
		row["detail"] = view.Cell{Text: i18n.T("the whole layer is off, so nothing configured here counts right now; it counts again the moment the layer is switched back on", nil)}
		return row
	}
	// 层开着、只有几枚插件被单独关掉：**只有它们那几行**不算数。这里不能说成
	// 「上面全都不算数」——另一行可能正跑得好好的，那句话会把人劝去关一个没问题
	// 的补丁（同一张表里两种说法，正是这一行要消灭的东西）。
	names := strings.Join(off, ", ")
	row["state"] = view.Cell{Text: i18n.T("on, but these plugins are switched off: {names}",
		i18n.A{"names": names}), Tone: view.ToneWarn}
	row["detail"] = view.Cell{Text: i18n.T("{names}: switched off individually, so that row counts for nothing right now; it counts again the moment it is switched back on",
		i18n.A{"names": names})}
	return row
}

// 插件名取自插件自己（`classifierNaked{}.Name()`），不是抄一份字面量：抄的那份
// 会漂移，而漂移的后果是 PluginOff 问了一个不存在的插件——**失败方向是 fail-open**，
// 那句提醒会静默地永远不出现。
var (
	nakedPluginName      = classifierNaked{}.Name()
	backgroundPluginName = background{}.Name()
)
