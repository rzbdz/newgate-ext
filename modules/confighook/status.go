package confighook

import (
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This file is the **per-client** block of `newgate status`.
//
// # Why this module reports it
//
// The client catalog (agents, slots, tiers) belongs here; the chain head (profile)
// belongs to config, but "which chain this client starts from" needs both sides - and
// this module is the spot (see profile.go).
//
// The previous status had only config's line: it named the global default and expanded an
// agent only when **the override differed from the default**. So "claude pinned ds while
// the default is also ds" was invisible in the terminal and in the TUI alike; users had to
// dig through state.json. Here every client is listed, profile marked honestly.
const rankStatusAgents = 25

type agentStatus struct{ agents AgentCatalog }

var _ cliapi.BlockProvider = agentStatus{}

func (s agentStatus) StatusBlocks() []cliapi.StatusBlock {
	names := s.agents.Names()
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	var lines []string
	for _, id := range names {
		a, ok := s.agents.Get(id)
		if !ok {
			continue
		}
		ap := AgentProfile{AgentID: a.ID}
		source := i18n.T("default", nil)
		if ap.IsOverride() {
			source = i18n.T("set", nil)
		}
		// Two lines per client, not a table: slot names are **unbreakable identifiers**,
		// and at 75 columns a shrinking table breaks `subagent` into `s / ubagent`
		// (observed 2026-10-28). Two lines split "who" from "which tiers", wrapping at spaces.
		lines = append(lines,
			style.Field(a.ID, style.Cyan(ap.Active())+style.Dim(" ("+source+")")),
			style.Hint(slotSummary(a.Slots, s.agents.Facts(a.ID))),
		)
	}
	return []cliapi.StatusBlock{{
		Rank:  rankStatusAgents,
		Title: i18n.T("Agents", nil),
		Lines: lines,
	}}
}

// slotSummary is a one-line summary of which tiers this client's slots are on.
//
// It reports **tiers**, not provider/model: resolving the latter is config's job (see
// the `newgate agents` table with Binding). status is an overview; spreading every
// binding out would drown the tier-binding table below - and tiers are what users edit.
func slotSummary(slots []Slot, facts AgentFacts) string {
	if len(slots) == 0 {
		return i18n.T("discovered from its config at takeover", nil)
	}
	parts := make([]string, 0, len(slots))
	for _, s := range slots {
		parts = append(parts, s.Name+"="+TierOf(facts, s))
	}
	return strings.Join(parts, "  ")
}
