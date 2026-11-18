// Package pluginmanager is the ledger of "which parts exist, what each can switch".
//
// It is an **ordinary module** (everything is module): no kernel hook, no whitelist,
// no init() special case; tools/genmodules scans the directory into the graph, on
// par with the other 15 modules. Its only peculiarity: "most modules choose to
// depend on it" - a module wanting runtime switches Needs this capability and
// calls RegisterSelf in its Start; not doing so is **legal**, no missing port.
// Exactly isomorphic to gateway.RegisterRequestHook's voluntary participation.
//
// Two kinds of fact, kept clearly apart - do not mix them:
//
//   - **Static classification** (Type) follows the component definition and lives
//     on component.Component, enumerable once assembled - no module help, no startup.
//     `newgate plugin` groups by it.
//   - **Runtime switch points** (Switch) cannot be static: their Paths front live
//     service dependencies (deepseek needs thinking/breaker, claudecode needs
//     thinkingapi.Service), so they can only be reported when a module Starts.
//
// **One dependency direction only**: modules/* -> pluginmanager -> component. This
// package imports no business module (cli/gateway least of all), so anyone can afford
// it; and no line in component knows this package exists. The hot path is one pure function.
package pluginmanager

import (
	"time"

	modules "github.com/rzbdz/newgate/component"
)

// Type is a module's classification label, used by `newgate plugin` for grouping.
//
// It is a **product concept**, so it is defined here, not in component: the kernel
// only requires "every component has a classification key" (Type non-empty); which
// ones exist is newgate's business - same rule as "module keys stay out of core".
type Type = modules.Type

const (
	TypeInfra   Type = "infra"   // foundation: config / confighook / breaker / plugin-manager
	TypeGateway Type = "gateway" // data plane: gateway
	TypeCLI     Type = "cli"     // command line: cli
	TypeClient  Type = "client"  // clients: claudecode / opencode / opencodeomo / runtime / wrapper
	TypeModel   Type = "model"   // model family: deepseek / glm / thinking
	TypeBridge  Type = "bridge"  // cross client x model: claudecode_deepseek / claudecode_glm

	// TypeOthers is where every classification **not in the table above** lands.
	TypeOthers Type = "others"
)

// DisplayOrder is the group order of `newgate plugin`.
//
// It is an **order table, not a whitelist**: unlisted Types land in TypeOthers, no error,
// no refusal. Why no closed-set check - classification is a product concept that grows
// with releases, and hard refusal costs "a new module crashes all of `newgate plugin`
// over a new word" when all it wanted was "others". So: convention plus slack - common
// ones get a row, rare ones fall to others; when one really grows, add a row.
func DisplayOrder() []Type {
	return []Type{TypeInfra, TypeGateway, TypeCLI, TypeClient, TypeModel, TypeBridge, TypeOthers}
}

// Danger is a switch point's danger level: CLI default limit and warning color, not decoration.
type Danger string

const (
	// DangerSafe: turning it off only loses an optimization, safe at any time.
	DangerSafe Danger = "safe"
	// DangerQuirk: turning it off makes an upstream start failing - it exists to fix that quirk.
	DangerQuirk Danger = "quirk"
	// DangerFootgun: off costs correctness or observability, **must** time-limit (Switch.TTL).
	DangerFootgun Danger = "footgun"
)

// Switch is a runtime switch point, reported by a module in its own Start.
//
// Path must be prefixed with the owning module's name (`<module>.<path>`), globally
// unique. That makes "whose switch" visible without a lookup, and self-verifiable.
type Switch struct {
	Path  string // "<module>.<path>", e.g. "deepseek.tail-shape"
	Title string // plain words
	Why   string // what happens if you turn it off

	Danger Danger

	// Default is the factory state. true = on from the factory, user can turn it off
	// (kill switch, read with Off()); false = off, opened explicitly (On()).
	Default bool

	// TTL is the CLI's default time limit for opening/closing it; 0 = none. A
	// DangerFootgun must be > 0, enforced at registration - structural, not discipline.
	TTL time.Duration
}

// Module is what one `newgate plugin` row needs: static classification + the
// module's reported switches. Type from the graph, Switches from the registry.
type Module struct {
	Name string
	Type Type
	// Desc is the module's own sentence (see component.Component.Desc); empty = none.
	// It stores the **evaluated** string: this is a UI snapshot, and a snapshot fixes
	// the language when it is made (same rule as view.Concept.Title).
	Desc     string
	Switches []Switch
}

// Manager is plugin-manager's public control surface.
//
// RegisterSelf is **voluntary**: call it for runtime switches, skip it otherwise.
// Skipping affects neither assembly nor listing in `newgate plugin` (the list comes
// from the graph) - such a module shows "no runtime switch (v1)": stated, not blank.
type Manager interface {
	// RegisterSelf: a module reports its own runtime switch points. name must match
	// Component.Name; switches may be empty (= nothing switchable, but still on the books).
	//
	// Duplicate module, cross-module Path collision, footgun without a limit: all fail on the spot.
	RegisterSelf(name string, switches []Switch) (modules.Release, error)

	// Lookup finds a switch point by Path.
	Lookup(path string) (Switch, bool)

	// Modules returns all modules (graph union registry) in start order; the CLI's only source.
	Modules() []Module
}

// **How the list gets in** (changed 2026-09-20): this module implements
// component.CatalogAware; the kernel hands the component list over once after assembly
// - the composition root no longer names SetCatalog.
//
// Why: the `MustGet(pluginmanagerapi.Capability).SetCatalog(...)` line in `app.New` was
// a **root-to-concrete-module** edge, and it made "plugin-manager can be removed" false
// (drop it, no compile). The kernel already held the list; now it finds the implementer.

// Capability is plugin-manager's port identity.
var Capability = modules.NewCapability[Manager]("plugin-manager")
