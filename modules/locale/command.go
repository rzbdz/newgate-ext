package locale

import (
	"fmt"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate/lib/i18n"
)

// command wires the language into the UI: `newgate lang` shows it, `newgate lang <tag>` changes it.
//
// It is also the provider for Status / Diagnostics / Glossary (one object registered in four
// places) -- "whoever owns the knowledge reports it", the same shape as plugin-manager's command.
type command struct{ svc *service }

var _ cliapi.Documented = (*command)(nil)

func (c *command) Names() []string { return []string{"lang", "language"} }

// Help makes `newgate lang` appear in --help.
//
// It used to register the command without declaring a HelpLine, so the command worked and the help
// screen had not one line on it -- and a user will not guess an invisible verb (found on
// 2026-10-28 while auditing every registered command; omo was filled in at the same time).
func (*command) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionUI, Rank: 40,
		Usage:   "lang [tag]",
		Summary: i18n.T("which message catalog the interface reads; lang <tag> switches it", nil),
	}
}

func (c *command) Run(host cliapi.Host, args []string) int {
	want := cliapi.Positional(args, 0)
	if want == "" {
		c.show()
		return 0
	}
	return c.set(host, want)
}

// show prints the current language, where it came from, and how much each selectable language
// has translated.
func (c *command) show() {
	fmt.Println(style.Field(i18n.T("Language", nil), c.svc.lang+"  "+style.Dim("("+c.sourceWord()+")")))
	avail := i18n.Available()
	fmt.Println(style.Section(i18n.T("Available", nil)))
	for _, info := range avail {
		mark, line := coverageLine(info, c.svc.lang)
		fmt.Println(style.Item(mark, line))
	}
	if n := i18n.Overlaid(); n > 0 {
		fmt.Println(style.Hint(i18n.N("Overridden by {n} file in the config dir", "Overridden by {n} files in the config dir", n, i18n.A{"n": n})))
	}
}

// set changes the language: writes it into state.json, and says clearly "who is overriding it".
func (c *command) set(host cliapi.Host, want string) int {
	tag := i18n.Normalize(want)
	if tag == "" {
		return host.Die(64, i18n.T("Usage: newgate lang <tag>  (see `newgate lang` for the list)", nil))
	}
	avail := make([]string, 0, 4)
	for _, info := range i18n.Available() {
		avail = append(avail, info.Language)
	}
	match := i18n.Match(tag, avail)
	if match == "" {
		return host.Die(65, i18n.T("No such language: {tag}. Available: {list}",
			i18n.A{"tag": want, "list": strings.Join(avail, " ")}))
	}
	if err := saveConfigured(match); err != nil {
		return host.Die(70, i18n.T("Cannot write the language to state.json: {err}", i18n.A{"err": err.Error()}))
	}
	fmt.Println(style.Item(style.OK, i18n.T("Saved to state.json: {tag}", i18n.A{"tag": match})))
	// A successful save does not mean it is in effect **in this shell** right away: only two
	// things override the config -- the environment variable and the system locale (see the order
	// in resolve.go). Both must be said, or the user thinks it was not saved -- and that is the
	// hardest kind of "I clearly set it" to track down.
	//
	// `default` (nobody said anything) and `config` (what was read is the config) are **neither of
	// them an override**: the former is overridden by the config, the latter is what the next read
	// gets, i.e. exactly what was just written. Before 2026-09-20 this said "warn if the source is
	// not config", so "nobody had set it, now it is set" came out as "not in effect" -- a flat-out
	// lie, hit for real (see the commit message).
	switch c.svc.source {
	case SourceEnvOverride:
		fmt.Println(style.Item(style.Warn, i18n.T(
			"Not in effect now: {source} overrides it in this shell (use NEWGATE_LANG={tag} for one command)",
			i18n.A{"source": c.sourceWord(), "tag": match})))
	case SourceSystem:
		fmt.Println(style.Item(style.Warn, i18n.T(
			"Currently following the system locale; this setting will take effect once LANG/LC_ALL stop overriding it",
			nil)))
	}
	return 0
}

// sourceWord says the source in plain words.
func (c *command) sourceWord() string {
	switch c.svc.source {
	case SourceEnvOverride:
		return "NEWGATE_LANG"
	case SourceConfig:
		return i18n.T("from config", nil)
	case SourceSystem:
		return i18n.T("from the system locale", nil)
	}
	return i18n.T("default", nil)
}

// coverageLine lays one checkup result out into a line: language, coverage, and the bad news
// that needs saying.
func coverageLine(info i18n.Info, current string) (string, string) {
	name := info.Language
	if info.Language == current {
		name = style.Bold(name)
	}
	pct := 100
	if info.Total > 0 {
		pct = info.Translated * 100 / info.Total
	}
	// Padding is computed by **display width** (style.Pad): `%-14s` goes crooked when the
	// language name has Chinese in it.
	parts := []string{fmt.Sprintf("%s %3d%%  %d/%d", style.Pad(name, 14), pct, info.Translated, info.Total)}
	var notes []string
	if info.Language == i18n.SourceLang {
		notes = append(notes, i18n.T("source", nil))
	}
	if info.Machine > 0 {
		notes = append(notes, i18n.N("machine-translated, unreviewed: {n}",
			"machine-translated, unreviewed: {n}", info.Machine, i18n.A{"n": info.Machine}))
	}
	if n := info.Total - info.Translated; n > 0 {
		notes = append(notes, i18n.N("missing: {n}", "missing: {n}", n, i18n.A{"n": n}))
	}
	mark := style.OK
	if len(notes) > 0 {
		mark = style.Warn
	}
	return mark, strings.Join(append(parts, notes...), "  ")
}
