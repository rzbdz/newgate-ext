package codex

// Codex 客户端的工具方言抬到顶层 `tools`：单一上游无关的一手。
//
// 为什么不是 crossing：这一步修的是 **Codex 旧版形状** —— 把工具放在
// `input[0].additional_tools` 里 —— 与**任何上游**都不能读。把这个判断留在
// `modules/codex`（客户端方言拥有者），让 crossing 模块（codex_deepseek / 未来
// codex_ark 等）只负责**各家上游自己的修整**（DeepSeek 的 custom → function
// 之类）。两层之间加这个 +1。
//
// 范围（实测过的范围）：
//
//   - Ark / Claude / smt-deepseek / Kimi / smt-claude 旧版形状会拿到 200 但零
//     tool_call（silent failure，最坑的那一族）
//   - gemini / smt-codex / minimax 直接 400
//   - 在 `tools` 已是顶层非空数组的**新版**形状里，本层 no-op
//
// 没有覆盖形状的就不动手：未知 `type` 的工具项原样保留（让 Codex 的新版扩展
// 能在不修改本模块的情况下被上游看到）。

import (
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/domain"
	gatewayapi "github.com/rzbdz/newgate/modules/gateway"
	"github.com/rzbdz/newgate/modules/gateway/special"
	"github.com/rzbdz/newgate/modules/pluginmanager"
)

var _ special.Plugin = (*ToolsLift)(nil)

// ToolsLift 是 codex 客户端的工具方言抬到顶层 `tools` 的单一上游无关的一手。
//
// 名字（`codex-tools`）进日志与 `newgate st`；plugin 唯一身份要对得上。
type ToolsLift struct{}

func (ToolsLift) Name() string { return "codex-tools" }

// Before 这一手必须排在 codex-deepseek **之前**——codex-deepseek 的 Apply 会再去
// `input[0].additional_tools` 那里再抬一次（实测那次再抬的结果是把顶层 tools
// **替换**成同样的字节 + 顺带做 DeepSeek 专属的 custom→function 降级）。所以这
// 一手先抬上去，codex-deepseek 的「custom 降级」再在替换里生效。
//
// 名字必须等于 codex-deepseek 注册时声明的 Name（special.Register 用它）。
// `special.Ordered` 的图对未注册的名字**静默忽略**（见 modules/thinking/st-always.go
// 文件头那条引用）——所以在 dist-hello / dist-dashboard 等没装 codex-deepseek 的
// 发行版里，这一条边是空转的；判断这一手是否在场的依据是「codex 装没装」，
// 已经是更大一层在控制。
func (ToolsLift) Before() []string { return []string{"codex-deepseek"} }
func (ToolsLift) After() []string  { return nil }

// Why 给 `newgate st` 看的一段话——写得能让运维看出来「关掉它会发生什么」。
func (ToolsLift) Why() string {
	return i18n.T("Codex 0.155 puts tool definitions in `input[0]` (type=additional_tools) while "+
		"every upstream we ship (Ark, DeepSeek, Claude, Gemini, GPT, Kimi, MiniMax, ...) reads tools "+
		"from the top level only; without the lift the model sees no tools, answers in text, and the "+
		"client never gets a tool_result. Switch off and every Codex call loses its tools.", nil)
}

// Match 只看客户端身份——不绑上游。绑定上游是各 crossing 的事（见
// modules/codex_deepseek/st-tools.go 的 model.MatchTarget 那一段）。
//
// 这一手是**客户端方言归一化**，与上游无关。
func (ToolsLift) Match(r *special.Request) bool {
	return r != nil && r.Agent == ID
}

// Apply 抬工具树。三条不满足都不动手——fail-open 是这一层的规矩。
//
// 形状与开关：
//
//   - 开关点关着（`newgate plugin codex.lift-tools off`）→ 不动（见 switches.go）
//   - input[].additional_tools 不存在                    → 不动（没有可抬的东西）
//   - 顶层 tools 已是**非空数组**                          → 客户端已经写明 → 不动
//   - 其余                                                → 把嵌套树拍平成顶层 tools 数组
//
// 「其余」包括 `"tools": null`、`"tools": []`、缺失三个情况——HasUsableTopLevelTools
// 把它们都判定为「没有可用的顶层那份」。判断为「有」的那一边是新版 Codex 形状
// （顶层非空数组），是**客户端自己的明确意图**，不应当 shadow。
//
// 走的是纯字节手术（gateway/rewrite 的原语）：不读不写额外字段、不动 input[] 里
// 的任何一项——上游对额外的 additional_tools 项不反对（实测），所以保留它。
func (ToolsLift) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	if pluginmanager.Off(stateOf(r), SwitchLiftTools) {
		return body, nil, nil
	}
	if HasUsableTopLevelTools(body) {
		return body, nil, nil
	}
	arr, ok := ToolsInInput(body)
	if !ok {
		return body, nil, nil
	}
	tools, n := FlattenToolTree(arr)
	if n == 0 {
		return body, nil, nil
	}
	out, err := SetTopLevelTools(body, tools)
	if err != nil {
		// fail-open：改不动就让上游自己报错
		return body, nil, err
	}
	return out, []string{noteLifted(n)}, nil
}

// stateOf 从插件上下文取配置快照。允许 nil（旧调用面与单测）——
// pluginmanager.Off 对 nil 一律回 false，也就是「没关」。方向不能反：快照缺席
// 不该把补丁关掉。与 codex_deepseek 那份同款。
func stateOf(r *special.Request) *domain.State {
	if r == nil {
		return nil
	}
	return r.State
}

// registerToolsLift 注册这一手，并上报它的开关点。
//
// 开关点跟着**行为的拥有者**走：抬工具是 codex 客户端方言的事，与上游是谁无关，
// 所以开关在 `codex.lift-tools`（见 switches.go 里那段为什么）。
//
// 两个端口都是**已经拿到的 capability 值**，不是 ctx：调用方（module.go 的 Start）
// 持有它们，而这一手的 Optional 语义（网关切了就不挂、plugin-manager 切了就只挂
// hook 不报开关）在那里判断——见 module.go 里那段。
func registerToolsLift(gw gatewayapi.Gateway, pm pluginmanager.Manager) ([]modules.Release, error) {
	var releases []modules.Release
	hook, err := gw.RegisterRequestHook(ToolsLift{})
	if err != nil {
		return nil, err
	}
	releases = append(releases, hook)
	if pm == nil {
		// 没有 plugin-manager 的装配（骨架关掉了它）：hook 照挂，开关点不报。
		// 「开关缺席」与「开关默认开」在行为上是同一件事——pluginmanager.Off
		// 对没有记录的路径回 false（见 st-tools.go 的 Apply）。
		return releases, nil
	}
	self, err := pm.RegisterSelf(ID, Switches())
	if err != nil {
		modules.ReleaseAll(releases)
		return nil, err
	}
	releases = append(releases, self)
	return releases, nil
}
