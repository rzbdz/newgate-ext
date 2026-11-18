// Package resolve 是解析流水线：给定配置快照，算出一个档位的 fallback 链。
//
// 全部是纯函数——外部事实（熔断状态、禁用规则、时钟）都作为入参传进来。
// 这样「给定这些配置，应该解析出什么链」可以完全离线测试。
package resolve

import (
	"sort"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Step 链上的一步：用哪个 profile 的哪个绑定。
type Step struct {
	Profile  string
	Binding  domain.Binding
	Provider domain.Provider
}

func (s Step) String() string { return s.Profile + ":" + s.Binding.String() }

// Skip 某个候选为什么没进链。给 explain 视图和 X-Newgate-Chain 用。
//
// 记录跳过原因不是锦上添花：用户唯一会问的问题是「为什么不是我想的那个」，
// 没有这个就只能靠猜（docs/04-configuration.md）。
type Skip struct {
	Profile string
	Target  string // provider/model；空表示整个 profile 被跳过
	// Reason 给用户看的那句话，已经过 i18n.T。
	Reason string
	// Kind 这条跳过的**类目**：机器标记，不随语言变（见下面那组常量）。
	//
	// 为什么要跟 Reason 分开：显示层要按类目分组（`newgate tier` 的「跳过 N 个
	// 候选」按原因计数、状态块尾行也一样），而分组判据不能是那句给人看的话——
	// 话是翻过的，中文下拿英文子串去匹配只会把每一类都落进「其他」。类目在建链
	// 时（这里）就定下来，措辞由显示层按类目查表。
	Kind string
}

// Skip.Kind 的取值。它们是**机器标记**：分组、计数、排序的判据；给人看的措辞
// 由显示层按类目查表（见 modules/config 的 skipLabel / skipDetail），一个字都
// 不在这一层。
//
// 为什么这一层分得清：拒绝这条候选的是**哪一道关**在这里是明确的（role 没定义 /
// provider 没定义 / 没 key / 被禁用 / 健康表说不可用 / 超出 maxAttempts / 重复 /
// 成环），而**为什么**不能用（准入回调返回的那句话）属于提供判据的人。两件事分开
// 正是因为这个：resolve 只认形状，不认任何一家的词汇（见 Opts.Available）。
const (
	// SkipExcluded profile 标了 excluded：不自动进链，只能被显式选中。
	SkipExcluded = "excluded"
	// SkipUndefined 这一层没提供这个 role；也用于「候选的 provider 未定义」
	// （两者原先归在同一栏，文案不同而已）。
	SkipUndefined = "undefined"
	// SkipNoKey provider 没有 api_key。
	SkipNoKey = "no-key"
	// SkipUnavailable 准入回调（健康表等）说这条候选现在不能用。
	SkipUnavailable = "unavailable"
	// SkipDisabled 被显式禁用。
	SkipDisabled = "disabled"
	// SkipMaxSteps 链长超过 maxAttempts，尾部落选。
	SkipMaxSteps = "max-attempts"
	// SkipDuplicate 与链上更靠前的候选重复。
	SkipDuplicate = "duplicate"
	// SkipCycle 引用成环，已跳过。
	SkipCycle = "cycle"
	// SkipOther 兜底：显示层的类目表里没有这一栏（如「链头 pinned，不往下掉」——
	// 那一类今天没有单独的栏位，见 modules/config 的 skipKindOrder）。
	SkipOther = "other"
)

// Opts 构建链需要的外部输入。全部以函数形式注入，保持 BuildChain 纯净。
type Opts struct {
	// Active 链头。per-agent：不同 agent 可以有不同的链头。
	Active string
	// Available 可用性判断。返回 false 时第二个值说明**为什么**不能用，
	// 那句理由会原样进 Skip.Reason 给用户看（`newgate tier` 的「为什么不是
	// 我想的那个」）。
	//
	// 为什么是「返回理由」而不是让 resolve 自己写「熔断中」：准入的判据属于
	// 提供它的人（今天只有健康表一家，但它也可以是配额、灰度、时段），
	// resolve 只认这个**形状**，不认任何一家的词汇。
	//
	// nil = 都可用。
	Available func(provider, model string) (bool, string)
	// Rank 最近 probe 的健康档。只重排链头之后的 fallback；值越小越优先，
	// 相同值保持 profile/list 的原始顺序。nil = 不动态重排。
	Rank func(provider, model string) int
	// Disabled 禁用判断。nil = 都没禁。target 形如 "provider/model"。
	Disabled func(tier, target string) (bool, string)
	// MaxSteps 0 = 不限。
	MaxSteps int
}

// BuildChain 为某个键构造 fallback 链。
//
// 「键」可以是档位（heavy/normal/mid/light/vision），也可以是模块贡献的
// 动态角色（omo-sisyphus，见 domain.ExtraRole）；解析路径完全同一条。
//
// 顺序：**profile 为主序（链头优先，其余按 priority），list 为次序**。
// 语义是「先在当前任务键内部找替代，找不到才降到下一个 profile」——
// 「我要便宜的」应该先在便宜的里面挑，而不是一失败就跳到贵的。
//
// 规则：
//   - 链头 = Opts.Active；其余按 (priority, name) 排序，name 做 tiebreak
//     保证可复现
//   - excluded 的 profile 不进链，除非它就是链头（显式选中优先）
//   - 链头是 pinned 时链只有它自己（含它内部的 list），不往下掉
//   - profile 可稀疏：没定义这个键就整层跳过（有别名时先走别名，见
//     domain.Profile.CandidatesFor）
//   - 候选里的**引用**（`@normal`）就地展开成那条链，深度优先，带环检测
//   - (provider, model) 全链去重，只试一次
//
// 展开后的结果就是一条扁平的 fallback 序列：所有 profile、所有候选（含引用
// 展开出来的）按「profile 优先级 → list 次序」排好。引用没有引入第二套
// fallback 语义，只是让键之间能互相指。见 docs/04-configuration.md。
func BuildChain(key string, profiles []*domain.Profile, provs *domain.Providers, o Opts) ([]Step, []Skip) {
	ordered, skips := orderProfiles(profiles, o.Active)
	b := &chainBuilder{profiles: ordered, provs: provs, o: o,
		seen: map[string]bool{}, visiting: map[string]bool{key: true}, skips: skips}
	b.expand(key)
	if o.Rank != nil && len(b.steps) > 2 {
		sort.SliceStable(b.steps[1:], func(i, j int) bool {
			a, z := b.steps[i+1], b.steps[j+1]
			return o.Rank(a.Binding.Provider, a.Binding.Model) <
				o.Rank(z.Binding.Provider, z.Binding.Model)
		})
	}
	b.steps, b.skips = capSteps(b.steps, b.skips, o.MaxSteps)
	return b.steps, b.skips
}

// capSteps 把链截到 maxAttempts（<=0 = 不限），被截掉的每一步都留一条跳过原因。
//
// 单独成函数是因为 ResolveRequest 还要在**不截断**的情况下反查具体模型名：
// 名字可能排在 maxAttempts 之后，先截断会把一个配置里明明存在的模型判成
// 「不认识」（2026-11-04 现场）。那里是「建整条 → 挪头 → 再截」，与这里
// 「建整条 → 截断」共用同一段收尾，措辞才不会漂。
func capSteps(steps []Step, skips []Skip, max int) ([]Step, []Skip) {
	if max <= 0 || len(steps) <= max {
		return steps, skips
	}
	for _, s := range steps[max:] {
		skips = append(skips, Skip{Profile: s.Profile, Target: s.Binding.String(),
			Kind:   SkipMaxSteps,
			Reason: i18n.T("past maxAttempts={limit}", i18n.A{"limit": max})})
	}
	return steps[:max], skips
}

type chainBuilder struct {
	profiles []*domain.Profile
	provs    *domain.Providers
	o        Opts
	seen     map[string]bool
	visiting map[string]bool // 正在展开的键（防引用成环）
	steps    []Step
	skips    []Skip
}

// expand 把 key 的候选按 profile 优先、list 次序展开进 steps。
func (b *chainBuilder) expand(key string) {
	for _, p := range b.profiles {
		cands := p.CandidatesFor(key)
		if len(cands) == 0 {
			b.skips = append(b.skips, Skip{Profile: p.Name, Kind: SkipUndefined,
				Reason: i18n.T("role {role} is not defined", i18n.A{"role": key})})
			continue
		}
		for _, bd := range cands {
			if bd.IsRef() {
				if b.visiting[bd.Ref] {
					b.skips = append(b.skips, Skip{Profile: p.Name, Target: bd.String(),
						Kind: SkipCycle,
						Reason: i18n.T("reference cycle ({from} → {to}), skipped",
							i18n.A{"from": key, "to": bd.Ref})})
					continue
				}
				b.visiting[bd.Ref] = true
				b.expand(bd.Ref)
				delete(b.visiting, bd.Ref)
				continue
			}
			b.add(p, key, bd)
		}
	}
}

// add 一条具体候选过准入检查后进链。
func (b *chainBuilder) add(p *domain.Profile, key string, bd domain.Binding) {
	k := bd.String()
	if b.seen[k] {
		b.skips = append(b.skips, Skip{Profile: p.Name, Target: k, Kind: SkipDuplicate,
			Reason: i18n.T("duplicate of an earlier candidate, deduplicated", nil)})
		return
	}

	prov, ok := b.provs.Providers[bd.Provider]
	if !ok {
		b.skips = append(b.skips, Skip{Profile: p.Name, Target: k, Kind: SkipUndefined,
			Reason: i18n.T("provider is not defined", nil)})
		return
	}
	if prov.Key() == "" {
		b.skips = append(b.skips, Skip{Profile: p.Name, Target: k, Kind: SkipNoKey,
			Reason: i18n.T("provider has no api_key", nil)})
		return
	}
	if b.o.Disabled != nil {
		if yes, why := b.o.Disabled(key, k); yes {
			b.skips = append(b.skips, Skip{Profile: p.Name, Target: k, Kind: SkipDisabled,
				Reason: i18n.T("disabled: {why}", i18n.A{"why": why})})
			return
		}
	}
	if b.o.Available != nil {
		if ok, why := b.o.Available(bd.Provider, bd.Model); !ok {
			// 理由（why）原样透传：它属于提供判据的人，措辞不归这里。
			// 类目归这里——「这一关拒绝了它」是本层的知识。
			b.skips = append(b.skips, Skip{Profile: p.Name, Target: k,
				Kind: SkipUnavailable, Reason: why})
			return
		}
	}
	// **标记放在准入检查之后**（2026-09-18 挪的）：原来它在最前面，于是一个被拒的
	// 候选（没 key / 熔断 / 已禁用）再出现在别的 profile 里时，`newgate tier` 报的
	// 是「与链上更前面的重复，已去重」——而它从没上过链，真正的原因是第一次那个。
	// 用户唯一会问的问题是「为什么不是我想的那个」，一个指向错误的原因比没有原因
	// 更费时间。去重语义不变：同一个 key 在链上仍然只出现一次。
	b.seen[k] = true
	b.steps = append(b.steps, Step{Profile: p.Name, Binding: bd, Provider: prov})
}

// OverrideChain 构造「覆盖绑定为链头、tier 档链为 fallback」的链。
//
// 覆盖绑定的 provider 未定义 / 没 key / 熔断 / 禁用时覆盖不生效：回落纯
// tier 链并返回 applied=false（顺带记一条 Skip 说明为什么没覆盖上）。成功则
// 把覆盖 Step 插到最前，tier 链里与它同 key 的重复步去掉——「全局覆盖第一
// 优先」落在这里，覆盖 model 挂了才轮到 tier 档的候选。
func OverrideChain(source string, override domain.Binding, tier string, profiles []*domain.Profile,
	provs *domain.Providers, o Opts) ([]Step, []Skip, bool) {

	steps, skips := BuildChain(tier, profiles, provs, o)
	if source == "" {
		source = "override"
	}
	source = "(" + source + ")"

	if override.Provider == "" || override.Model == "" {
		return steps, skips, false
	}
	prov, ok := provs.Providers[override.Provider]
	switch {
	case !ok:
		skips = append(skips, Skip{Profile: source, Target: override.String(), Kind: SkipUndefined,
			Reason: i18n.T("provider is not defined, override not applied", nil)})
		return steps, skips, false
	case prov.Key() == "":
		skips = append(skips, Skip{Profile: source, Target: override.String(), Kind: SkipNoKey,
			Reason: i18n.T("provider has no api_key, override not applied", nil)})
		return steps, skips, false
	}
	if o.Disabled != nil {
		if yes, why := o.Disabled(tier, override.String()); yes {
			skips = append(skips, Skip{Profile: source, Target: override.String(), Kind: SkipDisabled,
				Reason: i18n.T("disabled: {why}", i18n.A{"why": why})})
			return steps, skips, false
		}
	}
	// 覆盖绑定同样要过准入：被摘牌的 binding 不该因为用户在命令行点了它就能
	// 上链（那样「点名的那个挂了」会绕过 fallback 保护，直接撞上去）。
	if o.Available != nil {
		if ok, why := o.Available(override.Provider, override.Model); !ok {
			skips = append(skips, Skip{Profile: source, Target: override.String(),
				Kind:   SkipUnavailable,
				Reason: i18n.T("{why}, override not applied", i18n.A{"why": why})})
			return steps, skips, false
		}
	}

	ovStep := Step{Profile: source, Binding: override, Provider: prov}
	out := []Step{ovStep}
	for _, s := range steps {
		if s.Binding.String() == override.String() {
			continue // 已经在链头
		}
		out = append(out, s)
	}
	return out, skips, true
}

// orderProfiles 链头 + 其余按 (priority, name)。
func orderProfiles(profiles []*domain.Profile, active string) ([]*domain.Profile, []Skip) {
	var head *domain.Profile
	var rest []*domain.Profile
	var skips []Skip

	for _, p := range profiles {
		if p.Name == active {
			head = p
			continue
		}
		rest = append(rest, p)
	}
	sort.SliceStable(rest, func(i, j int) bool {
		if pi, pj := rest[i].Prio(), rest[j].Prio(); pi != pj {
			return pi < pj
		}
		return rest[i].Name < rest[j].Name
	})

	var out []*domain.Profile
	if head != nil {
		out = append(out, head) // 显式选中优先，excluded 挡不住链头
		if head.Pinned {
			for _, p := range rest {
				// 类目是「其他」：显示层的类目表里没有给 pinned 单开一栏
				// （`newgate tier` 的跳过汇总按那张表分组），这里不擅自加一栏
				// ——加一栏是产品决定，不是翻译。
				skips = append(skips, Skip{Profile: p.Name, Kind: SkipOther,
					Reason: i18n.T("chain head {name} is pinned, the chain stops there",
						i18n.A{"name": head.Name})})
			}
			return out, skips
		}
	}
	for _, p := range rest {
		if p.Excluded {
			skips = append(skips, Skip{Profile: p.Name, Kind: SkipExcluded,
				Reason: i18n.T("excluded: explicit selection only", nil)})
			continue
		}
		out = append(out, p)
	}
	return out, skips
}
