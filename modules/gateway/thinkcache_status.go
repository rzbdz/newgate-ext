package gateway

import (
	"fmt"

	"github.com/rzbdz/newgate-ext/lib/style"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
	"github.com/rzbdz/newgate/lib/i18n"
)

// printThinkCache shows how the reasoning-content cache is doing (the last few lines of
// `newgate st`).
//
// **Why it lives here** (moved out of modules/cli on 2026-09-18): the reasoning cache is part
// of the data plane (modules/gateway/thinkcache), and "which counters are worth looking at,
// what a hit and a miss each mean" is all this module's knowledge. While it stayed in the UI,
// the UI had to open a hole on Host called PrintThinkCache -- which meant the UI **knew a
// module's concept by name**, exactly what moving the `probe` / `metrics` / `st` commands back
// was meant to tear out. After the move, cliapi.Host has no method named after any module.
//
// The numbers come from the **running daemon**: the cache lives in its memory, the command
// runs in another process, and reading this process's thinkcache.Default would show an empty
// cache -- more misleading than showing nothing. The fetch goes through the control-plane leaf
// (controlplane.State()), which has nothing to do with the UI and any module can read.
//
// Only counters, never content.
func printThinkCache() {
	info, ps := controlplane.State()
	if info == nil || ps == nil {
		return // the daemon is not running: no cache to report
	}
	t := ps.Think
	fmt.Println()
	fmt.Println(style.Field(i18n.T("Reasoning cache", nil), i18n.N(
		"{n} entry · {used} / {max} · hits {hits} · misses {misses} · evictions {evictions}",
		"{n} entries · {used} / {max} · hits {hits} · misses {misses} · evictions {evictions}",
		t.Entries, i18n.A{"used": humanBytes(t.Bytes), "max": humanBytes(t.MaxBytes),
			"hits": t.Hits, "misses": t.Misses, "evictions": t.Evictions})))
	fmt.Println(style.Hint(i18n.T("Clients drop the reasoning content the upstream sends; the proxy stores it and puts it back as-is on the next turn", nil)))
	if t.Misses > 0 {
		fmt.Println(style.Hint(i18n.T("A miss can only be filled with an empty string (for those turns the model cannot see its own previous reasoning); every case is logged", nil)))
	}
	// The unparsable ones and the "misses" look the same in outcome (both backfill an empty
	// string) but are handled completely differently: the former is a JSON shape sent by the
	// client that we do not know, the latter is genuinely absent from the cache. Reporting them
	// together sends people the wrong way, so they get their own line.
	if t.Unparsable > 0 {
		fmt.Println(style.Item(style.Warn, i18n.N(
			"Of these, {n} is an assistant message in the request that could not be parsed (not a cache problem, the shape the client sends has changed)",
			"Of these, {n} are assistant messages in the request that could not be parsed (not a cache problem, the shape the client sends has changed)",
			int(t.Unparsable), nil)))
	}
	fmt.Println(style.Hint(i18n.T("Memory + the thinkcache.bin cold layer; restored automatically after a restart, counts only, never content", nil)))
}

// humanBytes renders a byte count as KB/MB.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
