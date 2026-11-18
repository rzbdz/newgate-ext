package locale

import (
	"fmt"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Row order: in status it comes after "patch switches" (40); in doctor it comes last (the checkup's
// closing item is meta-information like "is this machine reading the right language"). The values
// use the same scale as other modules' Rank: the constants in modules/{gateway,runtime,config}.
const (
	rankStatusLocale = 50
	rankCheckLocale  = 60
)

// Status contributes one line to `newgate status`: the current language + coverage.
func (c *command) Status() []cliapi.StatusLine {
	value := c.svc.lang
	if info, ok := c.currentInfo(); ok && info.Total > 0 {
		value += fmt.Sprintf("  %d%%", info.Translated*100/info.Total)
	}
	return []cliapi.StatusLine{{Rank: rankStatusLocale, Label: i18n.T("Language", nil), Value: value}}
}

// Diagnostics contributes one item to doctor: whether everything is translated, how many
// machine-translated entries await review, and how many keys come from disk overrides.
//
// Why it is worth a line: with localization added, there is only one cause of "the UI is half
// English, half Chinese" -- missing translations. That must be visible at a glance in the checkup,
// not left for the user to guess "is it just not translated".
func (c *command) Diagnostics() []cliapi.Diagnostic {
	info, ok := c.currentInfo()
	d := cliapi.Diagnostic{Rank: rankCheckLocale, Label: i18n.T("Language", nil)}
	if !ok {
		d.State = "skip"
		d.Line = i18n.T("No catalog for {lang}", i18n.A{"lang": c.svc.lang})
		return []cliapi.Diagnostic{d}
	}
	missing := info.Total - info.Translated
	switch {
	case info.Language == i18n.SourceLang:
		d.State = "ok"
		d.Line = i18n.T("{lang} (source language) · {total} messages", i18n.A{"lang": info.Language, "total": info.Total})
	case missing > 0 || info.Machine > 0:
		d.State = "warn"
		d.Line = i18n.T("{lang} · {pct}% translated ({done}/{total}), {machine} machine, {missing} missing",
			i18n.A{"lang": info.Language, "pct": pctOf(info), "done": info.Translated,
				"total": info.Total, "machine": info.Machine, "missing": missing})
	default:
		d.State = "ok"
		d.Line = i18n.T("{lang} · {total} messages, all translated", i18n.A{"lang": info.Language, "total": info.Total})
	}
	if c.svc.requested != "" && c.svc.requested != info.Language {
		d.Details = append(d.Details, i18n.T("Requested {want}, using {got} (no catalog for it)",
			i18n.A{"want": c.svc.requested, "got": info.Language}))
	}
	if n := i18n.Overlaid(); n > 0 {
		d.Details = append(d.Details, i18n.N("{n} message comes from the config dir",
			"{n} messages come from the config dir", n, i18n.A{"n": n}))
	}
	if missing > 0 {
		// The missing ones are listed as **source text**: they are language-independent identities,
		// and anyone can fill them in. Only the first few -- doctor is a checkup, not a ledger; the
		// full set is in `newgate lang` and tools/i18n.
		for i, msg := range i18n.Missing(info.Language) {
			if i == 5 {
				d.Details = append(d.Details, i18n.N("… and {n} more", "… and {n} more", missing-5, i18n.A{"n": missing - 5}))
				break
			}
			d.Details = append(d.Details, "  "+msg)
		}
	}
	return []cliapi.Diagnostic{d}
}

// Glossary leaves one line in the help screen's glossary: the language thing itself deserves a
// sentence of explanation.
func (c *command) Glossary() []cliapi.GlossaryLine {
	return []cliapi.GlossaryLine{{
		Rank:       40,
		Term:       i18n.T("language", nil),
		Definition: i18n.T("which message catalog the interface reads — see `newgate lang`", nil),
	}}
}

func (c *command) currentInfo() (i18n.Info, bool) {
	for _, info := range i18n.Available() {
		if info.Language == c.svc.lang {
			return info, true
		}
	}
	return i18n.Info{}, false
}

func pctOf(info i18n.Info) int {
	if info.Total == 0 {
		return 100
	}
	return info.Translated * 100 / info.Total
}
