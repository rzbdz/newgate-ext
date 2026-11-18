package gateway

// This file is `newgate metrics`: gateway counters plus a model health overview.
//
// **Why it lives here and not in modules/cli** (2026-09-18): counters are gateway
// semantics - which keys, how grouped, what they mean all live in gateway/metrics; the
// UI only lays them out. With the command in the UI, the "metricOrder" table and the
// "model health" block stayed there too, leaving data-plane knowledge in the UI layer
// (the user's words: nothing in cli/ unrelated to showing cli - no metrics, no probe).
//
// After the move the UI no longer imports gateway/metrics - it does not even know the names.
//
// Dependency direction: gateway -> cli/extension (a leaf contract), not -> modules/cli.
// So this is a plain "owner injects its own command into the UI", with no back edge.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/durarg"
	"github.com/rzbdz/newgate-ext/lib/style"
	"github.com/rzbdz/newgate-ext/modules/breaker/status"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
	"github.com/rzbdz/newgate-ext/modules/gateway/metrics"
	"github.com/rzbdz/newgate-ext/modules/gateway/policy"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Rank sorts **within a section** only; section order comes from the UI's slot table
// (see cli/extension.Section). Numbers share their source with modules/cli, gaps kept.
const (
	rankObserve     = 40
	rankMaintenance = 50
)

type metricsCommand struct{}

var (
	_ cliapi.Command    = (*metricsCommand)(nil)
	_ cliapi.Documented = (*metricsCommand)(nil)
)

func (metricsCommand) Names() []string { return []string{"metrics", "stat"} }

func (metricsCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionObserve, Rank: rankObserve,
		Usage: "metrics", Summary: i18n.T("proxy counters: interception / timeouts / failover / rerouting", nil)}
}

func (metricsCommand) Run(host cliapi.Host, _ []string) int { return runMetrics(host) }

// cmdMetrics shows per-binding scores and gateway counters. Usable bindings are listed,
// stalled/unavailable ones summed; reasons come from probe, so metrics is not a log.
//
// Layout: by **group** (request / chain / timeout / client / plugin / fallback), the
// group name only on its first row. Raw counter names all appear - users grep them.
func runMetrics(host cliapi.Host) int {
	info, ps := controlplane.State()
	if info == nil {
		return host.Die(69, i18n.T("proxy is not running (newgate start) — counters live in daemon memory", nil))
	}
	counter, uptime, ok := controlplane.Metrics(info.Port)
	if !ok {
		return host.Die(69, i18n.T("cannot reach proxy 127.0.0.1:{port} (newgate doctor)", i18n.A{"port": info.Port}))
	}
	fmt.Println(style.Title("newgate metrics",
		fmt.Sprintf("pid %d · %s", info.PID, durarg.Format(uptime))))
	if ps != nil {
		fmt.Println(style.Hint(i18n.T("{req}req/{err}err · counters reset when the daemon restarts",
			i18n.A{"req": ps.Requests, "err": ps.Failures})))
	} else {
		fmt.Println(style.Hint(i18n.T("counters reset when the daemon restarts", nil)))
	}
	printModelHealth(ps)

	if len(counter) == 0 {
		fmt.Print(style.Section(i18n.T("request counters", nil)) + "\n")
		fmt.Println(style.Dim(i18n.T("  no counters (no requests since the daemon started)", nil)))
	} else {
		t := style.NewTable(i18n.T("group", nil), i18n.T("counter", nil),
			i18n.T("count", nil), i18n.T("description", nil))
		t.AlignRight(2)
		keys := metrics.SortedKeys(counter)
		// Sort by **group**, then by name within it. Without this, alphabetical order
		// interleaves "chain" and "timeout" and the group column is pointless.
		sort.SliceStable(keys, func(i, j int) bool {
			gi, gj := metricRank(keys[i]), metricRank(keys[j])
			if gi != gj {
				return gi < gj
			}
			return keys[i] < keys[j]
		})
		// Dedupe by **identity**, print by **wording**: rows of one group name it only once.
		lastGroup := ""
		for _, k := range keys {
			id, label := metricGroup(k)
			cell := style.Dim(label)
			if id == lastGroup {
				cell = ""
			}
			lastGroup = id
			t.Row(cell, k, fmt.Sprintf("%d", counter[k]), style.Dim(metricHint(k)))
		}
		fmt.Print(style.Section(i18n.T("request counters", nil)) + "\n")
		fmt.Print(t.String())
	}
	return 0
}

// metricOrder is the display order of groups: "request" first, then the chain ->
// timeout -> fallback -> plugin a request meets in turn, and finally breaker and
// client. The order itself tells the request's life cycle, more useful than alphabetical.
//
// The table holds **identities** (each contributor's self-reported ASCII id), not the
// printed wording - see metricRank. Unknown groups sort last.
var metricOrder = []string{"requests", "chain", "timeout", "fallback", "plugin", "breaker", "client", "other"}

func printModelHealth(ps *controlplane.Doc) {
	if ps == nil {
		return
	}
	snap, err := store.Load()
	if err != nil {
		return
	}
	statuses := healthFromProxy(ps)
	bindings := map[string]bool{}
	for _, profile := range snap.Profiles {
		for _, candidates := range profile.Roles {
			for _, binding := range candidates {
				if !binding.IsRef() && binding.Provider != "" && binding.Model != "" {
					if provider, ok := snap.Providers.Providers[binding.Provider]; ok && provider.Key() != "" {
						bindings[binding.String()] = true
					}
				}
			}
		}
		if profile.Fallback != nil && !profile.Fallback.IsRef() {
			if provider, ok := snap.Providers.Providers[profile.Fallback.Provider]; ok && provider.Key() != "" {
				bindings[profile.Fallback.String()] = true
			}
		}
	}
	for _, binding := range special.Bindings(snap.State) {
		if provider, ok := snap.Providers.Providers[binding.Provider]; ok && provider.Key() != "" {
			bindings[binding.String()] = true
		}
	}
	var names []string
	for binding := range bindings {
		names = append(names, binding)
	}
	sort.SliceStable(names, func(i, j int) bool {
		a, z := statuses[names[i]], statuses[names[j]]
		if a.Open != z.Open {
			return !a.Open
		}
		ra, rz := healthDisplayRank(a), healthDisplayRank(z)
		if ra != rz {
			return ra < rz
		}
		return names[i] < names[j]
	})

	counts := map[string]int{}
	for _, name := range names {
		h := statuses[name]
		state := modelHealthState(h)
		counts[state]++
	}
	blocked := counts[healthLaggy] + counts[healthUnavailable]
	fmt.Print(style.Section(i18n.T("model health", nil)) + "\n")
	fmt.Println(style.Hint(i18n.T(
		"{usable} usable ({fast} fast · {ok} usable · {unrated} unrated) · {laggy} laggy · {down} unavailable",
		i18n.A{"usable": len(names) - blocked, "fast": counts[healthFast],
			"ok": counts[healthUsable], "unrated": counts[healthUnrated],
			"laggy": counts[healthLaggy], "down": counts[healthUnavailable]})))
	for _, name := range names {
		h := statuses[name]
		state := modelHealthState(h)
		if state == healthLaggy || state == healthUnavailable {
			continue
		}
		fmt.Println(style.Item(style.Skip, name))
		fmt.Println(style.Hint("    " + modelScoreLine(h)))
	}
	if blocked > 0 {
		fmt.Println(style.Hint(i18n.T("details for lagging and unavailable models: newgate probe", nil)))
	}
}

func healthDisplayRank(h status.Status) int {
	switch modelHealthState(h) {
	case healthFast:
		return 0
	case healthUnrated:
		return 1
	case healthUsable:
		return 2
	default:
		return 3
	}
}

// modelHealthState turns one health snapshot row into a human-facing tier name.
//
// Note it reads the **per-tier detail** (Scores/Buckets), not the daemon's sort key
// Rank - this screen shows ms per context tier, and Rank only encodes the ≤4K tier.
//
// Tier names and thresholds come from breaker/status (**one copy only**, 2026-09-18): this
// file once copied 3000/12000 itself, drifting from breaker's sort thresholds. A health tier's
// **identity** feeds comparison, counting and sorting; display goes through healthLabel.
//
// Why not status.Latency.String() as the criterion: that is a **rendering** (it follows
// the language, see breaker/status), so switching on it would make this screen depend
// on the current language. Identity stays here, the rendering is given in healthLabel.
const (
	healthFast        = "fast"        // smooth
	healthUsable      = "usable"      // usable
	healthLaggy       = "laggy"       // laggy
	healthUnavailable = "unavailable" // unavailable
	healthUnrated     = "unrated"     // not yet rated
)

// healthLabel turns a tier identity into a human-facing rendering.
func healthLabel(state string) string {
	switch state {
	case healthFast:
		return i18n.T("fast", nil)
	case healthUsable:
		return i18n.T("usable", nil)
	case healthLaggy:
		return i18n.T("laggy", nil)
	case healthUnavailable:
		return i18n.T("unavailable", nil)
	case healthUnrated:
		return i18n.T("unrated", nil)
	}
	return state
}

func modelHealthState(h status.Status) string {
	if h.Open {
		score, sampled := modelDisplayScore(h)
		if h.Grade == status.ProbeLaggy || status.Grade(score, sampled) == status.LatencySlow {
			return healthLaggy
		}
		return healthUnavailable
	}
	score, sampled := modelDisplayScore(h)
	switch status.Grade(score, sampled) {
	case status.LatencyFast:
		return healthFast
	case status.LatencyOK:
		return healthUsable
	case status.LatencySlow:
		return healthLaggy
	default:
		return healthUnrated
	}
}

func modelScoreLine(h status.Status) string {
	const labels = "≤4K,≤32K,≤128K,>128K"
	names := strings.Split(labels, ",")
	var scores []string
	for i, n := range h.Buckets {
		if n > 0 {
			latency := fmt.Sprintf("%dms", h.Scores[i])
			if h.Scores[i] == 0 {
				latency = "<1ms"
			}
			scores = append(scores, i18n.N("{bucket} {latency}/{n} sample",
				"{bucket} {latency}/{n} samples", n,
				i18n.A{"bucket": names[i], "latency": latency, "n": n}))
		}
	}
	if len(scores) == 0 && h.ScoreMs > 0 {
		samples := h.Samples
		if samples < 1 {
			samples = 1
		}
		scores = append(scores, i18n.N("≤4K {ms}ms/{n} sample", "≤4K {ms}ms/{n} samples",
			samples, i18n.A{"ms": h.ScoreMs, "n": samples}))
	}
	if len(scores) == 0 {
		return healthLabel(healthUnrated)
	}
	return healthLabel(modelHealthState(h)) + " · " + strings.Join(scores, " · ")
}

func modelDisplayScore(h status.Status) (int, bool) {
	for i, n := range h.Buckets {
		if n > 0 {
			return h.Scores[i], true
		}
	}
	return h.ScoreMs, h.ScoreMs > 0
}

// metricGroup asks which group a counter belongs to: **ask the data plane's policy
// ledger first, and fall back to the data plane's own table when nobody claims it**.
//
// Why this order: a counter's ownership follows **who wrote it**. `breaker.opened` is
// written by policy (Metrics returned with the verdict), so the "breaker" group name and
// its three hints come from policy (see MetricGroup/MetricHint in modules/breaker/plane.go);
// `chain.*` is Inc'ed by the data plane itself, so it goes to gateway/metrics. Before
// 2026-09-18 breaker's three lines were hardcoded in metrics/hints.go - giving the right
// to explain "what a breaker opening means" to a package that knows nothing about breakers.
//
// This asks the ledger **this process** assembled (policy.Default()). In a CLI
// process the graph is assembled as usual (the dispatcher runs commands during Serve).
func metricGroup(k string) (id, label string) {
	if id, label := policy.Default().MetricGroup(k); id != "" {
		return id, label
	}
	return metrics.Group(k)
}

// metricHint is the same, giving a counter name's plain-language description.
func metricHint(k string) string {
	if h := policy.Default().MetricHint(k); h != "" {
		return h
	}
	return metrics.Hint(k)
}

// metricRank is the group order in the table. **Order** is
// a layout decision and stays in the CLI; **ownership** is data plane
// knowledge and belongs to modules/gateway/metrics (see Group there).
//
// It matches on **identity**, not on rendering: this table's order tells the life of one
// request, and must be kept as-is in another language; matching on translations, the words
// have no common order across two languages, so Chinese and English users would not see the
// same table. Identity has another benefit: it is a literal, so comparing needs no translation.
func metricRank(k string) int {
	id, _ := metricGroup(k)
	for i, name := range metricOrder {
		if name == id {
			return i
		}
	}
	return len(metricOrder)
}

// healthFromProxy indexes the daemon's breaker table
// by "provider/model" into a snapshot for one command.
func healthFromProxy(ps *controlplane.Doc) map[string]status.Status {
	out := map[string]status.Status{}
	if ps != nil {
		for _, s := range ps.Breakers {
			out[s.Provider+"/"+s.Model] = s
		}
	}
	return out
}
