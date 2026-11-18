package locale

import "github.com/rzbdz/newgate/lib/i18n"

// Where the language comes from -- highest priority first, the first one with an opinion wins:
//
//  1. NEWGATE_LANG                      session-level explicit override (tests, scripts, temporary)
//  2. locale.lang in state.json         persisted choice (`newgate lang zh-Hans`)
//  3. LC_ALL → LC_MESSAGES → LANG       follow the system (POSIX convention)
//  4. source language                   nobody said anything
//
// **Why the config sits above the system locale** (this is a trade-off, not a casual ordering):
// many developers run an English system yet want to read Chinese, and the reverse happens too.
// POSIX's `LANG` expresses "this machine's default language", "the config" expresses "what **I**
// want to read" -- the latter is more explicit, so the latter wins. The cost is that
// `newgate lang` may show no visible effect in a shell with `LANG=en_US.UTF-8`, so that command
// **must print the source** (see command.go), so people know who is talking.
//
// To have it the other way (environment first), swap these two entries -- one place only, and a
// test pins it.
type request struct {
	Tag    string // the raw request, maybe empty or spelled like `zh_CN.UTF-8`
	Source Source
}

func resolve(getenv func(string) string, configured string) request {
	if v := getenv("NEWGATE_LANG"); v != "" {
		return request{Tag: v, Source: SourceEnvOverride}
	}
	if configured != "" {
		return request{Tag: configured, Source: SourceConfig}
	}
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := getenv(key); v != "" {
			if tag := i18n.Normalize(v); tag != "" { // `C` / `POSIX` = no opinion
				return request{Tag: tag, Source: SourceSystem}
			}
		}
	}
	return request{Tag: i18n.SourceLang, Source: SourceDefault}
}
