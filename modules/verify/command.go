package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/targets"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This file is `newgate verify [profile]`: really fingerprint every binding of one (or all)
// profiles, reporting "who the bank says this is" and the relation to the binding's name.
//
// vs probe: probe answers "reachable? which dialect?", verify answers "who is this".
// Both share config/targets' profile resolution (see the note there), so "which profiles,
// which tiers, which deduped upstreams" is known in exactly one place.
//
// Layout follows probe: progress on stderr (stdout holds the result table, so `verify > f`
// is clean), and with `--json` stdout carries only JSON.

type verifyCommand struct{ svc *service }

var (
	_ cliapi.Command    = (*verifyCommand)(nil)
	_ cliapi.Documented = (*verifyCommand)(nil)
	_ cliapi.Unstyled   = (*verifyCommand)(nil)
)

func (verifyCommand) Names() []string { return []string{"verify"} }

func (verifyCommand) Unstyled(args []string) bool {
	return cliapi.Flag(args, "--json") || cliapi.FlagValue(args, "--json") != ""
}

func (verifyCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionObserve, Rank: 41,
		Usage:   "verify [profile] [flags]",
		Summary: i18n.T("fingerprint each binding; one model with --provider/--model (also --api, --count, --json)", nil),
	}
}

func (c verifyCommand) Run(host cliapi.Host, args []string) int {
	return runVerify(host, c.svc, args)
}

// rowOut is one result row: the config view plus the fingerprint it points at.
//
// When several profiles share one binding the fingerprint runs once and every row cites
// the same conclusion (same as probe's Cached: saves time and quota).
type rowOut struct {
	Profile  string `json:"profile,omitempty"`
	Role     string `json:"role,omitempty"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// ConfigErr is a problem in the config itself (tier unbound, provider not declared).
	ConfigErr string `json:"config_error,omitempty"`
	// RunErr means the request never went out (no key, bank failed to load).
	RunErr string `json:"error,omitempty"`
	Entry  *Entry `json:"fingerprint,omitempty"`
}

type verifyOutcome struct {
	entry *Entry
	err   error
}

func runVerify(host cliapi.Host, svc *service, args []string) int {
	// `--help` must be caught here. The command framework only treats `help` as a verb, so
	// `verify --help` hands `--help` in as a flag - and this command's default is **really
	// fingerprinting all profiles** (tokens, tens of seconds). 2026-10-28: `verify --help`
	// started a full verification and only Ctrl-C stopped it.
	if cliapi.Flag(args, "--help") || cliapi.Flag(args, "-h") {
		h := verifyCommand{}.Help()
		fmt.Println("usage: newgate " + h.Usage)
		fmt.Println(h.Summary)
		return 0
	}
	asJSON := cliapi.Flag(args, "--json") || cliapi.FlagValue(args, "--json") != ""
	quiet := asJSON
	only := profileArg(args)
	provider := cliapi.FlagValue(args, "--provider")
	model := cliapi.FlagValue(args, "--model")
	api := cliapi.FlagValue(args, "--api")
	if api == "" {
		api = "auto"
	}
	count := 3
	if raw := cliapi.FlagValue(args, "--count"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 3 {
			return host.Die(64, i18n.T("--count must be 1, 2 or 3", nil))
		}
		count = n
	}
	if (provider == "") != (model == "") {
		return host.Die(64, i18n.T("give both --provider and --model, or neither", nil))
	}
	if provider != "" && only != "" {
		return host.Die(64, i18n.T("give either a profile or --provider/--model, not both", nil))
	}

	var rows []targets.Row
	var uniq []targets.Target
	if provider != "" {
		uniq = []targets.Target{{Provider: provider, Model: model}}
	} else {
		var err error
		rows, uniq, err = targets.Enumerate(targets.Options{Only: only})
		if err != nil {
			return host.Die(65, err.Error())
		}
	}

	started := time.Now()
	head := i18n.T("all profiles", nil)
	switch {
	case provider != "":
		head = provider + "/" + model
	case only != "":
		head = "profile " + only
	}
	if !quiet {
		fmt.Fprintf(os.Stderr, "%s\n", i18n.T(
			"verifying {n} targets (concurrency {c}; each target asks for {count} challenges)",
			i18n.A{"n": len(uniq), "c": 4, "count": count}))
		if !svc.cache.dataReady() {
			fmt.Fprintf(os.Stderr, "%s\n", style.Dim(i18n.T(
				"the first run downloads about 3.6 MB of reference data and caches it under {dir}",
				i18n.A{"dir": "~/.config/newgate/fingerprints"})))
		}
		fmt.Fprintln(os.Stderr)
	}

	var mu sync.Mutex
	got := map[targets.Target]verifyOutcome{}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, t := range uniq {
		wg.Add(1)
		go func(t targets.Target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			e, err := svc.Run(context.Background(), RunRequest{
				Provider: t.Provider, Model: t.Model, API: api, Count: count,
			})
			mu.Lock()
			got[t] = verifyOutcome{entry: e, err: err}
			mu.Unlock()
			if !quiet {
				fmt.Fprintln(os.Stderr, progressLine(t, e, err))
			}
		}(t)
	}
	wg.Wait()

	// Bind every config row to its fingerprint.
	out := make([]rowOut, 0, len(rows)+1)
	if provider != "" {
		o := rowOut{Provider: provider, Model: model}
		o.Entry, o.RunErr = outcomeOf(got, o.Provider, o.Model)
		out = append(out, o)
	} else {
		for _, r := range rows {
			// "This tier is unbound" is normal in a sparse profile; this screen is about
			// **models**, so spilling not-bound rows into the table buries the real rows
			// (same as the UI's groupProfiles).
			if r.Provider == "" && r.Model == "" {
				continue
			}
			o := rowOut{
				Profile: r.Profile, Role: r.Role, Provider: r.Provider, Model: r.Model,
				ConfigErr: r.Err,
			}
			if r.Provider != "" && r.Model != "" {
				o.Entry, o.RunErr = outcomeOf(got, o.Provider, o.Model)
			}
			out = append(out, o)
		}
	}

	failed := 0
	for _, o := range got {
		if o.err != nil || o.entry == nil || o.entry.Error != "" {
			failed++
		}
	}

	if asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		if failed > 0 {
			return 1
		}
		return 0
	}
	if len(out) == 0 {
		fmt.Println(style.Hint(i18n.T(
			"no bound models to verify — this profile only inherits or falls through", nil)))
		return 0
	}

	printVerifyTable(head, out, uniq, got, api, count, started)
	if failed > 0 {
		return 1
	}
	return 0
}

func outcomeOf(got map[targets.Target]verifyOutcome, provider, model string) (*Entry, string) {
	o := got[targets.Target{Provider: provider, Model: model}]
	msg := ""
	if o.err != nil {
		msg = firstLine(o.err.Error())
	}
	return o.entry, msg
}

// profileArg takes the first **real** positional argument.
//
// cliapi.Positional cannot be used directly: it only knows "starts with - is a flag" and
// does not know `vendor-model` in `--provider vendor-model` is that flag's **value**.
// 2026-10-28: `verify --provider vendor-model --model some-model` parsed "the profile is
// vendor-model" and complained that the tier file and --provider/--model cannot be mixed.
// The same bug makes `verify --count 1` verify a profile called `1`.
//
// So this skips only the value-taking flags this command knows; boolean flags like `--json`
// fall out on "starts with -". After `--` everything is positional (cliapi.Positional's rule).
func profileArg(args []string) string {
	takesValue := map[string]bool{
		"--provider": true, "--model": true, "--api": true, "--count": true,
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if takesValue[a] {
			i++ // skip its value: the value isn't a positional argument
			continue
		}
		if len(a) > 1 && a[0] == '-' {
			continue
		}
		return a
	}
	return ""
}

func progressLine(t targets.Target, e *Entry, err error) string {
	switch {
	case err != nil:
		return fmt.Sprintf("%s %s   %s", style.Mark(style.Bad), t, firstLine(err.Error()))
	case e == nil:
		return fmt.Sprintf("%s %s   %s", style.Mark(style.Bad), t, i18n.T("no result", nil))
	case e.Error != "":
		return fmt.Sprintf("%s %s   %s", style.Mark(style.Bad), t, firstLine(e.Error))
	default:
		return fmt.Sprintf("%s %s   %s  %s", style.Mark(style.OK), t,
			fingerprintLabel(e), matchLabel(e.Match))
	}
}

func printVerifyTable(head string, out []rowOut, uniq []targets.Target,
	got map[targets.Target]verifyOutcome, api string, count int, started time.Time) {
	fmt.Println(style.Title("newgate verify", i18n.N(
		"{head} · {n} target · {elapsed}", "{head} · {n} targets · {elapsed}",
		len(uniq), i18n.A{"head": head, "elapsed": time.Since(started).Round(time.Millisecond),
			"n": len(uniq)})))
	fmt.Println(style.Rule(72))

	t := style.NewTable(i18n.T("profile", nil), i18n.T("tier", nil),
		i18n.T("upstream/model", nil), i18n.T("fingerprint", nil),
		i18n.T("name match", nil), i18n.T("probability", nil))
	last := ""
	for _, r := range out {
		p := r.Profile
		if p == last {
			p = ""
		} else {
			last = r.Profile
		}
		fp, match, prob := "—", "—", "—"
		if r.Entry != nil {
			fp = fingerprintLabel(r.Entry)
			match = matchLabel(r.Entry.Match)
			if r.Entry.Probability != nil {
				prob = fmt.Sprintf("%.0f%%", *r.Entry.Probability*100)
			}
		}
		if r.ConfigErr != "" {
			fp, match = style.Dim(r.ConfigErr), style.Dim("—")
		}
		t.Row(p, r.Role, r.Provider+"/"+r.Model, fp, match, prob)
	}
	fmt.Print(t.String())

	// Warnings: mismatch/family/no ranking/request failed - **said once each** (one binding
	// may hang under several profiles, and four repeats just make people skip them).
	var warnings []string
	seen := map[targets.Target]bool{}
	for _, r := range out {
		if r.Provider == "" || r.Model == "" {
			continue
		}
		tg := targets.Target{Provider: r.Provider, Model: r.Model}
		if seen[tg] {
			continue
		}
		seen[tg] = true
		e := got[tg].entry
		err := got[tg].err
		switch {
		case err != nil:
			warnings = append(warnings, style.Item(style.Bad, i18n.T("{target}: {err}",
				i18n.A{"target": tg, "err": firstLine(err.Error())})))
		case e == nil:
			warnings = append(warnings, style.Item(style.Bad, i18n.T("{target}: no result",
				i18n.A{"target": tg})))
		case e.Error != "" && e.Predicted == "":
			warnings = append(warnings, style.Item(style.Bad, i18n.T("{target}: {err}",
				i18n.A{"target": tg, "err": firstLine(e.Error)})))
		case e.Match.Warning():
			warnings = append(warnings, style.Item(style.Warn, i18n.T(
				"{target}: fingerprint says {prediction}, but the binding asks for {model} ({match})",
				i18n.A{"target": tg, "prediction": fingerprintLabel(e),
					"model": e.Model, "match": matchLabel(e.Match)})))
		}
		if e != nil && e.CacheError != "" {
			warnings = append(warnings, style.Item(style.Warn, i18n.T(
				"{target}: verified, but the result could not be cached: {err}",
				i18n.A{"target": tg, "err": firstLine(e.CacheError)})))
		}
	}
	if len(warnings) > 0 {
		fmt.Println()
		for _, w := range warnings {
			fmt.Println(w)
		}
	}

	// Summary.
	var matched, dated, family, mismatch, unknown, bad int
	for _, o := range got {
		switch {
		case o.err != nil || o.entry == nil || o.entry.Error != "":
			bad++
		default:
			switch o.entry.Match {
			case MatchOK:
				matched++
			case MatchDated:
				dated++
			case MatchFamily:
				family++
			case MatchMismatch:
				mismatch++
			default:
				unknown++
			}
		}
	}
	fmt.Println()
	fmt.Println(style.Section(i18n.T("summary", nil)))
	fmt.Println(style.Bullet(i18n.T(
		"{n} matched · {dated} dated snapshot · {family} same family · {mismatch} different model · {unknown} no ranking · {bad} failed",
		i18n.A{"n": matched, "dated": dated, "family": family,
			"mismatch": mismatch, "unknown": unknown, "bad": bad})))
	fmt.Println(style.Hint(i18n.T(
		"fingerprints are evidence, not proof: fix the request parameters and repeat when it matters", nil)))
	if api != "auto" {
		fmt.Println(style.Hint(i18n.T("api: {api} · challenges per target: {count}",
			i18n.A{"api": api, "count": count})))
	} else {
		fmt.Println(style.Hint(i18n.T(
			"api: chosen from each provider's protocol · challenges per target: {count}",
			i18n.A{"count": count})))
	}
}

func fingerprintLabel(e *Entry) string {
	if e == nil {
		return "—"
	}
	switch {
	case e.Predicted != "" && e.PredictedName != "" && e.PredictedName != e.Predicted:
		return e.PredictedName + " (" + e.Predicted + ")"
	case e.Predicted != "":
		return e.Predicted
	case e.Error != "":
		return style.Dim(i18n.T("no ranking", nil))
	}
	return "—"
}

func matchLabel(m Match) string {
	switch m {
	case MatchOK:
		return style.Green(i18n.T("match", nil))
	case MatchDated:
		return style.Green(i18n.T("dated snapshot", nil))
	case MatchFamily:
		return style.Yellow(i18n.T("same family", nil))
	case MatchMismatch:
		return style.Yellow(i18n.T("different model", nil))
	case MatchUnknown:
		return style.Red(i18n.T("no ranking", nil))
	case MatchError:
		return style.Red(i18n.T("error", nil))
	}
	return string(m)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
