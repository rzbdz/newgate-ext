// Package hermes 描述 Hermes Agent（Nous Research 的 Python agent，CLI + 消息网关）客户端。
//
// 本模块是**客户端接入**那一类：它说清「hermes 是什么、装在哪、怎么接管」，而不掺进
// 别的模块对该客户端的取舍——与 claudecode / codex / opencode 的边界一致。
//
// 它与另外三家最大的不同，是 hermes **会话里能换模型**（`/model`，CLI 与消息网关都有）。
// 所以接管同时给两种用法（2026-11-04 用户定的）：
//
//  1. **跟着 newgate 走**：缺省模型写成档位名（`normal`），实际落到哪个模型由 newgate
//     的 profile 决定——profile 改了，下一发请求就跟着变，hermes 什么都不用动。
//  2. **在 hermes 里直接点名**：`/model` 的列表里列出 newgate 背后的每一份 profile、
//     每一个绑定的模型（`provider/model`），选中谁就钉到谁。这一份列表是**接管时**
//     写进 config.yaml 的快照：profile 变了要重写一次（`newgate hermes sync`），而
//     正在跑的消息网关要不要重启由用户决定（见 sync.go）。
package hermes

import (
	"os"
	"path/filepath"
	"strings"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/paths"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
)

const ID = "hermes"

// InstallScript 是官方的安装方式（README 里那一行 curl | bash）。
//
// 它是一行会变的事实，所以住在客户端模块里（理由同 codex 的 NpmPackage）。
const InstallScript = "https://raw.githubusercontent.com/NousResearch/hermes-agent/main/scripts/install.sh"

// 槽位名。它们是**本模块的键**（state.json 的覆盖表、界面那张卡、status 那一行都用
// 这几个名字），不是 config.yaml 的路径——一个槽位可能对应文件里的好几处（见
// takeover.go 的 slotTargets）。
const (
	SlotModel       = "model"
	SlotDelegation  = "delegation"
	SlotVision      = "vision"
	SlotCompression = "compression"
	SlotAuxiliary   = "auxiliary"
)

// Agent 返回 Hermes 的描述符。
//
// # 槽位表从哪来
//
// = config.yaml 里**所有取值是模型名的键**，按「它在干什么活」归成五组。查的是
// hermes 0.13 的 DEFAULT_CONFIG 与读这些键的地方（`hermes_cli/config.py`、
// `agent/auxiliary_client.py`、`tools/delegate_tool.py`，2026-11-04）：
//
//	model        model.default                主循环
//	delegation   delegation.model             delegate_task 起的子 agent
//	vision       auxiliary.vision.model       看图
//	compression  auxiliary.compression.model  长上下文压缩成摘要
//	auxiliary    auxiliary.<其余任务>.model    起标题、审批、搜会话、web 摘要、curator……
//
// **不在表里的**：`smart_model_routing.cheap_model` 与 `compression.summary_model`
// 是死配置（前者的功能在上游 424e9f36b 删掉了，后者 v16→17 迁进了
// auxiliary.compression），没有代码读它们——接管它们只会制造「改了没用」的错觉。
// `fallback_providers` 也不碰：链上的兜底由 newgate 在服务端做，而 hermes 激活
// fallback 时**不认**具名 provider 的 api_mode（run_agent.py 按 URL 重新猜），
// 指回我们反而会换错方言。
// 为什么 auxiliary 下面十来个任务只算一个槽位：它们都是「主循环之外的小活」，用户对
// 它们的期望是同一个（便宜、快），逐个开槽位只会让卡片上出现十行没人会改的下拉。
// vision 与 compression 单列，是因为它们**不是**小活：一个要多模态，一个要读得下整段
// 上下文——缺省档位与其余的不同（与 Claude Code 把 /compact 放在 sonnet 档同一条）。
//
// 槽位都**没有 EnvVar**：hermes 的模型只认 config.yaml（它不读 ANTHROPIC_* 那一类
// 环境变量），值由接管写进文件——与 codex 同一种形态。
func Agent() *agentapi.Agent {
	return &agentapi.Agent{
		Icon:    iconPath,
		ID:      ID,
		Bin:     []string{"hermes"},
		Dialect: "openai",
		Slots: []agentapi.Slot{
			{Name: SlotModel, Tier: "normal",
				Desc: i18n.T("the main loop (model.default); /model can still switch to any newgate model", nil)},
			{Name: SlotDelegation, Tier: "mid",
				Desc: i18n.T("subagents started by delegate_task (delegation.model)", nil)},
			{Name: SlotVision, Tier: "vision",
				Desc: i18n.T("image understanding (auxiliary.vision)", nil)},
			{Name: SlotCompression, Tier: "mid",
				Desc: i18n.T("context compression summaries (auxiliary.compression)", nil)},
			{Name: SlotAuxiliary, Tier: "light",
				Desc: i18n.T("every other auxiliary task (titles, approval, session search, web extract, curator …)", nil)},
		},
		Notes: i18n.T("the model and provider live in ~/.hermes/config.yaml; the messaging gateway re-reads it on every message", nil),
	}
}

// Home 是 hermes 的数据目录。
//
// 次序：`$HERMES_HOME` → 上一次接管写的那份所在的目录 → `~/.hermes`。
//
//   - `$HERMES_HOME` 优先，因为那是 hermes 自己的规矩（`hermes_constants.get_hermes_home`：
//     环境变量 → `~/.hermes`），它的 profile 机制也靠这个变量切（`profiles/<name>`）。
//   - 中间那一格是给**守护进程**的：它的环境不是用户 shell 的环境。用户
//     `HERMES_HOME=… newgate on hermes` 接管的那份，守护进程自己算永远是 `~/.hermes`
//     ——于是后台同步、网页上的保存、重启按钮都会作用到一份没人在用的文件上，而
//     接管的那份清单再也不更新。所以接管时记下写的是哪份（record.Target），没有
//     显式环境变量的进程就认它。
func Home() string {
	if h := strings.TrimSpace(getenv("HERMES_HOME")); h != "" {
		return h
	}
	if t := loadRecord().Target; t != "" {
		return filepath.Dir(t)
	}
	return filepath.Join(paths.Home(), ".hermes")
}

// ConfigPath 是 hermes 读的那份文件。
func ConfigPath() string { return filepath.Join(Home(), "config.yaml") }

// getenv 是这一层唯一的间接：测试要能把 HERMES_HOME 指到沙箱，而**不能**动真实
// 用户的 ~/.hermes。
var getenv = os.Getenv
