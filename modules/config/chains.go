package config

import (
	"encoding/json"
	"errors"
	"os"
	"sort"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate/lib/i18n"
)

// chainsConceptID 是这一族结论**如果由内核自己画成一张卡**会用的身份。
//
// 它今天只出现在一个地方：CAS 冲突对象里的 Concept 字段（见 applyProfileRoles →
// writeThrough）。行上的动作那条路上，冲突到了调用方那里只剩一句人话（见
// switchHead），所以这个字符串不会漂到界面上——它只是「这件事说的是哪张卡」的
// 一个稳定说法，不能空着（空串会让 `errors.As` 之外的人以为那是「没有概念」）。
const chainsConceptID = "config.chains"

// Chains 是 Config 端口上的读（见 api.go 里 Config 的注释）。
//
// # 它为什么是机制而不是某个界面的私事
//
// 「这一档此刻解析成什么、谁被跳过、为什么」是 resolve 的结论，而 resolve 住在
// 本模块内部。任何想摊开这件事的人（`/ui` 的聚合主页、将来的 tui、任何想解释
// 「为什么这一发走了那家」的地方）自己 import store + resolve 都能算出来——发行版
// 里今天就有两处在这么干——但那是**抄一份内核的内部形状**：内核一重构就断在另一个
// 仓库里，而那里没有测试会红。所以端口的形状是「给我结论」，不是「给我原料」。
//
// # 它为什么不问健康表
//
// 熔断摘牌与 probe 延迟（`controlplane.Doc` 的 Available/Rank）会随时间变，而且是
// **跑着的那个进程**的状态；本方法读的是**盘上此刻的样子**。两者混在一起的话，
// 同一份配置在「CLI 里看」与「浏览器里看」会给出不同答案，而差异来自「谁恰好连上了
// daemon」——那种不一致比少一点信息糟得多。链上的 Tone 因此只表达「有没有链头」，
// 不表达健康（见 lib/view 的 ChainRow.Tone）。
func (p *port) Chains(profile string, keys ...string) (*Chains, error) {
	snap, err := store.Load()
	if err != nil {
		return nil, i18n.Ef(err, "cannot read the configuration: {err}", nil)
	}

	// 空 = 此刻全局生效的那一份。**不做 per-agent 的推演**：`Active[agent]` 说的是
	// 「某个客户端从哪条链起步」，而这里问的是「这一份文件解析成什么」，两者是不同的
	// 问题（前者是 `newgate tier` 的多链头视图，它自己那套在 commands.go 里）。
	name := profile
	if name == "" {
		name = snap.State.DefaultProfile
	}
	pr := profileNamed(snap, name)
	if pr == nil {
		// 报错而不是给一份空链：空链的形状是「这份档位什么都没配」，而事实是
		// **没有这份档位**。静默的话调用方会把它画成一张空卡，读的人以为配置丢了。
		// （`store.Load` 会跳过坏掉的 profile 文件，所以这里也覆盖「文件坏了」那一档
		// ——`newgate doctor` 会报出具体哪个文件。）
		return nil, i18n.E("no such profile: {name}", i18n.A{"name": name})
	}
	return buildChains(snap, pr, keys...), nil
}

// AllChains 报**每一份** profile 此刻解析出来的链，生效的那一份排在最前。
//
// # 为什么它和 Chains 是两个方法，而不是让调用方循环
//
// 循环是**十次 store.Load**，也就是十个时刻的配置拼在**一屏**上：前几份卡还认旧
// 文件、后几份已经认新文件，而界面上看不出任何异样（每一张卡单看都对）。这条与
// 「一次给全部键」是同一条规矩，只是这一层再往外一格——一次快照，一屏一个时刻。
//
// # 为什么不需要单独的「有哪些 profile」
//
// 名单就是这里的下标：调用方拿到的每一份都带着 Profile，顺序也定死了（生效的那
// 一份最前，其余按名字）。多一个只报名字的方法，等于让调用方有机会拼出「用名单
// 去逐个问」那条更慢也更不一致的路——而那正是这个方法要取代的那条。
func (p *port) AllChains(keys ...string) ([]*Chains, error) {
	snap, err := store.Load()
	if err != nil {
		return nil, i18n.Ef(err, "cannot read the configuration: {err}", nil)
	}
	out := make([]*Chains, 0, len(snap.Profiles))
	for _, pr := range orderedProfiles(snap) {
		out = append(out, buildChains(snap, pr, keys...))
	}
	return out, nil
}

// orderedProfiles 把快照里的档位按「生效的那一份最前，其余按名字」排好。
//
// 排序判据只能是**名字**，不能是「profile 的 priority」：那个数字是链里的次序
// （谁先当备选），与「用户现在站在哪一份上」是两件事。用 priority 排的话，生效的
// 那一份会夹在中间，而这一屏上第一眼要找的恰恰是它。
func orderedProfiles(snap *store.Snapshot) []*domain.Profile {
	out := make([]*domain.Profile, len(snap.Profiles))
	copy(out, snap.Profiles)
	sort.SliceStable(out, func(i, j int) bool {
		di, dj := out[i].Name == snap.State.DefaultProfile, out[j].Name == snap.State.DefaultProfile
		if di != dj {
			return di
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func profileNamed(snap *store.Snapshot, name string) *domain.Profile {
	for _, pr := range snap.Profiles {
		if pr.Name == name {
			return pr
		}
	}
	return nil
}

// buildChains 把一份 profile 的每一档算成一条链。
//
// 空 keys = 跟着 domain.Roles（heavy → normal → mid → light，外加 vision）。顺序
// 由**调用方**决定（传进来就要原样还回去）：谁在前谁在后是产品取舍——`newgate
// status` 与 doctor 都按能力从高到低排，界面照同一条；而传进来的次序是它唯一能
// 表达这件事的地方。默认那一套只是「没想法时的合理答案」。
func buildChains(snap *store.Snapshot, pr *domain.Profile, keys ...string) *Chains {
	want := keys
	if len(want) == 0 {
		want = domain.Roles
	}
	out := &Chains{Profile: pr.Name, Default: pr.Name == snap.State.DefaultProfile}

	// 原文（extends 未合并）只为了**动作**而读：能重排的候选是这一份文件里写着
	// 的那几个，而合并后的视图里混着父档位的候选与别的键展开出来的引用——对着它
	// 排出来的次序写回去就是错的。
	//
	// 读不到就不给动作（这次读本身照常返回）：fail-open，一份同时被删掉的文件
	// 不该让整屏链消失。
	raw, file, _ := rawFor(pr.Name)

	for _, key := range want {
		// MaxSteps: 0——**不截断**。maxAttempts 是单次请求的执行上限，不是链的
		// membership（2026-09-17 实查：ark 是第 4 站，传 Attempts() 会把它静默裁掉，
		// 于是屏幕上看起来像「normal 档里根本没有 ark」）。截断的事实在命令行那边由
		// 一行提示说出来，不是把链本身剪短。
		steps, skips := resolve.BuildChain(key, snap.Profiles, snap.Providers, resolve.Opts{
			Active:   pr.Name,
			MaxSteps: 0,
		})
		c := Chain{Key: key, Steps: steps, Skips: skips}
		if raw != nil {
			c.Actions = chainActions(pr.Name, key, file, raw.Roles[key])
		}
		out.Keys = append(out.Keys, c)
	}
	return out
}

// rawFor 取一份 profile 的原文与它落在哪份文件（相对配置根）。
func rawFor(name string) (*domain.Profile, string, error) {
	raw, err := store.LoadProfileRaw(name)
	if err != nil {
		return nil, "", err
	}
	file, err := profileFile(name)
	if err != nil {
		return nil, "", err
	}
	return raw, file, nil
}

// chainActions 报「把这一档的链头换成 X」这一组按钮。
//
// 候选**只取这一份 profile 自己写在这一档下的那几个**（own 就是原文里那一行）：
// 链上别的站要么是别的 profile 顶上来的 fallback，要么是引用展开出来的（那些
// 字面写在**另一个键**下面）。改这一档重排不到它们，给一个按了不生效的按钮比
// 不给按钮糟。
//
// **第一个候选不给按钮**：它就是此刻的链头，点了不改变任何事——与 codex 那对模式
// 按钮、档位卡那对 apply 是同一条取舍（把已经在生效的那个也画成按钮，站在旁边只会
// 把真会做事的那个淹掉）。
//
// 标签就是那个绑定本身（`ark/deepseek-v3`），不写「切换到…」：这一行上下文的全部
// 意义就是「换成谁」，而 binding 读起来比一句动作描述短，也不用翻译。
func chainActions(profile, key, file string, own domain.Candidates) []view.Action {
	if len(own) < 2 {
		return nil
	}
	out := make([]view.Action, 0, len(own)-1)
	for _, bd := range own[1:] {
		target := bd.String()
		out = append(out, view.Action{
			ID:    "head:" + target,
			Label: func() string { return target },
			Run:   func() (string, error) { return switchHead(profile, key, file, target) },
		})
	}
	return out
}

// switchHead 把这一档的候选重排成「target 在最前、其余保持原序」，落盘。
//
// # 它为什么不接参数、基线从哪来
//
// 行上的动作按设计**不接任何参数**（见 lib/view 的 Action.Run）：界面能提供的只有
// 「用户点了这个按钮」，而「点的是哪一份 profile 的哪一档、要换成谁」在构造这个
// 闭包的时候就已经定下来了。所以基线也拿不到界面手里那份——只能在这里**读一次、
// 立刻 CAS 一次**：中间那个窗口由 CAS 兜住（别人在这中间写过 → StaleError，我们
// 一个字节都不写，报一句让人重来）。
//
// # 它为什么不自己拼序列化
//
// 它与档位卡走的是**同一条写入路径**（applyProfileRoles）：那份文件是 kv 还是
// json、哪些字段不许碰、未知键怎么保住，答案只有一处。代价照抄那条路已有的一个
// 性质：交上去的是**整份 roles**（那条路要的是完整的表），于是 .json 里写成对象的
// 单候选会被规范成数组——这不是这里新引入的，档位卡上改一档也是同样结果。
func switchHead(profile, key, file, target string) (string, error) {
	raw, err := store.LoadProfileRaw(profile)
	if err != nil {
		return "", err
	}
	own := raw.Roles[key]
	moved := -1
	for i, bd := range own {
		if bd.String() == target {
			moved = i
			break
		}
	}
	if moved < 0 {
		return "", i18n.E("{tier} in {profile} has no candidate called {target} any more — reload the page and try again",
			i18n.A{"tier": key, "profile": profile, "target": target})
	}
	if moved == 0 {
		return "", nil // 已经是链头。界面上那个按钮本该已经被这一条挡住（chainActions 跳过了第一个）
	}

	next := make(domain.Candidates, 0, len(own))
	next = append(next, own[moved])
	next = append(next, own[:moved]...)
	next = append(next, own[moved+1:]...)

	roles := make(map[string][]bindingData, len(raw.Roles))
	for k, c := range raw.Roles {
		roles[k] = bindingDataOf(c)
	}
	roles[key] = bindingDataOf(next)
	edit, err := json.Marshal(struct {
		Roles map[string][]bindingData `json:"roles"`
	}{Roles: roles})
	if err != nil {
		return "", err
	}

	disk, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	if _, err := applyProfileRoles(chainsConceptID, file, edit, store.RevisionOf(disk)); err != nil {
		// 冲突在这里是**一句话**而不是那个两栏对话框：apply 那条路能弹对话框，是
		// 因为请求里带着界面手里那份草稿与基线（两边原文都在）。行上的动作两者都
		// 没有——用户点的是一个按钮，不是一个草稿，所以能给的只有「重来一遍」。
		var cf *view.Conflict
		if errors.As(err, &cf) {
			return "", i18n.E("{file} changed on disk after this page read it — reload and try again",
				i18n.A{"file": paths.RelToRoot(file)})
		}
		return "", err
	}
	return "", nil
}

func bindingDataOf(c domain.Candidates) []bindingData {
	out := make([]bindingData, 0, len(c))
	for _, b := range c {
		out = append(out, bindingData{Provider: b.Provider, Model: b.Model, Ref: b.Ref})
	}
	return out
}
