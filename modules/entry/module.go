// Package entry is **the owner of the entry ledger**: the one module the kernel knows,
// and the only necessary one.
//
// # It is the only necessary one
//
// Every other module is optional - core merely happens to ship general plugins (cli,
// gateway, wrapper...), and a distribution can turn any of them off or install none.
// **Only entry cannot be removed**, because it provides the **entry ledger** (the
// Registry in component/entry): whoever wants to be the entry declares itself (cli
// "default", wrapper "argv0 is a taken-over client", a future web-daemon "entry=web").
// The composition root does one Resolve; it knows neither cli nor wrapper nor web.
//
// With no entry declared, the process must say something human and exit; if the ledger
// were removable, three removals would leave a dead binary - its only reason to exist.
//
// # How "the only one known" is expressed (changed 2026-09-20)
//
// Not by naming and not by position. It sits in modules/, **exactly like** the others:
// scanned, listed in the assembly manifest, removable by name. One difference, derived:
//
//	the composition root needs the port it provides (that Resolve in app.Main) ->
//	removing it leaves nobody to answer "who owns this invocation"; Load refuses.
//
// So what the kernel knows is not "a module called entry" but **a port**; today one
// module provides it. If the composition root later depends on another port, the guard
// follows automatically - nobody has to remember to edit a list.
//
// It was not always so: it used to be called root, lived outside modules/ (go/root),
// was installed explicitly by the composition root, skipped scanning, and the selection
// hardcoded `case dir == "root"` - a module key hard-coded into the kernel, against
// "core provides the table, the product fills it", splitting "what is removable" in two.
//
// # It does no business of its own
//
// Start is empty (`return nil`): the ledger is a value bound statically into the graph
// at composition time, not a Start side effect, so order cannot affect this Resolve. It
// is not "the kernel's manager" - a graph node whose consumer is the composition root.
// The criterion is clear: it knows component and component/entry, and no business module.
//
// # Why the contract is not in this directory
//
// The entry contract (Process / Handler / Registry / Capability) lives in
// component/entry: the root must import it, and the root **may not** import anything
// under modules/ (all of it must stay removable and build - app/direction_test.go). The
// contract is the exception: this irremovable module provides it, so it cannot be removed.
//
// That is why the same word appears twice here: **component/entry is the contract,
// modules/entry is the module owning it** (this file aliases the contract as entryapi,
// so two entries in one file are not confusable).
package entry

import (
	"context"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	modules "github.com/rzbdz/newgate/component"
	entryapi "github.com/rzbdz/newgate/component/entry"
)

// TypeBuiltin is entry's classification label. The vocabulary belongs to the product
// layer (see component.Type's comment); only the value is declared here.
//
// Its **only** consumer today is the removal matrix: apart from the item carrying it,
// every item must be removable and must also load standalone (see app/matrix_test.go).
// It no longer feeds "can this be turned off" - that follows the root's dependencies.
const TypeBuiltin = "builtin"

// New declares the entry component.
//
// Requires is empty: entry depends on no module, and no module must depend on it -
// entries are declared voluntarily. A graph where nobody declares an entry is
// **legal** (only the data plane installed, driven by another process; or a skeleton
// distribution with just hello, where that distribution decides who answers `newgate`).
//
// Conversely, other modules may hard-depend on it (wrapper Needs the entry port) - a
// second reason it cannot be removed: composition then fails, naming who asked.
func New() modules.Component {
	table := entryapi.NewTable()
	return modules.Component{
		Name: "entry",
		Desc: func() string {
			return i18n.T("the entry ledger: every `newgate`/`claude` invocation is claimed here", nil)
		},
		Type: TypeBuiltin,
		Provides: []modules.Provision{
			modules.Provide(entryapi.Capability, entryapi.Registry(table)),
		},
		Start: func(context.Context, modules.Context) error { return nil },
	}
}
