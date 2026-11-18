// Package opencode owns the base OpenCode client descriptor. Add-ons such as
// OMO are separate modules and are composed by the catalog.
package opencode

import (
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

const ID = "opencode"

// NpmPackage 是官方 npm 包（2026-11-17 核过 registry 的 `latest`，bin 里是 opencode）。
//
// 为什么不用 `https://opencode.ai/install`：那个脚本要联网挑版本、改 PATH、还要
// 处理一堆平台分支——它是**给人装的**，不是给另一个程序在向导里调用的。npm 这条
// 渠道与 claude / codex 一致，且失败时错误信息是可读的（见内核 install.go）。
const NpmPackage = "opencode-ai"

func Agent() *agentapi.Agent {
	return &agentapi.Agent{
		// 界面上的图标（见 icon.go：它为什么住在这个模块里）。
		Icon:    iconPath,
		ID:      ID,
		Bin:     []string{"opencode"},
		Dialect: "openai",
		Notes:   i18n.T("slots are discovered by the config extension module", nil),
	}
}
