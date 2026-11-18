package pluginmanager

import (
	"strings"
	"sync"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// reported is one module's self-report. Classification (Type) is not here - it follows
// the component definition on component.Component; Modules() merges the two (see there).
type reported struct {
	name     string
	switches []Switch
}

// service holds the ledger: self-reported switch points (byName) plus the component
// graph handed in by the composition root (catalog).
//
// The ledger uses component.Registry[T], not a hand-written table: usable at zero
// value, monotonic tokens, exact undo by token, check under the write lock (docs/09).
type service struct {
	mu      sync.Mutex
	byName  modules.Registry[reported]
	catalog []modules.Component
}

var _ Manager = (*service)(nil)

// RegisterSelf, see Manager. The only entry for a module to **voluntarily** join switches.
func (s *service) RegisterSelf(name string, switches []Switch) (modules.Release, error) {
	if name == "" {
		return nil, i18n.E("plugin-manager: module name must not be empty", nil)
	}
	// First validate this entry on its own (nothing else needed), then dedup.
	if err := validateSwitches(name, switches); err != nil {
		return nil, err
	}
	return s.byName.Register(reported{name: name, switches: switches}, func(existing []reported) error {
		for _, e := range existing {
			if e.name == name {
				return i18n.E(
					"plugin-manager: module \"{name}\" already reported (each module reports once)",
					i18n.A{"name": name})
			}
		}
		for _, e := range existing {
			for _, have := range e.switches {
				for _, want := range switches {
					if have.Path == want.Path {
						return i18n.E(
							"plugin-manager: switch point \"{path}\" is already taken by module \"{module}\"",
							i18n.A{"path": want.Path, "module": e.name})
					}
				}
			}
		}
		return nil
	})
}

// validateSwitches validates one switch point on its own, not looking at others.
func validateSwitches(name string, switches []Switch) error {
	for _, sw := range switches {
		if sw.Path == "" {
			return i18n.E(
				"plugin-manager: module \"{name}\" reported a switch point with an empty Path",
				i18n.A{"name": name})
		}
		// Prefix rule: the path hangs under the module name, so "whose" needs no lookup.
		if !strings.HasPrefix(sw.Path, name+".") {
			return i18n.E("plugin-manager: switch point \"{path}\" must start with the "+
				"module name \"{name}\" plus a dot ({name}.<path>)",
				i18n.A{"path": sw.Path, "name": name})
		}
		if sw.Title == "" || sw.Why == "" {
			return i18n.E("plugin-manager: switch point \"{path}\" must fill in Title "+
				"(plain words) and Why (what turning it off does)", i18n.A{"path": sw.Path})
		}
		switch sw.Danger {
		case DangerSafe, DangerQuirk:
		case DangerFootgun:
			// "No indefinite footgun" is a structural guarantee here, not discipline:
			// a correctness-breaking switch cannot be registered in a "forever on" shape.
			if sw.TTL <= 0 {
				return i18n.E("plugin-manager: switch point \"{path}\" is a footgun, "+
					"it must carry a default time limit (TTL > 0)", i18n.A{"path": sw.Path})
			}
		default:
			return i18n.E("plugin-manager: unknown Danger \"{danger}\" on switch point "+
				"\"{path}\" (safe/quirk/footgun)",
				i18n.A{"path": sw.Path, "danger": sw.Danger})
		}
	}
	return nil
}

// Lookup finds a switch point by Path; the CLI uses it to pick the Off or On table.
func (s *service) Lookup(path string) (Switch, bool) {
	for _, r := range s.byName.All() {
		for _, sw := range r.switches {
			if sw.Path == path {
				return sw, true
			}
		}
	}
	return Switch{}, false
}

// SetCatalog is called once by the kernel after assembly (component.CatalogAware),
// see "how the list gets in" next to the Manager interface. **Only the kernel should
// call it** - ordinary modules contribute facts via RegisterSelf.
//
// Boundary: this loads the list only, registering nothing (each module's Start does).
func (s *service) SetCatalog(components []modules.Component) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.catalog = append([]modules.Component(nil), components...)
}

// Modules merges the two sources and returns them in component start order.
//
// **The enumeration source is the component graph, not the registry** - on purpose: a
// module without RegisterSelf must still be listed, or "newgate plugin lists all
// plugins" is a lie, and "no switch" and "forgot to register" look the same.
func (s *service) Modules() []Module {
	s.mu.Lock()
	catalog := append([]modules.Component(nil), s.catalog...)
	s.mu.Unlock()

	byName := map[string][]Switch{}
	for _, r := range s.byName.All() {
		byName[r.name] = r.switches
	}

	out := make([]Module, 0, len(catalog))
	seen := map[string]bool{}
	for _, c := range catalog {
		out = append(out, Module{
			Name: c.Name, Type: c.Type, Desc: descOf(c), Switches: byName[c.Name],
		})
		seen[c.Name] = true
	}
	// Reported but not in the graph: the root has not handed it over yet, or the module
	// name differs from the component name. The latter is a config error - list it
	// (under others), never swallow it; silence is what this feature exists to kill.
	var orphans []Module
	for _, r := range s.byName.All() {
		if seen[r.name] {
			continue
		}
		orphans = append(orphans, Module{Name: r.name, Type: TypeOthers, Switches: r.switches})
	}
	return append(out, orphans...)
}

// descOf pulls out the sentence the module wrote; empty if none.
//
// The **timing** of evaluation is here: Modules() is called when the UI/CLI takes a
// snapshot, and the language layer is long installed by then (only after assembly
// does a UI ask). Desc is a closure, not a string, to push evaluation here.
func descOf(c modules.Component) string {
	if c.Desc == nil {
		return ""
	}
	return c.Desc()
}
