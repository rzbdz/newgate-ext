package codex

import (
	"os"
	"path/filepath"

	"github.com/rzbdz/newgate/modules/config/paths"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
)

// facts 是本模块交给内核的运行期事实（见 confighook.AgentFacts）。
//
// Codex 是 npm 管理的 Node 工具。通用 OnPath 仍然是第一判据；这里再认标准 nvm
// 的版本 bin 目录，是因为 daemon 的 PATH 不是启动它的交互 shell 的 PATH：2026-09-22
// 实测 daemon 的 PATH 没有 `/root/.nvm/versions/node/v24.13.0/bin`，而 codex 正是
// `/root/.nvm/versions/node/v24.13.0/bin/codex`。这不是把 OnPath 改成「猜文件」：
// 只有 Codex 这个知道 npm 渠道的发行版模块认这个目录，其他客户端仍走内核的通用判据。
// 不调用 `npm prefix -g` 或 shell：卡片状态会被频繁轮询，文件探测必须便宜且不产生
// 外部进程；安装器仍继承 daemon 的 PATH，找不到 npm 时照实报错。
type facts struct{}

var _ agentapi.AgentFacts = facts{}

func (facts) Installed() bool {
	return Agent().OnPath() || codexOnNVMPath()
}

func codexOnNVMPath() bool {
	nvmDir := os.Getenv("NVM_DIR")
	if nvmDir == "" {
		nvmDir = filepath.Join(paths.Home(), ".nvm")
	}
	matches, err := filepath.Glob(filepath.Join(nvmDir, "versions", "node", "*", "bin", "codex"))
	if err != nil {
		return false
	}
	for _, path := range matches {
		if filepath.Clean(filepath.Dir(path)) == filepath.Clean(paths.ShimDir()) {
			continue
		}
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return true
		}
	}
	return false
}

// SlotTier 这个槽位此刻走哪个档位：用户改过就用改的，没改返回空串（内核据此回落到
// 描述符里的缺省）。**不在这里兜底**——「没改过就用缺省」只有一份实现（内核的
// confighook.TierOf）。
//
// 它同时被两处问：接管写 config.toml 那一刻（takeover.go 的 tierOf），以及界面
// 上那张卡显示的此刻值。同一个实现保证了两边不会分家。
func (facts) SlotTier(s agentapi.Slot) string { return slotsOf().Tier(s) }
