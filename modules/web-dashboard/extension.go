package webdashboard

import (
	"regexp"
	"sync"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// 扩展端点的登记表。形状照抄 view.Registry 与 porthub.Registry：一张表、一个
// 单调序号（撤销只撤自己那条）、重名当场报错。
//
// 为什么**不**按名字覆盖：两个模块都叫 `modellist` 时，后登记的那一个会让前一个
// 永远收不到请求，而前一个在界面上仍然有一个下拉、点开却总是 404。这种静默的
// 「一半功能消失」正是装配期该拒绝的东西。

// extensionName 限定名字的字符集：它会出现在 URL 路径段里，放 `/`、`?`、`#`
// 进去等于让「登记的名字」与「收到请求的名字」可以不一致。
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

// Register 收下一个扩展端点。
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
		// 按序号认人，不按名字：撤销晚到的那一条时，不能把后来者顶上去的那一条
		// 删掉（与 view.Registry.Register 的撤销同一条理由）。
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
