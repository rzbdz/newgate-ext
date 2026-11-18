// Package roleprov aggregates **dynamic role keys contributed by modules**.
//
// Why this layer exists: the tiers (heavy/normal/mid/light/vision) are the framework's
// capability ladder, shared by every client; but client plugins often bring their own
// finer slot systems -- opencode's oh-my-openagent has a dozen intra-agents, sisyphus
// / librarian / ..., and other plugins will have others later. What these keys are
// **called, how many there are, and who they follow by default** is that module's
// knowledge, and should not be hard-coded into core (docs/04-configuration.md).
//
// The split:
//
//	a module implements RoleProvider and registers it via the Config component's capability;
//	the framework calls Refresh on each snapshot install and CLI start, pouring into domain.
//
// core recognizes only the "key -> default binding" table, treating it like built-in aliases.
package roleprov

import (
	"fmt"
	"sort"
	"sync"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/lib/i18n"
)

// RoleProvider lets modules contribute dynamic tiers to the
// config layer, keeping the origin for conflict diagnostics.
//
// The contract is defined here rather than in the config root: the role registry's
// implementation is this package, and only with the definition here can the config root
// import this package without a cycle (see the layering in docs/03-architecture.md).
type RoleProvider interface {
	Source() string
	Roles() ([]domain.ExtraRole, error)
}

// RoleWatchProvider is RoleProvider's optional capability:
// declaring which files need a reload after they change.
type RoleWatchProvider interface {
	WatchFiles() []string
}

// Registry aggregates dynamic tier providers, and uses
// tokens to maintain precise ownership of each registration.
type Registry struct {
	mu        sync.RWMutex
	providers []RoleProvider
	tokens    map[string]uint64
	next      uint64
}

var (
	defaultMu       sync.RWMutex
	defaultRegistry = &Registry{}
)

// NewRegistry creates an isolated registry, owned independently by each Config component instance.
func NewRegistry() *Registry { return &Registry{} }

// SetDefault sets the default registry used by the old
// data plane; new assemblies should use InstallDefault.
func SetDefault(registry *Registry) {
	if registry == nil {
		panic("roleprov: nil default registry")
	}
	defaultMu.Lock()
	defaultRegistry = registry
	defaultMu.Unlock()
}

// InstallDefault takes over the compatibility global for now, and
// returns a restore function that will not overwrite a new owner.
func InstallDefault(registry *Registry) func() {
	if registry == nil {
		panic("roleprov: nil default registry")
	}
	defaultMu.Lock()
	previous := defaultRegistry
	defaultRegistry = registry
	defaultMu.Unlock()
	return func() {
		defaultMu.Lock()
		if defaultRegistry == registry {
			defaultRegistry = previous
		}
		defaultMu.Unlock()
	}
}

func currentRegistry() *Registry {
	defaultMu.RLock()
	registry := defaultRegistry
	defaultMu.RUnlock()
	return registry
}

// Register is the compatibility entry point for the old data plane;
// production assemblies register through the Config capability.
func Register(p RoleProvider) {
	if _, err := currentRegistry().Register(p); err != nil {
		panic(err)
	}
}

// Register adds a dynamic tier provider with a unique source, and returns the ownership Release.
func (r *Registry) Register(p RoleProvider) (modules.Release, error) {
	if p == nil || p.Source() == "" {
		return nil, fmt.Errorf("roleprov: provider source is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, q := range r.providers {
		if q.Source() == p.Source() {
			return nil, fmt.Errorf("roleprov: duplicate provider %s", p.Source())
		}
	}
	if r.tokens == nil {
		r.tokens = make(map[string]uint64)
	}
	r.next++
	token := r.next
	// tokens this table is **not** for tolerating duplicate registration (that was already
	// rejected above), it is to make **a stale Release harmless**: registering the same key
	// again after an unregister gets a new token, and that old Release arriving late would delete
	// the new registration. Comparing tokens blocks exactly that case. (Reading the code it is
	// easy to think this branch is unreachable -- that misses "unregister then register again".)
	r.tokens[p.Source()] = token
	r.providers = append(r.providers, p)
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.tokens[p.Source()] != token {
			return nil
		}
		delete(r.tokens, p.Source())
		for i, provider := range r.providers {
			if provider.Source() == p.Source() {
				r.providers = append(r.providers[:i], r.providers[i+1:]...)
				break
			}
		}
		return nil
	}, nil
}

// Refresh asks every registered provider again and pours the result into domain.
//
// Call sites: store.Load (on every config snapshot install) -- role
// keys are part of the config, and changing omo-slots.json should take
// effect as automatically as changing providers.json, with no restart.
//
// One provider failing to read its file does not affect the others: record an
// error and continue (fail-open); that module's keys degrade to "not registered",
// and the user sees a queryable error rather than everything stalling.
func Refresh() []error {
	registry := currentRegistry()
	registry.mu.RLock()
	ps := append([]RoleProvider(nil), registry.providers...)
	registry.mu.RUnlock()

	var (
		out  []domain.ExtraRole
		errs []error
		seen = map[string]string{} // key -> who registered it
	)
	for _, p := range ps {
		roles, err := p.Roles()
		if err != nil {
			errs = append(errs, i18n.Ef(err, "cannot read the role keys of module {module}: {err}",
				i18n.A{"module": p.Source()}))
			continue
		}
		for _, r := range roles {
			if r.Key == "" {
				continue
			}
			// The ladder tiers belong to the framework, and modules may not override them -- that would
			// make "what heavy means" depend on which plugin is installed, leaving no way in when debugging.
			if domain.IsRole(r.Key) {
				errs = append(errs, i18n.E("module {module} tried to register {key}, which is a built-in role name; ignored",
					i18n.A{"module": p.Source(), "key": r.Key}))
				continue
			}
			if who, dup := seen[r.Key]; dup {
				errs = append(errs, i18n.E("key {key} is registered by both {first} and {second}; keeping the first",
					i18n.A{"key": r.Key, "first": who, "second": p.Source()}))
				continue
			}
			seen[r.Key] = p.Source()
			if r.Source == "" {
				r.Source = p.Source()
			}
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	domain.SetExtraRoles(out)
	return errs
}

// Sources the module short names already registered (for doctor / `newgate omo`).
func Sources() []string {
	registry := currentRegistry()
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	out := make([]string, 0, len(registry.providers))
	for _, p := range registry.providers {
		out = append(out, p.Source())
	}
	sort.Strings(out)
	return out
}

// WatchFiles aggregates every provider file that supports hot reload, sorted stably.
func WatchFiles() []string {
	registry := currentRegistry()
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	var files []string
	for _, provider := range registry.providers {
		if watcher, ok := provider.(RoleWatchProvider); ok {
			files = append(files, watcher.WatchFiles()...)
		}
	}
	sort.Strings(files)
	return files
}
