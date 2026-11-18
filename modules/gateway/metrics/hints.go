package metrics

import (
	"strings"

	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Group returns a counter's group: **identity** (a stable ASCII id) and **rendering** (a
// human-facing anchor).
//
// Why two things: `newgate metrics`' table sorts by group, and that order tells **the life of
// one request** (request → chain → timeout → fallback → plugin → breaker → client) --
// the order itself is content, not layout. Sorting needs something **language-independent**;
// sort by rendering and the order may follow the language (those words have no common order in
// two languages), so Chinese and English users would not see the same table. Identity handles
// sorting and dedup, rendering only prints.
//
// **Why this table lives here and not in the CLI**: these names are produced by the forwarding
// hot path (all written by metrics.Default.Inc in gateway/forward), and only the data plane
// knows what they mean. The CLI's `newgate metrics` is that screen's **typesetter** and should
// not know what "chain.failover" is -- before 2026-09-18 that table was hardcoded in
// modules/cli/diag.go, so renaming a counter meant editing two places, and the CLI one had no
// idea what it was changing.
//
// **This table covers only the data plane's own counters.** Others' counters are self-reported:
// the caller asks the policy ledger first (policy.Registry.MetricGroup) and only falls back
// here when nobody claims them. breaker's three lines moved back to modules/breaker on
// 2026-09-18 exactly this way -- what a breaker opening means is not this package's to explain.
//
// Identity is **not translated** either: it is the key of the contract between counters and
// table, used only inside this process.
func Group(k string) (id, label string) {
	switch {
	case strings.HasPrefix(k, "requests."):
		return "requests", i18n.T("requests", nil)
	case strings.HasPrefix(k, "chain."):
		return "chain", i18n.T("chain", nil)
	case strings.HasPrefix(k, "timeout."):
		return "timeout", i18n.T("timeout", nil)
	case strings.HasPrefix(k, "client."):
		return "client", i18n.T("client", nil)
	case strings.HasPrefix(k, "special."):
		return "plugin", i18n.T("plugin", nil)
	case strings.HasPrefix(k, "count_tokens."):
		return "fallback", i18n.T("fallback", nil)
	}
	return "other", i18n.T("other", nil)
}

// Hint returns a plain-language note for a counter name. No padding for unlisted ones -- an
// empty note beats one made up.
//
// Same as Group: only the data plane's own counters are here; others are self-reported by their
// contributor through MetricNamer. The `special.*` family is self-reported too
// (special.MetricProvider) -- those names carry the plugin name, and only the plugin knows what
// that entry means in business terms.
func Hint(k string) string {
	switch {
	case k == "requests.total":
		return i18n.T("requests entering the gateway", nil)
	case k == "count_tokens.forwarded":
		return i18n.T("forwarded upstream to get the true value", nil)
	case k == "count_tokens.local":
		return i18n.T("local rough estimate fallback (upstream has no such endpoint)", nil)
	case k == "count_tokens.probe_404":
		return i18n.T("lazy probe returned 404, recorded as upstream unsupported", nil)
	case k == "timeout.first_byte.non_stream":
		return i18n.T("first byte timed out (non-stream), moved down the chain", nil)
	case k == "timeout.first_byte.stream":
		return i18n.T("first byte timed out (stream), moved down the chain", nil)
	case strings.HasPrefix(k, "timeout.first_byte"):
		return i18n.T("first byte timed out, moved down the chain", nil)
	case k == "chain.failover":
		return i18n.T("an earlier candidate failed and a later one succeeded", nil)
	case k == "chain.step_failed":
		return i18n.T("a chain step failed (connection / transferable error)", nil)
	case k == "chain.budget_exhausted":
		return i18n.T("chain budget exhausted", nil)
	case k == "client.cancel":
		return i18n.T("cancelled by the client", nil)
	case strings.HasPrefix(k, "special."):
		if hint, ok := special.MetricHint(k); ok {
			return hint
		}
		return i18n.T("a plugin rewrote the request (each rewrite is logged)", nil)
	}
	return ""
}
