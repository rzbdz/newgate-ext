package selfupdate

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"sync"
	"time"

	"github.com/rzbdz/newgate/lib/buildinfo"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	upgradeapi "github.com/rzbdz/newgate-ext/modules/upgrade"
	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
)

// 本文件是本模块在网页界面上的那一面：顶栏一颗「更新」按钮，点开能看当前版本、
// 检查有没有新的、跳到 GitHub 的 Release 页。
//
// # 它全部住在这里
//
// 前端代码是 web/plugin.js（嵌进本模块，经 web-dashboard 的**插件口**发出去），
// 数据与文案是下面这个扩展端点。web-dashboard 的前端目录里没有一行认识
// self-update：它只提供「顶栏有一格」与「插件长什么样」这份契约（见那边 api.go
// 的「前端插件」）。摘掉本模块，那颗按钮跟着消失，界面一个字不用改。
//
// # 只查、不装
//
// 网页上不换二进制：页面本身就由要被换掉的那个 daemon 服务着，在里面点一下
// 「安装」，换的是脚下的地板；而且「问一句 y/N」那道确认是终端的事（见 upgrade
// 的命令）。这里给的是**看得见**：有没有新的、去哪儿看发布说明、用哪条命令装。

//go:embed web
var webFiles embed.FS

const webName = "self-update"

// autoCheckEvery 是页面自己去查一次的最短间隔。查一次要碰 GitHub（或者 git fetch），
// 而页面每次打开都会问 status——按页面打开次数查，未认证 API 那每小时 60 次的
// 额度（按出口 IP 算）很快就是别人的了。
const autoCheckEvery = 6 * time.Hour

type checkDoc struct {
	Message   string    `json:"message"`
	Available bool      `json:"available"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	Ago       string    `json:"ago"`
}

type statusDoc struct {
	Version     string            `json:"version"`
	Mode        string            `json:"mode"`
	Source      string            `json:"source"`
	ReleasesURL string            `json:"releases_url,omitempty"`
	Last        *checkDoc         `json:"last,omitempty"`
	CheckDue    bool              `json:"check_due"`
	Labels      map[string]string `json:"labels"`
}

// webState 记最近一次检查的结论（daemon 内存里，重启就忘：那正是「该再查一次」）。
type webState struct {
	mu   sync.Mutex
	last *checkDoc
	// check 可以换掉（测试不出网）。
	check func(ctx context.Context) (upgradeapi.Result, error)
}

func newWebState() *webState {
	return &webState{check: func(ctx context.Context) (upgradeapi.Result, error) {
		u := newUpdater()
		u.out, u.buildLog = io.Discard, io.Discard
		return u.run(ctx, upgradeapi.Request{Check: true})
	}}
}

func (w *webState) handle(req webapi.ExtensionRequest) (any, error) {
	var body struct {
		Action string `json:"action"`
	}
	if len(req.Body) > 0 {
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return nil, i18n.Ef(err, "cannot read the request: {err}", nil)
		}
	}
	switch body.Action {
	case "", "status":
	case "check":
		ctx, cancel := context.WithTimeout(req.Context, 2*time.Minute)
		res, err := w.check(ctx)
		cancel()
		doc := &checkDoc{Message: res.Message, Available: err == nil && !res.UpToDate, CheckedAt: time.Now()}
		if err != nil {
			doc.Error = err.Error()
		}
		w.mu.Lock()
		w.last = doc
		w.mu.Unlock()
	default:
		return nil, i18n.E("unknown action {action}", i18n.A{"action": body.Action})
	}
	return w.status(), nil
}

func (w *webState) status() statusDoc {
	st, _ := ensureSettings()
	doc := statusDoc{Version: buildinfo.Version(), Mode: st.Mode, Labels: webLabels()}
	if repo := st.github(); repo != "" {
		doc.ReleasesURL = "https://github.com/" + repo + "/releases"
	}
	if st.Mode == modeGit {
		doc.Source = i18n.T("built from {repo}", i18n.A{"repo": st.Repo})
	} else {
		doc.Source = i18n.T("GitHub releases of {repo}", i18n.A{"repo": st.github()})
	}
	w.mu.Lock()
	if w.last != nil {
		last := *w.last
		last.Ago = i18n.T("checked {ago} ago", i18n.A{"ago": time.Since(last.CheckedAt).Round(time.Minute).String()})
		if time.Since(last.CheckedAt) < time.Minute {
			last.Ago = i18n.T("checked just now", nil)
		}
		doc.Last = &last
	}
	doc.CheckDue = w.last == nil || time.Since(w.last.CheckedAt) > autoCheckEvery
	w.mu.Unlock()
	return doc
}

// webLabels 是插件上的每一句话。文案在后端，是因为它们属于本模块：前端的消息
// 目录是界面的，插件里写死英文又等于不翻译（与 CLI 走同一份 zh-Hans 目录）。
func webLabels() map[string]string {
	return map[string]string{
		"button":    i18n.T("Updates", nil),
		"title":     i18n.T("newgate version", nil),
		"check":     i18n.T("Check for updates", nil),
		"checking":  i18n.T("Checking…", nil),
		"releases":  i18n.T("GitHub releases", nil),
		"available": i18n.T("An update is available", nil),
		"current":   i18n.T("Up to date", nil),
		"failed":    i18n.T("The check failed", nil),
		"never":     i18n.T("Not checked yet", nil),
		"install":   i18n.T("Install it from a terminal with newgate upgrade (it asks before replacing anything).", nil),
	}
}

func webAssets() fs.FS {
	sub, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err) // 嵌入的目录在编译期就定了，这里失败是构建坏了
	}
	return sub
}
