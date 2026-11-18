package webdashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// The front-end plugin registry and the two routes that serve its files (contract in api.go).
// Shape copied from extensionRegistry: one table, monotonic token, duplicate names fail.

type pluginEntry struct {
	id     int
	plugin Plugin
	// version is the entry file's content hash: manifest addresses carry it (`?v=`), so a new
	// binary always gets the new copy - the same pit as cacheControl's "always ask" for entry.
	version string
}

type pluginRegistry struct {
	mu      sync.RWMutex
	next    int
	entries map[string]pluginEntry
}

func newPluginRegistry() *pluginRegistry {
	return &pluginRegistry{entries: map[string]pluginEntry{}}
}

// RegisterPlugin takes in a plugin. The entry file is read at registration: missing, or a
// misspelled slot, is the **author's** error - red at assembly, not gone for a user.
func (r *pluginRegistry) RegisterPlugin(p Plugin) (modules.Release, error) {
	if !extensionName.MatchString(p.Name) {
		return nil, i18n.E("web: {name} is not a usable plugin name — use lowercase letters, digits, '-' and '_'",
			i18n.A{"name": p.Name})
	}
	if p.Assets == nil || p.Entry == "" {
		return nil, i18n.E("web: plugin {name} has no assets or no entry file", i18n.A{"name": p.Name})
	}
	entry, err := fs.ReadFile(p.Assets, p.Entry)
	if err != nil {
		return nil, i18n.Ef(err, "web: plugin {name} cannot read its entry {entry}: {err}",
			i18n.A{"name": p.Name, "entry": p.Entry})
	}
	if len(p.Slots) == 0 {
		return nil, i18n.E("web: plugin {name} fills no slot, so it would never appear", i18n.A{"name": p.Name})
	}
	for _, s := range p.Slots {
		if !pluginSlots[s] {
			return nil, i18n.E("web: plugin {name} asks for the slot {slot}, which this interface does not have",
				i18n.A{"name": p.Name, "slot": string(s)})
		}
	}
	sum := sha256.Sum256(entry)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.entries[p.Name]; dup {
		return nil, i18n.E("web: the plugin {name} is already registered by another module", i18n.A{"name": p.Name})
	}
	e := pluginEntry{id: r.next, plugin: p, version: hex.EncodeToString(sum[:6])}
	r.next++
	r.entries[p.Name] = e
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if cur, ok := r.entries[p.Name]; ok && cur.id == e.id {
			delete(r.entries, p.Name)
		}
		return nil
	}, nil
}

// pluginDoc is one `/api/plugins` entry. Addresses are relative to `/ui/` (BASE_URL is added).
type pluginDoc struct {
	Name  string       `json:"name"`
	Entry string       `json:"entry"`
	Slots []PluginSlot `json:"slots"`
}

func (r *pluginRegistry) manifest() []pluginDoc {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]pluginDoc, 0, len(r.entries))
	for name, e := range r.entries {
		out = append(out, pluginDoc{
			Name:  name,
			Entry: "plugins/" + name + "/" + e.plugin.Entry + "?v=" + e.version,
			Slots: e.plugin.Slots,
		})
	}
	// Stable order: who comes first in a slot must not change on every refresh.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// servePlugin serves `/plugins/<name>/<path>`: only from that plugin's own Assets.
// no fallback to index.html (SPA only; for a plugin it turns a bad import into HTML).
func (r *pluginRegistry) servePlugin(w http.ResponseWriter, req *http.Request) {
	rest := strings.TrimPrefix(req.URL.Path, "/plugins/")
	name, file, ok := strings.Cut(rest, "/")
	if !ok || file == "" || !fs.ValidPath(file) || path.Clean(file) != file {
		http.NotFound(w, req)
		return
	}
	r.mu.RLock()
	e, found := r.entries[name]
	r.mu.RUnlock()
	if !found {
		http.NotFound(w, req)
		return
	}
	b, err := fs.ReadFile(e.plugin.Assets, file)
	if err != nil {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", contentType(file))
	// Plugin filenames carry no content hash (only the entry has ?v=), so ask with a condition.
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}
