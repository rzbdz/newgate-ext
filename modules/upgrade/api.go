// Package upgrade is the registry and command for "each module upgrading its dynamic data".
//
// # What it solves
//
// Some modules depend on something **not in the binary**: the model reference bank that
// fingerprinting needs, a future remote config snapshot, a plugin's own database file.
// None should be tied to newgate's version - a new bank model should not force reinstall,
// a plugin swapping data should not wait for a release. But it is **the module's own
// knowledge**: only the owner knows where to get it, how to verify, how to roll back. So
// one seam: a module registers an upgrade action in its Start; `upgrade` runs them all.
//
// # The binary is an action too (2026-10-29)
//
// The first version only did dynamic data, and said "no binary swap, no git pull". The
// swap is now registered by `modules/self-update` as **one action in the same table** (ID
// `binary`): this module still knows nothing about Release download, build or daemon
// handover - that is that module's knowledge; only two things were added, for all actions:
//
//   - **Target version** (`newgate upgrade@v0.9.0`). Only Versioned actions get it; data
//     actions have no "version", so they are skipped with a note when one is given.
//   - **Check only** (`--check`) and **force** (`--force`). They ride in Request to each
//     action, whose meaning each implements (see the Request field comments).
//
// Why it lives in the distribution: what may be upgraded and which actions ship by default
// are product decisions; the kernel only provides the module framework. Without this module,
// verify and friends still work (just no manual refresh entry; the bank refreshes by TTL).
package upgrade

import (
	"context"

	modules "github.com/rzbdz/newgate/component"
)

// Name is this module's name in the assembly table and in `newgate plugin` (= the dir name).
const Name = "upgrade"

// Action is an upgrade action registered by a module.
//
// Title/Describe are **closures**, not strings: registration happens in each Start, when the
// language layer may not be installed yet (same reason as view.Title). Evaluation happens
// when `newgate upgrade` really lists or runs it.
type Action struct {
	// ID is a machine tag (a command can name one action, and output can sort by it). Not
	// translated; only lowercase letters, digits, `.`, `_`, `-`.
	ID string
	// Title is a one-line human-readable name.
	Title func() string
	// Describe says what this touches and what it costs (download? tokens?).
	Describe func() string
	// Run performs the upgrade. Result is **human-facing** (new version, what was downloaded)
	// and whether nothing was to be done; error = this one failed (the rest still run).
	Run func(ctx context.Context, req Request) (Result, error)
	// Versioned says this action understands Request.Version. Actions without it are skipped
	// when a version is given: handing `@v0.9.0` to a "latest only" action means it either
	// ignores it (silently) or each reports its own error - better said once, here.
	Versioned bool
	// Kind decides who it is asked together with (see Kind). The zero value is KindData.
	Kind Kind
}

// Kind is an action's class. `newgate upgrade` does not run silently: it checks first, then
// asks y/N **once per class** - swapping newgate and refreshing module data differ (the
// former restarts the daemon, the latter writes a file); one prompt gives no half of it.
type Kind int

const (
	// KindData: a module's own dynamic data/config (the fingerprint bank). Asked once together.
	KindData Kind = iota
	// KindProgram: swapping newgate itself (self-update's binary). Asked separately.
	KindProgram
)

// Result is one action's conclusion for this run.
type Result struct {
	// Message is printed verbatim by the command.
	Message string
	// UpToDate says "already latest / nothing to do". Actions that said so during the check
	// are not followed up with and not run - asking "upgrade?" then doing nothing wastes a confirm.
	UpToDate bool
}

// Done and Current: did something (or would, when checking) / already latest.
func Done(msg string) Result    { return Result{Message: msg} }
func Current(msg string) Result { return Result{Message: msg, UpToDate: true} }

// Request is this `newgate upgrade` run's parameters.
type Request struct {
	// Version is the value in `upgrade@<value>`: tag, branch or commit, handed to the action as
	// is. Empty = "latest".
	Version string
	// Check is look-only: report "what would happen / anything new", **change nothing** (no
	// download to the final place, no replace, no restart). An action that cannot do that says so.
	Check bool
	// Force allows reinstalling the same version and rolling back (`-f` also skips the prompt).
	Force bool
}

// Service is the upgrade action registry.
//
// Same shape as web-dashboard's extension endpoints and cli's command registry: hand in, get
// an undo, return it on Stop; duplicate names fail on the spot (when two modules claim one ID,
// the command only runs one of them, and the output does not show the missing one).
type Service interface {
	Register(a Action) (modules.Release, error)
	Actions() []Action
}

// Capability is its name in the assembly graph.
var Capability = modules.NewCapability[Service]("upgrade")
