// Package confighook 管理“newgate 能接管哪些客户端”这份运行时目录。
//
// 客户端模块注册 Agent 描述符，配置插件再为该 Agent 绑定可逆的 takeover。
// runtime 只需要查询并启动客户端，不能修改目录；因此同一注册表拆成
// ConfigHooks 写端口和 AgentCatalog 读端口。这个拆分不是形式上的接口隔离，
// 而是防止启动路径获得注册权限。
//
// 每次写入都返回带 token 的 Release。旧组件停止时只能撤销自己那次注册，
// 不会误删热重载或新应用实例后来写入的值。
package confighook

import (
	"context"
	"fmt"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"sort"
	"sync"

	modules "github.com/rzbdz/newgate/component"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
)

type registry struct {
	mu     sync.RWMutex
	agents map[string]*Agent
	// facts 是客户端模块交上来的**运行时事实**（见 AgentFacts）。它与 agents 分开
	// 存是有意的：描述符是「这个客户端是什么」，事实是「它此刻怎么样」，后者可以不交。
	facts map[string]AgentFacts
	// installers 是「怎么把这家工具装上」（见 AgentInstaller）。与 facts 分开：
	// 一个客户端可以只交其中一份，两份的用途也不一样（一份用来回答、一份用来动手）。
	installers map[string]AgentInstaller
	fields     map[string]string
	tokens     map[string]uint64
	next       uint64
}

var (
	_ ConfigHooks  = (*registry)(nil)
	_ AgentCatalog = (*registry)(nil)
)

// New 创建同时实现写入端口和只读目录端口的注册表。
// 两个 capability 共享同一事实源，但通过不同接口限制消费者权限。
func New() modules.Component {
	registry := &registry{
		agents:     make(map[string]*Agent),
		facts:      make(map[string]AgentFacts),
		installers: make(map[string]AgentInstaller),
		fields:     make(map[string]string),
		tokens:     make(map[string]uint64),
	}
	var releases []modules.Release
	return modules.Component{
		Name: "config-hook",
		Desc: func() string {
			return i18n.T("the client catalog: what a client is, how to install it, and how it is right now", nil)
		},
		Type: "infra",
		Requires: []modules.Requirement{
			// ui 是**弱依赖**（见 component.Optional）：装着界面就有 `newgate
			// agents` 这个入口和术语表那一行；没装就跳过，客户端描述符照常工作。
			modules.Optional(cliapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(ConfigHooksCapability, ConfigHooks(registry)),
			modules.Provide(AgentCatalogCapability, AgentCatalog(registry)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			ui, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			release, err := ui.RegisterCommand(agentsCommand{AgentCatalog(registry)})
			if err != nil {
				return err
			}
			releases = append(releases, release)
			// status 里的「每个客户端」一块：谁的状态谁自己报（见 status.go）。
			statusRelease, err := ui.RegisterStatusBlocks(agentStatus{AgentCatalog(registry)})
			if err != nil {
				return err
			}
			releases = append(releases, statusRelease)
			glossaryRelease, err := ui.RegisterGlossary(glossary{AgentCatalog(registry)})
			if err != nil {
				return err
			}
			releases = append(releases, glossaryRelease)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}

// RegisterAgent 原子加入客户端描述符，并用 token 防止过期 Release 删除后来的注册。
func (r *registry) RegisterAgent(agent *Agent) (modules.Release, error) {
	if agent == nil || agent.ID == "" {
		return nil, fmt.Errorf("agent ID is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.agents[agent.ID]; exists {
		return nil, fmt.Errorf("duplicate agent %s", agent.ID)
	}
	token := r.newToken("agent:" + agent.ID)
	r.agents[agent.ID] = agent
	return r.release("agent:"+agent.ID, token, func() {
		delete(r.agents, agent.ID)
	}), nil
}

// RegisterAgentInstaller 记下客户端模块交上来的安装方式。
//
// 与 RegisterAgentFacts 同一条规矩：要求描述符先到、不许两个人抢同一个客户端。
func (r *registry) RegisterAgentInstaller(agentID string, installer AgentInstaller) (modules.Release, error) {
	if installer == nil {
		return nil, fmt.Errorf("nil installer for agent %s", agentID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.agents[agentID]; !ok {
		return nil, fmt.Errorf("agent installer targets unknown agent %s", agentID)
	}
	if _, exists := r.installers[agentID]; exists {
		return nil, fmt.Errorf("agent %s already has an installer registered", agentID)
	}
	token := r.newToken("installer:" + agentID)
	r.installers[agentID] = installer
	return r.release("installer:"+agentID, token, func() {
		delete(r.installers, agentID)
	}), nil
}

// Installer 返回客户端的安装方式；没交返回 nil。
func (r *registry) Installer(id string) AgentInstaller {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.installers[id]
}

// RegisterAgentFacts 记下客户端模块交上来的运行时事实。
//
// 它要求那个 agent 已经登记过（同 BindTakeover 那条：先有描述符，再谈「它此刻
// 怎么样」）。重复交会报错——两个实现抢同一个客户端的事实，是我们无法替用户裁决
// 的一件事（同 RegisterStateField 的字段冲突）。
func (r *registry) RegisterAgentFacts(agentID string, facts AgentFacts) (modules.Release, error) {
	if facts == nil {
		return nil, fmt.Errorf("nil facts for agent %s", agentID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.agents[agentID]; !ok {
		return nil, fmt.Errorf("agent facts target unknown agent %s", agentID)
	}
	if _, exists := r.facts[agentID]; exists {
		return nil, fmt.Errorf("agent %s already has facts registered", agentID)
	}
	token := r.newToken("facts:" + agentID)
	r.facts[agentID] = facts
	return r.release("facts:"+agentID, token, func() {
		delete(r.facts, agentID)
	}), nil
}

// BindTakeover 把配置接管绑定到已存在客户端，避免产生无法启动的孤立配置插件。
func (r *registry) BindTakeover(agentID string, takeover ConfigTakeover) (modules.Release, error) {
	if takeover == nil {
		return nil, fmt.Errorf("nil config takeover for agent %s", agentID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	agent, ok := r.agents[agentID]
	if !ok {
		return nil, fmt.Errorf("config takeover targets unknown agent %s", agentID)
	}
	if agent.Config != nil {
		return nil, fmt.Errorf("agent %s has multiple config takeovers", agentID)
	}
	token := r.newToken("takeover:" + agentID)
	agent.Config = takeover
	return r.release("takeover:"+agentID, token, func() {
		agent.Config = nil
	}), nil
}

// RegisterStateField 为共享状态文件登记字段所有者，提前拒绝跨模块字段冲突。
func (r *registry) RegisterStateField(owner, name string) (modules.Release, error) {
	if name == "" {
		return nil, fmt.Errorf("state field name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if owner, exists := r.fields[name]; exists {
		return nil, fmt.Errorf("state field %s already registered by %s", name, owner)
	}
	token := r.newToken("field:" + name)
	r.fields[name] = owner
	return r.release("field:"+name, token, func() {
		delete(r.fields, name)
	}), nil
}

func (r *registry) newToken(key string) uint64 {
	r.next++
	r.tokens[key] = r.next
	return r.next
}

func (r *registry) release(key string, token uint64, remove func()) modules.Release {
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.tokens[key] != token {
			return nil
		}
		delete(r.tokens, key)
		remove()
		return nil
	}
}

// Get 返回描述符副本，防止只读消费者修改注册表持有的切片。
func (r *registry) Get(id string) (*Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	agent, ok := r.agents[id]
	if !ok {
		return nil, false
	}
	clone := *agent
	clone.Bin = append([]string(nil), agent.Bin...)
	clone.Slots = append([]Slot(nil), agent.Slots...)
	// Also 是切片里的切片：不深拷贝的话，克隆体与原件的 Also 指向同一块数组
	// （Bin / UnsetEnv 上面已经这么处理了，同一条理由）。
	for i := range clone.Slots {
		clone.Slots[i].Also = append([]string(nil), agent.Slots[i].Also...)
	}
	clone.UnsetEnv = append([]string(nil), agent.UnsetEnv...)
	return &clone, true
}

// Facts 返回客户端交上来的运行时事实；没交返回 nil。
func (r *registry) Facts(id string) AgentFacts {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.facts[id]
}

// Installed 报告这个客户端在不在**这台机器**上。
//
// 判据次序是这条的全部内容：**客户端自己的事实优先**（它有它的知识：工具可能不止
// 一个名字、可能不看 PATH），没交才用通用那条（在 PATH 上找 Bin 里的名字，排除
// 调用方给的 shim 目录）。反过来先查 PATH 的话，一个装在奇怪位置的客户端会被判成
// 没装，而它明明交了「我在」。
func (r *registry) Installed(id string) bool {
	r.mu.RLock()
	facts, agent := r.facts[id], r.agents[id]
	r.mu.RUnlock()
	return InstalledDefault(agent, facts)
}

// Names 返回稳定排序的客户端名，使 CLI 输出和测试结果可复现。
func (r *registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.agents))
	for name := range r.agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
