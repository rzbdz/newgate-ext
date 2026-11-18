package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/buildinfo"
	"github.com/rzbdz/newgate-ext/lib/style"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate/lib/i18n"
)

// cmdDoctor health check.
//
// **The UI knows not one item here**: every row is reported by the module that owns
// that thing via RegisterDiagnostics (files/links belong to config, env/proxy to gateway,
// takeover/backup to runtime), and the UI only does the uniform layout and the "is anything
// red" conclusion. The previous version hardcoded six items here, so the UI imported
// paths / store / resolve / takeover for them -- all of which is someone else's knowledge.
func cmdDoctor(service *service) int {
	fmt.Println(style.Title("newgate doctor", buildinfo.Version()))
	fmt.Println(style.Rule(64))

	checks := service.moduleDiagnostics()
	fmt.Println()
	bad := 0
	for _, d := range checks {
		mark := style.Skip
		switch d.State {
		case "ok":
			mark = style.OK
		case "warn":
			mark = style.Warn
		case "bad":
			mark = style.Bad
		}
		if mark == style.Bad {
			bad++
		}
		fmt.Println(style.Field(d.Label, style.Mark(mark)+" "+d.Line))
		for _, detail := range d.Details {
			fmt.Println(style.Bullet(style.Dim(detail)))
		}
	}

	fmt.Println()
	if bad == 0 {
		fmt.Println(style.Green(i18n.T("all checks passed", nil)))
		return 0
	}
	// The conclusion line uses i18n N (messages with a count): English has singular
	// and plural, Chinese does not -- the Chinese catalog only needs one entry. The
	// status mark in front stays outside the message (it is a layout symbol, not copy).
	fmt.Println(style.Mark(style.Bad) + " " +
		i18n.N("{n} check failed", "{n} checks failed", bad, i18n.A{"n": bad}))
	return 1
}

// cmdAllLogs is the diagnostic bundle: **it pastes together the raw text modules submit**.
//
// The UI does only two things here: print its own section (version, env, state), and
// assemble the material modules submit in Rank order. Where the raw text comes from and what
// it looks like is entirely each module's business -- whoever owns the data submits it (see
// cliapi.Dumper), and the UI does not know providers.json, nor which files takeover changed.
//
// In the previous version this whole function lived in the UI, so it had to know the
// providers' structure, the profile's tier table, where target files are, what evidence
// files are called, and where the logs are. This round gave all that knowledge back.
func cmdAllLogs(service *service) int {
	line := func(t string) { fmt.Printf("\n===== %s =====\n", t) }

	// This section is the UI's own: it is about "where this process runs, what version, what env".
	line(i18n.T("Version and environment", nil))
	fmt.Println(VersionLine())
	// Field labels go through the catalog (they are copy), values do not (they
	// are paths and process facts). The column width is fixed, so three rows line up --
	// `env` is a **machine name** (the raw segment of an env var) and is not translated.
	const dumpLabelW = 12
	field := func(label, value string) { fmt.Println(style.Pad(label, dumpLabelW) + value) }
	field(i18n.T("Config dir", nil), paths.Config())
	field(i18n.T("Log file", nil), paths.LogFile())
	for _, k := range []string{"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY",
		"no_proxy", "NO_PROXY", "NEWGATE_HOME", "NEWGATE_TARGET_DIR", "NEWGATE_DUMP"} {
		if v := os.Getenv(k); v != "" {
			field("env", k+"="+v)
		}
	}

	line(i18n.T("Status", nil))
	cmdStatus(service)

	for _, section := range service.dumpSections() {
		if section.Title != "" {
			line(section.Title)
		}
		fmt.Println(strings.Join(section.Lines, "\n"))
	}
	return 0
}

// cmdStatus is the one-screen overview.
//
// **Every line of this screen belongs to its owner**: whoever owns a state
// reports it (RegisterStatus / RegisterStatusBlocks). The UI knows not one
// line here -- proxy, takeover, and config are each reported by gateway /
// runtime / config, and the UI's whole job is to sort by Rank and print.
func cmdStatus(service *service) int {
	fmt.Println(style.Title("newgate "+buildinfo.Version(), buildinfo.BuildTimeDisplay()))
	fmt.Println(style.Rule(64))

	for _, line := range service.statusLines() {
		fmt.Println(style.Field(line.Label, line.Value))
	}
	for _, block := range service.statusBlocks() {
		if block.Title != "" {
			fmt.Print(style.Section(block.Title) + "\n")
		}
		for _, l := range block.Lines {
			fmt.Println(l)
		}
	}
	return 0
}

// routingOf has been replaced by runtime/takeover.List() -- that is the single source of truth for
// takeover state, read by CLI / TUI / Web alike, so none of them judges the mechanism separately.
