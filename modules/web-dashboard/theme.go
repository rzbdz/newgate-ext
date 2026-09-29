package webdashboard

import (
	"encoding/json"
	"sort"
	"sync"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/store"
)

// 本文件是皮肤登记处的实现（形状见 api.go），外加「这一台机器选了哪一套」这件事。
//
// # 为什么选中哪一套是本模块的状态
//
// 与网关那几个开关同一条（见 core/modules/gateway/gatewaystate）：**谁的状态谁自己
// 托管**，住在 state.json 的 `ModuleConfig[Key]` 里，core 不认识这个键。"用哪套皮肤"
// 是这块界面自己的偏好，不是配置语义的一部分——把它抬进 domain.State 的话，内核就
// 得认识一个纯前端的概念。

// themeKey 是 state.json 的 ModuleConfig 里属于本模块的字段名。
const themeKey = "web-dashboard"

// themeState 是本模块在 state.json 里自己那一段。
//
// 一个字段也单开一个结构体：ModuleConfig 里存的是**整段原文**，往一个裸字符串里
// 塞值，下次想加第二个偏好（比如密度、字号）时就要做兼容读。
type themeState struct {
	// Theme 是选中的皮肤 ID。空 = 没选过，界面走出厂那套令牌
	// （也就是 app.css 的 `:root`，它自己跟着系统深浅走）。
	Theme string `json:"theme,omitempty"`
}

// loadThemeState 读本模块那一段。**永不失败**：没有这一段、或者 JSON 坏掉都当
// 「没选过」——读不出来就退回出厂那套，比拿一段读不懂的东西去改界面好。
func loadThemeState() themeState {
	var s themeState
	raw := store.LoadState().ModuleConfig[themeKey]
	if len(raw) == 0 {
		return s
	}
	_ = json.Unmarshal(raw, &s)
	return s
}

// selectedTheme 是这台机器选中的皮肤 ID（空 = 没选过）。
func selectedTheme() string { return loadThemeState().Theme }

// setSelectedTheme 记下选中的皮肤。
func setSelectedTheme(id string) error {
	raw, err := json.Marshal(themeState{Theme: id})
	if err != nil {
		return err
	}
	st := store.LoadState()
	if st.ModuleConfig == nil {
		st.ModuleConfig = map[string][]byte{}
	}
	st.ModuleConfig[themeKey] = raw
	return store.SaveState(st)
}

// themeRegistry 是登记进来的那几套皮肤（接口见 api.go 的 ThemeService）。
type themeRegistry struct {
	// 加锁而不是「注册期结束之后只读」：这个界面是**挂在共享端口上的**，而共享
	// 端口的 HTTP 服务在网关 Start 的时候就起来了——也就是说别的模块还在 Start
	// 里注册皮肤时，浏览器已经能打到 /api/themes 上。那个窗口很短，但它的表现是
	// 一次并发读写 map（Go 直接崩进程），不是一次难看的数据。
	mu   sync.RWMutex
	byID map[string]Theme
}

func newThemeRegistry() *themeRegistry {
	return &themeRegistry{byID: map[string]Theme{}}
}

var _ ThemeService = (*themeRegistry)(nil)

// RegisterTheme 见 api.go 的 ThemeService。
//
// 重复 ID 报错、而不是后到的覆盖先到的：一个界面上两套皮肤抢同一个身份时，
// 「哪一套赢了」取决于装配顺序（也就是目录名的字母序），而那件事没有人看得出来。
// 报出来的话，两个模块的作者各看一眼就知道该改谁。
func (r *themeRegistry) RegisterTheme(t Theme) (modules.Release, error) {
	if t.ID == "" {
		return nil, i18n.E("a theme needs an id", nil)
	}
	if t.Name == nil {
		return nil, i18n.E("theme {id} has no name", i18n.A{"id": t.ID})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byID[t.ID]; dup {
		return nil, i18n.E("theme {id} is already registered", i18n.A{"id": t.ID})
	}
	r.byID[t.ID] = t
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.byID, t.ID)
		return nil
	}, nil
}

// snapshot 是这一刻登记着的皮肤，按 ID 排（同一个进程跑两次得到同一个顺序）。
//
// 排序而不是保持注册序：注册序是**装配顺序**（目录名字母序），它没有含义，而界面
// 上那排按钮的先后每次都不一样的话，用户每次都要重新找一遍（与 OverviewAgent
// 按 id 排是同一条）。
func (r *themeRegistry) snapshot() []Theme {
	r.mu.RLock()
	out := make([]Theme, 0, len(r.byID))
	for _, t := range r.byID {
		out = append(out, t)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// has 报某个 ID 登记着没有（用来判断「存下来的那套还在不在」）。
func (r *themeRegistry) has(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.byID[id]
	return ok
}

// themeDoc 是 /api/themes 的报文。
type themeDoc struct {
	// Active 是此刻生效的皮肤 ID。**空 = 出厂那套令牌**（app.css 的 :root，
	// 它自己跟着系统深浅走）——它不是一个皮肤模块，所以不在下面的表里。
	Active string `json:"active"`
	// Themes 是登记着的皮肤。名字在这里就翻好（语言是后端解析出来的，前端再猜
	// 一遍就有了第二处，见 web/src/i18n.ts 的文件头）。
	Themes []themeItem `json:"themes"`
}

type themeItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Dark bool   `json:"dark"`
	CSS  string `json:"css"`
}

// themeDocOf 拼一份报文。存下来的那套**不在了**（模块被关掉、目录被删）时
// active 报空：界面于是落回出厂令牌，而不是抱着一个不存在的 ID 不动。
//
// 这里不悄悄把它改掉（不写回 state.json）：用户可能只是临时关掉了那个模块，
// 下次装回来它选的还是那一套。写回去就把这个选择**永久**抹掉了——而抹掉它的那
// 一下，用户什么都没点。
func themeDocOf(r *themeRegistry) themeDoc {
	active := selectedTheme()
	if active != "" && !r.has(active) {
		active = ""
	}
	doc := themeDoc{Active: active, Themes: make([]themeItem, 0, 4)}
	for _, t := range r.snapshot() {
		doc.Themes = append(doc.Themes, themeItem{
			ID: t.ID, Name: t.Name(), Dark: t.Dark, CSS: t.CSS,
		})
	}
	return doc
}
