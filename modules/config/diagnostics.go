package config

// This file reports **the config's own** three display surfaces to the
// UI: doctor's two checks (files, chain paths), the "config" line in
// status, and the two tables in status (tier bindings, fallback chain).
//
// **Why they live here** (2026-09-18): these passages used to be in modules/cli/diag.go,
// so the UI had to import config/{paths,store,resolve,domain} to render them
// -- how profiles resolve, how chains are built, which keys count as valid, all config
// knowledge, yet the UI had to compute it for it. The user's words: "the cli directory
// must not contain anything unrelated to cli display". Whoever owns the state reports
// it; the UI only loops and typesets (see cli/extension's StatusLine / StatusBlock).
//
// Dependency direction: config -> cli/extension (the leaf contract), not -> modules/cli.
// So this is an ordinary "the owner injects its own things into the UI", with no back edge.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/config/roleprov"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Position of the status line/block (smaller Rank first). Config sits after
// the proxy and takeover: it is "which profile is used", not "is it running".
const (
	rankCheckFiles   = 10
	rankCheckChain   = 15
	rankStatusConfig = 30

	// Position inside the diagnostics package (see cliapi.DumpSection).
	rankDumpProviders = 20
	rankDumpBindings  = 30
	rankBlockBinding  = 100
	rankBlockChain    = 110
)

// reporter is three kinds of contributor at once: check, status line, status block.
//
// Merged into one type because all three answer "what does the config look like
// now", and splitting into three only means two more registrations in module.go.
type reporter struct{}

var (
	_ cliapi.DiagnosticProvider = reporter{}
	_ cliapi.Dumper             = reporter{}
	_ cliapi.StatusProvider     = reporter{}
	_ cliapi.BlockProvider      = reporter{}
)

// ---------- doctor ----------

func (reporter) Diagnostics() []cliapi.Diagnostic {
	return []cliapi.Diagnostic{checkFiles(), checkChain()}
}

// checkFiles: are the three required files there.
func checkFiles() cliapi.Diagnostic {
	d := cliapi.Diagnostic{Rank: rankCheckFiles, Label: i18n.T("Files", nil)}
	var missing, ok []string
	for _, p := range []string{paths.ProvidersFile(), paths.Mappings()} {
		if _, err := os.Stat(p); err != nil {
			missing = append(missing, filepath.Base(p))
		} else {
			ok = append(ok, filepath.Base(p))
		}
	}
	// state.json is checked separately: **existing is not enough, it must be readable**. A half
	// JSON going through fail-open gives a zero-value State (default profile empty, port 0), with
	// the symptom "no usable candidate" or "the proxy is running but cannot be reached", while
	// a check that only os.Stat lists that file as ok and reports everything passing -- the user
	// gets a set of mutually contradictory symptoms (see the explanation on store.ValidateState).
	if err := store.ValidateState(); err != nil {
		missing = append(missing, i18n.T("{file} ({err})",
			i18n.A{"file": "state.json", "err": err.Error()}))
	} else {
		ok = append(ok, "state.json")
	}
	extra := ""
	if names, err := store.ListProfiles(); err == nil {
		extra = " · " + i18n.N("{n} profile", "{n} profiles", len(names), i18n.A{"n": len(names)})
	}
	if len(missing) > 0 {
		d.State = "bad"
		d.Line = i18n.T("{files} missing", i18n.A{"files": strings.Join(missing, " · ")})
		d.Details = append(d.Details, i18n.T("newgate init lays down the default configuration", nil))
		return d
	}
	d.State = "ok"
	d.Line = strings.Join(ok, " · ") + extra
	return d
}

// checkChain: are the providers and keys a profile references all there.
func checkChain() cliapi.Diagnostic {
	d := cliapi.Diagnostic{Rank: rankCheckChain, Label: i18n.T("Chain", nil)}
	probs := store.Validate()
	names, _ := store.ListProfiles()
	if len(probs) == 0 {
		d.State = "ok"
		d.Line = i18n.N("{n} profile has usable providers and keys",
			"{n} profiles have usable providers and keys", len(names), i18n.A{"n": len(names)})
		return d
	}
	d.State = "bad"
	d.Line = i18n.N("{n} configuration problem", "{n} configuration problems",
		len(probs), i18n.A{"n": len(probs)})
	d.Details = probs
	return d
}

// ---------- status ----------

// Status says in one line the global default, and which clients have **set their own** chain head.
//
// The criterion for "set their own" is **whether there is a record** in state.Active, not
// "is it the same as the default". The latter looks more economical, but it hides something
// completely different in config terms: when the user explicitly picked ds for claude while
// the global default happens to be ds too, that cell looks exactly like "nothing was set"
// in the UI -- later changing the global default, it would not follow, and the user would
// not know (hit the same thing on the web's AgentProfile on 2026-09-29; it was missed here).
func (reporter) Status() []cliapi.StatusLine {
	st := store.LoadState()
	line := style.Cyan(st.DefaultProfile) + style.Dim(" "+i18n.T("default", nil))
	var parts []string
	for _, id := range sortedAgentIDs(st) {
		if p := st.Active[id]; p != "" {
			parts = append(parts, id+" → "+style.Cyan(p))
		}
	}
	if len(parts) > 0 {
		line += "   " + strings.Join(parts, "   ")
	}
	return []cliapi.StatusLine{{Rank: rankStatusConfig, Label: i18n.T("Config", nil), Value: line}}
}

// sortedAgentIDs is a stable order of known agents.
//
// The order comes from sorting the keys of state.Active rather than from
// the registry: this line only cares about agents **with an override**, and those
// without never appear anyway. The ones in the registry (AgentCatalog on the cli
// side) are not used on this screen, so there is no need to depend on that port here.
func sortedAgentIDs(st *domain.State) []string {
	ids := make([]string, 0, len(st.Active))
	for id := range st.Active {
		ids = append(ids, id)
	}
	sortStrings(ids)
	return ids
}

func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j] < ss[j-1]; j-- {
			ss[j], ss[j-1] = ss[j-1], ss[j]
		}
	}
}

// ---------- status blocks ----------

func (reporter) StatusBlocks() []cliapi.StatusBlock {
	st := store.LoadState()
	_, doc := controlplane.State()
	return []cliapi.StatusBlock{bindingBlock(st), chainBlock(st, doc)}
}

// bindingBlock is the tier binding table of the default profile.
func bindingBlock(st *domain.State) cliapi.StatusBlock {
	block := cliapi.StatusBlock{Rank: rankBlockBinding,
		Title: i18n.T("role bindings   profile {name}", i18n.A{"name": st.DefaultProfile})}
	pr, err := store.LoadProfile(st.DefaultProfile)
	if err != nil {
		block.Lines = append(block.Lines,
			style.Item(style.Warn, i18n.T("cannot read the default profile {name}: {err}",
				i18n.A{"name": st.DefaultProfile, "err": err.Error()})))
		return block
	}
	provs, _ := store.LoadProviders()
	t := style.NewTable(i18n.T("Role", nil), i18n.T("Binding", nil), i18n.T("Note", nil))
	for _, role := range domain.Roles {
		b, ok := pr.Resolve(role)
		if !ok {
			t.Row(style.Dim(role), style.Dim(i18n.T("unbound", nil)), "")
			continue
		}
		note := ""
		if provs != nil {
			if p, exists := provs.Providers[b.Provider]; !exists {
				note = style.Red(i18n.T("provider is not defined", nil))
			} else if p.Key() == "" {
				note = style.Yellow(i18n.T("api_key missing", nil))
			}
		}
		t.Row(style.Cyan(role), b.String(), note)
	}
	block.Lines = append(block.Lines, strings.TrimRight(t.String(), "\n"))
	return block
}

// chainBlock is the complete candidate chain of the normal tier.
func chainBlock(st *domain.State, doc *controlplane.Doc) cliapi.StatusBlock {
	block := cliapi.StatusBlock{Rank: rankBlockChain}
	snap, err := store.Load()
	if err != nil {
		return block
	}
	block.Title = i18n.T("fallback chain   role normal, tried in order", nil)
	// MaxSteps: 0 -- `status` shows the **complete candidate chain**; maxAttempts is the execution
	// limit of one request, not membership. Passing Attempts() silently trims everything after
	// the 4th stop, which is how the perception problem "why is there no ark in the normal tier"
	// (measured 2026-09-17: ark is the 4th stop, trimmed here) came about. The fact of truncation
	// is stated in the hint line below, rather than making it look like it is not on the chain.
	steps, skips := resolve.BuildChain("normal", snap.Profiles, snap.Providers, resolve.Opts{
		Active: st.DefaultProfile, Available: doc.Available(), Rank: doc.Rank(),
		MaxSteps: 0})
	if len(steps) == 0 {
		block.Lines = append(block.Lines,
			style.Item(style.Warn, i18n.T("no usable candidate   newgate tier normal", nil)))
		return block
	}
	block.Lines = append(block.Lines, strings.TrimRight(bindingChain(steps, "  "), "\n"))
	if limit := st.Chain.Attempts(); limit < len(steps) {
		block.Lines = append(block.Lines, style.Hint(i18n.T(
			"a request tries at most the first {limit} stops; the remaining {rest} stay in the chain (details: newgate tier normal)",
			i18n.A{"limit": limit, "rest": len(steps) - limit})))
	}
	if tail := chainTail(steps, skips); tail != "" {
		block.Lines = append(block.Lines, style.Hint(tail))
	}
	return block
}

// chainTail is a one-sentence summary of the chain's tail:
// how many candidates were skipped, and where to read why.
func chainTail(steps []resolve.Step, skips []resolve.Skip) string {
	if len(skips) == 0 {
		return i18n.N("{n} stop in the chain", "{n} stops in the chain",
			len(steps), i18n.A{"n": len(steps)})
	}
	reasons := map[string]int{}
	for _, s := range skips {
		reasons[skipKind(s)]++
	}
	var parts []string
	for _, k := range skipKindOrder {
		if n := reasons[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", skipLabel(k), n))
		}
	}
	return i18n.N("{stops} stop in the chain; {skipped} skipped ({reasons})   newgate tier normal",
		"{stops} stops in the chain; {skipped} skipped ({reasons})   newgate tier normal",
		len(steps), i18n.A{"stops": len(steps), "skipped": len(skips),
			"reasons": strings.Join(parts, " · ")})
}

// ---------- raw material for the diagnostics package ----------

// Dump hands out the config's own two source texts:
// providers.json (redacted) and all profiles' bindings.
//
// Why **redacted**: a diagnostics package is pasted into issues
// / shown to others, so keys report only their length and prefix.
func (reporter) Dump() []cliapi.DumpSection {
	return []cliapi.DumpSection{dumpProviders(), dumpBindings()}
}

func dumpProviders() cliapi.DumpSection {
	s := cliapi.DumpSection{Rank: rankDumpProviders, Title: i18n.T("providers.json (keys redacted)", nil)}
	provs, err := store.LoadProviders()
	if err != nil {
		s.Lines = append(s.Lines, "  "+i18n.T("cannot read: {err}", i18n.A{"err": err.Error()}))
		return s
	}
	for _, n := range sortedKeys(provs.Providers) {
		p := provs.Providers[n]
		k := i18n.T("(empty)", nil)
		if v := p.Key(); v != "" {
			if len(v) > 10 {
				k = v[:7] + "…" + i18n.T("{n} chars", i18n.A{"n": len(v)})
			} else {
				k = i18n.T("(too short)", nil)
			}
		}
		s.Lines = append(s.Lines, fmt.Sprintf("  %-14s %-45s protocol=%-10s key=%s",
			n, p.BaseURL, p.Protocol, k))
		// Upstreams where the two dialects split: report the other base too, otherwise
		// "where does claude's traffic actually go" is a black box on this screen.
		if p.AnthropicURL != "" {
			s.Lines = append(s.Lines, fmt.Sprintf("  %-14s %-45s %s", "", p.AnthropicURL,
				i18n.T("(the anthropic dialect goes to this one)", nil)))
		}
	}
	return s
}

func dumpBindings() cliapi.DumpSection {
	s := cliapi.DumpSection{Rank: rankDumpBindings, Title: i18n.T("bindings of all profiles", nil)}
	names, _ := store.ListProfiles()
	st := store.LoadState()
	for _, n := range names {
		mark := " "
		if n == st.DefaultProfile {
			mark = "*"
		}
		s.Lines = append(s.Lines, fmt.Sprintf(" %s %s", mark, n))
		pr, err := store.LoadProfile(n)
		if err != nil {
			continue
		}
		for _, role := range domain.Roles {
			if b, ok := pr.Resolve(role); ok {
				s.Lines = append(s.Lines, fmt.Sprintf("      %-8s %s/%s", role, b.Provider, b.Model))
			}
		}
	}
	return s
}

// sortedKeys: map keys in a stable order.
func sortedKeys(m map[string]domain.Provider) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// glossary contributes the "slot keys" line in the help screen's glossary.
//
// It is the vocabulary of **dynamic roles** (keys contributed by modules, written like
// tiers), so it belongs to this module. The UI used to have to know domain.ExtraRoles for
// this line, and remember to Refresh once first (see the comment below) -- that is timing
// this module should worry about, not something the UI should handle on its behalf.
type glossary struct{}

var _ cliapi.Glossarist = glossary{}

func (glossary) Glossary() []cliapi.GlossaryLine {
	// Refresh the dynamic role table right now. It is normally refreshed only in store.Load
	// (when installing a config snapshot), and `--help` installs no snapshot -- without the refresh
	// what is read here is always empty, and the example in the help line disappears quietly (measured
	// 2026-09-18: after switching to a live fetch the example was gone). A read failure does not
	// block help (fail-open); the worst case is that line degrading to the version without an example.
	_ = roleprov.Refresh()

	var keys []string
	for _, r := range domain.ExtraRoles() {
		if r.Source == "builtin" {
			continue // a builtin alias (normal→mid) is backwards compatibility, not a "role key"
		}
		keys = append(keys, r.Key)
	}
	def := i18n.T("a dynamic role contributed by a module, written like a role", nil)
	switch {
	case len(keys) > 2:
		keys = keys[:2]
		fallthrough
	case len(keys) > 0:
		def = i18n.T("a dynamic role contributed by a module ({keys}), written like a role",
			i18n.A{"keys": strings.Join(keys, " / ")})
	}
	// The wording of profiles and config files used to be hardcoded in the
	// UI (cli.usageText), but both are **this module's vocabulary**: how would the UI
	// know what providers.json is called, or where state.json is. After the move back
	// on 2026-09-18, all that section of the UI does is "read lines from the ledger".
	//
	// The directory and the file name go on **two lines**: combined into one it would be
	// 78 columns, over the layout limit of 75 (style.MaxColumns) -- and `style.Wrap` cuts
	// by display width and does not respect word boundaries, so it would break `state.json`
	// in the middle into `stat` / `e.json`, looking like a typo. Here one more line is
	// preferable. The directory is fetched live from paths.Config(): which one really takes
	// effect under a sandbox (NEWGATE_HOME) or a --home override is decided only here.
	return []cliapi.GlossaryLine{
		{Rank: 15, Term: i18n.T("profile", nil),
			Definition: i18n.T("a set of role → provider/model bindings", nil)},
		{Rank: 20, Term: i18n.T("slot key", nil), Definition: def},
		// The directory and file name are **paths**, not prose:
		// they must be pasteable verbatim, so they are given as is.
		{Rank: 30, Term: i18n.T("config dir", nil), Definition: paths.Config()},
		{Rank: 31, Term: i18n.T("config files", nil),
			Definition: "providers.json · mappings/*.kv · state.json"},
	}
}
