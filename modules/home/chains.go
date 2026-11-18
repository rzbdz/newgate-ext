package home

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	configapi "github.com/rzbdz/newgate-ext/modules/config"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This file translates config's **read-port** conclusion (configapi.Config.AllChains) into
// the shape the UI wants (view.Chains). It is a pure function: one conclusion in, one piece of
// data out; it reads no disk (apart from that one stat for "which file the profile lives in")
// and never touches a clock.
//
// # Why this layer deserves to exist on its own, instead of config producing view.Chains
//
// Because they are **two questions**. config answers "what this tier resolves to right now",
// which has nothing to do with who is asking or on which screen (the command-line
// `newgate tier` wants the same conclusion); while "how a screen is laid out, how the line at
// the end of a chain reads, which actions hang on which row" is a **product trade-off**, and
// varies per distribution -- the kernel should not contain the name of the `home` module; this
// layer is that seam (see core/CLAUDE.md §0).
//
// # One snapshot, one moment per screen
//
// The all above comes from **one** AllChains call (see the comment on
// configapi.Config.AllChains): asking separately ten times assembles the config of ten
// moments, and nothing on screen looks wrong. So this **does not re-read the disk**; it only
// lays out the conclusion it was given; the one and only disk access fetches "which file this
// profile lives in", and that is not part of the chain's conclusion (it is a file address,
// unchanged by resolution).
//
// # Dependency direction
//
// The kernel imports are just three kinds: the **public contract** (configapi), shared leaves
// (paths / store, which core/CLAUDE.md says anyone may import directly), and the contracts
// themselves (lib/view, lib/i18n). The chain's **conclusion** always goes through the port --
// this does not import store/resolve to compute a chain itself; that kind of dependency snaps
// off in the other repository when the kernel is refactored, and no test there goes red.

// ---------- skips: summary only, never one row per skip ----------

// skipNote is the line at the end of a chain: "who else was skipped, and why".
//
// # One trade-off (same source as `newgate tier`, each writes its own wording)
//
// **Counts by category only**. Measured on one config: of 106 skips, 91 are "duplicate of an
// earlier candidate" -- laying out an inevitable dedup row by row only drowns the real
// conclusion (who gets used), and this page is structured as "one row per chain" anyway.
// Someone who wants each one in detail wants another door (`newgate tier <tier>` groups by
// category and gives each one a "so what"), not this.
//
// Category order is fixed (skipKindOrder): the same config run twice must produce this
// sentence word for word -- the chain card is recomputed over and over (every snapshot is
// one), and a sentence that follows map iteration order looks like "the config moved again".
func skipNote(skips []configapi.Skip) string {
	if len(skips) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, s := range skips {
		counts[skipKind(s)]++
	}
	parts := make([]string, 0, len(counts))
	for _, kind := range skipKindOrder {
		if n := counts[kind]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", skipLabel(kind), n))
		}
	}
	return i18n.N("{n} candidate did not make it: {reasons}",
		"{n} candidates did not make it: {reasons}", len(skips),
		i18n.A{"reasons": strings.Join(parts, " · ")})
}

// skipKindOrder is the **fixed order** of categories (only categories listed here are drawn).
//
// It is also a **contract table**: skipLabel below recognizes only these markers, and an
// unrecognized one (a category the kernel adds later) falls into the skipOther slot -- counted,
// just without a name of its own. The cost of one category missing from this table is that
// category being swallowed in silence, so chains_test.go has a ratchet here: once real skips
// are passed in, every category must appear in that sentence.
//
// # Where these markers come from
//
// They are not this module's vocabulary; they are the **protocol between resolve and the
// display layer**: stamped on `Skip.Kind` when the chain is built, and used by `newgate tier`
// to group and count, by the status block's last line, and here
// (see the comments on that group of constants in resolve/chain.go: they are machine markers
// and do not change with language).
//
// The literals are rewritten here rather than imported from resolve: the chain's conclusion
// always goes through the configapi port (see api.go -- the distribution importing resolve to
// assemble on its own is exactly the dependency the kernel calls out to avoid), and these
// strings are **protocol**, not implementation detail. A typo / a kernel rename is caught by
// the test: chains_test.go passes the real skips (resolved in a sandbox) through here and goes
// red if they do not line up.
const (
	skipExcluded    = "excluded"
	skipUndefined   = "undefined"
	skipNoKey       = "no-key"
	skipUnavailable = "unavailable"
	skipDisabled    = "disabled"
	skipMaxSteps    = "max-attempts"
	skipDuplicate   = "duplicate"
	skipCycle       = "cycle"
	skipOther       = "other"
)

var skipKindOrder = []string{
	skipExcluded, skipUndefined, skipNoKey, skipUnavailable, skipDisabled,
	skipMaxSteps, skipDuplicate, skipCycle, skipOther,
}

// skipKind returns a skip's category; one without a marker counts as "other" (which cannot
// happen today -- chains always stamp it; this is a fallback for hand-built Skips from outside).
func skipKind(s configapi.Skip) string {
	if s.Kind != "" {
		return s.Kind
	}
	return skipOther
}

// skipLabel is the category's wording **on this screen**.
//
// The wording is this module's own (each side writes its own, like `newgate tier`: that one is
// a statistic line in a terminal, this is a sentence at the end of a chain in a browser, with
// different readers and different room), but **the categories are the same set** -- both sides
// recognize the same batch of machine markers, so the same config counts the same number of
// entries in both UIs.
//
// Only this goes through i18n: the categories themselves are machine markers, and translating
// them leaves no criterion (the same note as in resolve).
func skipLabel(kind string) string {
	switch kind {
	case skipExcluded:
		// Not translated: it is the flag name in the config file (`excluded=true`), and it is
		// what the user greps for.
		return skipExcluded
	case skipUndefined:
		return i18n.T("not defined", nil)
	case skipNoKey:
		// Say the whole `api_key` rather than "no key": the field the user has to add is
		// called exactly that.
		return i18n.T("no api_key", nil)
	case skipUnavailable:
		return i18n.T("unavailable", nil)
	case skipDisabled:
		return i18n.T("disabled", nil)
	case skipMaxSteps:
		return i18n.T("past max attempts", nil)
	case skipDuplicate:
		return i18n.T("already on the chain", nil)
	case skipCycle:
		return i18n.T("a reference loop", nil)
	}
	return i18n.T("other", nil)
}

// ---------- which file this profile lives in ----------

// profileFile is this profile's file address, **relative to the config root** (empty = cannot
// be obtained).
//
// # Why this field has to exist
//
// The UI uses it to pair the "controls" half with the "raw text" half side by side (see
// view.ChainCard.File and fileOf in nav.ts), and the pairing criterion is **string equality**
// -- so the spelling here must match config's own cards: relative to the config root
// (paths.RelToRoot). With an absolute path here the symptom is the right column not showing
// while the left column is perfectly fine, looking merely like "the raw-text half was not
// done".
//
// # Why the order is ".kv first"
//
// One profile can have both .kv and .json, and **.kv wins** (store.LoadProfile's read order).
// Probing follows the same order here, falling back to .json -- if the two sides named
// different files, the user would edit one in the UI while resolution reads the other (no
// effect after the edit, even though the file really changed).
//
// Read once with store.LoadProfileRaw before taking the path: **only what reads back gets an
// address**. One that does not read back never appears on this card anyway (store.Load skips
// broken files), so this step is always true today; it is written out because it is the real
// criterion for "this file exists and is usable", while stat only answers "something with that
// name is there".
func profileFile(profile string) string {
	if _, err := store.LoadProfileRaw(profile); err != nil {
		return ""
	}
	for _, ext := range []string{".kv", ".json"} {
		p := filepath.Join(paths.Mappings(), profile+ext)
		if _, err := os.Stat(p); err == nil {
			return paths.RelToRoot(p)
		}
	}
	return ""
}
