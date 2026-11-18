package cli

import (
	"os"
	"sort"
	"testing"

	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/runtime/agentstate"
)

type testCatalog map[string]*agentapi.Agent

func (catalog testCatalog) Get(id string) (*agentapi.Agent, bool) {
	agent, ok := catalog[id]
	return agent, ok
}

// Installer / Facts / Installed 补齐目录端口（见 agentapi.AgentCatalog）：这些测试只关心
// 「有哪些 agent」，事实一律没有，装没装走缺省那条（与真实现同一份判据）。
func (catalog testCatalog) Facts(string) agentapi.AgentFacts { return nil }

// Installer：这些测试不验安装，一律没有（那就不提供自助安装）。
func (catalog testCatalog) Installer(string) agentapi.AgentInstaller { return nil }

func (catalog testCatalog) Installed(id string) bool {
	return agentapi.InstalledDefault(catalog[id], nil)
}

func (catalog testCatalog) Names() []string {
	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TestMain 给 cli 的测试装一份最小客户端目录。
//
// **字段是手写的，不是 claudecode.Agent() / opencode.Agent()**（2026-09-18 改）：
// cli 的测试只需要「有两个 id 叫 claude 和 opencode 的客户端」这个事实，
// 不需要那两个模块真实的描述符。而 import 它们曾经是**双向依赖的一半**——
// cli 的内部测试 import 了 claudecode，claudecode 又要 import cliapi 去注册
// 自己的命令（naked），于是 cli ⇄ claudecode 成环、直接编译不过。
//
// 这次不是「顺手绕开编译错误」，而是把依赖降到了测试真正需要的那一档：
// 谁要是让 cli 的测试非要用上真实描述符（比如断言 claude 的槽位），那说明
// 被断言的东西其实属于 claudecode 模块自己的测试，该搬到那里去。
func TestMain(m *testing.M) {
	claude := &agentapi.Agent{ID: "claude", Bin: []string{"claude"}}
	open := &agentapi.Agent{ID: "opencode", Bin: []string{"opencode"}}
	restore := agentstate.Set(testCatalog{claude.ID: claude, open.ID: open})
	code := m.Run()
	restore()
	os.Exit(code)
}
