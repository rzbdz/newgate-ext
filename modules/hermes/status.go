package hermes

import (
	"time"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// status reports hermes' takeover state into `newgate status`: how many profiles are listed in
// /model, when the list was last synced, and whether the gateway needs a restart (the reason is
// in gateway.go).
//
// Reported by this module: all three are hermes' own knowledge, and the UI knows none of it. On
// a machine without hermes it says nothing at all (that would be describing the behavior of a
// tool that does not exist).
type status struct{}

var _ cliapi.StatusProvider = status{}

func (status) Status() []cliapi.StatusLine {
	if !(facts{}).Installed() {
		return nil
	}
	return []cliapi.StatusLine{{Rank: 27, Label: "Hermes", Value: statusText(true)}}
}

// statusText is the sentence shared by the status line and `newgate hermes-gateway`.
// color=false is for the web page (it colors on its own).
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
