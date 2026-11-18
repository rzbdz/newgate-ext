package takeover

import (
	"os"
	"sort"
	"strings"
	"testing"

	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/runtime/agentstate"
)

type testCatalog map[string]*agentapi.Agent

func (catalog testCatalog) Get(id string) (*agentapi.Agent, bool) {
	agent, ok := catalog[id]
	return agent, ok
}

// Installer / Facts / Installed 补齐目录端口（见 agentapi.AgentCatalog），走真实现那条判据。
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

// fakeConfigTakeover 是测试专用的最小 ConfigTakeover：这个包测的是
// shim/config 两种接管机制怎么选、怎么切换，不测某个具体客户端插件的
// 改写逻辑，所以不该依赖 opencodeomo 这种可选插件模块（那会把
// runtime/takeover 的测试和 cli → runtime/takeover 的生产依赖拼成环，
// 2026-09-17 实测触发）。
type fakeConfigTakeover struct{}

func (fakeConfigTakeover) Targets() []string       { return nil }
func (fakeConfigTakeover) IsTakenOver(string) bool { return false }
func (fakeConfigTakeover) Apply(int) ([]*agentapi.TakeoverReport, error) {
	return nil, nil
}
func (fakeConfigTakeover) Restore() ([]string, error) { return nil, nil }

// syntheticAgent 是内核测试用的**合成**客户端定义。
//
// 真实的 claude / opencode 定义住在发行版里（客户端接入是产品取舍，2026-09-20
// 搬走），而这个包测的是「shim / config 两种接管机制怎么选、怎么切换」——它要的是
// 「一个带 config 接管的 agent」，不是「claude 长什么样」。合成定义还更稳：产品那边
// 改定义不会让内核的测试红。id 仍写 claude / opencode 只是让断言好读。
func syntheticAgent(id, dialect string) *agentapi.Agent {
	upper := strings.ToUpper(id)
	return &agentapi.Agent{
		ID:         id,
		Bin:        []string{id},
		Dialect:    dialect,
		BaseURLEnv: upper + "_BASE_URL",
		AuthEnv:    upper + "_API_KEY",
		// Config 留空 = 这个客户端靠 **shim**（PATH 前置）接管；想测 config 改写那条
		// 机制，调用方自己塞一个（见 TestMain 里的 open）。
	}
}

func TestMain(m *testing.M) {
	claude := syntheticAgent("claude", "anthropic")
	open := syntheticAgent("opencode", "openai")
	open.Config = fakeConfigTakeover{}
	restore := agentstate.Set(testCatalog{claude.ID: claude, open.ID: open})
	code := m.Run()
	restore()
	os.Exit(code)
}
