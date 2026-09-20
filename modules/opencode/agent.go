// Package opencode owns the base OpenCode client descriptor. Add-ons such as
// OMO are separate modules and are composed by the catalog.
package opencode

import agentapi "github.com/rzbdz/newgate/modules/confighook"

const ID = "opencode"

func Agent() *agentapi.Agent {
	return &agentapi.Agent{
		ID:      ID,
		Bin:     []string{"opencode"},
		Dialect: "openai",
		Notes:   "槽位由配置扩展模块发现",
	}
}
