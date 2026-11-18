package pluginmanager

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// 本模块贡献给 web 界面的东西：**运行期开关点**。
//
// 这是「开关账本」最自然的第二个入口——`newgate plugin` 是命令行那一份，浏览器
// 这一份把同一批开关点画成开关。两份入口、**一份写语义**：都走 SetSwitch，所以
// 出厂态决定走哪张表、动作等于出厂态就是撤销、footgun 不收永久，这几条不会在
// 两个界面上给出不同答案（见 set.go 的说明）。
//
// 为什么分成两个概念（可写的 / 只读的）而不是一张卡片里混着：概念的可写性是
// **整个概念**的属性（前端据此决定给不给保存），而 footgun 恰恰不能在这里随手开
// 关——它必须带时限。硬塞进同一个概念的结果是界面上给了一个按下去会失败的开关，
// 那比不给更糟。所以 footgun 单列一张只读卡片，写明它为什么只读、该用什么命令。

type switchItem struct {
	ID     string `json:"id"`     // 开关点路径（StateKey 里的键）
	Label  string `json:"label"`  // 人话标题
	Kind   string `json:"kind"`   // 界面渲染用：全是 bool
	Value  bool   `json:"value"`  // **当前生效**的状态（出厂态 + 用户的设定）
	Why    string `json:"why"`    // 关掉会发生什么
	Group  string `json:"group"`  // 模块名（界面拿它分组）
	Danger string `json:"danger"` // safe / quirk / footgun
	Until  string `json:"until,omitempty"`
}

type switchesData struct {
	File  string       `json:"file"`
	Base  string       `json:"base"`
	Items []switchItem `json:"items"`
}

// viewConcepts 产出这一刻的两张开关卡片。
//
// 它每次被调用都重新算一遍「现在开着没有」——用户刚在命令行改过的开关，界面上
// 刷新一下就对了（见 lib/view 的包注释：产出发生在有人来问的那一刻）。
func viewConcepts(m Manager) ([]view.Concept, error) {
	st := store.LoadState()
	file := paths.StateFile()
	base := store.Revision(file)

	var writable, footguns []switchItem
	for _, mod := range m.Modules() {
		for _, sw := range mod.Switches {
			it := switchItem{
				ID: sw.Path, Label: sw.Title, Kind: "bool",
				Value: switchOn(st, sw), Why: sw.Why,
				Group: mod.Name, Danger: string(sw.Danger),
			}
			if until := Remaining(st, sw.Path); !until.IsZero() {
				it.Until = until.Format(time.RFC3339)
			}
			if sw.Danger == DangerFootgun {
				footguns = append(footguns, it)
				continue
			}
			writable = append(writable, it)
		}
	}
	// 模块清单**先**放进来，而且不受下面那个「一个开关点都没有就别报」的早退
	// 影响：它回答的是另一个问题（这个构建由哪些模块组成），而骨架发行版恰恰
	// 一个开关点都没有——那种构建最需要这张清单（「装了没有」是升级后第一个
	// 要确认的事，见 docs/08-operations.md）。
	out := []view.Concept{{
		ID: "plugin-manager.modules", Kind: view.KindTable,
		Title: i18n.T("Modules in this build", nil),
		Data:  moduleTable(m),
	}}

	if len(writable) == 0 && len(footguns) == 0 {
		// 一个开关点都没上报（比如骨架发行版）：开关那两张卡片不报，而不是报
		// 空卡片。空卡片在界面上看起来像「有东西没加载出来」。
		return out, nil
	}

	if len(writable) > 0 {
		out = append(out, view.Concept{
			ID: "plugin-manager.switches", Kind: view.KindToggles,
			Title: i18n.T("Runtime switches", nil),
			// Live：这些开关**也会被终端拨动**（`newgate plugin … on/off`，以及
			// TTL 到期那次自动恢复），而读它们只是把 state.json 读一把。停在打开
			// 页面那一刻，界面就会说一个已经不对的当前值。
			Live: true,
			Data: switchesData{File: file, Base: base, Items: writable},
			Apply: func(edit json.RawMessage, base string) (string, error) {
				return applySwitches(m, edit, base)
			},
		})
	}
	if len(footguns) > 0 {
		// 只读（没有 Apply）。为什么不给个「带时限」的开关：时限要有人选，而这一版
		// 没有那个界面；给一个按下去必定失败的开关比不给更糟。命令写在 why 里。
		for i := range footguns {
			footguns[i].Why = i18n.T("{why} — footguns need a time limit; use: newgate plugin {path} on 5m",
				i18n.A{"why": footguns[i].Why, "path": footguns[i].ID})
		}
		out = append(out, view.Concept{
			ID: "plugin-manager.footguns", Kind: view.KindToggles,
			Title: i18n.T("Switches that need a time limit", nil),
			// Live 同 .switches：这些值同样会被终端改（而它们带时限，所以更会
			// **自己**变回去）。
			Live: true,
			Data: switchesData{File: file, Base: base, Items: footguns},
		})
	}
	return out, nil
}

// moduleTable 是**这个构建由哪些模块组成**。
//
// 顺序与 `newgate plugin` 一字不差：分类按 DisplayOrder（那是有意义的——地基、
// 数据面、界面、客户端、模型、桥），组内按名字（启动顺序对读的人没有意义，
// 拿它排会让人每次都要重新找一遍）。两个界面用同一个顺序，是因为它们是同一个
// 问题的两个入口：在浏览器里看到 arch-diagram 排在「其它」，就该和终端里一致。
//
// 「装了什么」在排障时是第一个要确认的事（升级后模块数不对 = 装错了产物，
// 见 docs/08-operations.md），所以这张表报的是**全部**模块，不管它有没有开关点。
func moduleTable(m Manager) view.Table {
	t := view.Table{
		Columns: []view.Column{
			{ID: "module", Label: i18n.T("module", nil)},
			{ID: "type", Label: i18n.T("type", nil)},
			// 描述：模块自己写的一句话（见 component.Component.Desc）。排在这里
			// 而不是最后：它是给人读的那一列，而「开关点」是个计数——读的时候先
			// 知道这是什么，再关心有几个开关。
			{ID: "desc", Label: i18n.T("What it does", nil)},
			{ID: "switches", Label: i18n.T("Switch points", nil), Align: "right"},
		},
		Rows: []view.Row{},
	}
	all := m.Modules()
	for _, typ := range DisplayOrder() {
		var group []Module
		for _, mod := range all {
			if groupOf(mod.Type) == typ {
				group = append(group, mod)
			}
		}
		sort.Slice(group, func(i, j int) bool { return group[i].Name < group[j].Name })
		for _, mod := range group {
			t.Rows = append(t.Rows, view.Row{Cells: map[string]view.Cell{
				"module": {Text: mod.Name},
				// 分类词是机器标记（infra / gateway / …），原样显示：它是
				// `newgate plugin` 的分组名，也是模块自己声明的 Type，翻它
				// 等于让两个界面用两个词说同一件事。
				"type": {Text: string(groupOf(mod.Type))},
				// 没写描述就**空着**，不编一句「（无描述）」：那一格的信息量本来就是
				// 「这个模块没说自己是干什么的」，而空着正是这个意思（同 dashIfEmpty
				// 那条的反面——那里是「这里就是没有」，这里是「作者没写」）。
				"desc":     {Text: mod.Desc},
				"switches": switchCountCell(len(mod.Switches)),
			}})
		}
	}
	return t
}

func switchCountCell(n int) view.Cell {
	if n == 0 {
		// 「一个都没有」与「0 个」不是一回事：前者是「这个模块不支持运行期开关」，
		// 后者不是一个可能的状态（登记了就是至少一个）。用横杠，别用 0。
		return view.Cell{Text: "-"}
	}
	return view.Cell{Text: i18n.T("{n}", i18n.A{"n": n})}
}

// switchOn 是**当前生效**的状态：出厂开着的（kill switch）看 Off 表，出厂关着的
// （mode）看 On 表。判据与 CLI 的 switchState 一字不差——两边说不一样的话，
// 用户会以为其中一个界面在骗他。
func switchOn(st *domain.State, sw Switch) bool {
	if st == nil {
		return sw.Default
	}
	if sw.Default {
		return !Off(st, sw.Path)
	}
	return On(st, sw.Path)
}

// applySwitches 把界面这次交上来的整张表写进 state.json。
//
// edit 的形状：`{"<开关点路径>": true|false}`——界面把手里的**全部**项交回来
// （见前端 Toggles 的注释），所以这里逐条应用，等于「把每个开关设成这个样子」。
// 未变的那几条是无害的幂等写（见 SetSwitch）。
func applySwitches(m Manager, edit json.RawMessage, base string) (string, error) {
	var patch map[string]bool
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the switches in this request are not readable: {err}", i18n.A{"err": err})
	}
	// 排序只为让错误可复现：同一个坏请求跑两次该报同一条。
	paths_ := make([]string, 0, len(patch))
	for p := range patch {
		paths_ = append(paths_, p)
	}
	sort.Strings(paths_)

	st := store.LoadState()
	for _, p := range paths_ {
		sw, ok := m.Lookup(p)
		if !ok {
			// 界面手里的开关点已经不在了（模块被换掉、产物换了一份）。说清楚比
			// 静默跳过强：静默跳过的那天，用户会以为自己按的开关生效了。
			return "", i18n.E("no switch point named \"{path}\" — the page is probably stale, reload it",
				i18n.A{"path": p})
		}
		if err := SetSwitch(st, sw, patch[p], 0, false); err != nil {
			return "", err
		}
	}

	b, err := store.StateBytes(st)
	if err != nil {
		return "", err
	}
	return writeThrough("plugin-manager.switches", paths.StateFile(), base, b)
}

// writeThrough 把 store 的「基线不对」翻译成界面契约的 Conflict。
//
// # 为什么这六行抄了三份（config / locale / 这里）
//
// 这是**刻意**的，2026-09-21 复核过：CAS 的语义（比对什么、怎么锁、写多原子）在
// store 里只有一份，这里只是把它的失败换个形状。唯一能把它收成一份的办法是让
// store 认识 view 契约（或者为它单开一个包），而那两件事都比三份六行的映射更糟
// ——前者把界面的知识长进数据层，后者为了一个结构体字面量多一层间接。
//
// 抄的**判据**：`Conflict` 加了字段时三处都要跟。漏改的症状是冲突对话框里少一栏
// （前端画空白），不会红。
func writeThrough(conceptID, file, base string, data []byte) (string, error) {
	rev, err := store.WriteIfUnchanged(file, base, data)
	var stale *store.StaleError
	if errors.As(err, &stale) {
		return "", &view.Conflict{
			Concept: conceptID,
			Path:    file,
			Base:    stale.Base,
			Current: stale.Current,
			Yours:   string(data),
			Theirs:  string(stale.Disk),
		}
	}
	return rev, err
}
