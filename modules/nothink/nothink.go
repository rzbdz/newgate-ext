package nothink

import (
	"encoding/json"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/gateway/rewrite"
	"github.com/rzbdz/newgate/modules/gateway/special"
	thinkingapi "github.com/rzbdz/newgate/modules/thinking"
)

var _ special.Plugin = nothink{}

// StateKey 是 state.json 里这一格的名字（本模块所有，见 module.go 的注册）。
//
// 键名**别改**：known-good 回滚二进制读的是同一份配置文件，改了键就等于把这个
// 开关忘在打开状态——而它开着的时候用户的模型是不推理的（同 classifier_naked）。
const StateKey = "nothink"

// disabled 是替客户端写下去的思考开关：Anthropic 方言里「这一轮不要思考」。
var disabled = []byte(`{"type":"disabled"}`)

// Config 是一次「全局不思考」窗口的描述。CLI 构建/持久化，插件消费。
//
// **JSON 标签不能省**：`expires_at` 靠标签才对得上 `ExpiresAt`（Go 的默认字段名
// 匹配忽略大小写但不忽略下划线）。少了标签，反序列化出来的 ExpiresAt 是零值，
// `time.Now().Before(zero)` 恒为 false → `on` 当场判定过期 → 开关**永远不生效**，
// 而 status 那行会打出 `-2562047h…`。这个坑在 classifier_naked 上实跑撞过一次
// （2026-09-17），形状一样的结构就别再踩第二遍。
type Config struct {
	Mode      string    `json:"mode"`       // "on"（自限窗口）| "forever"
	ExpiresAt time.Time `json:"expires_at"` // Mode=="on" 时这个窗口到哪一刻失效
}

// Marshal 编成要写进 state.json 的字节。
func Marshal(c Config) ([]byte, error) { return json.Marshal(c) }

// Parse 解析 state.json 里那一格，并把**过期判定放在这里**。
//
// `on` 是懒过期的（daemon 不持 timer），所以「现在还算不算开着」只能由读的那一刻
// 决定；而读它的地方有三个（Match / Status / 命令），各判一次必然漂移
// ——classifier_naked 上实跑撞过那一版：Respond 判了过期、Status 没判，于是窗口
// 过去之后 status 打出一行「还有 -6m3s 自动关」。一个已经失效的窗口在**任何**读点
// 都该表现为「没开」，这条不变式只有一个执行点才不会破。
//
// 坏数据（空、坏 JSON、模式不在白名单）一律按「没开」处理——关起来是安全侧。
func Parse(raw []byte) (Config, bool) {
	var c Config
	if len(raw) == 0 || json.Unmarshal(raw, &c) != nil {
		return c, false
	}
	switch c.Mode {
	case "on":
		return c, time.Now().Before(c.ExpiresAt)
	case "forever":
		return c, true
	}
	return c, false
}

// nothink 是那个插件：开关开着时，把这一次请求改成「不思考」。
//
// 它自己只做一件事——**覆盖客户端的思考意图**（见 Apply）；补与翻那两半在
// 内核的 BestEffortDisable 里，本模块通过 capability 调它，而不是复制一份。
type nothink struct{ thinking thinkingapi.Service }

func (nothink) Name() string { return "nothink" }

// Before 让本插件排在兜底翻译器（always-thinks）前面：我们写下去的 disabled 要
// 轮到它看一眼（模型关不掉思考时它会把 disabled 翻成 enabled + low）。反过来排
// 的后果不是顺序难看，是那一发带着 disabled 撞 400。
//
// 这里只写内核自己的插件名：产品插件的排序边由产品自己声明，而内核那条
// `app/ordering_test.go` 拒「内核的排序边指向内核之外的插件」——方向是
// 「本模块 → 内核插件」这一条。
func (nothink) Before() []string { return []string{"always-thinks"} }
func (nothink) After() []string  { return nil }

func (nothink) Why() string {
	return i18n.T("the global no-reasoning switch (newgate nothink on|forever) is on: every Anthropic-dialect "+
		"request goes upstream with thinking disabled, adaptive/enabled included\n"+
		"best effort — a model that always thinks gets enabled + reasoning_effort=low instead of a 400, "+
		"and OpenAI-dialect requests (no such field to write) are left alone\n"+
		"newgate nothink off ends it; st off nothink takes this plugin out without touching the setting", nil)
}

// Match 认「这个开关此刻开着」这一件事，**外加**这次请求说的是 Anthropic 方言。
//
// 方言判据放在 Match 而不是 Apply 里：它不是「这次没什么可改的」那种例外，而是
// 「这件事与我无关」——OpenAI 方言的请求从头到尾就不该被这条策略算进去
// （见包注释里 opencode 那段现场）。
func (nothink) Match(r *special.Request) bool {
	return r != nil && domain.IsAnthropicPath(r.Path) && active(r)
}

// active 这个开关此刻开着吗。
//
// 读的是**这次请求看到的那份配置快照**（r.State），不是现去读盘：一次请求里所有
// 插件看的是同一个世界。State 缺席（单测、旧调用面）按「没开」处理——关着才是
// 这条开关的缺省态。
func active(r *special.Request) bool {
	if r == nil || r.State == nil {
		return false
	}
	_, on := Parse(r.State.ModuleConfig[StateKey])
	return on
}

// Apply 把「全局不思考」落到这一份请求体上。
//
// 两步，各自的边界都写清楚：
//
//  1. **客户端写了 thinking 就覆盖它**（adaptive / enabled / 别的形状都算）。
//     这是这个开关存在的全部意义：主循环那一发带的正是 adaptive，不覆盖它，
//     这个开关对真正在思考的那条路径等于不存在。改写只动 thinking 这一个值的
//     字节区间（rewrite 的原语），body 里其余每个字节原样保留。
//  2. **剩下的交给内核的 best-effort**：没写 thinking 的补上 disabled；模型
//     关不掉思考的（always-thinks 那张 quirk 表）就地翻成 enabled +
//     reasoning_effort=low。翻不动就原样发——best effort 的边界是「绝不因为这
//     个意图让请求失败」，而不是「一定要关掉」。
//
// 客户端显式设了 reasoning_effort 的那种请求，第 2 步会整个跳过（内核那条判据：
// 设了强度就是要思考）；但第 1 步照样把 thinking 改成 disabled——两条字段是
// **两回事**，这里不替用户猜哪一条说了算，只保证「全局不思考」这一条落地。
func (n nothink) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	// 再确认一次 Match。框架那条链本来就只对匹配的插件调 Apply，所以这一句是**多**
	// 的——但它守的东西值得：这条插件改的是客户端**写出来的意图**，而「开关关着就
	// 一个字节都不动」这条不变式写在改动的旁边，换个调用面（测试、将来的别的链）
	// 也破不了。
	if !n.Match(r) {
		return body, nil, nil
	}

	out, notes := body, []string(nil)
	if raw, has := rewrite.TopLevelRaw(out, "thinking"); has {
		if kind, _ := rewrite.TopLevelString(raw, "type"); kind != "disabled" {
			rewritten, err := rewrite.ReplaceTopLevelRaw(out, "thinking", disabled)
			if err != nil {
				// 改不动就照原样发：宁可这一发按客户端的意图思考，也不能因为
				// 一个开关把请求弄坏（fail-open，它同时也是一条 note）。
				return body, nil, i18n.Ef(err, "thinking left as the client wrote it ({err})", nil)
			}
			out = rewritten
			notes = append(notes, i18n.T("thinking:{was} → disabled (nothink is on)",
				i18n.A{"was": kind}))
		}
	}

	translated, more, err := n.thinking.BestEffortDisable(out, r)
	if err != nil {
		// 到不了这里：内核那一手的失败都折进 notes（它自己就是 fail-open 的）。
		// 真到了就当它没跑——上面那一手已经落地，照它发出去更接近用户的意图。
		return out, notes, nil
	}
	return translated, append(notes, more...), nil
}

// Status 让 `newgate status` 在开关开着的时候一直打出来。
//
// 为什么这条比别的状态行更该在：它改的是**模型的行为**，而症状（不推理了、
// 变笨了、变快了）没有任何一处会自己说是谁干的。忘了自己开着它的那一刻，
// 那件事就成了一道要翻配置才答得上来的问题。
func (nothink) Status(state *domain.State) []special.StatusItem {
	if state == nil {
		return nil
	}
	cfg, on := Parse(state.ModuleConfig[StateKey])
	if !on {
		return nil
	}
	if cfg.Mode == "forever" {
		return []special.StatusItem{{
			Label: i18n.T("Nothink", nil),
			Value: i18n.T("on (forever) — every request goes upstream with thinking off, best effort; newgate nothink off ends it", nil),
		}}
	}
	return []special.StatusItem{{
		Label: i18n.T("Nothink", nil),
		Value: i18n.T("on — it turns itself off in {left} (newgate nothink off ends it early)",
			i18n.A{"left": time.Until(cfg.ExpiresAt).Round(time.Second)}),
	}}
}
