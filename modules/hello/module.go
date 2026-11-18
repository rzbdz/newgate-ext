// Package hello is the smallest sample of a **distribution module**, and the entire content of
// the skeleton distribution (`dist-hello.json`): once the whole newgate framework is up, the
// `newgate` entry point prints one hello world.
//
// It does two things, exactly "the two things a module can do to a process":
//
//   - at Start, print one hello line to the assembly log -- proving it was installed;
//   - declare itself to the **entry ledger** (see component/entry) -- when no other entry
//     claims the invocation it goes to this one, so the "framework + hello" distribution runs
//     as one hello world.
//
// The second is where this sample's value lies: entries are not a dispatch hard-coded in the
// composition root (`app` knows no module), they are declared by the modules themselves.
// `newgate plugin` lists it in the `example` category, and `newgate` (with no other entry)
// arrives here.
package hello

import (
	"context"
	"fmt"
	"log"

	modules "github.com/rzbdz/newgate/component"
	entryapi "github.com/rzbdz/newgate/component/entry"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// TypeExample is the classification tag. The vocabulary belongs to the product layer (see the
// comment on component.Type); the values are given here: `newgate plugin` groups by it, and an
// unknown value does not error.
const TypeExample = "example"

// This sample has only two sentences, but both **go through the catalog** -- because the most
// expensive part of translation is not the tooling, it is the habit: whichever line a module
// copies the first time it writes copy decides how it looks from then on. The skeleton
// distribution (`dist-hello.json`) runs this very module, so it is also the template for "how
// to write one sentence".
//
// The rule (the kernel's `docs/13-i18n.md` is the authoritative text): write **English** (the
// source language) in source, `i18n.T("the English sentence", i18n.A{...})`, and Chinese lives in
// `modules/i18n/catalogs/zh-Hans.json`. The key is the English sentence itself, so there is no
// such thing as a "wrong key"; the price is that rewording orphans the old translation, which
// `tools/i18n check` reports.
//
// **Do not put the message in a constant and call `i18n.T(constant)`**: the scanner recognizes
// only string literals at the call site; a constant reference does not count -- that rule is
// there so "what the sentence looks like" is visible at the call site at a glance.

// New declares this component: log one line at start + declare a fallback process entry.
//
// The dependency is a **hard** one on the entry ledger (`Need`): with no ledger there is no
// one to declare to, and this module loses its point. Declaring it (rather than quietly
// MustGet-ing in Start) is what makes "remove entry" fail at graph time and name the missing
// port.
func New() modules.Component {
	// Declaration handle: a contribution taken in Start must be withdrawn in Stop (see the
	// comment on component.Release). Keep it as a closure variable rather than a package-level
	// one -- when the same module is installed twice (common in tests), a package-level
	// variable would make the second graph withdraw the first graph's registration.
	var release modules.Release

	return modules.Component{
		Name:     "hello",
		Type:     TypeExample,
		Requires: []modules.Requirement{modules.Need(entryapi.Capability)},
		Start: func(_ context.Context, ctx modules.Context) error {
			log.Printf("[hello] %s", i18n.T("hello from newgate-ext", nil))
			registry := modules.MustGet(ctx, entryapi.Capability)
			rel, err := registry.Register(entry{}, entryapi.DefaultRank)
			if err != nil {
				return err
			}
			release = rel
			return nil
		},
		Stop: func(context.Context) error {
			if release == nil {
				return nil
			}
			rel := release
			release = nil
			return rel()
		},
	}
}

// entry is this invocation's entry declaration: a **fallback** (entry.DefaultRank) -- it
// always claims.
//
// Only one declarer can exist per rank, and the kernel's `cli` is also at this rank (it is
// a fallback too: whoever claims nothing gets it). The two **must not be installed into the
// same graph**: which one Resolve picks would degrade to "the process behavior is decided by
// directory-name alphabetical order" (cli sorts before hello, so cli wins), and that is
// exactly the kind of implicit criterion this mechanism exists to avoid. The distribution's
// spec-level test guards this (testing/graph_test.go).
type entry struct{}

func (entry) Name() string { return "hello" }

// Claims is always true: the fallback entry's condition is "nobody claimed it".
func (entry) Claims(entryapi.Process) bool { return true }

// Handle is the skeleton distribution's entire behavior. It carries argv0 and the version:
// when debugging "which build am I running", these two are the first things to look at.
func (entry) Handle(p entryapi.Process) int {
	fmt.Println(i18n.T("hello world — {argv0} ({version})",
		i18n.A{"argv0": p.Argv0, "version": p.Version}))
	return 0
}
