package claudecode

import (
	"bytes"
	"encoding/json"
	"fmt"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/gateway/rewrite"
	"github.com/rzbdz/newgate/modules/gateway/special"
	thinkingapi "github.com/rzbdz/newgate/modules/thinking"
)

var (
	_ special.Plugin          = (*background)(nil)
	_ special.RoutePlugin     = (*background)(nil)
	_ special.StatusProvider  = (*background)(nil)
	_ special.BindingProvider = (*background)(nil)
	_ special.MetricProvider  = (*background)(nil)
)

func Treatments(thinking thinkingapi.Service) []special.Plugin {
	return []special.Plugin{background{thinking: thinking}}
}

// claudeBg 修 Claude Code 后台小请求撞上「默认思考」模型后的慢。
//
// 现场实抓（2026-09，Claude Code 2.1.263，NEWGATE_DUMP 落盘复核）：
//
//	主循环     normal + stream=true + tools:33 + thinking:adaptive
//	后台调用   mid   + 非流式      + tools 无 + thinking 没写
//	分类器本体 再叠加：system ~126KB，开头是
//	           "You are a security monitor for autonomous AI coding agents"；
//	           2 条 user 消息（CLAUDE.md + 完整会话 transcript）；
//	           stop_sequences ["</block>"]；max_tokens 2112
//
// 而 glm-5.3 这类国模**默认开思考**，「没写」不等于「关了」，于是分类器
// 15-30 秒才回、成波超时重试（日志里一串「客户端在连接阶段就取消」），
// 整个开发会话跟着卡死。
//
// 分两层，各管一件事：
//
//  1. 改道（Route，路由层）：分类器本体整个走 **light 链**——不只是
//     链头换 light，fallback 也在 light 链里走。「这条命令安全不安全」要
//     的是快和便宜，这个意图必须贯穿整条链：只换头的话，light 一挂掉回
//     mid 的体格，又慢回去了。其他后台调用（compact 总结、起标题）不改
//     道：它们要一点质量，且不挡交互；标记对不上时也自然不改道。
//  2. 禁思考（Apply，body 层）：对「没写 thinking 的」后台调用补显式
//     thinking:{"type":"disabled"}，写了的不碰（compact 总结显式带
//     adaptive，强改 disabled 会被始终思考模型拒掉——见 BestEffortDisableThink）。
//     撞上「该模型始终思考」的 400（GLM 1210）时由排最后的 always-thinks
//     兜底（改回 enabled + reasoning_effort:low）——本插件只表达「客户端
//     这次不想思考」的意图，翻译成各家上游听得懂的话是模型级插件的事。
//
// 特征为什么可靠：主循环**永远是流式的**（dump 佐证，含 -p 模式），非流式
// 的只剩后台小调用；后台调用里分类器本体再靠 system 标记精确认出。
// 两层都不看 Tier——真实模型名注入后 Tier 是「名字反查的猜测」（见
// resolve.realNameRoleOrder），标记和非流式才是精确特征。
//
// 这是少数会**改写客户端显式意图**的补丁，所以三条保险：
//   - notes / 日志明说改了什么（不静默，docs/01-product.md）；
//   - `newgate st off claude-bg` 一键摘除（改道和禁思考一起停）；
//   - 只认 Agent=="claude"（opencode 等其他客户端不受影响）。
type background struct{ thinking thinkingapi.Service }

// classifierMarker 是分类器 system 的自报家门（cc 2.1.263 实抓 4/4 命中；
// 2.1.295 反混淆核对仍在）。**四个 gate 共用同一份 system**：2026-10-10 在
// 2.1.295 里读出来，gate（Bash）/ sandboxNetwork / workflowGate / handoff
// 都走 xio()→GNe() 拼出的这一句，flavour 只改**消息里**追加的尾巴。
const classifierMarker = "You are a security monitor"

// classifierStopSequences 是分类器的两个终止符。**整个客户端里只有 auto_mode
// 分类器会设顶层 stop_sequences**（2026-10-10 反混淆核对：设置点唯一，就是
// tao() 里那句 `…V!=="fast"&&{stop_sequences:[_e?"</severity>":"</block>"]}`；
// 另两处是通用透传，得调用方先给，还有一处是源码表）。所以「顶层有
// stop_sequences」是一条**与提示词措辞无关**的结构判据。
var classifierStopSequences = []string{"</block>", "</severity>"}

// isClassifier 认「这条非流式请求是 Claude Code 的安全分类器」。两条判据是
// **或**的关系，任一条成立即认：
//
//  1. system 里出现分类器的自报家门（四个 gate 共用，见 classifierMarker）；
//  2. 顶层 stop_sequences 带着分类器的终止符（结构判据，措辞改了也还在）。
//
// 为什么要在标记之外再要一条结构判据：标记是**提示词**，措辞是客户端的事，
// 一次改版就可能把它挪走或改写；而漏认的代价不是「少省一次上游调用」，是
// **这一发被送去上游、上游回不出分类器形状 → 客户端判「无法评估」并把动作
// 拦下来**（见 classifier-naked.go 里 classifierAnswer 的注释）。多认的代价
// 只是一个后台小调用被短路，所以宁可宽。
//
// **为什么不连消息正文一起认**：分类器的 flavour 尾巴（stage 1/2 的说明）确实
// 落在消息里，但 compact 总结请求**带着整段会话**从这条路过去——会话里只要有人
// 引用过那几句话（本仓库的文档与注释里就有），compact 就会被当成分类器短路掉，
// 而它的答复是要**替换整段历史**的：那是一次会话级损坏，比漏认贵得多。system
// 与 stop_sequences 都长在「客户端自己组装的壳」上，会话正文碰不到。
func isClassifier(body []byte) bool {
	if raw, ok := rewrite.TopLevelRaw(body, "system"); ok && bytes.Contains(raw, []byte(classifierMarker)) {
		return true
	}
	if raw, ok := rewrite.TopLevelRaw(body, "stop_sequences"); ok {
		for _, end := range classifierStopSequences {
			if bytes.Contains(raw, []byte(end)) {
				return true
			}
		}
	}
	return false
}

func (background) Name() string { return "claude-bg" }

// Before 让「先补 thinking」的插件排在兜底翻译器（always-thinks）前面动手。
//
// 这里只写内核自己的插件名——2026-09-20 之前还列着 `deepseek`、`glm` 两个**发行版**
// 的名字，那是方向反了（内核不该认识产品插件），而且 special 的排序图对未注册的
// 名字静默忽略，所以那两条边在纯内核构建里一直是空转的。现在那条边由产品自己声明
// （见发行版仓库 modules/deepseek / modules/claudecode_glm 的 Before），
// `app/ordering_test.go` 钉住「内核的排序边只指向内核自己的插件」。
func (background) Before() []string { return []string{"always-thinks"} }
func (background) After() []string  { return nil }

func (background) Why() string {
	return i18n.T("Claude Code's background non-streaming requests (the Bash classifier, etc.) carry no thinking, "+
		"yet Chinese models think by default → 15-30 seconds, waves of timeouts, a wedged session\n"+
		"the classifier (the system prompt says \"security monitor\") takes the whole chain to the light tier "+
		"(fallback included); other background calls only get thinking disabled "+
		"(the streaming main loop is unaffected)", nil)
}

func (background) Route(body []byte, request *special.Request, state *domain.State) (special.RouteDecision, bool) {
	if request == nil || request.Agent != ID || request.Stream || !isClassifier(body) {
		return special.RouteDecision{}, false
	}
	decision := special.RouteDecision{
		Tier:             "light",
		FirstByteTimeout: state.Timeouts.ClassifierFirstByte(),
		Note:             i18n.T("classifier rerouted → the light-tier chain (fallback included)", nil),
		Metric:           "route_light",
	}
	if override, _ := classifierOverride(state); override != nil {
		head := *override
		decision.Head = &head
		decision.OverrideNote = i18n.T("classifier override → {head} (the highest priority globally, ahead of any profile)",
			i18n.A{"head": head.String()})
		decision.OverrideFailNote = i18n.T("classifier override {head} did not take effect; falling back to the light-tier chain",
			i18n.A{"head": head.String()})
	}
	return decision, true
}

func (background) Status(state *domain.State) []special.StatusItem {
	if state == nil {
		return nil
	}
	override, err := classifierOverride(state)
	if err != nil {
		return []special.StatusItem{{
			Label: i18n.T("Classifier config", nil),
			Value: i18n.T("classifier_override is not valid: {err}", i18n.A{"err": err}),
		}}
	}
	if override != nil {
		return []special.StatusItem{{
			Label: i18n.T("Classifier override", nil),
			Value: i18n.T("{head} · the highest priority globally, ahead of any profile", i18n.A{"head": override.String()}),
		}}
	}
	return []special.StatusItem{{
		Label: i18n.T("Classifier rerouting", nil),
		Value: i18n.T("Claude Code Bash classifier → the light-tier chain", nil),
	}}
}

func (background) Bindings(state *domain.State) []domain.Binding {
	if state == nil {
		return nil
	}
	if override, _ := classifierOverride(state); override != nil {
		return []domain.Binding{*override}
	}
	return nil
}

func classifierOverride(state *domain.State) (*domain.Binding, error) {
	if state == nil {
		return nil, nil
	}
	raw := state.ModuleConfig["classifier_override"]
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var binding domain.Binding
	if err := json.Unmarshal(raw, &binding); err != nil {
		return nil, fmt.Errorf("%w", err)
	}
	if binding.Provider == "" || binding.Model == "" {
		return nil, i18n.E("provider / model must both be filled in", nil)
	}
	return &binding, nil
}

func (background) Metrics() []special.MetricInfo {
	return []special.MetricInfo{{
		Action: "route_light",
		Hint:   i18n.T("Bash classifier: the whole chain is rerouted to light", nil),
	}}
}

// Match 认「Claude Code 的后台小调用」这个类：claude 发起 + 非流式。
// 分类器本体的精确判定（system 标记）在 Route 里，那边管改道。
func (background) Match(r *special.Request) bool {
	return r != nil && r.Agent == ID && !r.Stream
}

// Apply 认出后台调用后，把「这次调用不想思考」交给 BestEffortDisableThink
// ——意图在这里，翻译（模型不支持关思考时改成最小思考）在那边，best
// effort：关不掉就让它思考，绝不因此失败。改道没命中（Route 没认出
// 分类器）时同样只禁思考，慢而不死。
func (b background) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	if b.thinking == nil {
		return body, nil, fmt.Errorf("thinking capability is unavailable")
	}
	return b.thinking.BestEffortDisable(body, r)
}
