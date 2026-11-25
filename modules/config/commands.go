package config

// This file is **the config's own** batch of commands: tier / profiles / profile kv /
// --set-profile / agents. Moved here wholesale from modules/cli/profile.go on 2026-09-18.
//
// Why move it: how tiers resolve, how chains are built, who gets skipped, what a profile file looks
// like -- all of it is config knowledge, and the UI keeping a "copied understanding" necessarily
// drifts from the implementation (before this round, the UI had to import resolve + domain +
// paths + store just to render tier). After the move the UI knows neither profile nor chain.
//
// Dependency direction: config -> cli/extension (the leaf contract), not -> modules/cli.
// The commands are injected into the UI by this module in Start, exactly like other modules.

import (
	"encoding/json"
	"fmt"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/lib/style"
	"github.com/rzbdz/newgate-ext/modules/breaker/status"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
	"github.com/rzbdz/newgate/lib/i18n"

	"github.com/rzbdz/newgate-ext/modules/runtime/daemon"
)

// cmdSetProfile switches the chain head. An empty agent sets the global
// default; naming an agent switches only that agent, leaving every other
// agent — including ones with sessions in flight — untouched.
func cmdSetProfile(agent, name string) int {
	if err := store.SetActiveProfile(agent, name); err != nil {
		return style.Die(65, err.Error())
	}
	scope := i18n.T("global default", nil)
	if agent != "" {
		scope = "agent " + agent
	}
	fmt.Println(style.Item(style.OK, scope+" → "+style.Cyan(name)))

	if pr, err := store.LoadProfile(name); err == nil {
		if pr.Description != "" {
			fmt.Println(style.Hint(pr.Description))
		}
		if pr.Pinned {
			fmt.Println(style.Hint(i18n.T("pinned: the chain ends here; a failure is reported, not replaced", nil)))
		}
		t := style.NewTable(i18n.T("Role", nil), i18n.T("Binding", nil))
		for _, tier := range domain.Roles {
			if b, ok := pr.Resolve(tier); ok {
				t.Row(style.Cyan(tier), b.String())
			}
		}
		if t.Len() > 0 {
			fmt.Println()
			fmt.Print(t.String())
		}
	}
	controlplane.Notify()
	fmt.Println()
	if daemon.Running() != nil {
		fmt.Println(style.Hint(i18n.T("takes effect immediately; running sessions are unaffected", nil)))
	} else {
		fmt.Println(style.Hint(i18n.T("proxy is not running · newgate start", nil)))
	}
	return 0
}

// cmdProfiles lists every profile.
//
// One screen answers three questions: which is the default, how priorities sort,
// and which agents hang an aliased profile. Only profiles marked with a flag are the
// exceptions (pinned / excluded), so no extra section is added and all the information
// is pressed into one table -- more sections and skimming turns into reading.
func cmdProfiles() int {
	names, err := store.ListProfiles()
	if err != nil {
		return style.Die(65, i18n.T("cannot read mappings: {err}", i18n.A{"err": err.Error()}))
	}
	st := store.LoadState()
	var ps []*domain.Profile
	for _, n := range names {
		if p, err := store.LoadProfile(n); err == nil {
			ps = append(ps, p)
		}
	}
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Prio() != ps[j].Prio() {
			return ps[i].Prio() < ps[j].Prio()
		}
		return ps[i].Name < ps[j].Name
	})

	fmt.Println(style.Title("newgate profiles",
		i18n.N("{n} profile · default {name}", "{n} profiles · default {name}",
			len(ps), i18n.A{"n": len(ps), "name": st.DefaultProfile})))
	fmt.Println(style.Rule(72))

	t := style.NewTable(i18n.T("Priority", nil), "profile", i18n.T("Flag", nil), i18n.T("Description", nil))
	t.AlignRight(0)
	for _, p := range ps {
		var flags []string
		if p.Name == st.DefaultProfile {
			flags = append(flags, style.Green("default"))
		}
		if p.Pinned {
			flags = append(flags, style.Yellow("pinned"))
		}
		if p.Excluded {
			flags = append(flags, style.Yellow("excluded"))
		}
		if len(p.VisibleAgents) > 0 {
			flags = append(flags, style.Cyan("visible:"+strings.Join(p.VisibleAgents, ",")))
		}
		if len(p.HiddenAgents) > 0 {
			flags = append(flags, style.Yellow("hidden:"+strings.Join(p.HiddenAgents, ",")))
		}
		for agent, ap := range st.Active {
			if ap == p.Name {
				flags = append(flags, style.Cyan("←"+agent))
			}
		}
		name := p.Name
		if p.Name == st.DefaultProfile {
			name = style.Cyan(p.Name)
		}
		t.Row(fmt.Sprintf("%d", p.Prio()), name, strings.Join(flags, " "),
			style.Dim(p.Description))
	}
	fmt.Print(t.String())
	fmt.Println(style.Hint(i18n.T("pinned stops at the chain head and is never replaced · excluded is only selected explicitly · visible:/hidden: which agents may route through it · ←agent that agent uses this profile alone", nil)))
	return 0
}

// cmdProfileKV turns one profile into KV text -- the json->kv conversion point.
//
// Without --write it only prints (copy it away, edit, paste back); with --write it lands on
// disk as mappings/<name>.kv and renames the old .json to .bak (kv takes precedence over json,
// so keeping the old file would have it always overridden; retiring is cleaner than coexisting).
func cmdProfileKV(args []string) int {
	if len(args) < 1 || args[0] == "" {
		return style.Die(64, i18n.T("usage: newgate profile kv <name> [--write]", nil))
	}
	name := args[0]
	raw, err := store.LoadProfileRaw(name)
	if err != nil {
		return style.Die(65, err.Error())
	}
	text := store.SerializeProfileKV(raw)

	if len(args) < 2 || args[1] != "--write" {
		fmt.Print(text)
		fmt.Println(style.Dim(i18n.T("# to write it: newgate profile kv {name} --write",
			i18n.A{"name": name})))
		return 0
	}
	kvPath := filepath.Join(paths.Mappings(), name+".kv")
	// Goes through store.Write (atomic + backup ring), **not os.WriteFile**: this
	// command turns one profile into another format on the spot, overwriting exactly
	// the config in the user's hands; a direct write dying halfway leaves a truncated
	// file, and that version has not even a backup (see the comment on store.Write).
	if err := store.Write(kvPath, []byte(text)); err != nil {
		return style.Die(70, i18n.T("cannot write {path}: {err}",
			i18n.A{"path": kvPath, "err": err.Error()}))
	}
	jsonPath := filepath.Join(paths.Mappings(), name+".json")
	if _, err := os.Stat(jsonPath); err == nil {
		if err := os.Rename(jsonPath, jsonPath+".bak"); err != nil {
			return style.Die(70, i18n.T("cannot rename the old json (the kv is already written; handle it by hand): {err}",
				i18n.A{"err": err.Error()}))
		}
		fmt.Println(style.Item(style.OK, kvPath+style.Dim(i18n.T("   old .json → .json.bak", nil))))
	} else {
		fmt.Println(style.Item(style.OK, kvPath))
	}
	controlplane.Notify()
	return 0
}

// tierView is one tier's resolved result.
type tierView struct {
	name  string
	steps []resolve.Step
	skips []resolve.Skip
}

// skipKindOrder fixes the category order: with the numbers matching, two outputs compare directly.
//
// What the table holds is the **machine markers** resolve stamps when building the
// chain (resolve.Skip.Kind), not human-facing sentences -- display names are looked up by
// skipLabel on the spot. So "a translation wording change and the summary loses a column"
// cannot happen: grouping goes by marker, and markers do not change with language.
var skipKindOrder = []string{
	resolve.SkipHidden, resolve.SkipExcluded, resolve.SkipUndefined, resolve.SkipNoKey,
	resolve.SkipUnavailable, resolve.SkipDisabled, resolve.SkipMaxSteps,
	resolve.SkipDuplicate, resolve.SkipCycle, resolve.SkipOther,
}

// skipKind takes a skip's category. It was stamped when the chain
// was built (see resolve.Skip.Kind); anything unstamped (none today,
// and in future only Skips hand-made outside) counts as "other".
func skipKind(s resolve.Skip) string {
	if s.Kind != "" {
		return s.Kind
	}
	return resolve.SkipOther
}

// skipLabel is the category's display name -- only this goes
// through i18n, the category itself is a machine marker.
//
// Why a function rather than a package-level map: i18n.T only knows the translations
// once modules/locale has installed the language in Start, and a package-level
// variable evaluated at init would stay in the source language forever.
func skipLabel(kind string) string {
	switch kind {
	case resolve.SkipExcluded:
		return "excluded" // the profile's flag name; a machine marker, never translated
	case resolve.SkipHidden:
		return i18n.T("not visible", nil)
	case resolve.SkipUndefined:
		return i18n.T("not defined", nil)
	case resolve.SkipNoKey:
		return i18n.T("no key", nil)
	case resolve.SkipUnavailable:
		return i18n.T("unavailable", nil)
	case resolve.SkipDisabled:
		return i18n.T("disabled", nil)
	case resolve.SkipMaxSteps:
		return i18n.T("over maxAttempts", nil)
	case resolve.SkipDuplicate:
		return i18n.T("duplicate", nil)
	case resolve.SkipCycle:
		return i18n.T("reference cycle", nil)
	}
	return i18n.T("other", nil)
}

// cmdTier shows the fallback chain -- this is the **interface** of the whole config: at
// a glance it must answer "who will this request go to" and "why not the one I thought".
//
// The layout has two levels (detail where it matters):
//
//	newgate tier            one line per tier, `= heavy` when the chain is the same, skip stats last
//	newgate tier <tier>     expanded: numbered stops + skip stats grouped by reason
//
// Skips are always **summary only**, never laid out one by one. Measured, of 106
// skips in one config 91 were "duplicate of a candidate earlier on the chain" -- printing an
// inevitable deduplication as line after line of results only drowns out the real conclusion
// (who is taken). The user wants the chain, not a running account of the whole candidate set.
func cmdTier(args []string) int {
	which := ""
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		which = a
	}
	// Abbreviations are accepted too (heavy as h, normal as n...), so the
	// full names need not be remembered. Validation goes in tierReport, after
	// store.Load -- dynamic role keys are only known once Load has refreshed
	// roleprov (checking here would judge the keys just registered as unknown).
	return tierReport(which)
}

// knownRolesLine offers the options when reporting an error. Dynamic
// role keys can number dozens, and listing them all would turn the
// error message into a wall; only the count and where to look are given.
func knownRolesLine() string {
	if n := len(domain.ExtraRoles()); n > 0 {
		return i18n.T("roles {roles}, plus {n} dynamic role keys (newgate omo ls)",
			i18n.A{"roles": strings.Join(domain.Roles, "/"), "n": n})
	}
	return i18n.T("roles {roles}", i18n.A{"roles": strings.Join(domain.Roles, "/")})
}

func matchTier(s string) string {
	for _, r := range domain.Roles {
		if strings.HasPrefix(r, s) {
			return r
		}
	}
	return ""
}

func tierReport(which string) int {
	snap, err := store.Load()
	if err != nil {
		return style.Die(65, err.Error())
	}
	st := snap.State

	// Role key validation must happen here: dynamic role keys (omo-sisyphus / cat-deep) are
	// refreshed into store.Load together with roleprov, and checking before Load would judge the
	// keys just registered as unknown. To the resolution layer, module-registered slots and tiers
	// are the same thing, and the command layer should not be narrower than the resolution layer --
	// newgate tier omo-sisyphus is the proper entry point for asking "which chain this slot takes".
	if which != "" {
		if full := matchTier(which); full != "" {
			which = full
		} else if !domain.IsKnownRole(which) {
			return style.Die(64, i18n.T("no such role: {name} ({known})",
				i18n.A{"name": which, "known": knownRolesLine()}))
		}
	}

	// There may be more than one chain head (claude and opencode can each hang a profile).
	heads := map[string][]string{st.DefaultProfile: {i18n.T("default", nil)}}
	for agent, p := range st.Active {
		heads[p] = append(heads[p], agent)
	}
	var headNames []string
	for h := range heads {
		headNames = append(headNames, h)
	}
	sort.Strings(headNames)

	names := domain.Roles
	if which != "" {
		names = []string{which}
	}
	_, live := controlplane.State()
	available := live.Available()
	rank := live.Rank()
	liveHealth := healthFromProxy(live)

	for _, head := range headNames {
		sort.Strings(heads[head])
		fmt.Println(style.Title("newgate tier",
			i18n.T("chain head {head} ({who}) · max attempts {n} · budget {budget}", i18n.A{
				"head": head, "who": strings.Join(heads[head], ", "),
				"n": st.Chain.Attempts(), "budget": prettyMs(st.Chain.Budget())})))
		fmt.Println(style.Rule(72))

		var rows []tierView
		for _, name := range names {
			steps, skips := resolve.BuildChain(name, snap.Profiles, snap.Providers, resolve.Opts{
				Active:    head,
				Available: available,
				Rank:      rank,
				// tier shows the complete candidate chain; maxAttempts is an execution constraint, not
				// membership. Otherwise later profiles would be misread as not on the chain at all.
				MaxSteps: 0,
			})
			rows = append(rows, tierView{name, steps, skips})
		}

		fmt.Println()
		if which == "" {
			// Overview: tiers with the same chain are merged into `= <the first one that appeared>`
			fmt.Print(tierOverview(rows))
			if n := countSkips(rows); n > 0 {
				fmt.Println(style.Hint(i18n.N("skipped {n} candidate: {reasons}",
					"skipped {n} candidates: {reasons}", n,
					i18n.A{"n": n, "reasons": skipSummary(rows)})))
				fmt.Println(style.Hint(i18n.T("details: newgate tier <role>", nil)))
			}
			continue
		}

		r := rows[0]
		if len(r.steps) == 0 {
			fmt.Println(style.Item(style.Bad, i18n.T("no usable candidate", nil)))
		} else {
			fmt.Println(style.Field(i18n.T("Final", nil), style.Cyan(r.steps[0].Binding.String())))
			fmt.Println()
			fmt.Print(numberedBindingChain(r.steps, func(step resolve.Step) string {
				return bindingHealthLabel(liveHealth[step.Binding.String()])
			}))
			fmt.Println(style.Hint(i18n.T("the chain head is fixed; fallbacks follow the predicted TTFT of this context", nil)))
			fmt.Println(style.Hint(i18n.T("each (provider, model) appears once in the whole chain", nil)))
			if limit := st.Chain.Attempts(); limit < len(r.steps) {
				fmt.Println(style.Hint(i18n.T(
					"a request tries at most the first {limit} stops; the remaining {rest} stay in the chain",
					i18n.A{"limit": limit, "rest": len(r.steps) - limit})))
				fmt.Println(style.Hint(i18n.T("raise state.json chain.max_attempts to widen the actual attempt range", nil)))
			}
		}
		if len(r.skips) > 0 {
			fmt.Println()
			printSkips(r.skips)
		}
	}
	return 0
}

// bindingHealthLabel says in one line what state this binding is in right now.
//
// **Both the thresholds and the tier names come from breaker/status**
// (2026-09-18): this used to keep its own copy of 3000/12000, so
// when the daemon changed its standard this screen did not follow.
func bindingHealthLabel(h status.Status) string {
	if h.Open {
		return style.Red(i18n.T("circuit open", nil))
	}
	grade := status.Grade(h.ScoreMs, h.ScoreMs > 0)
	switch {
	case grade == status.LatencyUnknown && h.Grade == status.ProbeUnavailable:
		return style.Red(i18n.T("unavailable", nil))
	case grade == status.LatencyUnknown:
		return style.Dim(i18n.T("unprobed", nil))
	}
	label := fmt.Sprintf("%s %dms", grade, h.ScoreMs)
	switch grade {
	case status.LatencyFast:
		return style.Green(label)
	case status.LatencyOK:
		return style.Yellow(label)
	default:
		return style.Red(label)
	}
}

func chainSig(steps []resolve.Step) string {
	var b strings.Builder
	for _, s := range steps {
		b.WriteString(s.Binding.String())
		b.WriteByte('|')
	}
	return b.String()
}

func bindingChain(steps []resolve.Step, indent string) string {
	var out strings.Builder
	for i, step := range steps {
		out.WriteString(indent)
		if i > 0 {
			out.WriteString(style.Dim("→ "))
		}
		out.WriteString(step.Binding.String())
		out.WriteByte('\n')
	}
	return out.String()
}

// PrintChain renders the numbered candidate chain; the reason is the same as PrintSkips.
func PrintChain(steps []resolve.Step) { fmt.Print(numberedBindingChain(steps, nil)) }

func numberedBindingChain(steps []resolve.Step, extra func(resolve.Step) string) string {
	var out strings.Builder
	for i, step := range steps {
		out.WriteString(fmt.Sprintf("  %d. %s\n", i+1, step.Binding.String()))
		detail := "profile " + step.Profile
		if extra != nil {
			if value := extra(step); value != "" {
				detail += " · " + value
			}
		}
		out.WriteString(style.Hint(detail))
		out.WriteByte('\n')
	}
	return out.String()
}

func tierOverview(rows []tierView) string {
	var out strings.Builder
	out.WriteString("  ")
	out.WriteString(style.Dim(style.Pad(i18n.T("Role", nil), 6)))
	out.WriteString("  ")
	out.WriteString(style.Dim(i18n.T("Chain", nil)))
	out.WriteString("\n")
	firstOf := map[string]string{}
	for _, row := range rows {
		prefix := "  " + style.Cyan(style.Pad(row.name, 6)) + "  "
		switch {
		case len(row.steps) == 0:
			out.WriteString(prefix)
			out.WriteString(style.Red(i18n.T("no usable candidate", nil)))
			out.WriteString("\n")
		case firstOf[chainSig(row.steps)] != "":
			out.WriteString(prefix)
			out.WriteString(style.Dim("= " + firstOf[chainSig(row.steps)]))
			out.WriteString("\n")
		default:
			firstOf[chainSig(row.steps)] = row.name
			for i, step := range row.steps {
				if i == 0 {
					out.WriteString(prefix)
					out.WriteString(step.Binding.String())
					out.WriteString("\n")
				} else {
					out.WriteString("          ")
					out.WriteString(style.Dim("→ "))
					out.WriteString(step.Binding.String())
					out.WriteString("\n")
				}
			}
		}
	}
	return out.String()
}

func countSkips(rows []tierView) int {
	n := 0
	for _, r := range rows {
		n += len(r.skips)
	}
	return n
}

// skipSummary counts skip reasons by category. What the user really
// wants to ask is "why did it not get its turn", and out of a hundred
// lines there are really only three or five categories of reason.
func skipSummary(rows []tierView) string {
	reasons := map[string]int{}
	for _, r := range rows {
		for _, s := range r.skips {
			reasons[skipKind(s)]++
		}
	}
	var parts []string
	for _, k := range skipKindOrder {
		if n := reasons[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", skipLabel(k), n))
		}
	}
	return strings.Join(parts, " · ")
}

// printSkips the skip **summary**. Always only per-category counts and
// one explanation -- laying them out one by one is a running account of
// the whole candidate set, and the real conclusion is the chain above.
//
// When one needs to look one by one there are other openings: newgate profiles for flags,
// doctor for chain paths, metrics / probe for breakers; those are the actionable information.
//
// healthFromProxy indexes the daemon's breaker table by "provider/model" into a snapshot
// for one command. When the daemon is offline it is an empty table -- diagnostics degrade
// to looking only at the static config, and do not declare things broken out of nothing.
func healthFromProxy(ps *controlplane.Doc) map[string]status.Status {
	out := map[string]status.Status{}
	if ps != nil {
		for _, s := range ps.Breakers {
			out[s.Provider+"/"+s.Model] = s
		}
	}
	return out
}

// prettyMs milliseconds -> plain words. The chain budget is configured in ms
// (120000 in state.json), and printing should not throw a raw 120000ms at the user.
func prettyMs(ms int) string { return (time.Duration(ms) * time.Millisecond).String() }

// PrintSkips renders the skip summary. Why exported: **module commands that need it**
// (opencodeomo's `newgate omo`) call it directly, and the typesetting belongs to this module.
//
// It used to hang on the UI's `Host` (the UI forwarding for modules), removed on 2026-09-18
// -- forwarding would make the UI import config, and the UI should not know any module.
func PrintSkips(skips []resolve.Skip) { printSkips(skips) }

func printSkips(skips []resolve.Skip) {
	reasons := map[string][]resolve.Skip{}
	for _, s := range skips {
		k := skipKind(s)
		reasons[k] = append(reasons[k], s)
	}
	n := len(skips)
	fmt.Println(style.Item(style.Skip,
		i18n.N("{n} candidate skipped", "{n} candidates skipped", n, i18n.A{"n": n})))

	t := style.NewTable(i18n.T("Reason", nil), i18n.T("Count", nil), i18n.T("Note", nil))
	t.AlignRight(1)
	for _, k := range skipKindOrder {
		group := reasons[k]
		if len(group) == 0 {
			continue
		}
		t.Row(skipLabel(k), fmt.Sprintf("%d", len(group)), style.Dim(skipDetail(group[0])))
	}
	fmt.Print(t.String())
}

// skipDetail gives the whole group one "so what" sentence -- with
// only the category name, the user still does not know what to change.
//
// The sentences given per category are this module's own words, going through i18n; the two
// that return s.Reason directly (disabled / reference cycle) are **words from elsewhere**: the
// disabled reason is what the criterion provider says, and the cycle sentence already spells out
// what the cycle looks like (`a -> b`); wrapping another layer would only squash the information.
func skipDetail(s resolve.Skip) string {
	switch skipKind(s) {
	case resolve.SkipExcluded:
		return i18n.T("an excluded profile is only used when selected explicitly; it never joins automatic ordering", nil)
	case resolve.SkipUndefined:
		return i18n.T("this profile does not define this role (sparse layer, expected)", nil)
	case resolve.SkipNoKey:
		return i18n.T("the provider has no api_key", nil)
	case resolve.SkipUnavailable:
		return i18n.T("the provider was rejected as unavailable — see newgate metrics / probe", nil)
	case resolve.SkipDisabled:
		return s.Reason
	case resolve.SkipMaxSteps:
		return i18n.T("the chain's attempt budget is exhausted — see state.json chain.max_attempts", nil)
	case resolve.SkipDuplicate:
		return i18n.T("duplicate of an earlier candidate in the chain", nil)
	case resolve.SkipCycle:
		return s.Reason
	}
	return s.Reason
}

// ---------- the config's own two maintenance commands ----------

type initCommand struct{}

func (initCommand) Names() []string { return []string{"init"} }

func (initCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionMaintenance, Rank: 50,
		Usage: "init [--force]", Summary: i18n.T("lay down the default configuration", nil)}
}

func (initCommand) Run(_ cliapi.Host, args []string) int {
	return runInit(cliapi.Flag(args, "--force"))
}

// runInit lays out the default config. It belongs to config: what it writes
// is providers.json / state.json / mappings, all this module's files.
func runInit(force bool) int {
	created, err := store.Init(force)
	if err != nil {
		return style.Die(70, err.Error())
	}
	if len(created) == 0 {
		fmt.Println(i18n.T("the configuration already exists, nothing to initialize (--force overwrites)", nil))
	} else {
		for _, c := range created {
			fmt.Println(i18n.T("created {path}", i18n.A{"path": c}))
		}
	}
	fmt.Println()
	fmt.Println(i18n.T("next: put your upstream keys into {path}", i18n.A{"path": paths.ProvidersFile()}))
	fmt.Println(i18n.T("what is written is placeholders — replace them with your own provider, endpoint and model names", nil))
	fmt.Println(i18n.T("keep keys in environment variables (never on disk): NEWGATE_KEY_<PROVIDER in upper case, - becomes _>", nil))
	return 0
}

type reloadCommand struct{}

func (reloadCommand) Names() []string { return []string{"reload"} }

func (reloadCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionMaintenance, Rank: 50,
		Usage: "reload", Summary: i18n.T("re-read the configuration now (normally hot-reloaded within a second)", nil)}
}

func (reloadCommand) Run(_ cliapi.Host, _ []string) int {
	info, _ := controlplane.State()
	if info == nil {
		fmt.Println(style.Item(style.Skip, i18n.T("proxy is not running; the configuration is read on the next start", nil)))
		return 0
	}
	controlplane.Notify()
	fmt.Println(style.Item(style.OK, i18n.T("proxy notified to re-read the configuration   pid {pid}",
		i18n.A{"pid": info.PID})))
	fmt.Println(style.Hint(i18n.T("you rarely need this command: configuration changes apply within a second", nil)))
	return 0
}

// ---------- command declarations ----------
//
// Each one does only "parse argv -> call the implementation in this
// file": help lines, position, aliases are **metadata for the UI**,
// declared by the module that owns the command (see cliapi.Documented).

type tierCommand struct{}

var (
	_ cliapi.Command    = (*tierCommand)(nil)
	_ cliapi.Documented = (*tierCommand)(nil)
)

func (tierCommand) Names() []string { return []string{"tier", "tiers", "role", "roles"} }

func (tierCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionRouting, Rank: 30,
		Usage: i18n.T("tier [role]", nil), Summary: i18n.T("fallback chain: who is used, what was skipped", nil)}
}

func (tierCommand) Run(_ cliapi.Host, args []string) int { return cmdTier(args) }

type profilesCommand struct{}

var (
	_ cliapi.Command    = (*profilesCommand)(nil)
	_ cliapi.Documented = (*profilesCommand)(nil)
)

func (profilesCommand) Names() []string { return []string{"profiles", "ls"} }

func (profilesCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionRouting, Rank: 30,
		Usage: "profiles", Summary: i18n.T("all profiles (priority / flags / overrides)", nil)}
}

func (profilesCommand) Run(_ cliapi.Host, _ []string) int { return cmdProfiles() }

type profileCommand struct{}

var (
	_ cliapi.Command    = (*profileCommand)(nil)
	_ cliapi.Documented = (*profileCommand)(nil)
	_ cliapi.Unstyled   = (*profileCommand)(nil)
)

func (profileCommand) Names() []string { return []string{"profile"} }

// Unstyled: `profile kv` outputs raw KV text for pipes/files, not control-plane layout.
func (profileCommand) Unstyled(args []string) bool { return cliapi.Positional(args, 0) == "kv" }

func (profileCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionRouting, Rank: 30,
		Usage:   i18n.T("profile kv|history|restore <name>", nil),
		Summary: i18n.T("convert a profile to KV text, list its backups, or bring one back", nil)}
}

func (profileCommand) Run(host cliapi.Host, args []string) int {
	switch cliapi.Positional(args, 0) {
	case "kv":
		return cmdProfileKV(args[1:])
	case "history":
		return cmdProfileHistory(args[1:])
	case "restore":
		return cmdProfileRestore(args[1:])
	}
	return host.Die(64, i18n.T("usage: newgate profile kv|history|restore <name>", nil))
}

type setProfileCommand struct{}

var (
	_ cliapi.Command    = (*setProfileCommand)(nil)
	_ cliapi.Documented = (*setProfileCommand)(nil)
)

func (setProfileCommand) Names() []string { return []string{"--set-profile"} }

func (setProfileCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionRouting, Rank: 30,
		Usage:   i18n.T("--set-profile <name> [--agent <agent>]", nil),
		Summary: i18n.T("switch profile; omit --agent to set the global default", nil)}
}

// Run's shape is given after the UI normalizes: args[0] is the profile name, and the optional
// `--agent <agent>` follows. The UI is responsible for recognizing both `--set-profile=x`
// and `--set-profile x` (argv parsing is its job); the semantics belong to this module.
func (setProfileCommand) Run(_ cliapi.Host, args []string) int {
	name := cliapi.Positional(args, 0)
	if name == "" {
		return style.Die(64, i18n.T("--set-profile needs a profile name", nil))
	}
	return cmdSetProfile(flagValue(args, "--agent"), name)
}

// flagValue takes the value in `--name value`, empty string when not given.
func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// commands are all the commands this module injects into the UI. Without
// it there is no entry point -- and entry points are the ui's business,
// this module's functionality is unaffected (see CLAUDE.md §4).
func commands() []cliapi.Command {
	return []cliapi.Command{
		tierCommand{}, profilesCommand{}, profileCommand{}, setProfileCommand{},
		initCommand{}, reloadCommand{},
	}
}

// cmdProfileHistory lists the history versions of one tier
// file (the ones kept automatically before each write).
//
// # Why this command must exist
//
// The backup ring (see store/history.go) has been writing
// since 2026-09-20: before every write the current copy is copied into `.history/`. But
// those files were **reachable in only one way** -- by someone who knows the path spelling
// (`mappings__demo.kv/1789...bak`) and dares cp it back himself. For a safety net, "exists
// but cannot be retrieved" and "does not exist" are the same outcome: the user who broke
// something still has to rewrite it from memory. So this command is not a flourish, it
// is **the other completed half** of that feature (the criterion has always been asymmetric
// cost: one more command is a few dozen lines, one loss is the config in the user's hands).
func cmdProfileHistory(args []string) int {
	name := cliapi.Positional(args, 0)
	if name == "" {
		return style.Die(64, i18n.T("usage: newgate profile history <name>", nil))
	}
	file, err := profileFile(name)
	if err != nil {
		return style.Die(65, err.Error())
	}
	fmt.Println(style.Title("newgate profile history", name))
	fmt.Println(style.Rule(72))
	ents := store.HistoryEntries(file)
	if len(ents) == 0 {
		fmt.Println(style.Dim(i18n.T("nothing yet — one version is kept before every write", nil)))
		return 0
	}
	t := style.NewTable("#", i18n.T("When", nil), i18n.T("Size", nil), i18n.T("What it said", nil))
	t.AlignRight(0)
	for i, p := range ents {
		b, _ := os.ReadFile(p)
		t.Row(fmt.Sprintf("%d", i+1), historyStamp(p), fmt.Sprintf("%d B", len(b)), style.Dim(historyHint(file, b)))
	}
	fmt.Print(t.String())
	fmt.Println(style.Hint(i18n.T("bring one back with: newgate profile restore {name} <#>",
		i18n.A{"name": name})))
	return 0
}

// cmdProfileRestore puts one history version back (no version number = the most recent one).
//
// **The restore itself also goes through store.Write**, so the current copy is copied into history
// first -- a mistaken restore can be restored again. This matters: an irreversible "undo" is one
// nobody dares press while panicking, and every use case of this command is "I am panicking".
func cmdProfileRestore(args []string) int {
	name := cliapi.Positional(args, 0)
	if name == "" {
		return style.Die(64, i18n.T("usage: newgate profile restore <name> [version]", nil))
	}
	file, err := profileFile(name)
	if err != nil {
		return style.Die(65, err.Error())
	}
	ents := store.HistoryEntries(file)
	if len(ents) == 0 {
		return style.Die(65, i18n.T("{name} has no history yet — there is nothing to bring back",
			i18n.A{"name": name}))
	}
	idx := 1 // no version number = the latest one: the usual use is "just broke it"
	if v := cliapi.Positional(args, 1); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > len(ents) {
			return style.Die(64, i18n.T("version must be 1..{max} (1 = most recent; see `newgate profile history {name}`)",
				i18n.A{"max": len(ents), "name": name}))
		}
		idx = n
	}
	src := ents[idx-1]
	b, err := os.ReadFile(src)
	if err != nil {
		return style.Die(70, err.Error())
	}
	if err := store.Write(file, b); err != nil {
		return style.Die(70, i18n.T("cannot write {path}: {err}",
			i18n.A{"path": file, "err": err.Error()}))
	}
	fmt.Println(style.Item(style.OK, i18n.T("brought {name} back to {when} (what it replaced is kept as a backup)",
		i18n.A{"name": name, "when": historyStamp(src)})))
	controlplane.Notify()
	return 0
}

// historyStamp reads `<unixnano>.bak` as a local time. When unreadable it falls back to the file
// name as is -- a string you cannot read is better than a fake date (same as the web UI one).
func historyStamp(path string) string {
	base := filepath.Base(path)
	var n int64
	if _, err := fmt.Sscanf(base, "%d.bak", &n); err != nil {
		return base
	}
	return time.Unix(0, n).Format("2006-01-02 15:04:05")
}

// historyHint writes one "what it said at the time" for a backup
// version -- the only thing visible when picking a version.
//
// It tries the description first (the only human sentence in a tier file), falling back to
// the first non-empty line. This step should not fail: unreadable returns the empty string,
// and an empty cell in the table is better than an error (this is a "take a look" command).
func historyHint(file string, b []byte) string {
	var pr *domain.Profile
	if strings.HasSuffix(file, ".kv") {
		pr, _ = store.ParseProfileKV(string(b))
	} else {
		var p domain.Profile
		if json.Unmarshal(b, &p) == nil {
			pr = &p
		}
	}
	if pr != nil && strings.TrimSpace(pr.Description) != "" {
		return style.Truncate(pr.Description, 48)
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if s := strings.TrimSpace(ln); s != "" {
			return style.Truncate(s, 48)
		}
	}
	return ""
}
