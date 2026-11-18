package config

// 本文件是**配置自己的**那批命令：tier / profiles / profile kv / --set-profile /
// agents。2026-09-18 从 modules/cli/profile.go 整体搬来。
//
// 为什么搬：档位怎么解析、链怎么建、跳过谁、profile 文件长什么样——全是配置的
// 知识，界面留着一份「抄来的理解」就必然和实现漂移（这一轮之前，界面为了渲染
// tier 得 import resolve + domain + paths + store）。搬回来之后界面既不认识
// profile，也不认识链。
//
// 依赖方向：config → cli/extension（叶子契约），不是 → modules/cli。命令由本模块
// 在 Start 里注入界面，和其它模块完全一样。

import (
	"encoding/json"
	"fmt"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/lib/style"
	"github.com/rzbdz/newgate-ext/modules/breaker/status"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
	"github.com/rzbdz/newgate/lib/i18n"

	"github.com/rzbdz/newgate-ext/modules/runtime/daemon"
)

// cmdSetProfile switches the chain head. An empty agent sets the global
// default; naming an agent switches only that agent, leaving every other
// agent — including ones with sessions in flight — untouched.
func cmdSetProfile(agent, name string) int {
	if err := store.SetActiveProfile(agent, name); err != nil {
		return style.Die(65, err.Error())
	}
	scope := i18n.T("global default", nil)
	if agent != "" {
		scope = "agent " + agent
	}
	fmt.Println(style.Item(style.OK, scope+" → "+style.Cyan(name)))

	if pr, err := store.LoadProfile(name); err == nil {
		if pr.Description != "" {
			fmt.Println(style.Hint(pr.Description))
		}
		if pr.Pinned {
			fmt.Println(style.Hint(i18n.T("pinned: the chain ends here; a failure is reported, not replaced", nil)))
		}
		t := style.NewTable(i18n.T("Role", nil), i18n.T("Binding", nil))
		for _, tier := range domain.Roles {
			if b, ok := pr.Resolve(tier); ok {
				t.Row(style.Cyan(tier), b.String())
			}
		}
		if t.Len() > 0 {
			fmt.Println()
			fmt.Print(t.String())
		}
	}
	controlplane.Notify()
	fmt.Println()
	if daemon.Running() != nil {
		fmt.Println(style.Hint(i18n.T("takes effect immediately; running sessions are unaffected", nil)))
	} else {
		fmt.Println(style.Hint(i18n.T("proxy is not running · newgate start", nil)))
	}
	return 0
}

// cmdProfiles 列出全部 profile。
//
// 一屏回答三个问题：默认是哪个、优先级怎么排、哪个 agent 挂了别名的 profile。
// 标了标志的 profile 才是异常的（pinned / excluded），所以不加额外段落，
// 全部信息压在一张表里——段落一多，扫读就变成阅读。
func cmdProfiles() int {
	names, err := store.ListProfiles()
	if err != nil {
		return style.Die(65, i18n.T("cannot read mappings: {err}", i18n.A{"err": err.Error()}))
	}
	st := store.LoadState()
	var ps []*domain.Profile
	for _, n := range names {
		if p, err := store.LoadProfile(n); err == nil {
			ps = append(ps, p)
		}
	}
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Prio() != ps[j].Prio() {
			return ps[i].Prio() < ps[j].Prio()
		}
		return ps[i].Name < ps[j].Name
	})

	fmt.Println(style.Title("newgate profiles",
		i18n.N("{n} profile · default {name}", "{n} profiles · default {name}",
			len(ps), i18n.A{"n": len(ps), "name": st.DefaultProfile})))
	fmt.Println(style.Rule(72))

	t := style.NewTable(i18n.T("Priority", nil), "profile", i18n.T("Flag", nil), i18n.T("Description", nil))
	t.AlignRight(0)
	for _, p := range ps {
		var flags []string
		if p.Name == st.DefaultProfile {
			flags = append(flags, style.Green("default"))
		}
		if p.Pinned {
			flags = append(flags, style.Yellow("pinned"))
		}
		if p.Excluded {
			flags = append(flags, style.Yellow("excluded"))
		}
		for agent, ap := range st.Active {
			if ap == p.Name {
				flags = append(flags, style.Cyan("←"+agent))
			}
		}
		name := p.Name
		if p.Name == st.DefaultProfile {
			name = style.Cyan(p.Name)
		}
		t.Row(fmt.Sprintf("%d", p.Prio()), name, strings.Join(flags, " "),
			style.Dim(p.Description))
	}
	fmt.Print(t.String())
	fmt.Println(style.Hint(i18n.T("pinned stops at the chain head and is never replaced · excluded is only selected explicitly · ←agent that agent uses this profile alone", nil)))
	return 0
}

// cmdProfileKV 把一个 profile 转成 KV 文本——json→kv 的转换口。
//
// 不带 --write 只打印（拷走、改完再贴回来都行）；带 --write 落盘成
// mappings/<名>.kv 并把旧 .json 改名 .bak（kv 优先于 json，留着旧文件
// 会永远被压着，退役比并存干净）。
func cmdProfileKV(args []string) int {
	if len(args) < 1 || args[0] == "" {
		return style.Die(64, i18n.T("usage: newgate profile kv <name> [--write]", nil))
	}
	name := args[0]
	raw, err := store.LoadProfileRaw(name)
	if err != nil {
		return style.Die(65, err.Error())
	}
	text := store.SerializeProfileKV(raw)

	if len(args) < 2 || args[1] != "--write" {
		fmt.Print(text)
		fmt.Println(style.Dim(i18n.T("# to write it: newgate profile kv {name} --write",
			i18n.A{"name": name})))
		return 0
	}
	kvPath := filepath.Join(paths.Mappings(), name+".kv")
	// 走 store.Write（原子 + 备份环），**不是 os.WriteFile**：这条命令是当场把一份
	// profile 换成另一种格式，覆盖的正是用户手上那份配置；直写让它死在半路时留下
	// 一份被截断的文件，而且那一版连备份都没有（见 store.Write 的注释）。
	if err := store.Write(kvPath, []byte(text)); err != nil {
		return style.Die(70, i18n.T("cannot write {path}: {err}",
			i18n.A{"path": kvPath, "err": err.Error()}))
	}
	jsonPath := filepath.Join(paths.Mappings(), name+".json")
	if _, err := os.Stat(jsonPath); err == nil {
		if err := os.Rename(jsonPath, jsonPath+".bak"); err != nil {
			return style.Die(70, i18n.T("cannot rename the old json (the kv is already written; handle it by hand): {err}",
				i18n.A{"err": err.Error()}))
		}
		fmt.Println(style.Item(style.OK, kvPath+style.Dim(i18n.T("   old .json → .json.bak", nil))))
	} else {
		fmt.Println(style.Item(style.OK, kvPath))
	}
	controlplane.Notify()
	return 0
}

// tierView 一个档位解析出来的结果。
type tierView struct {
	name  string
	steps []resolve.Step
	skips []resolve.Skip
}

// skipKindOrder 类目顺序固定：数字对不上时，两次输出可以直接比。
//
// 表里放的是 resolve 建链时打的**机器标记**（resolve.Skip.Kind），不是给人看的
// 话——显示名由 skipLabel 现查。所以「翻译改了措辞、汇总就少一栏」这种事不可能
// 发生：分组认的是标记，标记不随语言变。
var skipKindOrder = []string{
	resolve.SkipExcluded, resolve.SkipUndefined, resolve.SkipNoKey,
	resolve.SkipUnavailable, resolve.SkipDisabled, resolve.SkipMaxSteps,
	resolve.SkipDuplicate, resolve.SkipCycle, resolve.SkipOther,
}

// skipKind 取一条 skip 的类目。建链时就打好了（见 resolve.Skip.Kind）；
// 没打标记的（今天没有，将来也只可能是外面手搓的 Skip）归「其他」。
func skipKind(s resolve.Skip) string {
	if s.Kind != "" {
		return s.Kind
	}
	return resolve.SkipOther
}

// skipLabel 类目的显示名——只有这里过 i18n，类目本身是机器标记。
//
// 为什么是函数而不是一张包级 map：i18n.T 要等 modules/locale 在 Start 里把语言
// 装上才认得译文，包级变量在初始化时求值会永远停在源语言。
func skipLabel(kind string) string {
	switch kind {
	case resolve.SkipExcluded:
		return "excluded" // profile 的标志名，机器标记，不翻
	case resolve.SkipUndefined:
		return i18n.T("not defined", nil)
	case resolve.SkipNoKey:
		return i18n.T("no key", nil)
	case resolve.SkipUnavailable:
		return i18n.T("unavailable", nil)
	case resolve.SkipDisabled:
		return i18n.T("disabled", nil)
	case resolve.SkipMaxSteps:
		return i18n.T("over maxAttempts", nil)
	case resolve.SkipDuplicate:
		return i18n.T("duplicate", nil)
	case resolve.SkipCycle:
		return i18n.T("reference cycle", nil)
	}
	return i18n.T("other", nil)
}

// cmdTier 展示 fallback 链——这是整套配置的**接口**：一眼要能回答
// 「这次请求会走谁」和「为什么不是我想的那个」。
//
// 版式分两档（详略得当）：
//
//	newgate tier            每个档位一行，链相同就写 `= heavy`，末行给跳过统计
//	newgate tier <档位>     展开：编号的站 + 按原因分组的跳过统计
//
// 跳过一律**只给汇总**，永不逐条铺开。实测一份配置里 106 条跳过中有 91 条
// 是「与链上更靠前的候选重复」——把必然发生的去重当成一行行结果打出来，
// 只会把真正的结论（走谁）淹掉。用户要的是链，不是候选全集的流水账。
func cmdTier(args []string) int {
	which := ""
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		which = a
	}
	// 缩写也认（heavy 写成 h、normal 写成 n…），省得记全名。
	// 校验放在 tierReport 里、store.Load 之后——动态角色键要等 Load 把
	// roleprov 刷新过才认得（在这里查会把自己刚注册的键判成未知）。
	return tierReport(which)
}

// knownRolesLine 报错时给的可选项。动态角色键可能几十个，全列出来会把
// 错误信息冲成一堵墙，只给数量与查询入口。
func knownRolesLine() string {
	if n := len(domain.ExtraRoles()); n > 0 {
		return i18n.T("roles {roles}, plus {n} dynamic role keys (newgate omo ls)",
			i18n.A{"roles": strings.Join(domain.Roles, "/"), "n": n})
	}
	return i18n.T("roles {roles}", i18n.A{"roles": strings.Join(domain.Roles, "/")})
}

func matchTier(s string) string {
	for _, r := range domain.Roles {
		if strings.HasPrefix(r, s) {
			return r
		}
	}
	return ""
}

func tierReport(which string) int {
	snap, err := store.Load()
	if err != nil {
		return style.Die(65, err.Error())
	}
	st := snap.State

	// 角色键的校验必须在这里做：动态角色键（omo-sisyphus / cat-deep）是
	// store.Load 里跟着 roleprov 刷进来的，在 Load 之前查会把自己刚注册
	// 的键判成未知。模块注册的槽位和档位在解析层是一回事，命令层不该比
	// 解析层更窄——newgate tier omo-sisyphus 是查「这个槽位走哪条链」的
	// 正规入口。
	if which != "" {
		if full := matchTier(which); full != "" {
			which = full
		} else if !domain.IsKnownRole(which) {
			return style.Die(64, i18n.T("no such role: {name} ({known})",
				i18n.A{"name": which, "known": knownRolesLine()}))
		}
	}

	// 链头可能不止一个（claude 和 opencode 可以各挂一个 profile）。
	heads := map[string][]string{st.DefaultProfile: {i18n.T("default", nil)}}
	for agent, p := range st.Active {
		heads[p] = append(heads[p], agent)
	}
	var headNames []string
	for h := range heads {
		headNames = append(headNames, h)
	}
	sort.Strings(headNames)

	names := domain.Roles
	if which != "" {
		names = []string{which}
	}
	_, live := controlplane.State()
	available := live.Available()
	rank := live.Rank()
	liveHealth := healthFromProxy(live)

	for _, head := range headNames {
		sort.Strings(heads[head])
		fmt.Println(style.Title("newgate tier",
			i18n.T("chain head {head} ({who}) · max attempts {n} · budget {budget}", i18n.A{
				"head": head, "who": strings.Join(heads[head], ", "),
				"n": st.Chain.Attempts(), "budget": prettyMs(st.Chain.Budget())})))
		fmt.Println(style.Rule(72))

		var rows []tierView
		for _, name := range names {
			steps, skips := resolve.BuildChain(name, snap.Profiles, snap.Providers, resolve.Opts{
				Active:    head,
				Available: available,
				Rank:      rank,
				// tier 展示的是完整候选链；maxAttempts 是执行约束，不是
				// membership。否则后续 profile 会被误解成根本没进链。
				MaxSteps: 0,
			})
			rows = append(rows, tierView{name, steps, skips})
		}

		fmt.Println()
		if which == "" {
			// 概览：链一样的档位合并成 `= <先出现的那个>`
			fmt.Print(tierOverview(rows))
			if n := countSkips(rows); n > 0 {
				fmt.Println(style.Hint(i18n.N("skipped {n} candidate: {reasons}",
					"skipped {n} candidates: {reasons}", n,
					i18n.A{"n": n, "reasons": skipSummary(rows)})))
				fmt.Println(style.Hint(i18n.T("details: newgate tier <role>", nil)))
			}
			continue
		}

		r := rows[0]
		if len(r.steps) == 0 {
			fmt.Println(style.Item(style.Bad, i18n.T("no usable candidate", nil)))
		} else {
			fmt.Println(style.Field(i18n.T("Final", nil), style.Cyan(r.steps[0].Binding.String())))
			fmt.Println()
			fmt.Print(numberedBindingChain(r.steps, func(step resolve.Step) string {
				return bindingHealthLabel(liveHealth[step.Binding.String()])
			}))
			fmt.Println(style.Hint(i18n.T("the chain head is fixed; fallbacks follow the predicted TTFT of this context", nil)))
			fmt.Println(style.Hint(i18n.T("each (provider, model) appears once in the whole chain", nil)))
			if limit := st.Chain.Attempts(); limit < len(r.steps) {
				fmt.Println(style.Hint(i18n.T(
					"a request tries at most the first {limit} stops; the remaining {rest} stay in the chain",
					i18n.A{"limit": limit, "rest": len(r.steps) - limit})))
				fmt.Println(style.Hint(i18n.T("raise state.json chain.max_attempts to widen the actual attempt range", nil)))
			}
		}
		if len(r.skips) > 0 {
			fmt.Println()
			printSkips(r.skips)
		}
	}
	return 0
}

// bindingHealthLabel 一行说清这条 binding 现在什么状态。
//
// **阈值与档位名都来自 breaker/status**（2026-09-18）：这里曾经自己抄了一份
// 3000/12000，于是 daemon 改口径这一屏不会跟着变。
func bindingHealthLabel(h status.Status) string {
	if h.Open {
		return style.Red(i18n.T("circuit open", nil))
	}
	grade := status.Grade(h.ScoreMs, h.ScoreMs > 0)
	switch {
	case grade == status.LatencyUnknown && h.Grade == status.ProbeUnavailable:
		return style.Red(i18n.T("unavailable", nil))
	case grade == status.LatencyUnknown:
		return style.Dim(i18n.T("unprobed", nil))
	}
	label := fmt.Sprintf("%s %dms", grade, h.ScoreMs)
	switch grade {
	case status.LatencyFast:
		return style.Green(label)
	case status.LatencyOK:
		return style.Yellow(label)
	default:
		return style.Red(label)
	}
}

func chainSig(steps []resolve.Step) string {
	var b strings.Builder
	for _, s := range steps {
		b.WriteString(s.Binding.String())
		b.WriteByte('|')
	}
	return b.String()
}

func bindingChain(steps []resolve.Step, indent string) string {
	var out strings.Builder
	for i, step := range steps {
		out.WriteString(indent)
		if i > 0 {
			out.WriteString(style.Dim("→ "))
		}
		out.WriteString(step.Binding.String())
		out.WriteByte('\n')
	}
	return out.String()
}

// PrintChain 把编号后的候选链渲染出来，理由同 PrintSkips。
func PrintChain(steps []resolve.Step) { fmt.Print(numberedBindingChain(steps, nil)) }

func numberedBindingChain(steps []resolve.Step, extra func(resolve.Step) string) string {
	var out strings.Builder
	for i, step := range steps {
		out.WriteString(fmt.Sprintf("  %d. %s\n", i+1, step.Binding.String()))
		detail := "profile " + step.Profile
		if extra != nil {
			if value := extra(step); value != "" {
				detail += " · " + value
			}
		}
		out.WriteString(style.Hint(detail))
		out.WriteByte('\n')
	}
	return out.String()
}

func tierOverview(rows []tierView) string {
	var out strings.Builder
	out.WriteString("  ")
	out.WriteString(style.Dim(style.Pad(i18n.T("Role", nil), 6)))
	out.WriteString("  ")
	out.WriteString(style.Dim(i18n.T("Chain", nil)))
	out.WriteString("\n")
	firstOf := map[string]string{}
	for _, row := range rows {
		prefix := "  " + style.Cyan(style.Pad(row.name, 6)) + "  "
		switch {
		case len(row.steps) == 0:
			out.WriteString(prefix)
			out.WriteString(style.Red(i18n.T("no usable candidate", nil)))
			out.WriteString("\n")
		case firstOf[chainSig(row.steps)] != "":
			out.WriteString(prefix)
			out.WriteString(style.Dim("= " + firstOf[chainSig(row.steps)]))
			out.WriteString("\n")
		default:
			firstOf[chainSig(row.steps)] = row.name
			for i, step := range row.steps {
				if i == 0 {
					out.WriteString(prefix)
					out.WriteString(step.Binding.String())
					out.WriteString("\n")
				} else {
					out.WriteString("          ")
					out.WriteString(style.Dim("→ "))
					out.WriteString(step.Binding.String())
					out.WriteString("\n")
				}
			}
		}
	}
	return out.String()
}

func countSkips(rows []tierView) int {
	n := 0
	for _, r := range rows {
		n += len(r.skips)
	}
	return n
}

// skipSummary 跳过原因按类目计数。用户真正想问的是「为什么没轮到它」，
// 一百行里其实只有三五类原因。
func skipSummary(rows []tierView) string {
	reasons := map[string]int{}
	for _, r := range rows {
		for _, s := range r.skips {
			reasons[skipKind(s)]++
		}
	}
	var parts []string
	for _, k := range skipKindOrder {
		if n := reasons[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", skipLabel(k), n))
		}
	}
	return strings.Join(parts, " · ")
}

// printSkips 跳过**汇总**。永远只给按原因分组的计数与一句解释——
// 逐条铺开是候选全集的流水账，真正的结论是上面那条链。
//
// 需要一个个看的时候有别的口子：newgate profiles 看标志、doctor 看链路、
// metrics / probe 看熔断，那些才是可操作的信息。
//
// healthFromProxy 把 daemon 的熔断表按 "provider/model" 索引成一次命令内的快照。
// daemon 不在线时是空表——诊断退化为只看静态配置，不凭空判坏。
func healthFromProxy(ps *controlplane.Doc) map[string]status.Status {
	out := map[string]status.Status{}
	if ps != nil {
		for _, s := range ps.Breakers {
			out[s.Provider+"/"+s.Model] = s
		}
	}
	return out
}

// prettyMs 毫秒 → 人话。链预算是按 ms 配的（state.json 里 120000），
// 打印时不该原样甩 120000ms 给用户。
func prettyMs(ms int) string { return (time.Duration(ms) * time.Millisecond).String() }

// PrintSkips 把跳过汇总渲染出来。导出的理由：**需要它的模块命令**（opencodeomo
// 的 `newgate omo`）直接调它，排版归本模块。
//
// 它曾经挂在界面的 `Host` 上（界面替模块转发），2026-09-18 摘掉了——转发让界面
// import 配置，而界面不该认识任何模块。
func PrintSkips(skips []resolve.Skip) { printSkips(skips) }

func printSkips(skips []resolve.Skip) {
	reasons := map[string][]resolve.Skip{}
	for _, s := range skips {
		k := skipKind(s)
		reasons[k] = append(reasons[k], s)
	}
	n := len(skips)
	fmt.Println(style.Item(style.Skip,
		i18n.N("{n} candidate skipped", "{n} candidates skipped", n, i18n.A{"n": n})))

	t := style.NewTable(i18n.T("Reason", nil), i18n.T("Count", nil), i18n.T("Note", nil))
	t.AlignRight(1)
	for _, k := range skipKindOrder {
		group := reasons[k]
		if len(group) == 0 {
			continue
		}
		t.Row(skipLabel(k), fmt.Sprintf("%d", len(group)), style.Dim(skipDetail(group[0])))
	}
	fmt.Print(t.String())
}

// skipDetail 给整组配一句「所以呢」——光有类目名，用户还是不知道要改什么。
//
// 按类目给的那几句是本模块自己的话，过 i18n；两处直接回 s.Reason 的（已禁用 /
// 引用成环）是**别处来的话**：禁用理由是判据提供者说的，成环那句已经写清了
// 环长什么样（`a → b`），再包一层只会把信息压掉。
func skipDetail(s resolve.Skip) string {
	switch skipKind(s) {
	case resolve.SkipExcluded:
		return i18n.T("an excluded profile is only used when selected explicitly; it never joins automatic ordering", nil)
	case resolve.SkipUndefined:
		return i18n.T("this profile does not define this role (sparse layer, expected)", nil)
	case resolve.SkipNoKey:
		return i18n.T("the provider has no api_key", nil)
	case resolve.SkipUnavailable:
		return i18n.T("the provider was rejected as unavailable — see newgate metrics / probe", nil)
	case resolve.SkipDisabled:
		return s.Reason
	case resolve.SkipMaxSteps:
		return i18n.T("the chain's attempt budget is exhausted — see state.json chain.max_attempts", nil)
	case resolve.SkipDuplicate:
		return i18n.T("duplicate of an earlier candidate in the chain", nil)
	case resolve.SkipCycle:
		return s.Reason
	}
	return s.Reason
}

// ---------- 配置自己的两条维护命令 ----------

type initCommand struct{}

func (initCommand) Names() []string { return []string{"init"} }

func (initCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionMaintenance, Rank: 50,
		Usage: "init [--force]", Summary: i18n.T("lay down the default configuration", nil)}
}

func (initCommand) Run(_ cliapi.Host, args []string) int {
	return runInit(cliapi.Flag(args, "--force"))
}

// runInit 铺开默认配置。它归 config：写的是 providers.json / state.json / mappings，
// 全是本模块的文件。
func runInit(force bool) int {
	created, err := store.Init(force)
	if err != nil {
		return style.Die(70, err.Error())
	}
	if len(created) == 0 {
		fmt.Println(i18n.T("the configuration already exists, nothing to initialize (--force overwrites)", nil))
	} else {
		for _, c := range created {
			fmt.Println(i18n.T("created {path}", i18n.A{"path": c}))
		}
	}
	fmt.Println()
	fmt.Println(i18n.T("next: put your upstream keys into {path}", i18n.A{"path": paths.ProvidersFile()}))
	fmt.Println(i18n.T("what is written is placeholders — replace them with your own provider, endpoint and model names", nil))
	fmt.Println(i18n.T("keep keys in environment variables (never on disk): NEWGATE_KEY_<PROVIDER in upper case, - becomes _>", nil))
	return 0
}

type reloadCommand struct{}

func (reloadCommand) Names() []string { return []string{"reload"} }

func (reloadCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionMaintenance, Rank: 50,
		Usage: "reload", Summary: i18n.T("re-read the configuration now (normally hot-reloaded within a second)", nil)}
}

func (reloadCommand) Run(_ cliapi.Host, _ []string) int {
	info, _ := controlplane.State()
	if info == nil {
		fmt.Println(style.Item(style.Skip, i18n.T("proxy is not running; the configuration is read on the next start", nil)))
		return 0
	}
	controlplane.Notify()
	fmt.Println(style.Item(style.OK, i18n.T("proxy notified to re-read the configuration   pid {pid}",
		i18n.A{"pid": info.PID})))
	fmt.Println(style.Hint(i18n.T("you rarely need this command: configuration changes apply within a second", nil)))
	return 0
}

// ---------- 命令声明 ----------
//
// 每一条都只做「解析 argv → 调本文件里的实现」：帮助行、位置、别名是**给界面看
// 的元数据**，由拥有这条命令的模块声明（见 cliapi.Documented）。

type tierCommand struct{}

var (
	_ cliapi.Command    = (*tierCommand)(nil)
	_ cliapi.Documented = (*tierCommand)(nil)
)

func (tierCommand) Names() []string { return []string{"tier", "tiers", "role", "roles"} }

func (tierCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionRouting, Rank: 30,
		Usage: i18n.T("tier [role]", nil), Summary: i18n.T("fallback chain: who is used, what was skipped", nil)}
}

func (tierCommand) Run(_ cliapi.Host, args []string) int { return cmdTier(args) }

type profilesCommand struct{}

var (
	_ cliapi.Command    = (*profilesCommand)(nil)
	_ cliapi.Documented = (*profilesCommand)(nil)
)

func (profilesCommand) Names() []string { return []string{"profiles", "ls"} }

func (profilesCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionRouting, Rank: 30,
		Usage: "profiles", Summary: i18n.T("all profiles (priority / flags / overrides)", nil)}
}

func (profilesCommand) Run(_ cliapi.Host, _ []string) int { return cmdProfiles() }

type profileCommand struct{}

var (
	_ cliapi.Command    = (*profileCommand)(nil)
	_ cliapi.Documented = (*profileCommand)(nil)
	_ cliapi.Unstyled   = (*profileCommand)(nil)
)

func (profileCommand) Names() []string { return []string{"profile"} }

// Unstyled：`profile kv` 吐的是给管道/文件用的原始 KV 文本，不是控制面版式。
func (profileCommand) Unstyled(args []string) bool { return cliapi.Positional(args, 0) == "kv" }

func (profileCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionRouting, Rank: 30,
		Usage:   i18n.T("profile kv|history|restore <name>", nil),
		Summary: i18n.T("convert a profile to KV text, list its backups, or bring one back", nil)}
}

func (profileCommand) Run(host cliapi.Host, args []string) int {
	switch cliapi.Positional(args, 0) {
	case "kv":
		return cmdProfileKV(args[1:])
	case "history":
		return cmdProfileHistory(args[1:])
	case "restore":
		return cmdProfileRestore(args[1:])
	}
	return host.Die(64, i18n.T("usage: newgate profile kv|history|restore <name>", nil))
}

type setProfileCommand struct{}

var (
	_ cliapi.Command    = (*setProfileCommand)(nil)
	_ cliapi.Documented = (*setProfileCommand)(nil)
)

func (setProfileCommand) Names() []string { return []string{"--set-profile"} }

func (setProfileCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionRouting, Rank: 30,
		Usage:   i18n.T("--set-profile <name> [--agent <agent>]", nil),
		Summary: i18n.T("switch profile; omit --agent to set the global default", nil)}
}

// Run 的形状由界面归一后给：args[0] 是 profile 名，可选的 `--agent <agent>` 跟在
// 后面。界面负责认 `--set-profile=x` 与 `--set-profile x` 两种写法（argv 解析是它
// 的活），语义归本模块。
func (setProfileCommand) Run(_ cliapi.Host, args []string) int {
	name := cliapi.Positional(args, 0)
	if name == "" {
		return style.Die(64, i18n.T("--set-profile needs a profile name", nil))
	}
	return cmdSetProfile(flagValue(args, "--agent"), name)
}

// flagValue 取 `--名字 值` 里的值，没有给空串。
func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// commands 是本模块注入界面的全部命令。没有它就等于没有入口——而入口是 ui 的事，
// 本模块的功能不受影响（见 CLAUDE.md §4）。
func commands() []cliapi.Command {
	return []cliapi.Command{
		tierCommand{}, profilesCommand{}, profileCommand{}, setProfileCommand{},
		initCommand{}, reloadCommand{},
	}
}

// cmdProfileHistory 列出某个档位文件的历史版本（写盘之前自动留的那些）。
//
// # 为什么这个命令必须存在
//
// 备份环（见 store/history.go）从 2026-09-20 起就在写：每一次落盘之前都把当前那份
// 抄进 `.history/`。但那批文件**只有一个地方够得着**——知道路径拼法
// （`mappings__demo.kv/1789…bak`）、并且敢自己 cp 回来的人。对一个安全网来说，
// 「存在但取不回来」与「不存在」是同一种结果：改坏了的那个用户照样只能凭记忆重写。
// 所以这条命令不是锦上添花，它是那个功能**完成的另一半**（判据一直是代价不对称：
// 多一条命令是几十行，丢一次是用户手上的配置）。
func cmdProfileHistory(args []string) int {
	name := cliapi.Positional(args, 0)
	if name == "" {
		return style.Die(64, i18n.T("usage: newgate profile history <name>", nil))
	}
	file, err := profileFile(name)
	if err != nil {
		return style.Die(65, err.Error())
	}
	fmt.Println(style.Title("newgate profile history", name))
	fmt.Println(style.Rule(72))
	ents := store.HistoryEntries(file)
	if len(ents) == 0 {
		fmt.Println(style.Dim(i18n.T("nothing yet — one version is kept before every write", nil)))
		return 0
	}
	t := style.NewTable("#", i18n.T("When", nil), i18n.T("Size", nil), i18n.T("What it said", nil))
	t.AlignRight(0)
	for i, p := range ents {
		b, _ := os.ReadFile(p)
		t.Row(fmt.Sprintf("%d", i+1), historyStamp(p), fmt.Sprintf("%d B", len(b)), style.Dim(historyHint(file, b)))
	}
	fmt.Print(t.String())
	fmt.Println(style.Hint(i18n.T("bring one back with: newgate profile restore {name} <#>",
		i18n.A{"name": name})))
	return 0
}

// cmdProfileRestore 把某一个历史版本放回来（不带版本号 = 最近那一版）。
//
// **恢复本身也走 store.Write**，于是当前那份会先被抄进历史——恢复错了可以再恢复
// 回来。这一点很重要：一个不可撤销的「撤销」，没人在慌的时候敢按，而这条命令的
// 全部使用场景都是「我正在慌」。
func cmdProfileRestore(args []string) int {
	name := cliapi.Positional(args, 0)
	if name == "" {
		return style.Die(64, i18n.T("usage: newgate profile restore <name> [version]", nil))
	}
	file, err := profileFile(name)
	if err != nil {
		return style.Die(65, err.Error())
	}
	ents := store.HistoryEntries(file)
	if len(ents) == 0 {
		return style.Die(65, i18n.T("{name} has no history yet — there is nothing to bring back",
			i18n.A{"name": name}))
	}
	idx := 1 // 不带版本号 = 最近那一版：最常见的用法是「刚改坏了」
	if v := cliapi.Positional(args, 1); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > len(ents) {
			return style.Die(64, i18n.T("version must be 1..{max} (1 = most recent; see `newgate profile history {name}`)",
				i18n.A{"max": len(ents), "name": name}))
		}
		idx = n
	}
	src := ents[idx-1]
	b, err := os.ReadFile(src)
	if err != nil {
		return style.Die(70, err.Error())
	}
	if err := store.Write(file, b); err != nil {
		return style.Die(70, i18n.T("cannot write {path}: {err}",
			i18n.A{"path": file, "err": err.Error()}))
	}
	fmt.Println(style.Item(style.OK, i18n.T("brought {name} back to {when} (what it replaced is kept as a backup)",
		i18n.A{"name": name, "when": historyStamp(src)})))
	controlplane.Notify()
	return 0
}

// historyStamp 把 `<unixnano>.bak` 读成一个本地时间。读不出来就原样退回文件名
// ——一个看不懂的字符串也比一个假的日期强（同 web 界面那条）。
func historyStamp(path string) string {
	base := filepath.Base(path)
	var n int64
	if _, err := fmt.Sscanf(base, "%d.bak", &n); err != nil {
		return base
	}
	return time.Unix(0, n).Format("2006-01-02 15:04:05")
}

// historyHint 给一版备份写一句「它当时说了什么」——挑版本时唯一能看的东西。
//
// 先试描述（那是档位文件里唯一一句人话），没有就退回第一行非空内容。这一步不该
// 失败：读不出来就返回空串，表格里那一格空着也比报错强（这是「看一眼」的命令）。
func historyHint(file string, b []byte) string {
	var pr *domain.Profile
	if strings.HasSuffix(file, ".kv") {
		pr, _ = store.ParseProfileKV(string(b))
	} else {
		var p domain.Profile
		if json.Unmarshal(b, &p) == nil {
			pr = &p
		}
	}
	if pr != nil && strings.TrimSpace(pr.Description) != "" {
		return style.Truncate(pr.Description, 48)
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if s := strings.TrimSpace(ln); s != "" {
			return style.Truncate(s, 48)
		}
	}
	return ""
}
