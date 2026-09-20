package claudecode

import (
	"encoding/json"
	"fmt"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/gateway/rewrite"
	"github.com/rzbdz/newgate/modules/gateway/special"
)

var (
	_ special.Plugin         = (*classifierNaked)(nil)
	_ special.Responder      = (*classifierNaked)(nil)
	_ special.NoteProvider   = (*classifierNaked)(nil)
	_ special.StatusProvider = (*classifierNaked)(nil)
	_ special.MetricProvider = (*classifierNaked)(nil)
	_ special.Ordered        = (*classifierNaked)(nil)
)

// NakedConfigKey 是 state.json 里控制裸奔的 module 字段（与
// classifier_override 同族，归 claudecode 拥有）。键名**别改**：
// known-good 回滚二进制读同一份配置文件时，改了键就等于忘记关裸奔。
const NakedConfigKey = "classifier_naked"

// NakedConfig 是一次裸奔窗口的描述。CLI 构建/持久化，插件消费。
//
// **JSON 标签不能省**：`expires_at` 靠标签才对得上 `ExpiresAt`（Go 的默认
// 字段名匹配忽略大小写但**不忽略下划线**）。少了标签，反序列化出来的
// ExpiresAt 是零值，`time.Now().Before(zero)` 恒为 false → `on` 模式当场
// 判定过期 → 裸奔**永远不生效**，而 status 那行会打出 `-2562047h…`
// （time.Until 对零值溢出）。2026-09-17 上线后第一次实跑就撞上了。
type NakedConfig struct {
	Mode      string    `json:"mode"`       // "on"（自限窗口）| "forever"
	ExpiresAt time.Time `json:"expires_at"` // Mode=="on" 时这个窗口到哪一刻失效
}

// Marshal 编成要写进 state.json 的字节。字段键名由结构体标签决定，
// 与 ParseNakedConfig 共用同一份定义——两边各写一遍键名就是这类
// 「写进去读不出来」bug 的温床。
func (c NakedConfig) Marshal() ([]byte, error) {
	return json.Marshal(c)
}

// ParseNakedConfig 解析 state.json 里的裸奔配置，并把**过期判定放在这里**。
//
// 为什么过期检查必须在解析处、而不是各个调用点：`on` 窗口是懒过期的
// （daemon 不持 timer），所以「现在还算不算开着」只能由读的那一刻决定。三个
// 读点（Respond / Status / RespondNote）各判一次，必然漂移——2026-09-17 实跑
// 就是这样：Respond 判了过期、Status 没判，于是窗口过去之后 `newgate status`
// 打出一行 `还有 -6m3s 自动关`。一个已经失效的窗口在**任何**读点都该表现为
// 「没开」，这条不变式只有一个执行点才不会破。
//
// 坏数据（空、坏 JSON、模式不在白名单）一律按「没开」处理——关起来是安全侧，
// 宁可不生效也不能误短路。
func ParseNakedConfig(raw []byte) (NakedConfig, bool) {
	var c NakedConfig
	if len(raw) == 0 || json.Unmarshal(raw, &c) != nil {
		return c, false
	}
	switch c.Mode {
	case "on":
		if !time.Now().Before(c.ExpiresAt) {
			return c, false // 窗口已过（或 expires_at 缺失/为零值）：当作没开
		}
		return c, true
	case "forever":
		return c, true
	}
	return c, false
}

// classifierNaked 是「裸奔」：把 Claude Code 的 Bash 分类器请求直接短路成
// 批准，一个 LLM 调用都不发。
//
// 现场（2026-09 多次实抓）：分类器本体是**非流式**的
// 「You are a security monitor for autonomous AI coding agents」系统提示词请求
// （max_tokens 2112、stop_sequences ["</block>"]）。它每要执行一条可能危险的
// Bash 命令就发一次，慢 + 费 token，最要命的是会**误判**——把合法的本地调试
// 命令 block 掉，会话里蹦出「command not allowed」，用户只能手动改规则重来。
// 于是「干脆别让它管」。
//
// 因为是替用户关掉**自家安全门**，设计成一条条你能看见的硬条款（这也是这个
// 仓库「不静默」守则的一部分）：
//
//   - 默认全程关。只有用户**显式** `newgate naked on / forever` 才生效；
//   - `on` 是一个 **60 秒**自限窗口（expires_at 写进 config，读侧**懒过期**，
//     不需要 daemon 里的 timer，重启也不残留）；用途是「就这会儿我在调某个
//     被它误伤的命令」——旁路必须会自己消失，否则它静默变成永久行为就是
//     事故；
//   - `forever` 长期开，但拦下的**每一个**请求都打一行 `[naked]` 日志，且
//     `newgate status` 每次打印红色大警告；`newgate st` / `newgate metrics`
//     也有对应席位；
//   - 只认 Claude Code 的 security-monitor 标记，绝不碰别的请求；
//   - `newgate st off classifier-naked` 可以把它从插件层单独摘掉，比改配置
//     还快。
//
// 为什么不做成「永久 + 毫无痕迹」：那等于让一个静默的安全异形常驻，忘记它的
// 那一刻就是事故。60 秒自限 + 永久的满屏警告，是「方便」和「别突然想不起来」
// 之间的平衡点。它等价于 Claude Code 自带的 --dangerouslySkipPermissions，
// 只是落在代理这一层、且对用户可见。
type classifierNaked struct{}

func (classifierNaked) Name() string     { return "classifier-naked" }
func (classifierNaked) Before() []string { return []string{"claude-bg"} }
func (classifierNaked) After() []string  { return nil }
func (classifierNaked) Why() string {
	return i18n.T("when the user explicitly runs newgate naked on/forever, Bash classifier requests "+
		"are short-circuited into an approval without a single LLM call — no more 15-30s "+
		"classifier latency and no more sessions wedged by a wrong block"+
		"\non = a 60-second self-limiting window; forever = always on (every request is logged, status warns)"+
		"\nonly the Claude Code security-monitor marker is recognised; newgate naked off / st off both end it", nil)
}

// Match 认「Claude Code 的后台小调用」这个类（与 claude-bg 同判据）。真正的
// 分类器判定（security-monitor 标记）在 Respond 里，那个才是短路条件。
func (classifierNaked) Match(r *special.Request) bool {
	return r != nil && r.Agent == ID && !r.Stream
}

// Apply 是约定俗成的无操作：裸奔是短路不是改写。返回原样 body，
// 不碰请求。排在 claude-bg 之后，避免抢它该做的事。
func (classifierNaked) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	return body, nil, nil
}

// Respond 把分类器请求短路成 `<block>no</block>`。
//
// 响应格式来自分类器系统提示词自己的「Output Format」段（真现场快照）：
// 允许就是 `<block>no</block>`，禁止是 `<block>yes</block><category>…</category>
// <reason>…</reason>`。所以 mock 一个「批准」就是回一个内容为
// `<block>no</block>`、stop_reason=end_turn 的合法 anthropic Messages 响应，
// 让客户端对 block 标记的解析直接落到「没拦」。id 用单调毫秒当伪随机后缀，
// 客户端不校验它，只要类型/结构对。
func (classifierNaked) Respond(body []byte, r *special.Request, state *domain.State) ([]byte, bool) {
	if r == nil || r.Agent != ID || r.Stream || !isClassifier(body) {
		return nil, false
	}
	if _, active := ParseNakedConfig(state.ModuleConfig[NakedConfigKey]); !active {
		return nil, false
	}

	model := r.InModel
	if m, has := rewrite.TopLevelString(body, "model"); has && m != "" {
		model = m
	}
	resp := map[string]interface{}{
		"id":            fmt.Sprintf("msg_naked_%d", time.Now().UnixNano()),
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       []interface{}{map[string]interface{}{"type": "text", "text": "<block>no</block>"}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         map[string]interface{}{"input_tokens": 0, "output_tokens": 1},
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, false
	}
	return out, true
}

// RespondNote 是写给日志那一行的说明：这一发为什么没走上游。
//
// 短路意味着「这一发上游调用**没发生**」——对一个排查「为什么没有分类器
// 请求」的人来说，日志里只有 HTTP 200 是不够的，必须写清是谁替它回答的、
// 以及这个决定还有多久自动失效。
//
// 这里**不**依赖 ParseNakedConfig 的 active：本方法只在短路已经发生后被调用，
// 而窗口可能恰好在这两个调用之间到期（毫秒级，最长的一次日志就撞不上）。那种
// 情况下说「配置刚被关掉」是误导，直接报告「窗口刚好到期」才是事实。
func (classifierNaked) RespondNote(state *domain.State) string {
	cfg, active := ParseNakedConfig(state.ModuleConfig[NakedConfigKey])
	switch {
	case cfg.Mode == "forever":
		return i18n.T("[naked] forever mode: the classifier request was approved directly, the upstream was not called", nil)
	case active:
		return i18n.T("[naked] the classifier request was approved directly, the upstream was not called (the window has {left} left)",
			i18n.A{"left": time.Until(cfg.ExpiresAt).Round(time.Second)})
	case !cfg.ExpiresAt.IsZero():
		return i18n.T("[naked] the classifier request was approved directly, the upstream was not called (the window expired right after the interception)", nil)
	default:
		// 配置读不出来（坏数据 / 刚被删）：这一发确实短路了，照实说。
		return i18n.T("[naked] the classifier request was approved directly, the upstream was not called (the configuration is no longer valid)", nil)
	}
}

func (classifierNaked) Status(state *domain.State) []special.StatusItem {
	cfg, active := ParseNakedConfig(state.ModuleConfig[NakedConfigKey])
	if !active {
		return nil
	}
	if cfg.Mode == "forever" {
		return []special.StatusItem{{
			Label: i18n.T("Naked", nil),
			Value: i18n.T("on (forever) — the classifier is short-circuited and every request logs [naked]; newgate naked off ends it", nil),
		}}
	}
	return []special.StatusItem{{
		Label: i18n.T("Naked", nil),
		Value: i18n.T("on — it turns itself off in {left} (newgate naked off ends it early)",
			i18n.A{"left": time.Until(cfg.ExpiresAt).Round(time.Second)}),
	}}
}

func (classifierNaked) Metrics() []special.MetricInfo {
	return []special.MetricInfo{{
		Action: "shortcircuit",
		Hint:   i18n.T("naked: the classifier request is approved directly, the upstream is not called", nil),
	}}
}
