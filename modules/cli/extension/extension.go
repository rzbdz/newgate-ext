// Package extension is the set of interfaces modules implement
// to inject things into the UI, plus the UI's own port identity.
//
// # Why the contract is sunk into a leaf package
//
// The UI (modules/cli) is a heavy package: it imports runtime (daemon/takeover/injection),
// gateway (forward/special/probe/…) and breaker, because it handles the process
// lifecycle, data-plane assembly, and a pile of rendering. If a module imported it just
// to get the `Command` interface, it would drag all of that into its dependencies, so:
//
//   - whoever imports that module can no longer be referenced by runtime's / gateway's
//     **tests** -- those tests build client descriptors, and one import forms a cycle (measured:
//     the tests in `runtime/launch` and `runtime/takeover` failed to compile on the spot);
//   - gateway cannot register its own commands (`newgate st` was forced into the UI).
//
// After the contract was sunk, a module imports only this leaf plus component + config, the arrow
// is `modules/* → cli/extension → config`, and the cycle is gone. This is the **same rule** as
// gateway/api.go sinking the plugin contract into gateway/special (docs/03-architecture.md §3).
//
// # Where the ledger is
//
// **The ledger (the three Registries) is on modules/cli's own service**,
// which Provides the Capability. What a module gets is a set of registration
// callbacks; the UI loops over them to fetch data when dispatching commands and
// rendering status / doctor -- the UI **imports no module** and knows no one.
//
// (On 2026-09-18 the ledger was once moved into a standalone modules/surface module, which
// was superfluous: all it bought was the freedom for the UI to depend on modules, while the
// UI's goal is precisely **not to depend on anyone**. Folded back, it is one module fewer
// and one concept fewer, consistent with the model "the UI only offers injection points".)
//
// # Boundary
//
// This package **imports no config package**, only component: the contract
// types (command, status line, diagnostic, glossary, diagnostic material)
// are all pure data, with not one field carrying enterprise config semantics.
//
// Before 2026-09-18 it imported config/resolve here (`Host.PrintChain` took []Step), while the
// config root package needs to import this package (module injection uses these types), which is
// a mutual import -- that time it was dodged by "importing the resolve leaf". Later PrintChain was
// removed from Host (modules call config's renderer directly), and the import was no longer needed.
package extension

import (
	"strings"

	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Diagnostic is a group of structured state a module hands the CLI to display, so
// the module keeps the diagnostic knowledge and the CLI only orchestrates and prints.
type Diagnostic struct {
	// Rank decides row order (smaller first). Same as StatusLine.Rank: the order of health-check
	// items is a layout judgment, declared by the contributor, and the UI hardcodes none of them.
	Rank    int
	Label   string
	State   string
	Line    string
	Details []string
}

// DiagnosticProvider lets any module append doctor
// information without the CLI importing that module back.
type DiagnosticProvider interface {
	Diagnostics() []Diagnostic
}

// StatusLine is a line a module contributes to `newgate status` (field name + value).
//
// It is separate from Diagnostic because their **timings** differ: doctor
// is a health check a human runs deliberately and can expand details;
// status is an overview glanced at every time and should take one line.
type StatusLine struct {
	// Rank decides **row order** (smaller first). Equal Rank keeps registration order, stable.
	//
	// "Proxy goes on the first line" and the like are **layout** judgments, so they are
	// declared by the contributor rather than hardcoded by the UI -- once the UI hardcodes
	// "which row goes first", that row becomes evidence that the UI knows some module.
	Rank  int
	Label string
	Value string
}

// StatusBlock is a **pre-laid-out** piece of status
// output: an optional section heading + several lines.
//
// Why pre-rendering is allowed (rather than only reporting label/value): two blocks
// in `status` are tables (tier bindings, the fallback chain), and their layout and data
// are one know-how -- making the UI re-lay them out would move resolve's knowledge back
// into the UI, which is exactly what this round tears down. So the block's **content
// and layout** come from the owner, and the UI only decides **position** (Rank).
//
// It is an optional port: most modules only need StatusLine.
type StatusBlock struct {
	Rank int
	// Title empty = do not print a section heading (for continuation lines after the previous block).
	Title string
	// Lines are already laid out; printed verbatim, line by line.
	Lines []string
}

// BlockProvider lets a module contribute block content
// to `newgate status`; see StatusBlock for why.
type BlockProvider interface {
	StatusBlocks() []StatusBlock
}

// StatusProvider lets a module contribute one line to `newgate status`, same reason
// as DiagnosticProvider: the module keeps its own knowledge, the CLI only lays it out.
//
// Why it is needed (rather than having the CLI read some module directly):
// `status` must show "which switch points are non-factory", and that ledger belongs
// to plugin-manager. If the CLI read it for that line, that line would weld two modules
// together -- "whoever owns the state reports it" is the only way to undo that coupling.
type StatusProvider interface {
	// Status: the **contributor itself** loads the config snapshot
	// it needs (store.LoadState); the UI does not pass it.
	//
	// Why state is not passed in (2026-09-18): that would make the UI import
	// config/domain + config/store just to forward a state -- "the UI knows
	// config's internal structure" is exactly what this round tears down.
	// Whoever owns the knowledge fetches it; the UI only loops and lays out.
	Status() []StatusLine
}

// Host is the minimal set of CLI capabilities an extension command may use. It keeps Command from
// getting the whole CLI internals, and makes clear which interactions the main CLI still controls.
//
// Adding a method deserves care: it is the surface **every** module command can see. The test is
// "is this something only the CLI can do" -- not "this way I do not have to move the logic over".
type Host interface {
	Die(code int, message string) int

	// Verb is **by which name the dispatcher found me this time**.
	//
	// Why it is needed (added from measurement, 2026-09-18): the args `Command.Run` receives
	// carry no verb (see Command's contract), so "one command serving several verbs" loses
	// a piece of information. The previous workaround was to register one instance per verb,
	// each remembering its own name -- that path requires knowing **all verbs at registration
	// time**, and verbs grow at runtime (client modules register agents in their own Start),
	// so it has to either bet on assembly order or read someone else's registry in Start.
	// Neither belongs in Start (see the three-phase rule in docs/02-component-framework.md).
	//
	// Handing back "by which name you were found this time" lets the command **look
	// up** its verb table on the spot, registering just one command. When the host
	// has no dispatch concept (test stubs, direct calls) it returns the empty string.
	Verb() string
	// LiveRouting is also **not on Host** (moved out 2026-09-18): what it gives is the
	// breaker-table snapshot the daemon self-reports, which is data-plane/control-plane
	// data (Available / Rank in modules/gateway/controlplane.Doc), not
	// something "only the UI can do". Whoever wants it reads that leaf directly.
	NotifyProxy()

	// DaemonRunning: is the daemon running now (read the pidfile).
	//
	// Only the CLI can do this: it owns the process lifecycle (start/stop, graceful handover, when
	// the pidfile is read and written). What the command really wants to ask is "will anyone read
	// my change immediately" -- not running means it only landed on disk and takes effect next
	// start, and telling the user that plainly beats letting them think it already took effect.
	DaemonRunning() bool

	// The layout of chains and skips is **not on Host** (moved out 2026-09-18): that is config's
	// knowledge; whoever wants it calls modules/config's PrintChain / PrintSkips. Forwarding it
	// through the UI would only make the UI import config -- and the UI should know no module.

	// PrintThinkCache is **not on Host** (moved out 2026-09-18, same reason as LiveRouting):
	// it is gateway's thinkcache counters and layout, and whoever wants it renders it
	// from modules/gateway/thinkcache_status.go. It used to be on Host, so this interface
	// carried a method **named after a module** -- and the UI thus "knew" that module.
}

// Command is the command port a module contributes to the CLI; Names
// declares the routing names, Run executes the command's semantics.
//
// **args carries no command name**: when `newgate plugin deepseek off` dispatches to
// the `plugin` command, Run receives `["deepseek", "off"]` -- the verb has been stripped by
// the dispatcher. This was found by running the real binary on 2026-09-18: the contract did
// not state it, so people wrote indices starting at 1 by os.Args intuition, and the whole
// command was off by one (`plugin <module>` printed a list, `plugin <path> off 90s` reported
// "unknown verb 90s"). Things like the index base must be in the contract, not guessed.
//
// Multiple names in Names are aliases of **the same command**;
// dispatching to any of them gets args of the same shape.
type Command interface {
	Names() []string
	Run(Host, []string) int
}

// Command is a command a module registered. args are the raw arguments **after
// removing the command name** (options included); positional arguments are taken
// with Positional, switches with Flag / FlagValue -- do not count indices yourself.

// Section is one **slot name** on the help screen.
//
// It is just a string, taking effect by **convention**, not a closed set (settled 2026-09-18):
//
//   - the UI publishes a set of **generic slot keys** (the constants
//     below): they are categories that "make sense logically" (takeover /
//     routing / observation / maintenance …), and anyone may choose them;
//   - beyond the generic slots, a module may **make up a name** -- first come first served,
//     up to MaxCustomSections, ordered by first declaration after the generic slots;
//   - those that cannot get a quota (or never made up a name) all go into SectionOthers.
//
// Why not a closed enum: categorization is a convention and grows new members with installs,
// and the cost of hard rejection is "a new module using a new section name crashes the whole
// --help" -- and all it wanted was to be listed. Why a cap then: with too many slots help
// degenerates into a pile of one-line sections, which is no grouping at all. These two are
// the same design as pluginmanager's Type vocabulary (an ordered list + others fallback).
//
// The previous version had the UI maintain a list of "which sections are
// mine" (the switch in coreSection) -- **the UI needing edits because
// of someone else's section names**, exactly what this round tears down.
//
// # The key is identity, not copy (localization, 2026-09-20)
//
// Slot names used to be written directly as Chinese literals (`SectionTakeover` held the
// Chinese word for takeover), so one name played two roles: the grouping **key** (the
// test for plan.slot) and the on-screen **heading**. With i18n the two roles must split
// -- the key is identity and must not vary by language (if it did, "the Chinese name
// and English name of the same section" would be two different slots, and custom quotas,
// ordering, and the others fallback would all misalign), while the heading is copy.
//
// So keys are always stable ASCII markers (`"takeover"`), and the human-facing
// name goes through Display() to look up the catalog. Custom slots are keys
// modules made up, with no catalog entry, and Display() returns them as is.
type Section string

// Generic slot keys. The UI publishes them, modules choose
// them; the order is the display order (see PlanSections).
//
// The values are **stable markers**, not copy: changing a literal here
// is changing the protocol (custom quotas and ordering all compare by
// key), so they do not vary by language and should not be translated.
const (
	SectionTakeover    Section = "takeover"
	SectionRunOnce     Section = "once"
	SectionRouting     Section = "routing"
	SectionObserve     Section = "observe"
	SectionMaintenance Section = "maintenance"
	SectionModules     Section = "modules"
	SectionUI          Section = "ui"

	// SectionOthers is the **fallback slot**: those that cannot get
	// a quota, and those that never made up a name, all land here.
	SectionOthers Section = "others"
)

// Display is this slot's **human-facing** name (the key
// is identity; the display name varies by language).
//
// When the catalog has no entry (the source language, or this language not yet translated),
// i18n.T returns the English sentence written here, so the default path depends on no data
// file. A custom slot falls into default: its key was made up by a module, the UI does not know
// it, and it has no display name -- showing that key as is is the **correct degradation**, not
// an error (the same fail-open as PlanSections putting unfamiliar section names into others).
func (s Section) Display() string {
	switch s {
	case SectionTakeover:
		return i18n.T("Takeover", nil)
	case SectionRunOnce:
		return i18n.T("One-shot (keeps no global state)", nil)
	case SectionRouting:
		return i18n.T("Routing and configuration", nil)
	case SectionObserve:
		return i18n.T("Probing and observation", nil)
	case SectionMaintenance:
		return i18n.T("Maintenance", nil)
	case SectionModules:
		return i18n.T("Modules", nil)
	case SectionUI:
		return i18n.T("Interface", nil)
	case SectionOthers:
		return i18n.T("Other", nil)
	}
	return string(s) // custom slot: a key a module invented; show it as-is
}

// generalSections are the generic slots the UI publishes; the
// order is the display order. SectionOthers is always last.
var generalSections = []Section{
	SectionTakeover, SectionRunOnce, SectionRouting,
	SectionObserve, SectionMaintenance, SectionModules, SectionUI,
}

// MaxCustomSections is how many **custom sections** can fit beyond the generic slots.
//
// The cap exists because with many sections help amounts to no grouping;
// first-come-first-served because "whoever declares first takes the slot" is the only rule
// that needs no value judgment from the UI. When a newly installed module cannot get a quota,
// it falls into "other" and is still listed, it just no longer has a section of its own.
const MaxCustomSections = 4

// SectionPlan is the mapping from a batch of declared section names
// to **the sections actually displayed**, plus the display order.
//
// It is a product of one assembly (declaration order = command
// registration order), so the UI computes it fresh at render time.
type SectionPlan struct {
	order []Section
	slot  map[Section]Section
}

// PlanSections assigns each declared section name a landing place by the rules above.
//
// declared must be a **stable order** (command registration order), because custom
// sections are first come first served -- with an unstable order, the same install
// would render different help, and that kind of nondeterminism is the hardest to trace.
func PlanSections(declared []Section) SectionPlan {
	plan := SectionPlan{slot: map[Section]Section{}}
	var customs []Section
	for _, name := range declared {
		if _, done := plan.slot[name]; done {
			continue
		}
		switch {
		case name == "":
			// Not declared = no positional claim; straight to the fallback.
			plan.slot[name] = SectionOthers
		case isGeneralSection(name):
			plan.slot[name] = name
		case len(customs) < MaxCustomSections:
			customs = append(customs, name)
			plan.slot[name] = name
		default:
			plan.slot[name] = SectionOthers
		}
	}
	plan.order = append(plan.order, generalSections...)
	plan.order = append(plan.order, customs...)
	plan.order = append(plan.order, SectionOthers)
	return plan
}

// Slot is which section this declaration actually shows up in.
func (p SectionPlan) Slot(declared Section) Section {
	if s, ok := p.slot[declared]; ok {
		return s
	}
	return SectionOthers
}

// Order returns all sections that will appear (or may be empty), in display order.
func (p SectionPlan) Order() []Section { return append([]Section(nil), p.order...) }

func isGeneralSection(name Section) bool {
	for _, known := range generalSections {
		if name == known {
			return true
		}
	}
	return false
}

// FlagValue takes the value from `--name value` or `--name=value`; names are aliases of the
// same option, and with none of them the empty string is given. Along with Arg it is the
// shared part of argv parsing: written once per module, one copy will certainly be wrong.
func FlagValue(args []string, names ...string) string {
	for i, a := range args {
		for _, name := range names {
			if a == name && i+1 < len(args) {
				return args[i+1]
			}
			if strings.HasPrefix(a, name+"=") {
				return strings.TrimPrefix(a, name+"=")
			}
		}
	}
	return ""
}

// Positional takes the i-th **positional argument** (skipping those
// starting with `-`), and gives the empty string when out of range.
//
// **Must be this function; do not count indices** (measured 2026-09-18): the contract package
// once had a raw index accessor `Arg`, and `newgate start --force` then took `--force` as
// the agent name, reporting "unknown agent "--force"" -- **and doctor itself recommends
// that command**; the same class of scene also included `newgate probe --json` (`--json`
// taken as a profile name). Two names, two semantics side by side, and the next person
// will certainly step on the wrong one, so `Arg` has been deleted and only this remains.
//
// A positional argument cannot start with `-`: agent names, profile names, and
// subcommands are all in this set. Everything after `--` counts as a positional
// (use it to pass arguments starting with `-` through to a client), `--` itself
// is not one; a lone `-` still counts as a positional (conventionally stdin).
func Positional(args []string, i int) string {
	var seen int
	literal := false
	for _, a := range args {
		if !literal {
			if a == "--" {
				literal = true
				continue
			}
			if len(a) > 1 && a[0] == '-' {
				continue
			}
		}
		if seen == i {
			return a
		}
		seen++
	}
	return ""
}

// Flag reports whether this option is on the command line (after `--` there are no more options).
//
// It matches **exactly**: `--profile=ds` does not count as `--profile`; that
// value-bearing form uses FlagValue. What it replaces is three identical `hasFlag` copies
// scattered across config / gateway / runtime -- the same three lines copied three times,
// and missing one on a change means "this command's switch does not work at this layer".
func Flag(args []string, name string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == name {
			return true
		}
	}
	return false
}

// HelpLine is the one line a command occupies in `newgate --help`.
//
// **Both section and position are declared by the command; the UI knows
// no command.** The previous version was the reverse: the UI hardcoded a cmd(...)
// list, so every new module meant coming back to edit the UI -- after commands moved
// back into modules the UI was still hardcoding them, and "moving" meant nothing.
type HelpLine struct {
	// Section: which slot it belongs to (see the slot table above). When none fits, leave it
	// empty; it falls into SectionOthers -- that is **correct degradation**, not an error.
	Section Section
	// Rank decides **within-section** position: smaller first, equal Rank sorted stably by Usage.
	// The order of sections is decided by SectionOrder and is unaffected by Rank -- otherwise a
	// module could raise its own Rank and move a whole section, and slots would be meaningless.
	//
	// Why integers rather than an enum: the set of sections varies with the install, and an enum
	// would force framework changes. Leaving gaps (10/20/30…) is the room left for others to jump in.
	Rank int
	// Usage: the left column, the command syntax.
	Usage string
	// Summary: the right column, one sentence of conclusion.
	Summary string
}

// Documented is Command's optional partner: the command declares for itself how it looks in --help.
//
// Why it must exist: after commands moved back from the CLI's switch into modules,
// `newgate --help` can no longer hardcode those lines -- **hardcoding equals "the
// command moved, the CLI still knows it", which is moving for nothing**. Help is the
// other face of "which modules the CLI knows", and the two faces must move together.
//
// A command without it still works, it just takes no line in help. That is not a good
// state (a command the user cannot see is roughly a command that does not exist),
// so new commands should implement it -- but it is an optional interface, because it
// is a **presentation** concern and should not block a command from running first.
type Documented interface {
	Help() HelpLine
}

// CLI is both the dispatcher the process entry ultimately calls
// and the port through which modules inject their own piece.
//
// Sharing one interface for two things is deliberate: both are faces of "the UI"
// -- the entry hands it one command-line invocation (Run), and modules hang part
// of themselves on it (RegisterXxx). Splitting into two ports would only make
// every module Need twice, while they are always provided by the same component.
//
// **The composition root no longer calls it by name** (2026-09-20): the UI declares
// a Handler to root's entry ledger in its own Start (rank = entry.DefaultRank, i.e.
// "if nobody claims it, it is me"), and the composition root only asks the ledger "who does
// this invocation belong to". So swapping the UI (even for a web-daemon) takes zero changes
// in the composition root, and `app` no longer imports this package in a single line.
//
// Why injection must be Register rather than having modules Provide a "command
// port": the latter has no lifecycle. Once a module claims a command name
// nobody can undo it, and nobody checks duplicates -- two modules claiming the
// same name silently goes to the first (measured 2026-09-17). With Register,
// every injection gets a Release, released by the injector itself on Stop.
//
// **The read side is not here**: the command ledger, diagnostics, and status lines all belong
// to the UI itself, and it reads its own service directly, without going through the interface.
type CLI interface {
	// Run executes one command-line invocation. p is all the facts of **this
	// process invocation** (argv0 / args / env / version), set up and handed in by
	// the composition root -- having the UI read os.Args itself would copy "how the
	// process was invoked" once more, which is exactly why the entry package exists.
	//
	// argv0 dispatch (`newgate-<preset>` ≡ `newgate --preset <preset>`) happens
	// inside the UI, because it is **command-line syntax**, which belongs to the
	// UI; "does this invocation belong to the UI at all" is entry's business.
	Run(p entry.Process) int

	// RegisterCommand injects one command. The command names (Names) are the logical key for
	// duplicate checking; a clash errors on the spot rather than first come first served.
	RegisterCommand(Command) (modules.Release, error)
	// RegisterDiagnostics injects a group of doctor output. Diagnostics can stack,
	// with no key namespace, so no duplicate check -- only the token is recorded.
	RegisterDiagnostics(DiagnosticProvider) (modules.Release, error)
	// RegisterStatus injects several lines into `newgate status`, same reason as RegisterDiagnostics.
	RegisterStatus(StatusProvider) (modules.Release, error)
	// RegisterStatusBlocks injects block content (tables
	// and the like) into `newgate status`, see StatusBlock.
	RegisterStatusBlocks(BlockProvider) (modules.Release, error)
	// RegisterDump injects raw material into the diagnostic bundle (`newgate alllogs`), see Dumper.
	RegisterDump(Dumper) (modules.Release, error)
	// RegisterGlossary injects its own row into the help screen's glossary, see Glossarist.
	RegisterGlossary(Glossarist) (modules.Release, error)
	// RegisterVerbose reports "my verbose mode is on", see Verbose.
	RegisterVerbose(Verbose) (modules.Release, error)
}

// Verbose reports "we are in a mode where details need
// watching" -- some module turned on its own verbose switch.
//
// The UI uses it for one decision: whether to run a layout self-check on its own output (see
// modules/cli/layout_audit). Whoever owns a mode reports it -- the UI used to read gateway's state
// section directly to ask "is debug on", which was the UI knowing someone else's config structure.
type Verbose interface {
	Verbose() bool
}

// GlossaryLine is one row in the glossary at the end of the help screen (term + one sentence).
type GlossaryLine struct {
	Rank int
	Term string
	// Definition is one sentence; the data it carries (agent
	// names, slot keys) is fetched by the contributor itself.
	Definition string
}

// Glossarist lets a module contribute its own row to the glossary.
//
// **Why it is needed** (2026-09-18): two rows in the glossary are someone
// else's words -- "agent" belongs to config-hook (which clients are taken over),
// and "slot key" belongs to config (the dynamic roles modules contribute). The UI
// had to know AgentCatalog and domain.ExtraRoles for them, which is one more face
// of "the UI knows modules". Now each submits its own, and the UI only lays out.
//
// The UI's own rows (pure UI vocabulary like profile)
// are still written in the UI: they belong to no module.
type Glossarist interface {
	Glossary() []GlossaryLine
}

// DumpSection is one piece of **raw material** in the diagnostic bundle: heading + raw lines.
//
// How it splits from Diagnostic / StatusLine: those two are **one-sentence
// conclusions for human scanning**, while this is "the raw text you take out
// when something goes wrong" (config, logs, rewritten files). The diagnostic
// bundle (`newgate alllogs`) is those raw texts spliced together in order.
type DumpSection struct {
	Rank  int
	Title string
	// Lines: raw text, printed verbatim line by line (no layout
	// processing -- it may need to be pasted into an issue).
	Lines []string
}

// Dumper lets a module hand its own raw material into the diagnostic bundle.
//
// **Why it is needed** (2026-09-18): the diagnostic bundle used to live entirely in the UI,
// so assembling it meant the UI had to know providers.json's structure, the profile's tier
// table, which files takeover changed, where the logs are, and what the evidence files are
// called. All of that is each module's own knowledge. Now each submits its own raw text, and
// the UI only sorts and splices -- the third application of the same rule as status / doctor.
//
// It is an optional port: modules with no raw material to submit do not implement it.
type Dumper interface {
	Dump() []DumpSection
}

// Handoff marks that this command hands control to **another process** (wrapping a client launch).
//
// The UI uses it for one decision: these commands' output comes from that process and should
// not be audited for alignment against newgate's layout. That decision used to be guessed
// by the UI's own `detectLaunch` ("is the first token a known agent") -- which required the
// UI to know the agent table; now the command declares it, and the UI just reads the flag.
type Handoff interface {
	// HandsOff: does this command hand control to another process.
	HandsOff()
}

// Unstyled reports that **this one invocation** of the command has output that is
// not newgate's layout: raw config text (`profile kv`), byte-by-byte logs (`logs`),
// a full-screen UI (`tui`), JSON (`probe --json`). The layout audit skips it.
//
// **Why the command declares it rather than the UI listing names** (2026-09-18):
// the UI used to hardcode a `switch args[0]` name table ("__serve", "logs",
// "alllogs", "tui", "run", "probe", "profile") -- one more face of "the UI knows
// other people's commands", and every new command meant coming back to edit the
// UI. Now the command says "this time I am not layout", and the UI just asks.
//
// It is **dynamic** (takes args): the same command may be un-laid-out
// only in some uses (`probe --json`, or the `profile kv` subcommand).
type Unstyled interface {
	Unstyled(args []string) bool
}

// **BuildInfo was deleted (2026-09-20)**: link-time version information is now
// part of **process facts** (Version/BuildTime/CommitTime on entry.Process),
// set up once by the composition root and landed into the buildinfo leaf
// via `entry.MakeProcess`, and the UI **reads** it like any other module.
//
// Why it is no longer handed through Run's parameter: that parameter is the shape of "this
// one command-line invocation", while the version is a fact about "this binary" -- mixing
// the two in one type means "swap in another UI implementation" also has to redefine how the
// version is passed. Once deleted, Run receives only the pure invocation facts (see CLI.Run).

// Capability is the UI's port identity. Modules use it to inject their own commands and
// status lines; the process composition root uses it to hand this invocation to the UI.
var Capability = modules.NewCapability[CLI]("cli")
