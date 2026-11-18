// Package cli is the command-line adapter between humans and the component graph.
//
// The CLI itself owns only argument dispatch, output, and process
// exit codes. Concrete modules contribute commands and diagnostics via RegisterCommand /
// RegisterDiagnostics, so adding features like OMO requires no central command registry.
// Host, in reverse, bounds what CLI capabilities an extension command may call.
//
// service obtains AgentCatalog and Runtime in Start and clears the references in Stop;
// extension contributions are managed by the Registry ledger, and each Release is kept by its
// contributor. main ultimately takes just this one CLI capability out of App and calls Run.
package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
	"github.com/rzbdz/newgate/lib/i18n"
)

// service is **the UI**: dispatch and layout.
//
// It owns the seven ledgers below (commands, diagnostics, status lines, status blocks, diagnostic
// material, glossary, verbose flags), and modules hang onto them in their own Start (they declare a
// weak dependency Optional(cli) on ui, so they always sort after the UI -- see component.Optional).
// The ledgers living on the UI is **right** -- it is "which things the UI recognizes" itself;
// on 2026-09-18 it was once sunk into a standalone modules/surface module, which was superfluous
// (see the package comment of cli/extension). The cycle that really needed breaking was not
// the ledger's location but the **dependency direction**: the UI may have no outgoing edges.
type service struct {

	// Seven ledgers: modules hang their own things in via RegisterXxx, and the UI loops over
	// them when dispatching commands and rendering status / doctor / alllogs. **The UI imports
	// no module**, so it knows no one -- other people's things are callbacks they injected.
	commands    modules.Registry[Command]
	diagnostics modules.Registry[DiagnosticProvider]
	statuses    modules.Registry[StatusProvider]
	blocks      modules.Registry[BlockProvider]
	dumps       modules.Registry[Dumper]
	glossary    modules.Registry[Glossarist]
	verboses    modules.Registry[Verbose]
}

var _ CLI = (*service)(nil)

// RegisterCommand injects one command; a command-name clash errors on the spot.
//
// The error text is i18n'd: this is a **plugin-author-facing** business conflict ("who took my
// command name"), not a framework-level assembly error, so it is consistent with user-visible
// copy. Duplicate checking runs inside Registry.Register's write lock, so concurrent
// injection of the same name yields only one winner -- first-come-first-served is the behavior
// this feature should least have (then "who took this name" is invisible in the manifest).
func (s *service) RegisterCommand(command Command) (modules.Release, error) {
	if command == nil {
		return nil, errCLI(i18n.E("a command cannot be nil", nil))
	}
	names := command.Names()
	if len(names) == 0 {
		return nil, errCLI(i18n.E("a command must declare at least one name (Names)", nil))
	}
	return s.commands.Register(command, func(existing []Command) error {
		for _, other := range existing {
			for _, have := range other.Names() {
				for _, want := range names {
					if have == want {
						return errCLI(i18n.E("command name {name} is already taken",
							i18n.A{"name": want}))
					}
				}
			}
		}
		return nil
	})
}

// errCLI prefixes assembly-time errors with `cli:`.
//
// The prefix is a **machine marker** (the component name), kept outside the message per
// the localization rule: the statement after it is what can be translated. %w rather than
// string concatenation, so i18n.ID can still pull the message identity out of this error
// -- the test asserts the identity (language-independent), not the rendered sentence.
func errCLI(err error) error { return fmt.Errorf("cli: %w", err) }

// RegisterDiagnostics injects a group of doctor output. Diagnostics stack; no duplicate check.
func (s *service) RegisterDiagnostics(provider DiagnosticProvider) (modules.Release, error) {
	if provider == nil {
		return nil, errCLI(i18n.E("a diagnostic provider cannot be nil", nil))
	}
	return s.diagnostics.Register(provider, nil)
}

// RegisterStatus injects several lines into `newgate status`.
// Same as diagnostics: stackable, no duplicate check.
//
// This port exists to **keep the UI from knowing modules**: `status`
// must show each module's own switch state, and that state belongs to each
// module. Whoever owns a state reports it -- the UI only loops and lays out.
func (s *service) RegisterStatus(provider StatusProvider) (modules.Release, error) {
	if provider == nil {
		return nil, errCLI(i18n.E("a status provider cannot be nil", nil))
	}
	return s.statuses.Register(provider, nil)
}

// RegisterStatusBlocks injects block content (tables and the like) into `newgate status`.
func (s *service) RegisterStatusBlocks(provider BlockProvider) (modules.Release, error) {
	if provider == nil {
		return nil, errCLI(i18n.E("a status block provider cannot be nil", nil))
	}
	return s.blocks.Register(provider, nil)
}

// RegisterDump injects raw material into the diagnostic bundle. Same reason as RegisterDiagnostics.
func (s *service) RegisterDump(dumper Dumper) (modules.Release, error) {
	if dumper == nil {
		return nil, errCLI(i18n.E("a dump provider cannot be nil", nil))
	}
	return s.dumps.Register(dumper, nil)
}

// RegisterGlossary injects its own row into the help
// screen's glossary. Same reason as RegisterDiagnostics.
func (s *service) RegisterGlossary(g Glossarist) (modules.Release, error) {
	if g == nil {
		return nil, errCLI(i18n.E("a glossary provider cannot be nil", nil))
	}
	return s.glossary.Register(g, nil)
}

// RegisterVerbose reports "my verbose mode is on". Same reason as RegisterDiagnostics.
func (s *service) RegisterVerbose(v Verbose) (modules.Release, error) {
	if v == nil {
		return nil, errCLI(i18n.E("a verbose provider cannot be nil", nil))
	}
	return s.verboses.Register(v, nil)
}

// verboseOn: does any module say it is in verbose mode.
func (s *service) verboseOn() bool {
	if s == nil {
		return false
	}
	for _, v := range s.verboses.All() {
		if v.Verbose() {
			return true
		}
	}
	return false
}

// Run enters unified command dispatch; module commands are fetched
// fresh from the ledger (not snapshotted at startup) -- the injector may
// register one step after the UI, and fetching fresh does not miss it.
//
// **argv0 syntax belongs to the UI** (`newgate-<preset>` ≡ `newgate --preset
// <preset>`, see docs/08-operations.md): the preset name is taken from the
// basename after the first `-`, without splitting (a preset name may contain `-`).
// "Does this invocation belong to the UI at all" is entry's business, not here.
func (s *service) Run(p entry.Process) int {
	args := p.Args
	if strings.HasPrefix(p.Argv0, "newgate-") {
		args = append([]string{"--preset", strings.TrimPrefix(p.Argv0, "newgate-")}, args...)
	}
	return runCLI(s, args)
}

// Claims is the test the entry declares itself with.
//
// The UI **always claims** (returns true): it is the one where "if nobody wants it,
// it is me", so it declares with entry.DefaultRank -- asked after every conditional
// entry. That is also why argv0 dispatch is not decided here: both `newgate`
// and `newgate-ds` belong to the UI, and that decision is UI-internal syntax.
func (s *service) Claims(entry.Process) bool { return true }

// Handle is where the entry's call lands, equivalent to
// the old "composition root takes the CLI and calls Run".
func (s *service) Handle(p entry.Process) int { return s.Run(p) }

// Name goes into logs and diagnostics (the composition root's `[entry] resolve: …` line).
func (s *service) Name() string { return "cli" }

// moduleCommand finds an injected command by name. Every alias in Names is a dispatch key.
func (s *service) moduleCommand(name string) (Command, bool) {
	for _, command := range s.commands.All() {
		for _, candidate := range command.Names() {
			if candidate == name {
				return command, true
			}
		}
	}
	return nil, false
}

// statusLines gathers all module-injected status lines,
// sorted by Rank (equal Rank keeps registration order).
func (s *service) statusLines() []StatusLine {
	var out []StatusLine
	for _, provider := range s.statuses.All() {
		out = append(out, provider.Status()...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}

// glossaryLines gathers module-contributed glossary rows, sorted by Rank.
func (s *service) glossaryLines() []GlossaryLine {
	var out []GlossaryLine
	for _, g := range s.glossary.All() {
		out = append(out, g.Glossary()...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}

// dumpSections gathers the diagnostic bundle's raw material, sorted by Rank.
func (s *service) dumpSections() []DumpSection {
	var out []DumpSection
	for _, dumper := range s.dumps.All() {
		out = append(out, dumper.Dump()...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}

// statusBlocks gathers all module-injected status blocks, sorted by Rank.
func (s *service) statusBlocks() []StatusBlock {
	var out []StatusBlock
	for _, provider := range s.blocks.All() {
		out = append(out, provider.StatusBlocks()...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}

// moduleDiagnostics gathers all module-injected health-check
// items, sorted by Rank (equal Rank keeps registration order).
func (s *service) moduleDiagnostics() []Diagnostic {
	var out []Diagnostic
	for _, provider := range s.diagnostics.All() {
		out = append(out, provider.Diagnostics()...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}

// New declares the final CLI entry and opens those seven ledgers to other modules.
//
// # Stop order: given by the dependency graph, injectors stop first, the UI last
//
// The dependency direction is **extension modules -> ui**, and it is an
// **ordering edge** (Optional(cli), see component.Optional). So the topological
// order puts cli before all injectors, and under reverse stopping the injectors'
// Stop has definitely finished before -- they withdraw their registrations while
// the UI is still there. Measured 2026-09-18 (17 components in the full graph):
//
//	start  [cli(1), breaker(2), config(3), …]        cli starts 1st
//	stop   [wrapper(17), …, breaker(2), cli(1)]      all injectors stopped, cli stops last
//
// This used to say "stop order is not guaranteed" -- that was true in the
// Inject era (it does not take part in ordering), measured then with breaker
// stopping **after** cli. Since going back through the ordering edge, that
// guarantee is back: **the UI's Stop need not assume anyone comes late**.
//
// But Stop still stays empty, for another reason unrelated to order: the UI's seven ledgers hold
// other people's callbacks, and withdrawing them is each module's Stop's job (each keeps its own
// Release). The UI has no resources of its own to release -- it is not a server. This module is
// one of the entry declarers (the default entry), with both interfaces pinned at compile time.
var (
	_ CLI           = (*service)(nil)
	_ entry.Handler = (*service)(nil)
)

func New() modules.Component {
	service := &service{}
	var self modules.Release
	// The UI's own commands go through the same ledger (see commands.go): duplicate checking,
	// dispatch, and help assembly have only one path, so run() is left with only orchestration.
	if err := service.registerOwnCommands(); err != nil {
		panic("cli: " + i18n.T("own command registration failed (assembly error): {err}",
			i18n.A{"err": err.Error()}))
	}
	return modules.Component{
		Name: "cli",
		Desc: func() string {
			return i18n.T("the terminal interface: dispatch, layout and injection points, with no business knowledge", nil)
		},
		Type: "cli",
		// **No Requires**: the UI depends on no module (see TestCLIDependenciesOnlyShrink
		// in app/default_test.go, which asserts this edge set is empty).
		//
		// All its capabilities come from injection: commands, status lines,
		// health-check items, glossary, diagnostic material -- seven ledgers built in Start, and
		// modules hang onto them in their own Start (they are Optional(cli), so they always sort
		// after the UI). Leaving not one outgoing edge is deliberate -- leaving one "we will
		// need it anyway" edge makes the other side's injection cyclic, which is exactly why
		// config/runtime/config-hook's commands were forced to stay in the UI before this round.
		//
		// It still Provides: the process composition root uses it to hand this invocation to the UI.
		Provides: []modules.Provision{
			modules.Provide(Capability, CLI(service)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			// Declare itself the **default entry**: what assembles this invocation is root's
			// ledger (built-in), and the composition root only asks the ledger "whose is it"
			// in main -- it does not know this module, and deleting this module still compiles
			// (then no entry claims it, and the process prints one plain sentence and exits).
			//
			// Get rather than MustGet is deliberate: MustGet's contract is "this port
			// is guaranteed to exist by a Need", while the UI **may have not one outgoing edge**
			// (the Requires comment above). The ledger is absent in only one case -- this graph
			// has no root (root is the only built-in, and in a normal build it is always there, see
			// app/matrix_test.go). Then it is handled fail-open: the UI itself works as usual, just
			// no entry claims this invocation; that is a **process-level** fact, and main prints
			// one plain sentence and exits, rather than becoming some module's startup error.
			//
			// Hard dependencies are declared on the wrapper side
			// (it is a business module, without the ui restriction).
			registry, ok := modules.Get(ctx, entry.Capability)
			if !ok {
				return nil
			}
			release, err := registry.Register(service, entry.DefaultRank)
			if err != nil {
				return err
			}
			self = release
			return nil
		},
		Stop: func(context.Context) error {
			if self == nil {
				return nil
			}
			return self()
		},
	}
}
