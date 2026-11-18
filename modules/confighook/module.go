// Package confighook manages the runtime catalog of "which clients newgate can take over".
//
// Client modules register Agent descriptors, and config plugins then bind a reversible
// takeover for that Agent. runtime only needs to query and start clients, and
// cannot modify the catalog; therefore the one registry is split into the ConfigHooks
// write port and the AgentCatalog read port. This split is not formal interface
// segregation, but preventing the start-up path from gaining registration permission.
//
// Every write returns a token-bearing Release. An old component
// stopping can only revoke its own registration, and will not mistakenly
// delete what hot reload or a new application instance wrote later.
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
	// facts are the **runtime facts** client modules submit (see AgentFacts). Storing
	// them separately from agents is deliberate: the descriptor is "what this client
	// is", the facts are "how it is right now", and the latter may not be submitted.
	facts map[string]AgentFacts
	// installers are "how to install this tool" (see AgentInstaller). Separate from facts: a
	// client may submit only one of the two, and their uses differ too (one answers, one acts).
	installers map[string]AgentInstaller
	fields     map[string]string
	tokens     map[string]uint64
	next       uint64
}

var (
	_ ConfigHooks  = (*registry)(nil)
	_ AgentCatalog = (*registry)(nil)
)

// New creates a registry implementing both the write port and
// the read-only catalog port. The two capabilities share one source of
// facts, but restrict consumer permissions through different interfaces.
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
			// ui is a **weak dependency** (see component.Optional): with
			// the UI installed there is the `newgate agents` entry point and the
			// glossary line; without it, skip -- client descriptors work as usual.
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
			// The "each client" block in status: whoever owns the state reports it (see status.go).
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

// RegisterAgent atomically adds a client descriptor, and uses a
// token to keep a stale Release from deleting a later registration.
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

// RegisterAgentInstaller records the install method a client module submitted.
//
// Same rule as RegisterAgentFacts: the descriptor must be
// there first, and two people may not fight over one client.
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

// Installer returns the client's install method; nil when none was submitted.
func (r *registry) Installer(id string) AgentInstaller {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.installers[id]
}

// RegisterAgentFacts records the runtime facts a client module submitted.
//
// It requires that agent to be registered already (the same as BindTakeover: the
// descriptor first, then "how it is right now"). A duplicate submission errors
// -- two implementations fighting over one client's facts is something we cannot
// arbitrate for the user (same as a field conflict in RegisterStateField).
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

// BindTakeover binds config takeover to an existing client,
// avoiding an orphan config plugin that cannot start.
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

// RegisterStateField registers a field owner for the shared
// state file, rejecting cross-module field conflicts up front.
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

// Get returns a copy of the descriptor, preventing read-only
// consumers from modifying the slice the registry holds.
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
	// Also is a slice within a slice: without a deep copy, the clone's and the original's Also
	// point at the same array (Bin / UnsetEnv are already handled this way above, same reason).
	for i := range clone.Slots {
		clone.Slots[i].Also = append([]string(nil), agent.Slots[i].Also...)
	}
	clone.UnsetEnv = append([]string(nil), agent.UnsetEnv...)
	return &clone, true
}

// Facts returns the runtime facts the client submitted; nil when none.
func (r *registry) Facts(id string) AgentFacts {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.facts[id]
}

// Installed reports whether this client is on **this machine**.
//
// The order of criteria is the whole content of this: **the client's own facts win** (it
// has its knowledge: the tool may have more than one name, may not look at PATH), and
// only without them is the generic one used (look for the name in Bin on PATH, excluding
// the shim directory the caller gives). Checking PATH first would judge a client
// installed in an odd place as not installed, while it plainly submitted "I am here".
func (r *registry) Installed(id string) bool {
	r.mu.RLock()
	facts, agent := r.facts[id], r.agents[id]
	r.mu.RUnlock()
	return InstalledDefault(agent, facts)
}

// Names returns the client names in stable order, making CLI output and test results reproducible.
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
