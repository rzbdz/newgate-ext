package webdashboard

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/store"
	"github.com/rzbdz/newgate/modules/gateway/metrics"
)

// Contract 是前端与 BFF 之间的契约版本。
//
// 前端**必须**先看它：不认识的版本号宁可白屏报一句「界面比后端旧/新」，也不要
// 拿旧前端去猜新形状——猜错的表现是「界面上少了一个开关」，没人会发现。
const Contract = 1

// secretKeys 是**绝不外发**的字段名。
//
// 为什么要显式列而不是「反正只挑我认识的字段」：原始文件视图是把整个文件发给
// 浏览器的（那是「硬核用户直接改配置源文件」那条 tab 的前提），而 providers.json
// 里的 api_key、state.json 里的 control_token 都是**凭据**。BFF 监听在 loopback
// 上，但 loopback 边界意味着**本机任何进程**都能读——凭据不该只靠这一点保护。
var secretKeys = map[string]bool{
	"api_key":       true,
	"control_token": true,
	"root_key":      true,
	"token":         true,
}

// Handler 是 BFF：一组 JSON 端点 + 静态资源。
//
// 它挂在 /ui 前缀下（mount 时 StripPrefix），所以这里的路径都以 / 开头。
type Handler struct {
	assets fs.FS
}

func NewHandler(assets fs.FS) *Handler { return &Handler{assets: assets} }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/snapshot":
		h.writeJSON(w, h.snapshot())
	case r.URL.Path == "/api/health":
		h.writeJSON(w, map[string]any{"ok": true, "contract": Contract})
	case r.URL.Path == "/api/commit":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, i18n.T("saving uses POST", nil), http.StatusMethodNotAllowed)
			return
		}
		h.commit(w, r)
	default:
		h.static(w, r)
	}
}

// ---------- 快照 ----------

type snapshotDoc struct {
	Contract       int              `json:"contract"`
	GeneratedAt    string           `json:"generated_at"`
	DefaultProfile string           `json:"default_profile"`
	Profiles       []profileDoc     `json:"profiles"`
	Providers      []providerDoc    `json:"providers"`
	Roles          []string         `json:"roles"`
	Metrics        []metricGroupDoc `json:"metrics"`
	Files          []fileDoc        `json:"files"`
	// Revisions 是**保存的基线**：路径 → 内容哈希（见 commit.go）。
	// 它是这个界面敢让用户点保存的前提——没有它，保存就是盲写。
	Revisions map[string]string `json:"revisions"`
	Takeover  map[string]bool   `json:"takeover,omitempty"`
	Notes     []string          `json:"notes,omitempty"`
}

type profileDoc struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Default     bool      `json:"default"`
	Pinned      bool      `json:"pinned,omitempty"`
	Excluded    bool      `json:"excluded,omitempty"`
	Tiers       []tierDoc `json:"tiers"`
}

type tierDoc struct {
	ID       string       `json:"id"`
	Bindings []bindingDoc `json:"bindings"`
}

type bindingDoc struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Ref      string `json:"ref,omitempty"`
}

type providerDoc struct {
	Name     string   `json:"name"`
	Protocol string   `json:"protocol,omitempty"`
	BaseURL  string   `json:"base_url"`
	KeyEnv   string   `json:"key_env,omitempty"`
	HasKey   bool     `json:"has_key"`
	Models   []string `json:"models"` // 从各 profile 的绑定里收集，给下拉框用
}

type metricGroupDoc struct {
	ID       string           `json:"id"`
	Label    string           `json:"label"`
	Counters []metricEntryDoc `json:"counters"`
}

type metricEntryDoc struct {
	Name  string `json:"name"`
	Value uint64 `json:"value"`
	Hint  string `json:"hint,omitempty"`
}

type fileDoc struct {
	Path       string `json:"path"`        // 相对配置根
	Language   string `json:"language"`    // json | kv
	Text       string `json:"text"`        // 有凭据的文件里，凭据已被替换成 ***
	HasSecrets bool   `json:"has_secrets"` // true = 只读（见 Handler 的说明）
	Editable   bool   `json:"editable"`
}

func (h *Handler) snapshot() snapshotDoc {
	snap, err := store.Load()
	doc := snapshotDoc{Contract: Contract, GeneratedAt: time.Now().Format(time.RFC3339)}
	if err != nil || snap == nil {
		doc.Notes = append(doc.Notes, i18n.T("the configuration could not be read", nil))
		return doc
	}
	doc.Roles = append([]string(nil), domain.Roles...)
	doc.DefaultProfile = snap.State.DefaultProfile
	doc.Takeover = map[string]bool{}
	for agent, want := range snap.State.Takeover {
		doc.Takeover[agent] = want
	}

	models := map[string]map[string]bool{}
	for _, p := range snap.Profiles {
		pd := profileDoc{
			Name: p.Name, Description: p.Description, Pinned: p.Pinned, Excluded: p.Excluded,
			Default: p.Name == snap.State.DefaultProfile,
		}
		ids := make([]string, 0, len(p.Roles))
		for id := range p.Roles {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			td := tierDoc{ID: id}
			for _, b := range p.Roles[id] {
				td.Bindings = append(td.Bindings, bindingDoc{Provider: b.Provider, Model: b.Model, Ref: b.Ref})
				if b.Provider != "" && b.Model != "" {
					if models[b.Provider] == nil {
						models[b.Provider] = map[string]bool{}
					}
					models[b.Provider][b.Model] = true
				}
			}
			pd.Tiers = append(pd.Tiers, td)
		}
		doc.Profiles = append(doc.Profiles, pd)
	}
	sort.Slice(doc.Profiles, func(i, j int) bool { return doc.Profiles[i].Name < doc.Profiles[j].Name })

	if snap.Providers != nil {
		names := make([]string, 0, len(snap.Providers.Providers))
		for name := range snap.Providers.Providers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			p := snap.Providers.Providers[name]
			seen := make([]string, 0, len(models[name]))
			for m := range models[name] {
				seen = append(seen, m)
			}
			sort.Strings(seen)
			doc.Providers = append(doc.Providers, providerDoc{
				Name: name, Protocol: p.Protocol, BaseURL: p.BaseURL,
				KeyEnv: p.APIKeyEnv, HasKey: p.APIKey != "" || p.APIKeyEnv != "",
				Models: seen,
			})
		}
	}

	doc.Metrics = metricGroups()
	doc.Files, doc.Revisions = h.configFiles()
	return doc
}

// metricGroups 把计数器按 Group 归拢：id 管排序与去重，label 只管印（同 CLI 的
// 约定，见 modules/gateway/metrics/hints.go）。
func metricGroups() []metricGroupDoc {
	// 快照只取一次：这是个原子替换出来的 map，取两次会拿到两份可能不同的时刻
	// （中间有请求在跑），同一组里的数就对不上了。
	snap := metrics.Default.Snapshot()
	byID := map[string]*metricGroupDoc{}
	for _, name := range metrics.SortedKeys(snap) {
		id, label := metrics.Group(name)
		g := byID[id]
		if g == nil {
			g = &metricGroupDoc{ID: id, Label: label}
			byID[id] = g
		}
		g.Counters = append(g.Counters, metricEntryDoc{
			Name: name, Value: snap[name], Hint: metrics.Hint(name),
		})
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]metricGroupDoc, 0, len(ids))
	for _, id := range ids {
		out = append(out, *byID[id])
	}
	return out
}

// configFiles 列出可编辑的配置源文件。
//
// 两个判据：
//   - 有凭据的文件**只读**——它的 text 已经脱敏，写回去会把 *** 落盘（那是数据
//     丢失，比不能编辑严重得多）；
//   - 其余文件（mappings/*.kv、*.json）可编辑，走 snapshot + 冲突检测那条路。
func (h *Handler) configFiles() ([]fileDoc, map[string]string) {
	var out []fileDoc
	revs := map[string]string{}
	add := func(path, language string) {
		b, err := os.ReadFile(path)
		if err != nil {
			return
		}
		rel, rerr := filepath.Rel(paths.Root(), path)
		if rerr != nil {
			rel = filepath.Base(path)
		}
		text, secrets := redact(string(b), language)
		out = append(out, fileDoc{
			Path: rel, Language: language, Text: text,
			HasSecrets: secrets, Editable: !secrets,
		})
		revs[rel] = revision(path)
	}
	add(paths.ProvidersFile(), "json")
	add(paths.StateFile(), "json")
	if ents, err := os.ReadDir(paths.Mappings()); err == nil {
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			switch {
			case strings.HasSuffix(name, ".kv"):
				add(filepath.Join(paths.Mappings(), name), "kv")
			case strings.HasSuffix(name, ".json"):
				add(filepath.Join(paths.Mappings(), name), "json")
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, revs
}

// redact 把凭据字段的值换成 ***，并报告这个文件里有没有凭据。
//
// JSON 走解析（保住结构），KV 走逐行（`key=value` 的语法在内核里，这里只需要认
// 得出「这一行的键是不是凭据」）。
func redact(text, language string) (string, bool) {
	if language == "json" {
		var doc map[string]any
		if json.Unmarshal([]byte(text), &doc) != nil {
			return text, false // 解析不了就原样给出去（只读视图，不写回）
		}
		if !scrub(doc) {
			return text, false
		}
		b, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return text, true
		}
		return string(b) + "\n", true
	}
	found := false
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		key, _, ok := strings.Cut(line, "=")
		if ok && secretKeys[strings.TrimSpace(key)] {
			lines[i] = key + "=***"
			found = true
		}
	}
	return strings.Join(lines, "\n"), found
}

// scrub 递归替换凭据字段，报告是否动过。
func scrub(v any) bool {
	touched := false
	switch node := v.(type) {
	case map[string]any:
		for k, val := range node {
			if secretKeys[k] {
				node[k] = "***"
				touched = true
				continue
			}
			if scrub(val) {
				touched = true
			}
		}
	case []any:
		for _, item := range node {
			if scrub(item) {
				touched = true
			}
		}
	}
	return touched
}

// ---------- 静态资源 ----------

// static 从嵌进二进制的产物里发文件；找不到的路径落回 index.html（前端自己
// 路由）。这样前端加一个页面不需要在后端登记路径。
func (h *Handler) static(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	if b, err := fs.ReadFile(h.assets, p); err == nil {
		w.Header().Set("Content-Type", contentType(p))
		_, _ = w.Write(b)
		return
	}
	// SPA 兜底：只有 GET/HEAD 且看起来不是资源请求时才回 index.html，
	// 否则一个拼错的 .js 会静默变成本页 HTML（浏览器报的错会指向别处）。
	if r.Method == http.MethodGet && !strings.Contains(filepath.Base(p), ".") {
		if b, err := fs.ReadFile(h.assets, "index.html"); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(b)
			return
		}
	}
	http.NotFound(w, r)
}

func contentType(p string) string {
	switch {
	case strings.HasSuffix(p, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(p, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(p, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(p, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(p, ".json"):
		return "application/json; charset=utf-8"
	case strings.HasSuffix(p, ".woff2"):
		return "font/woff2"
	}
	return "application/octet-stream"
}

func (h *Handler) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
