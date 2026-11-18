package webdashboard

import (
	"os"
	"regexp"
	"sort"
	"testing"
)

// Which Kinds the kernel declares (`core/lib/view`) and which the front end can render
// (the `concept.kind === "…"` chain in `ConceptCard.svelte`) must **match up**.
//
// Why: `lib/view`'s Kind constants are a **contract** ("the front end knows only these;
// adding a module's face needs no front-end change"), yet the halves live in two repos -
// kernel declares, distribution's front end implements. Declared but unrendered: **silent** -
// the concept hits the fallback branch, the UI shows raw JSON. So `table` surfaced on
// 2026-09-20: long declared, no contributor, no renderer, and nothing went red.
//
// The other direction too: a front-end Kind the kernel does not know is **dead code**
// (never reached), yet it reads like "this UI supports X".
//
// Boundary: a **text-level** check (CI must be offline, no front-end runner): it sees
// "is the branch there", not "does it render right" - a human must look. Same trade-off
// as web_test.go (artifacts-in-git + Go gate) and i18n_test.go (dictionary text compare).

// kindConst grabs the Kind constants in the kernel: `KindTable = "table"`.
//
// Only `Kind`-prefixed ones with purely lowercase/hyphen values count: the kernel has
// other Kind constants (e.g. `KindConnError`) whose values are camelCase (`"conn_error"`),
// **another vocabulary** (events/classes), not a render mode. Matching by value beats lists.
var kindConst = regexp.MustCompile(`(?m)^\s*Kind[A-Za-z]*\s*=\s*"([a-z][a-z-]*)"`)

// kindBranch grabs a front-end render branch: `{:else if concept.kind === "code"}`.
var kindBranch = regexp.MustCompile(`concept\.kind === "([a-z][a-z-]*)"`)

func TestEveryDeclaredKindHasARenderer(t *testing.T) {
	declared := kindsIn(t, "../../lib/view/view.go", kindConst, "the kernel's Kind constants")
	rendered := kindsIn(t, "web/src/ConceptCard.svelte", kindBranch, "the front end's render branches")

	if len(declared) == 0 {
		t.Fatal("not a single Kind was parsed -- the regex no longer matches lib/view (renamed or written differently)")
	}
	if len(rendered) == 0 {
		t.Fatal("not a single render branch was parsed -- the regex no longer matches ConceptCard")
	}

	var missing, dead []string
	for k := range declared {
		if !rendered[k] {
			missing = append(missing, k)
		}
	}
	for k := range rendered {
		if !declared[k] {
			dead = append(dead, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(dead)

	for _, k := range missing {
		t.Errorf("the kernel declares Kind %q, the front end has no render branch -- that concept falls to the fallback branch,"+
			"and the UI shows a blob of raw JSON (that is how table looked on 2026-09-20)", k)
	}
	for _, k := range dead {
		t.Errorf("the front end has a render branch for Kind %q, but the kernel does not declare that Kind --"+
			"that branch is unreachable yet reads like \"the UI supports X\"", k)
	}
}

// kindsIn extracts the set from one file. what names things in errors (when a path is
// wrong, "cannot read file" is far more useful than "test failed").
func kindsIn(t *testing.T, path string, re *regexp.Regexp, what string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s (%s): %v\n"+
			"    core/ is a submodule; without checking it out this cannot be read -- CI uses submodules: recursive",
			path, what, err)
	}
	out := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = true
	}
	return out
}
