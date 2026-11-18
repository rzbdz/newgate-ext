package webdashboard

import (
	"regexp"
	"sync"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Registry of extension endpoints. Shape copied from view.Registry and porthub.Registry:
// one table, one monotonic token (undo only its own), duplicate names fail on the spot.
//
// Why **not** overwrite by name: with two modules both called `modellist`, the later one
// would starve the earlier of requests, while the earlier still has a dropdown in the UI
// that always 404s. This silent "half the feature is gone" is what assembly should refuse.

// extensionName restricts the character set: the name appears in a URL path segment, so
// `/`, `?`, `#` would let "the registered name" differ from "the name that gets requests".
var extensionName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

type extensionEntry struct {
	id  int
	run ExtensionHandler
}

type extensionRegistry struct {
	mu      sync.RWMutex
	next    int
	entries map[string]extensionEntry
}

func newExtensionRegistry() *extensionRegistry {
	return &extensionRegistry{entries: map[string]extensionEntry{}}
}

// Register takes in one extension endpoint.
func (r *extensionRegistry) Register(name string, h ExtensionHandler) (modules.Release, error) {
	if !extensionName.MatchString(name) {
		return nil, i18n.E("web: {name} is not a usable extension name — use lowercase letters, digits, '-' and '_'",
			i18n.A{"name": name})
	}
	if h == nil {
		return nil, i18n.E("web: {name} registered a nil extension handler — the interface would get a 500 for it",
			i18n.A{"name": name})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.entries[name]; dup {
		return nil, i18n.E("web: the extension {name} is already registered by another module — "+
			"only one of them would ever receive a request", i18n.A{"name": name})
	}
	e := extensionEntry{id: r.next, run: h}
	r.next++
	r.entries[name] = e
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		// Identify by token, not name: undoing a late-arriving row must not delete the row
		// that a later registrant pushed up (same reason as view.Registry.Register's undo).
		if cur, ok := r.entries[name]; ok && cur.id == e.id {
			delete(r.entries, name)
		}
		return nil
	}, nil
}

func (r *extensionRegistry) Lookup(name string) (ExtensionHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[name]
	return e.run, ok
}
