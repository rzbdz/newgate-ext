// Package buildinfo holds **build information injected
// at link time**: version, build time, commit time.
//
// # Why it is a standalone leaf (2026-09-18)
//
// These values used to be visible only to modules/cli (the `cli.Version` group of package-level
// variables), because only the UI printed the version. But the **daemon must print it too**
// -- the log line it writes at startup, `newgate <version> … started`, is the only clue for "which
// version is running in production" (CLAUDE.md §0.1 relies on it to judge the production checkout).
// The daemon main loop has moved to gateway, so those values cannot stay locked inside the UI.
//
// Why lib/ and not a module: it is a **process-level fact** and belongs to no
// module. The composition root (main) injects it once, and whoever needs it reads it.
//
// Relation to the "never silent" rule: the version is the only basis for
// the user to judge "is what I am seeing the new version's behavior",
// so it must be reachable everywhere, not only printable by one command.
package buildinfo

import (
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Injected by the composition root through Set (ldflags -> main -> here). The zero
// value is developer-friendly: a local binary from make build should show dev.
var (
	version    = "dev"
	buildTime  = "unknown"
	commitTime = "unknown"
)

// Set injects this build's information. The composition root calls it once after assembly.
func Set(v, built, committed string) {
	if v != "" {
		version = v
	}
	if built != "" {
		buildTime = built
	}
	if committed != "" {
		commitTime = committed
	}
}

// Version is the version string (`newgate --version` and the daemon startup log both use it).
func Version() string { return version }

// BuildTimeDisplay is the display string for the build time.
func BuildTimeDisplay() string { return StampDisplay(buildTime) }

// VersionLine is the three-line output of `newgate version`.
//
// The `newgate ` prefix is the program name (a machine marker) and stays outside the message.
func VersionLine() string {
	return "newgate " + version +
		i18n.T("\n  built at {time}", i18n.A{"time": BuildTimeDisplay()}) +
		i18n.T("\n  committed at {time}", i18n.A{"time": StampDisplay(commitTime)})
}

// StampDisplay renders the timestamp from ldflags as **one** local time.
//
// Before 2026-09-18 the UI printed two times (raw UTC + converted local), because the Makefile
// stamped UTC with `date -u` while commitTime used git's local time -- two time zones on
// one line, and the reader had to convert. Now the Makefile stamps the local zone throughout
// (`%z` with offset), so only one is printed here. If it cannot be parsed (a dev build
// injects nothing, the value is "unknown"), return it as is instead of forcing something.
func StampDisplay(s string) string {
	if t, err := time.Parse("2006-01-02_15:04:05Z0700", s); err == nil {
		return t.Local().Format("2006-01-02 15:04:05 MST")
	}
	return pretty(s)
}

// pretty: ldflags cannot carry spaces, so underscores stand in and are swapped back for display.
func pretty(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '_' {
			r = ' '
		}
		out = append(out, r)
	}
	return string(out)
}
