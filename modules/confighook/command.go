package confighook

// This file is `newgate agents`: listing known clients and their model slots.
//
// **Why it lives here** (2026-09-18): who an agent is, which slots it has, which
// environment variable a slot takes -- all **client descriptor** knowledge, and the
// descriptor registry belongs to this module (AgentCatalog). It used to be written
// in modules/cli/profile.go, so the UI had to know every field of AgentCatalog.
//
// Dependency direction: config-hook -> cli/extension (the leaf contract), not -> modules/cli.

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// agentsCommand is `newgate agents`.
type agentsCommand struct{ agents AgentCatalog }

var (
	_ cliapi.Command    = (*agentsCommand)(nil)
	_ cliapi.Documented = (*agentsCommand)(nil)
)

func (agentsCommand) Names() []string { return []string{"agents"} }

func (agentsCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionRouting, Rank: 30,
		Usage: "agents", Summary: i18n.T("known agents: profile, model slots and their current bindings", nil)}
}

func (c agentsCommand) Run(_ cliapi.Host, _ []string) int { return runAgents(c.agents) }

// cmdAgents known agents and their model slots.
//
// One section per agent: the header line is identity (dialect + current profile),
// the slots a table. It used to be one big shared table, and the description
// column was long enough that a single Chinese string knocked the alignment off.
func runAgents(agents AgentCatalog) int {
	names := agents.Names()
	sort.Strings(names)

	fmt.Println(style.Title("newgate agents", i18n.T("{n} total", i18n.A{"n": len(names)})))
	fmt.Println(style.Rule(72))

	for _, n := range names {
		a, _ := agents.Get(n)
		// The runtime facts this client submitted (may be none). Fetch once
		// and pass down: every ask may read the disk (see the distribution's
		// claudecode implementation), and here one agent asks several times.
		facts := agents.Facts(n)
		ap := AgentProfile{AgentID: a.ID}
		profile := ap.Active()
		source := i18n.T("follows the global default", nil)
		if ap.IsOverride() {
			source = i18n.T("set for this client only", nil)
		}
		fmt.Println()
		fmt.Println("  " + style.Bold(a.ID) +
			style.Dim("   "+i18n.T("{dialect} dialect", i18n.A{"dialect": a.Dialect})) +
			style.Dim("   profile ") + style.Cyan(profile) +
			style.Dim(" · "+source))
		if a.Notes != "" {
			fmt.Println(style.Hint(a.Notes))
		}
		if len(a.Slots) == 0 {
			fmt.Println(style.Hint(i18n.T("slots are discovered from the config file at startup", nil)))
		} else {
			// The profile at the chain head is read only once: every slot below needs its binding,
			// and disk read + parse are the two most expensive things on this screen. When
			// it cannot be read, the slot table is still listed (tiers are descriptor facts), only
			// the Binding column shows "unknown" -- one bad file does not swallow the whole table.
			pr, profileErr := store.LoadProfile(profile)
			if profileErr != nil {
				fmt.Println(style.Item(style.Warn, i18n.T("cannot read profile {name}: {err}",
					i18n.A{"name": profile, "err": profileErr.Error()})))
			}
			provs, _ := store.LoadProviders()
			t := style.NewTable(i18n.T("Slot", nil), i18n.T("Tier", nil), i18n.T("Binding", nil))
			for _, s := range a.Slots {
				// This column says "where it actually goes right
				// now" (if changed, the changed one, see AgentFacts).
				tier := TierOf(facts, s)
				t.Row(s.Name, style.Cyan(tier), slotBinding(pr, provs, tier))
			}
			fmt.Print(t.String())
			// The description goes on its own line: in the table it would stretch
			// the whole thing past a hundred columns and make it unreadable.
			//
			// "Changed or not" is on this line too, not in the table: the first version spliced it into
			// the tier cell, and one row ended up a dozen **full-width** characters wider; column widths
			// are counted in characters, and the whole table went crooked on the spot (measured). This
			// line is already "what this slot is for", and changed-or-not is the same class of footnote.
			for _, s := range a.Slots {
				note := ""
				if tier := TierOf(facts, s); tier != s.Tier {
					note += " · " + i18n.T("{tier} was set in the config (the default is {def})",
						i18n.A{"tier": tier, "def": s.Tier})
				}
				if s.Desc != "" || note != "" {
					fmt.Println(style.Hint(s.Name + "  " + s.Desc + note))
				}
				// The environment variable goes on its own line: it is an **unbreakable
				// identifier** (ANTHROPIC_DEFAULT_...), and appended to the description it
				// gets cut in the middle at 75 columns (measured: `ANT` / `HROPIC...`). This
				// line is the real setting for "injected via env" clients like Claude Code.
				if s.EnvVar != "" {
					fmt.Println(style.Hint(i18n.T("env {var}", i18n.A{"var": s.EnvVar})))
				}
			}
		}
		// Its own config file and whether it is taken over right now. Claude Code
		// goes through shim/env and has no Config (so nothing is printed here); codex /
		// opencode go through config files, and these two lines are the answer to "where
		// the settings land". The TUI's detail page reads the same Targets/IsTakenOver.
		if a.Config != nil {
			for _, target := range a.Config.Targets() {
				state := i18n.T("not taken over", nil)
				mark := style.Skip
				if a.Config.IsTakenOver(target) {
					state = i18n.T("taken over", nil)
					mark = style.OK
				}
				fmt.Println("  " + style.Dim(i18n.T("config", nil)) + "  " + filepath.Base(target) +
					"  " + style.Mark(mark) + " " + state)
			}
		}
	}

	fmt.Println()
	fmt.Println(style.Hint(i18n.T("switch a single agent only: newgate --set-profile <name> --agent <agent>", nil)))
	fmt.Println(style.Hint(i18n.T("or use newgate tui to do the same thing", nil)))
	return 0
}

// slotBinding resolves a slot's tier into "which model of which upstream it lands on".
//
// This is exactly the last hop the user comes to `newgate agents` for: a tier
// (normal) is semantics, while a binding (provider/model) is the real request target
// that will be sent. When it cannot resolve (profile broken, this tier unbound) it
// returns a clear placeholder rather than blank -- blank looks like "not configured"
// and "cannot be read" are the same thing, while the fix is completely different.
func slotBinding(pr *domain.Profile, provs *domain.Providers, tier string) string {
	if pr == nil {
		return style.Dim(i18n.T("unknown", nil))
	}
	b, ok := pr.Resolve(tier)
	if !ok {
		return style.Dim(i18n.T("unbound", nil))
	}
	out := b.String()
	if provs == nil || b.IsRef() {
		return out
	}
	p, exists := provs.Providers[b.Provider]
	switch {
	case !exists:
		out += " " + style.Red(i18n.T("provider is not defined", nil))
	case p.Key() == "":
		out += " " + style.Yellow(i18n.T("api_key missing", nil))
	}
	return out
}

// glossary contributes the "agent" line in the help screen's glossary.
//
// It is **client catalog** vocabulary (which CLIs are taken over), so it belongs to
// this module -- the UI need not know AgentCatalog to write it (see cliapi.Glossarist).
type glossary struct{ agents AgentCatalog }

var _ cliapi.Glossarist = glossary{}

func (g glossary) Glossary() []cliapi.GlossaryLine {
	names := g.agents.Names()
	list := i18n.T("(not assembled)", nil)
	switch {
	case len(names) == 0:
		list = i18n.T("(none)", nil)
	default:
		list = strings.Join(names, " / ")
	}
	return []cliapi.GlossaryLine{{Rank: 10, Term: "agent",
		Definition: i18n.T("CLIs taken over: {list}", i18n.A{"list": list})}}
}
