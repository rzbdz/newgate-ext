package opencodeomo

import (
	"os"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
)

type diagnostics struct{}

var _ cliapi.DiagnosticProvider = (*diagnostics)(nil)

func (diagnostics) Diagnostics() []cliapi.Diagnostic {
	reg := ReadOmoSlots()
	if reg == nil {
		return []cliapi.Diagnostic{{
			Label: i18n.T("slot", nil), State: "skip",
			Line: i18n.T("no registry (oh-my-openagent has never been taken over)", nil),
		}}
	}
	keys := 0
	for _, slot := range reg.Slots {
		if slot.Key != "" {
			keys++
		}
	}
	if keys == 0 {
		return []cliapi.Diagnostic{{
			Label: i18n.T("slot", nil), State: "skip", Line: i18n.T("the registry is empty", nil),
		}}
	}
	if fi, err := os.Stat(SlotsFile()); err == nil && fi.Mode().Perm()&0o040 == 0 {
		return []cliapi.Diagnostic{{
			Label: i18n.T("slot", nil), State: "warn",
			Line: i18n.N("{n} slot key, but the registry is not group-readable (the user running the daemon cannot see it)",
				"{n} slot keys, but the registry is not group-readable (the user running the daemon cannot see it)",
				keys, i18n.A{"n": keys}),
			Details: []string{"chmod 0660 " + SlotsFile()},
		}}
	}
	item := cliapi.Diagnostic{
		Label: i18n.T("slot", nil), State: "ok",
		Line: i18n.N("{n} slot key · mode {mode}", "{n} slot keys · mode {mode}",
			keys, i18n.A{"n": keys, "mode": modeName(reg)}),
	}
	if n := diffCount(reg); n > 0 {
		item.Details = append(item.Details,
			i18n.N("{n} key has a suggested tier (see newgate omo ls)",
				"{n} keys have a suggested tier (see newgate omo ls)", n, i18n.A{"n": n}))
	}
	return []cliapi.Diagnostic{item}
}

func modeName(reg *OmoSlots) string {
	if reg != nil && reg.Mode == "suggested" {
		return "suggested"
	}
	return "current"
}

func diffCount(reg *OmoSlots) int {
	if reg == nil {
		return 0
	}
	n := 0
	for _, slot := range reg.Slots {
		if _, overridden := reg.Overrides[slot.Key]; overridden {
			continue
		}
		if slot.Suggested != "" && slot.Suggested != slot.Current {
			n++
		}
	}
	return n
}
