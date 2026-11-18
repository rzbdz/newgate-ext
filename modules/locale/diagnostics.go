package locale

import (
	"fmt"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate/lib/i18n"
)

// 行序：status 里排在「补丁开关」（40）之后；doctor 里排在最后（体检的收尾项是
// 「这台机器读的话对不对」这种元信息）。数值与别的模块的 Rank 是同一套刻度，
// 见 modules/{gateway,runtime,config} 里各自的常量。
const (
	rankStatusLocale = 50
	rankCheckLocale  = 60
)

// Status 贡献 `newgate status` 的一行：当前语言 + 覆盖率。
func (c *command) Status() []cliapi.StatusLine {
	value := c.svc.lang
	if info, ok := c.currentInfo(); ok && info.Total > 0 {
		value += fmt.Sprintf("  %d%%", info.Translated*100/info.Total)
	}
	return []cliapi.StatusLine{{Rank: rankStatusLocale, Label: i18n.T("Language", nil), Value: value}}
}

// Diagnostics 贡献 doctor 的一项：翻全了没有、机翻有几条待复核、有几个键来自磁盘覆盖。
//
// 为什么值得占一行：加了多语言之后「界面上半英半中」的原因只有一个——缺译文。
// 这件事必须能在体检里一眼看到，而不是让用户猜「是不是没翻译」。
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
		// 缺的那几条**原文**列出来：它们是语言无关的身份，谁都能照着补。
		// 只列前几条——doctor 是体检不是清单，全量在 `newgate lang` 与 tools/i18n。
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

// Glossary 在帮助屏的术语表里留一行：语言这件事本身就值得解释一句。
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
