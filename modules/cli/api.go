package cli

import (
	ext "github.com/rzbdz/newgate-ext/modules/cli/extension"
)

// This file is the **type-alias forwarding layer for the contract**.
//
// The contract itself lives in modules/cli/extension (a leaf package); the reason
// is in that package's comment: if a module imported this heavy package (it drags runtime
// / gateway / breaker) just to get the `Command` interface, runtime's and gateway's tests
// could no longer reference client modules, and gateway could not register its own commands.
//
// **The ledger itself is on this package's service** (see module.go):
// the UI holds three ledgers, modules inject callbacks through
// RegisterXxx, and the UI loops to fetch data. The UI imports no module.
//
// Code in this package keeps using these short names, so nobody has to write ext. everywhere.
type (
	Diagnostic         = ext.Diagnostic
	DiagnosticProvider = ext.DiagnosticProvider
	StatusLine         = ext.StatusLine
	StatusProvider     = ext.StatusProvider
	StatusBlock        = ext.StatusBlock
	BlockProvider      = ext.BlockProvider
	Host               = ext.Host
	Command            = ext.Command
	HelpLine           = ext.HelpLine
	Documented         = ext.Documented
	Handoff            = ext.Handoff
	Unstyled           = ext.Unstyled
	Dumper             = ext.Dumper
	DumpSection        = ext.DumpSection
	Glossarist         = ext.Glossarist
	Verbose            = ext.Verbose
	GlossaryLine       = ext.GlossaryLine
	CLI                = ext.CLI
)

// Capability is the UI's port identity (the definition, along
// with the CLI interfaces, is in the extension package).
//
// One capability serves two things: modules use it to inject commands/diagnostics/status
// lines (RegisterXxx), and the diagnostic path uses it to read the UI's ledger. **Entry
// no longer goes through it**: the UI declares itself to root's entry ledger in Start
// (see Start in module.go), and the composition root asks the ledger, not this package.
var Capability = ext.Capability

// Forwarding of the help screen's section vocabulary (defined in
// extension; the reason is there: it is a **presentation concept**, and the
// presentation contract belongs to the extension leaf, not to some module).
type Section = ext.Section

const (
	SectionTakeover    = ext.SectionTakeover
	SectionRunOnce     = ext.SectionRunOnce
	SectionRouting     = ext.SectionRouting
	SectionObserve     = ext.SectionObserve
	SectionMaintenance = ext.SectionMaintenance
	SectionModules     = ext.SectionModules
	SectionUI          = ext.SectionUI
	SectionOthers      = ext.SectionOthers
)

// SectionPlan is the mapping from a batch of section names to the actually
// displayed sections, plus the display order (rules in extension).
type SectionPlan = ext.SectionPlan

// PlanSections plans once by the rules of generic slots / custom quotas / others fallback.
func PlanSections(declared []Section) SectionPlan { return ext.PlanSections(declared) }
