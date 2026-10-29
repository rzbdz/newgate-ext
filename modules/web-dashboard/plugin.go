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

// 前端插件的登记表与发文件的那两条路由（契约见 api.go 的「前端插件」）。
// 形状照抄 extensionRegistry：一张表、单调序号、重名当场报错。

type pluginEntry struct {
	id     int
	plugin Plugin
	// version 是入口文件的内容哈希：清单里的地址带着它（`?v=`），换了一版二进制
	// 浏览器就一定拿新的那份——与 cacheControl 那条「入口每次都问」同一个坑。
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

// RegisterPlugin 收下一个插件。入口文件在登记时就读一遍：它不在、或者槽位拼错，
// 都是**作者**的错，该在装配时红，而不是在用户打开页面时少一块。
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

// pluginDoc 是 `/api/plugins` 里的一条。地址相对 `/ui/`（前端拼上 BASE_URL）。
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
	// 稳定的顺序：同一个槽位里谁先谁后不该每次刷新都换。
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// servePlugin 发 `/plugins/<名字>/<路径>`：只从那个插件自己的 Assets 里拿。
// 不落回 index.html（那是 SPA 兜底，对插件只会把一个拼错的 import 变成一段 HTML）。
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
	// 插件的文件名里没有内容哈希（只有入口地址带 ?v=），所以每次都带条件请求问一遍。
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}
