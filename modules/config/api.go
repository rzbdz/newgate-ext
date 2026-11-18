package config

import (
	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/config/roleprov"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	modules "github.com/rzbdz/newgate/component"
)

// ExtraRole is a semantic tier a module can append; the alias lets
// port users avoid depending on the config implementation package.
type ExtraRole = domain.ExtraRole

// The four types of the on-disk schema + the .kv parser for profiles: for
// **distributions** (the config-sharing module generates config files on another
// machine and must validate by the same ruler first -- "use the daemon's own
// parser", rather than writing another roughly similar one, was their own words).
//
// Before 2026-09-20 config sharing lived in this repository, so store/domain could be
// used directly; after it moved to the distribution this dependency must go through the public
// surface, otherwise the distribution would be importing the kernel's implementation packages
// -- a dependency that breaks when the kernel is refactored, and breaks in another repository.
// The aliases only forward, they are not a new contract: the truth of the schema is still in
// domain, the truth of the parser still in store (same style as the ExtraRole/Step items above).
type (
	Provider  = domain.Provider
	Providers = domain.Providers
	Profile   = domain.Profile
	Binding   = domain.Binding
)

// ParseProfileKV parses a profile's .kv text (`name = value` line by line).
func ParseProfileKV(text string) (*Profile, error) { return store.ParseProfileKV(text) }

// Step is one step in the candidate chain after a successful parse.
type Step = resolve.Step

// Skip explains why some candidate did not make it into the
// final chain; it is for observability, not control flow.
type Skip = resolve.Skip

// RoleProvider lets modules contribute dynamic tiers to the config layer, keeping
// the origin for conflict diagnostics. The contract itself is defined in roleprov
// (the package holding the registry implementation); this only forwards the name.
type RoleProvider = roleprov.RoleProvider

// RoleWatchProvider is RoleProvider's optional capability,
// for declaring which files need a reload after they change.
type RoleWatchProvider = roleprov.RoleWatchProvider

// Chains is the answer to one "current chain state" query:
// the chain of every key of one profile at this moment.
//
// Why give **all keys** at once rather than asking key by key: the readers (the `/ui`
// aggregate home page, `newgate tier`) look at **one screen** -- "what this tier file currently
// resolves each tier to". Asking key by key, ten snapshots would read the disk at ten different
// moments (someone edits it in between, and the screen shows a config that is inconsistent with
// itself), whereas here one snapshot is computed once and that screen is **from one moment**.
type Chains struct {
	// Profile is which profile this answer is for (a machine value, not translated).
	Profile string
	// Default true means it is the one in effect right now (the chain head).
	Default bool
	// Keys map one to one to the input order,
	// given by the caller (`domain.Roles` first).
	Keys []Chain
}

// Chain is the resolution result of one tier under this profile.
type Chain struct {
	// Key is the tier name (heavy/normal/...) and may
	// also be a dynamic role key contributed by a module.
	Key string
	// Steps is the whole candidate chain, chain head included, in actual
	// attempt order. **Not truncated**: maxAttempts is the execution limit
	// of one request, not membership (see the MaxSteps: 0 part in chainsFor).
	Steps []Step
	// Skips are the candidates that did not make it into the chain, and why. A skipped candidate often
	// is not on the chain at all, so it must be handed out separately rather than inferred from Steps.
	Skips []Skip
	// Actions are **the changes possible on this tier**
	// ("swap this tier's chain head to X"), not data.
	//
	// They are built by this module, because only this module knows the other things
	// about it: which file this tier is written in, what that file's baseline is right now,
	// and what to tell the user when concurrent edits collide (a CAS conflict). The caller
	// (some UI section) only hangs them as is on its row -- a `view.Action` closure is safe
	// to pass across packages; in the same process it waits to be called by RunRowAction.
	//
	// Both empty cases are normal but mean different things: **this tier
	// has no alternative candidate** (the file has only one binding, or the
	// candidate is supplied by another profile), and **it is already the chain
	// head**. Neither should draw a button that changes nothing when clicked.
	Actions []view.Action
}

// Config is the config module's minimal outward port.
//
// The two paths each have their own use; do not mix them:
//   - RegisterRoleProvider is **write** -- a module registers its dynamic tier keys here;
//   - Chains is **read** -- it hands out resolve's conclusion (what
//     each tier resolves to at this moment, who was skipped, why).
//
// Consumers must not bypass the store/resolve boundary and modify config internal state
// directly; reads are the same: if the caller wants that conclusion, it comes here to
// get it, rather than importing store/resolve itself and assembling it (when the kernel
// is refactored, that dependency breaks in another repository, and breaks silently).
type Config interface {
	RegisterRoleProvider(RoleProvider) (modules.Release, error)

	// Chains reports all chains one tier file resolves to at this moment.
	//
	// keys are **the tier names to ask about** (empty = the `domain.Roles` set, most capable
	// first); an empty profile = the one that is globally default right now. A profile that does
	// not exist returns an error -- that is not "an empty config", it is the caller asking a
	// wrong name, and quietly giving an empty chain would make people think that tier is empty.
	//
	// It reads **what is on disk right now** (not the daemon's in-memory state), so the CLI and the
	// browser get the same facts; the health table (breaker removal, probe latency) is **not here**
	// -- that is a different ledger, and it changes over time, see Chain below. So Steps is "the
	// chain as resolved from the config", not "the chain that would really be taken right now".
	Chains(profile string, keys ...string) (*Chains, error)

	// AllChains reports the chains of **every** profile, the effective one first.
	//
	// It is the "see everything on one screen" path: what is wanted is "what my chains
	// look like", and that is one fact on **one screen**, not ten separate queries.
	// Asked separately, that screen would piece together ten moments of config -- each
	// card right on its own, inconsistent together, with nothing visibly odd in the UI.
	//
	// keys means the same as in Chains.
	AllChains(keys ...string) ([]*Chains, error)
}

// Capability is the config port's unique identity; there can be only
// one config implementation, so that multiple states do not fork.
var Capability = modules.NewCapability[Config]("config")
