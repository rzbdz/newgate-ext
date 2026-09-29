// Package opencode owns the base OpenCode client descriptor. Add-ons such as
// OMO are separate modules and are composed by the catalog.
package opencode

import (
	i18n "github.com/rzbdz/newgate/lib/i18n"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
)

const ID = "opencode"

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
