// Package codex owns the base Codex client descriptor（OpenAI 官方的 codex CLI）。
//
// 本模块是**客户端接入**那一类：它说清「codex 是什么、装在哪、怎么装」，而不掺进
// 别的模块对该客户端的取舍。上游怪癖、交叉语义各自另开模块——与 claudecode /
// opencode 那条边界一致。
package codex

import (
	i18n "github.com/rzbdz/newgate/lib/i18n"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
)

const ID = "codex"

// NpmPackage 是官方发行渠道上那个包。
//
// 它就是**一行事实**，而且是会变的（官方换过包名、也换过渠道），所以它住在客户端
// 模块里而不是内核里：内核不认识 npm，更不认识某一家公司的包名（见 confighook 的
// AgentInstaller）。
const NpmPackage = "@openai/codex"

func Agent() *agentapi.Agent {
	return &agentapi.Agent{
		ID:      ID,
		Bin:     []string{"codex"},
		Dialect: "openai",
		// 模型名与 provider 走 ~/.codex/config.toml（`model` / `model_providers.*`），
		// 没有环境变量入口——所以这里没有 EnvVar 槽位。接管改的是那份 TOML，
		// 不是注入 env（见 config.go）。
		Notes: i18n.T("the model and provider live in ~/.codex/config.toml", nil),
	}
}
