package upgrade

import (
	"regexp"
	"sort"
	"sync"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// actionID restricts the ID charset: it is both the command's positional arg and appears in
// output; allowing spaces and slashes would let "the registered name" and "the typed name" differ.
var actionID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

type entry struct {
	seq    int
	action Action
}

type registry struct {
	mu      sync.RWMutex
	next    int
	entries map[string]entry
}

func newRegistry() *registry {
	return &registry{entries: map[string]entry{}}
}

func (r *registry) Register(a Action) (modules.Release, error) {
	if !actionID.MatchString(a.ID) {
		return nil, i18n.E("upgrade: {id} is not a usable action id — use lowercase letters, digits, '.', '_' and '-'",
			i18n.A{"id": a.ID})
	}
	if a.Title == nil || a.Describe == nil || a.Run == nil {
		return nil, i18n.E("upgrade: action {id} must provide Title, Describe and Run",
			i18n.A{"id": a.ID})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.entries[a.ID]; dup {
		return nil, i18n.E("upgrade: action {id} is already registered by another module",
			i18n.A{"id": a.ID})
	}
	e := entry{seq: r.next, action: a}
	r.next++
	r.entries[a.ID] = e
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		// Identify by ordinal, not by ID: when the late undo arrives, do not delete the later one.
		if cur, ok := r.entries[a.ID]; ok && cur.seq == e.seq {
			delete(r.entries, a.ID)
		}
		return nil
	}, nil
}

// Actions returns the registered actions sorted by ID (stable output; two runs do not reorder).
func (r *registry) Actions() []Action {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.entries))
	for id := range r.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Action, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.entries[id].action)
	}
	return out
}
