package selfupdate

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"sync"
	"time"

	"github.com/rzbdz/newgate-ext/lib/buildinfo"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	upgradeapi "github.com/rzbdz/newgate-ext/modules/upgrade"
	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
)

// This file is this module's face in the web UI: an "Update" button in the top bar
// that opens the current version, checks for a newer one, links to GitHub Releases.
//
// # It all lives here
//
// The front-end code is web/plugin.js (embedded in this module, served through
// web-dashboard's **plugin slot**); data and wording come from the extension endpoint
// below. No line in web-dashboard's front end knows self-update: it only gives the
// contract (see api.go's "front-end plugins"). Remove this module, the button goes too.
//
// # Check only, never install
//
// The web page never swaps a binary: the page itself is served by the daemon being
// replaced - clicking "install" swaps the floor under your feet; and the y/N confirm
// is the terminal's job. What this gives is **visibility**: newer? notes? which command?

//go:embed web
var webFiles embed.FS

const webName = "self-update"

// autoCheckEvery is the minimum gap between the page's own checks. A check touches
// GitHub (or git fetch) while the page asks status on every open - checking per page
// open spends the unauthenticated API's 60/hour (per egress IP) on everyone else.
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

// webState holds the last check's verdict (daemon memory, lost on restart - "check again").
type webState struct {
	mu   sync.Mutex
	last *checkDoc
	// check is swappable (tests never go to the network).
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

// webLabels is every sentence on the plugin. Wording lives in the backend: it belongs
// to this module, and the UI's catalog is not ours (same zh-Hans catalog as the CLI).
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
