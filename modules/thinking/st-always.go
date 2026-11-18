package thinking

import (
	"github.com/rzbdz/newgate-ext/modules/gateway/probe"
	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
	"github.com/rzbdz/newgate-ext/modules/gateway/rewrite"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

var _ special.Plugin = (*alwaysThinks)(nil)

func Treatments() []special.Plugin { return []special.Plugin{alwaysThinks{}} }

// BestEffortDisableThink 「这次调用不想思考」的完整语义，一个操作做完：
//
//  1. 落地意图：**只在没写 thinking 时**注入 thinking:{"type":"disabled"}。
//     写了（adaptive / enabled / disabled）就是客户端这次调用的明确意图，
//     尊重它、不碰它（2026-09-09 实抓 + 探针复现：compact 总结请求显式带
//     thinking:adaptive，强改成 disabled 会被「始终思考」模型拒掉——glm-5.3
//     code 1210；探针：adaptive+tools→200、disabled→1210）；
//  2. 按当前链步的模型翻译：不支持关闭思考的模型（quirk NoThinkingDisable）
//     听不懂 disabled，就地翻成 enabled + reasoning_effort:low。
//
// best effort 的含义：关不掉就让它思考（用户把不支持关思考的模型配进
// light 档，也只能由他去了——慢，但请求活着）；翻译不动就原样发。绝不
// 因为这个意图让请求失败。
//
// 架构位置：意图方（如 claude-bg 的后台调用）在 special 链里调用它；
// forward 的链循环保证它对**每一步的模型**各跑一遍（fallback 换了模型，
// 翻译跟着换）。always-thinks 插件是它的兜底半边——管别的来源写入的
// disabled 和「带 tools 没写 thinking」的 1210 形态，两边共用同一个翻译
// 核心（noDisableTranslate），语义只有一份。
func BestEffortDisableThink(body []byte, r *special.Request) ([]byte, []string, error) {
	why := i18n.T("this call does not want to think (best effort)", nil)
	var notes []string
	out := body

	// reasoning_effort 与 thinking:disabled 互斥（见 st-deepseek 同款注释）：
	// 客户端设了推理强度就别去关它。
	if _, effort := rewrite.TopLevelRaw(out, "reasoning_effort"); effort {
		return out, nil, nil
	}

	// 1) 落地意图——只在「没写 thinking」时注入 disabled。
	//    写了（adaptive/enabled/disabled）就是客户端这次调用的明确意图，尊重
	//    它、不动它：compact（总结）请求显式带 thinking:adaptive，强改成
	//    disabled 会被「始终思考」模型拒掉（glm-5.3 code 1210）。best effort
	//    的边界从「关不掉就让它想」收窄成「本来就没说要想，才替它说不想」。
	if _, has := rewrite.TopLevelRaw(out, "thinking"); !has {
		nb, err := rewrite.InsertTopLevelRaw(out, "thinking", []byte(`{"type":"disabled"}`))
		if err != nil {
			notes = append(notes, i18n.T("thinking not injected ({err})", i18n.A{"err": err}))
		} else {
			out = nb
			// 注入的那段 JSON 走实参而不是留在消息里：`{...}` 是占位符语法，
			// 留在消息里会被当成一个叫 `"type":"disabled"` 的占位符（tools/i18n
			// 的 check 会报「调用点没有对应实参」）。
			notes = append(notes, i18n.T("injected {patch} ({why})", i18n.A{
				"patch": `thinking:{"type":"disabled"}`, "why": why}))
		}
	}

	// 2) 模型听不懂 disabled？就地翻译成它听得懂的最小思考。
	//    翻译按 (provider, r.Model) 的 quirk 决定——body 已被切到别的模型
	//    就别动手：「glm-5.3 不能关思考」推不出「glm-4.5-air 不能关」。
	if bm, has := rewrite.TopLevelString(out, "model"); (!has || bm == "" || bm == r.Model) &&
		noThinkingDisable(r, r.Provider, r.Model) {
		if nb, translated, ns, err := noDisableTranslate(out); err == nil && translated {
			out = nb
			notes = append(notes, ns...)
		}
	}
	return out, notes, nil
}

// noDisableTranslate 翻译核心（只有这一份语义）：把 thinking:disabled 翻成
// 始终思考模型听得懂的 enabled + reasoning_effort:low。只在 thinking 确实
// 是 disabled 时动手；translated=false 表示没什么可翻的。
//
// 现场报错（智谱 GLM，code 1210）：
//
//	[1210][该模型始终思考，不支持关闭思考；请使用 low、high 或 max。][2026…]
//
// 实测复现（api.rvcompute.com 聚合器 + glm-5.3）：
//
//	tools + 不给 reasoning_effort       → 400 / 1210
//	tools + reasoning_effort=low|high   → 200
//	thinking:{"type":"disabled"}        → 400 / 1210
//	不带 tools、也不带 thinking          → 200
//
// 成因：聚合器看见 tools 就替我们给上游塞了「关闭思考」，而始终思考的模型
// 直接拒。改不了聚合器，但显式给一个思考强度就能盖过它塞的值；low 最接近
// 「别想太多」的本意（实测 none 也收，但报错原文没提它，不赌）。
func noDisableTranslate(body []byte) (out []byte, translated bool, notes []string, err error) {
	raw, has := rewrite.TopLevelRaw(body, "thinking")
	if !has {
		return body, false, nil, nil
	}
	if t, _ := rewrite.TopLevelString(raw, "type"); t != "disabled" {
		return body, false, nil, nil
	}
	nb, rerr := rewrite.ReplaceTopLevelRaw(body, "thinking", []byte(`{"type":"enabled"}`))
	if rerr != nil {
		return body, false, nil, rerr
	}
	notes = append(notes, i18n.T("thinking:disabled → enabled (this model cannot turn thinking off)", nil))

	// effort 跟着补上（已有了就不动——客户端设过强度就尊重）
	if _, has := rewrite.TopLevelRaw(nb, "reasoning_effort"); !has {
		if nb2, ierr := rewrite.InsertTopLevelRaw(nb, "reasoning_effort", []byte(`"low"`)); ierr == nil {
			nb = nb2
			notes = append(notes, i18n.T(`added reasoning_effort:"low" (the upstream error asks for low/high/max)`, nil))
		} else {
			// 补不上要说出来，不能只留着上面那个「thinking 翻过去了」的
			// 好消息：工具形态下恰恰是这一手在盖过聚合器的隐式 disable，
			// 少了它这一发还是会 400（同文件 tools 形态那条路径就是这么报的，
			// 两条路不该两种态度）。
			notes = append(notes, i18n.T("reasoning_effort not added ({err})", i18n.A{"err": ierr}))
		}
	}
	return nb, true, notes, nil
}

// alwaysThinks 兜底翻译器：「始终思考」模型收到「关闭思考」就 400
// （GLM 1210）。BestEffortDisableThink 是意图方的主动入口；这个插件管
// **别的来源**造成的 400 形态：
//
//   - 客户端自己的 settings 写了 thinking:disabled（泄漏进后台调用，或
//     用户真想关——分不出来，一律翻）；
//   - 带 tools 且没写 thinking：聚合器会隐式替我们关思考 → 1210。
//
// 为什么 Match 只信 quirk 注册表、不按模型名猜：「始终思考」是模型版本的
// 属性，会变，也没接口能查。按名字猜会给一堆无关请求乱加字段，比不修更糟。
// 注册表的两个来源：转发时撞上这个报错学到（一次失败换永久免疫），或者
// newgate probe 主动探出来。
type alwaysThinks struct{}

func (alwaysThinks) Name() string { return "always-thinks" }

// Before/After 都是空的：本插件在**内核**这一侧没有任何排序约束。
//
// 它该排在那几个「先动手」的插件后面（claude-bg 改道后台请求、deepseek/glm 按模型
// 补形状），但那些插件**住在发行版里**——2026-09-20 之前这里列着它们的名字，那是
// 方向反了：内核不认识产品插件，也不该认识。而且这种写法不会报错——special 的排序图
// 对未注册的名字**静默忽略**（见 modules/gateway/special 的 addEdge），所以在纯内核
// 构建里那几条边一直是空转的，写错了也没人发现。
//
// 正确的方向是让**拥有者**声明：产品插件在自己的 Before() 里说「我排在 always-thinks
// 前面」（见发行版仓库 modules/claudecode、modules/deepseek、modules/claudecode_glm）。
// `app/ordering_test.go` 把「内核的排序边只指向内核自己的插件」钉住了——这条测试
// 在 claudecode 搬走的那一刻就红过一次，正好证明它管用。
func (alwaysThinks) Before() []string { return nil }
func (alwaysThinks) After() []string  { return nil }

func (alwaysThinks) Why() string {
	return i18n.T("some models always think and return a 400 when told to turn thinking off "+
		"(GLM code 1210)\ngive these models an explicit reasoning_effort=low and change "+
		"thinking:disabled back to enabled (the fallback half of BestEffortDisableThink)", nil)
}

func (alwaysThinks) Match(r *special.Request) bool {
	if r == nil {
		return false
	}
	return matchTarget(r, r.Provider, r.Model)
}

// matchTarget 这个 (provider, model) 是不是「不支持关闭思考」——**判据只有这一处**，
// Match 与 Apply 共用它。两边各写一遍的代价不是重复，是分家：Apply 少判一次的症状
// 是「补丁打在一个从没表现出这个毛病的模型上」，而那是往用户的请求里加字段。
//
// 两个来源，任一命中即可：
//
//  1. **学到的那张表**（quirk.Flag）：转发撞一次 400 学会，或者 `newgate probe`
//     主动探出来。它活**内存**里，进程重启就得重学一次。
//  2. **探活写下的那份缓存**（probe-capabilities.json）：上面那张表的落盘版。少了
//     这一条，每次换版/重启之后的第一发 Codex→GLM 请求必然 400（表是空的），而用户
//     看到的是一句「stream disconnected before completion」——重启后必现、过一会儿
//     自己好，是最难复现也最难解释的那类故障。2026-09-22 实测到的正是这个形态。
//
// 缓存那一路走 quirk.Default 的**只读**加载（`probe.LoadCachedCapabilities` 装的是
// 同一份 Default，见 forward.Server.Start），而且按需问、不在 Apply 热路径上每次都读
// 盘：命中表时（学过的那些）根本不碰磁盘。
func matchTarget(r *special.Request, provider, model string) bool {
	if noThinkingDisable(r, provider, model) {
		return true
	}
	return cachedNoThinkingDisable(provider, model)
}

// cachedNoThinkingDisable 读探活缓存里那一位。
//
// 读失败（文件还没写、权限不对、内容坏）一律按「不知道」处理——**fail-open 的方向
// 在这里是「不翻译」**：宁可少修一发（上游报错，用户能看见原文），也不能凭一份读不
// 出来的文件去改请求体。同一条规矩见 noThinkingDisable 的说明。
var cachedNoThinkingDisable = func(provider, model string) bool {
	if err := probe.LoadCachedCapabilities(); err != nil {
		// 不写日志：这里是热路径上的每次请求，而「缓存读不出来」这件事
		// 网关启动时已经报过一次（forward.Server.Start 那条 [probe] 行）。
		return false
	}
	return quirk.Default.Has(provider, model, quirk.NoThinkingDisable)
}

// noThinkingDisable 这个 (provider, model) 是不是「不支持关闭思考」。
//
// 表由**网关经 special.Request 交过来**（见那个字段的说明）：本模块读的是同一次
// 请求看到的那张表，而不是 `gateway/quirk` 的包级变量。区别不在于值（是同一张
// 表），在于**依赖是否可见**——包级调用是一条 service locator 依赖，Requires 和
// 依赖图里都看不见它（docs/03-architecture.md §5 禁止新增这种依赖）。
//
// 表缺席（测试、旧调用面）按「没学到」处理：宁可少一次翻译，也不能瞎改请求。
func noThinkingDisable(r *special.Request, provider, model string) bool {
	if r == nil || r.Quirks == nil {
		return false
	}
	return r.Quirks.Has(provider, model, quirk.NoThinkingDisable)
}

func (alwaysThinks) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	// 不变量保险：quirk 是按 (provider, r.Model) 学的，只对还发往这个模型
	// 的请求生效。链上 r.Model 由框架从 body 同步（special.Apply），正常
	// 到不了这里就不相等；直调或将来有新路径时这一道把「补丁打错模型」
	// 挡在最后关口。
	if bm, has := rewrite.TopLevelString(body, "model"); has && bm != r.Model {
		return body, nil, nil
	}

	var notes []string
	out := body
	needEffort := false

	if _, hasThinking := rewrite.TopLevelRaw(out, "thinking"); hasThinking {
		// disabled → 翻译（与 BestEffortDisableThink 共用同一份核心）
		if nb, translated, ns, err := noDisableTranslate(out); err != nil {
			notes = append(notes, i18n.T("thinking left unchanged ({err})", i18n.A{"err": err}))
		} else if translated {
			out = nb
			notes = append(notes, ns...)
		}
	} else if _, hasTools := rewrite.TopLevelRaw(out, "tools"); hasTools {
		// 没写 thinking 还带 tools：聚合器会隐式替我们关思考 → 1210。
		// （显式写了 adaptive/enabled 的不在此列——客户端要思考，没人隐式关它。）
		needEffort = true
	}

	// 补显式 reasoning_effort（tools 形态）。这是真正盖过聚合器那个隐式
	// disable 的一手。
	if needEffort {
		if _, has := rewrite.TopLevelRaw(out, "reasoning_effort"); !has {
			if nb, err := rewrite.InsertTopLevelRaw(out, "reasoning_effort", []byte(`"low"`)); err != nil {
				notes = append(notes, i18n.T("reasoning_effort not added ({err})", i18n.A{"err": err}))
			} else {
				out = nb
				notes = append(notes, i18n.T(
					`added reasoning_effort:"low" (the upstream error asks for low/high/max)`, nil))
			}
		}
	}

	// Responses 方言（Codex）：推理强度住在**嵌套的 reasoning 对象**里，不叫顶层
	// reasoning_effort。上面那些份判断在这一路上一条都不成立——2026-09-22 之前
	// 本插件只看 thinking 与顶层 reasoning_effort，于是 Codex 的请求被一个字节都
	// 不动地发出去，撞 400。
	//
	// 现场（2026-09-22，Codex 0.155.1 → smt-glm/glm-5.3，路径 /a/codex/v1/responses）：
	//
	//	reasoning.effort = medium | minimal | none → 400「该模型始终思考，不支持关闭思考；请使用 low、high 或 max。」
	//	reasoning.effort = low | high | max        → 200
	//	reasoning 缺席 / {"summary":"auto"} / {} / "effort":null → 200
	//
	// 而且**不挂在 tools 上**：不带 tools 的同一份 body 一样 400（实测），所以这
	// 一条与 needsEffort 那条「带 tools 才有」的形态无关——判据只该是「这个模型
	// 始终思考」＋「这个值它不收」。
	//
	// 症状为什么难查：`stream:true` 时上游把这件事报成 **HTTP 200 + 事件流里一条
	// `response.failed`**，Codex 那边显示成「stream disconnected before completion:
	// 该模型始终思考…」——看着像连接断了，其实是请求体里那个值它不收。只看 HTTP
	// 状态码的判据在这里会全绿。
	if nb, fixed, ns, err := fixResponsesEffort(out); err != nil {
		notes = append(notes, i18n.T("reasoning.effort left unchanged ({err})", i18n.A{"err": err}))
	} else if fixed {
		out = nb
		notes = append(notes, ns...)
	}

	return out, notes, nil
}

// acceptedEfforts 是「始终思考」的上游**收下**的推理强度。
//
// 上游报错原文自己点名了这三个（「请使用 low、high 或 max」），2026-09-22 在
// Responses 方言上逐格复核过：low / high / max → 200，medium / minimal / none
// → 400。三个都列上而不是只认 low，是因为这条判据的用途是「**上游不收**才动
// 它」——只认 low 会把用户明确设的 high/max 也当成要修的，那是替用户降档，属于
// 静默改变行为，比不修更糟。
var acceptedEfforts = map[string]bool{"low": true, "high": true, "max": true}

// effortForAlwaysThinks 是修一个「上游不收的值」时换上去的东西：low 最接近
// 「别想太多」的本意，也是本仓库在 anthropic 方言那条路上一直用的取值
// （见 noDisableTranslate）。high / max 虽然也收，但那是**替用户加码**——把一个
// 它不认识的档位往「想得更多」的方向猜，代价是用户的钱和等待。
const effortForAlwaysThinks = "low"

// fixResponsesEffort 修 Responses 方言里那个「上游不收的」思考强度。
//
// 只动**一个值**的字节区间：reasoning.effort。同一个对象里别的东西（Codex 会把
// summary 放在这儿）、以及 body 里其余每一个字节都原样保留——「请求体不做 JSON
// 往返」那条规矩在这一手上的落点。
//
// 判据是**收不收**（acceptedEfforts），不是「像不像 low」：认得的值一个字节都不
// 动。形状不认识（reasoning 不是对象、effort 不是字符串、effort 缺席）一律不改
// 并返回 fixed=false——宁可让上游按原样报错，也不猜着改。effort 缺席是常态
// （实测 Codex 只发 `{"summary":"auto"}` 时上游 200），动它是无依据的。
func fixResponsesEffort(body []byte) (out []byte, fixed bool, notes []string, err error) {
	raw, has := rewrite.TopLevelRaw(body, "reasoning")
	if !has {
		return body, false, nil, nil
	}
	eff, ok := rewrite.TopLevelString(raw, "effort")
	if !ok || eff == "" || acceptedEfforts[eff] {
		return body, false, nil, nil
	}
	inner, rerr := rewrite.ReplaceTopLevelString(raw, "effort", effortForAlwaysThinks)
	if rerr != nil {
		return body, false, nil, rerr
	}
	nb, rerr := rewrite.ReplaceTopLevelRaw(body, "reasoning", inner)
	if rerr != nil {
		return body, false, nil, rerr
	}
	return nb, true, []string{i18n.T(
		`reasoning.effort "{was}" → "{to}" (this model always thinks and takes low/high/max only)`,
		i18n.A{"was": eff, "to": effortForAlwaysThinks})}, nil
}
