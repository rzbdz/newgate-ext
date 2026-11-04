package hermes

import (
	"os"
	"path/filepath"

	"github.com/rzbdz/newgate/modules/config/paths"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
)

// facts 是本模块交给内核的运行期事实（见 confighook.AgentFacts）。
//
// hermes 的官方安装器把入口放在 `~/.local/bin/hermes`（指向
// `$HERMES_HOME/hermes-agent/venv/bin/hermes`）。通用的 OnPath 仍是第一判据；再认
// 这两个位置，理由与 codex 认 nvm 目录同一条：daemon 的 PATH 不是交互 shell 的
// PATH，而 `~/.local/bin` 正是常被登录脚本才加进去的那一格。只看文件、不起进程：
// 卡片状态会被频繁轮询。
type facts struct{}

var _ agentapi.AgentFacts = facts{}

func (facts) Installed() bool { return realHermes() != "" }

// SlotTier 这个槽位此刻走哪个档位：用户改过就用改的，没改返回空串（内核据此回落到
// 描述符里的缺省，见 confighook.TierOf）。
func (facts) SlotTier(s agentapi.Slot) string { return slotsOf().Tier(s) }

// SlotsKey 是 state.json 的 `module_config` 里本模块那张覆盖表的键。
const SlotsKey = "hermes_slots"

func slotsOf() agentapi.SlotOverrides {
	return agentapi.SlotOverrides{Key: SlotsKey, AgentID: ID, Slots: Agent().Slots}
}

// tierFor 取某个槽位此刻该用的档位（按名字找，理由同 codex 的 tierFor）。
func tierFor(name string) string {
	for _, s := range Agent().Slots {
		if s.Name == name {
			return agentapi.TierOf(facts{}, s)
		}
	}
	return ""
}

// realHermes 是这台机器上真正的 hermes 入口（跳过我们自己的 shim）；没有返回空串。
func realHermes() string {
	if p, err := Agent().FindReal(paths.ShimDir()); err == nil {
		return p
	}
	for _, p := range []string{
		filepath.Join(paths.Home(), ".local", "bin", "hermes"),
		filepath.Join(Home(), "hermes-agent", "venv", "bin", "hermes"),
	} {
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}
