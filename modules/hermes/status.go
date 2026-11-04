package hermes

import (
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
)

// status 把 hermes 的接管状况报进 `newgate status`：/model 里列了几份 profile、
// 清单最近一次同步、以及网关要不要重启（理由见 gateway.go）。
//
// 由本模块报：这三件事都是 hermes 自己的知识，界面不认识它们。没装 hermes 的机器上
// 一个字都不报（说的是一个不存在的工具的行为）。
type status struct{}

var _ cliapi.StatusProvider = status{}

func (status) Status() []cliapi.StatusLine {
	if !(facts{}).Installed() {
		return nil
	}
	return []cliapi.StatusLine{{Rank: 27, Label: "Hermes", Value: statusText(true)}}
}

// statusText 是 status 那一行与 `newgate hermes-gateway` 共用的那句话。color=false 给
// 网页用（它自己上色）。
func statusText(color bool) string {
	paint := func(f func(string) string, s string) string {
		if color {
			return f(s)
		}
		return s
	}
	if !(Takeover{}).IsTakenOver(ConfigPath()) {
		return i18n.T("not taken over — newgate on hermes", nil)
	}
	in, err := currentInputs(proxyPort())
	if err != nil {
		return paint(style.Yellow, err.Error())
	}
	n := len(buildCatalog(in).Entries) - 1
	value := i18n.N("/model lists {n} newgate profile", "/model lists {n} newgate profiles", n, i18n.A{"n": n})
	if rec := loadRecord(); rec.SyncedAt > 0 {
		value += " · " + i18n.T("synced {at}", i18n.A{"at": time.Unix(rec.SyncedAt, 0).Format("01-02 15:04")})
	}
	if stale, err := staleCatalog(); err != nil {
		value += " " + paint(style.Yellow, i18n.T("(cannot check the model list: {err})", i18n.A{"err": err}))
	} else if stale {
		value += " " + paint(style.Yellow, i18n.T("(model list out of date — the daemon syncs it within seconds, or: newgate on hermes)", nil))
	}
	if restartPending() {
		value += " " + paint(style.Yellow, i18n.T("· the gateway started before the takeover: vision / web extract / approval still use the old upstream until newgate hermes-gateway restart", nil))
	}
	return value
}
