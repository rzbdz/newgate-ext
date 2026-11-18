package domain

import (
	"strings"
	"sync/atomic"

	"github.com/rzbdz/newgate/lib/i18n"
)

// ExtraRole is a **dynamic role key** outside the framework --
// contributed by a module; core does not know who specifically.
//
// Why this layer is needed: the tiers (heavy/normal/mid/light/vision) are a
// **capability ladder** shared by all clients; but some clients have their own finer
// slot system -- opencode + omo's intra-agent (sisyphus / librarian / ...), other
// tools' custom agents in future. The number, names and default bindings of these
// keys are **that module's** knowledge, and should not be hard-coded into core.
//
// So the split is:
//
//	a module (say the omo takeover) implements roleprov.Provider, saying which keys it has and which
//	  key each defaults to referencing;
//	core keeps one "key -> default ref" table, treated like built-in aliases.
//
// The spelling of key names in a profile is exactly the same as tiers (`omo-sisyphus =
// @normal`), because the resolution path is the same -- see CandidatesFor / resolve.BuildChain.
type ExtraRole struct {
	// Key the key name. Naming conflicts are the contributor's
	// responsibility (the name written into the profile is the same).
	Key string `json:"key"`
	// Source who contributed it (a module short name, e.g. "omo" / "builtin"). For humans only.
	Source string `json:"source"`
	// Default which key this one equals when it is **not written** in a profile (usually
	// some tier). Empty = no default; unwritten really means no candidate (sparse).
	Default string `json:"default,omitempty"`
	// Meta the contributor's own note; core does not interpret it (e.g. was / variant / suggested).
	Meta map[string]string `json:"meta,omitempty"`
}

// builtinAliases are the built-in role equivalence relations. The mechanism is exactly
// the same as a module-contributed ExtraRole, only the source is written in code
// -- so "backward compatibility for the four-tier split" needs no second code path.
//
// Its Meta (the "why" sentence for humans) is not written here; builtinAliasMeta attaches
// it live: this is a **package-level variable**, evaluated at init -- at that point modules/locale
// has not installed the language yet, and writing it in would stay in the source language forever.
var builtinAliases = []ExtraRole{{
	Key: "normal", Source: "builtin", Default: "mid",
}}

// builtinAliasMeta is the explanation to attach to
// a built-in alias's Meta (nil when there is none).
//
// Only the human-facing exit point (ExtraRoles) needs it: ExtraRoleOf
// is on the request hot path (every request with a dynamic role key goes
// through it), and there not even one map allocation should be added.
func builtinAliasMeta(key string) map[string]string {
	if key != "normal" {
		return nil
	}
	return map[string]string{"why": i18n.T(
		"four-tier compatibility: a profile that does not write normal behaves like mid", nil)}
}

// extraRoles are the dynamic role keys contributed by modules, **swapped atomically**.
//
// Two goroutines touch it at the same time, so it cannot be a bare package-level slice:
//
//	write: the daemon's config watcher goroutine, once a second (store.Reload -> store.Load ->
//	    roleprov.Refresh -> SetExtraRoles); config/diagnostics.go calls it too.
//	read: request goroutines (resolve.BuildChain -> CandidatesFor -> DefaultBindingFor ->
//	    ExtraRoleOf); every exchange carrying a dynamic role key in the request takes this read path.
//
// Before 2026-09-18 it was one bare assignment, and `-race` hit it 12 times. And the
// consequence is not just "reads a stale value": a slice header is three machine words (pointer
// + len + cap), and concurrent read/write can see a **torn header** -- a new pointer with an
// old length, so `range` walks out of bounds, at best resolving garbage as a known role key into
// a wrong binding, at worst panicking in a request goroutine. This whole repository's forwarding
// path is fail-open, and a panic in a request goroutine is exactly the worst kind of failure.
//
// Why an atomic.Pointer rather than a mutex: the reader is on the forwarding hot
// path, the writer only once a second. This is isomorphic to how store.Watcher
// handles the whole config snapshot (`cur atomic.Value` + atomic page swap)
// -- few writes, many reads, zero locks and zero copies on the read side.
var extraRoles atomic.Pointer[[]ExtraRole]

// SetExtraRoles installs the dynamic role keys contributed by modules. Called at start-up
// and on every config reload (see store.Load); nil goes back to "only built-in aliases".
//
// Copy before storing: the caller's (roleprov.Refresh) slice may be reused by
// it, and the copy the reader gets must never be rewritten -- after the page
// swap nobody may touch the old slice; that is the premise of lock-free reads.
func SetExtraRoles(rs []ExtraRole) {
	snapshot := make([]ExtraRole, len(rs))
	copy(snapshot, rs)
	extraRoles.Store(&snapshot)
}

// contributedRoles are the keys currently contributed by modules
// (empty when none). Read on the hot path, no lock, no copy -- the
// return value is read-only, nobody may modify it (see SetExtraRoles).
func contributedRoles() []ExtraRole {
	if p := extraRoles.Load(); p != nil {
		return *p
	}
	return nil
}

// ExtraRoles are all dynamic role keys right now (built-in
// aliases first, module ones after, sorted by key).
func ExtraRoles() []ExtraRole {
	mods := contributedRoles()
	out := make([]ExtraRole, 0, len(builtinAliases)+len(mods))
	for _, r := range builtinAliases {
		// The explanation is attached live (see builtinAliasMeta): copy the struct and
		// attach it, never write into that shared package-level variable -- it is read-only.
		r.Meta = builtinAliasMeta(r.Key)
		out = append(out, r)
	}
	// The module's copy is already sorted in Refresh, so no more sorting here -- but it is a shared
	// read-only slice, so it **must** be copied out, not appended to on behalf of the caller.
	out = append(out, mods...)
	return out
}

// ExtraRoleOf: is this key a dynamic role key.
func ExtraRoleOf(key string) (ExtraRole, bool) {
	for _, r := range builtinAliases {
		if r.Key == key {
			return r, true
		}
	}
	for _, r := range contributedRoles() {
		if r.Key == key {
			return r, true
		}
	}
	return ExtraRole{}, false
}

// AliasFor: when this key is not written in a profile, which **key** it equals ("" = not a
// key-to-key equivalence). For display/validation only; resolution goes through DefaultBindingFor.
func AliasFor(key string) string {
	r, ok := ExtraRoleOf(key)
	if !ok {
		return ""
	}
	d := strings.TrimSpace(r.Default)
	d = strings.TrimPrefix(d, "@")
	if d == "" || strings.Contains(d, "/") {
		return "" // 具体绑定（provider/模型）不是键的等价
	}
	return d
}

// DefaultBindingFor: when this key is not written in a profile, which binding it equals.
//
// Default supports three spellings; empty = no default (sparse, the whole level is skipped):
//
//	"@another key"   reference: expand that chain in place (omo-sisyphus = @normal: follows
//	                 the main tier, and moves when the tier moves)
//	"provider/model" a concrete binding: pinned to one real model (does not drift with tiers)
//	"another key"    equivalent to that key -- the built-in alias normal->mid is one such record
//
// All three are normalized into a Binding before returning, so **there
// is only one resolution path**: a reference is already a first-class citizen
// among candidates (chainBuilder.expand expands it, dedups, detects cycles), and
// aliases need not maintain a second path "go look for candidates in another key".
func DefaultBindingFor(key string) (Binding, bool) {
	r, ok := ExtraRoleOf(key)
	if !ok {
		return Binding{}, false
	}
	d := strings.TrimSpace(r.Default)
	if strings.HasPrefix(d, "@") {
		d = strings.TrimSpace(d[1:])
		if d == "" {
			return Binding{}, false
		}
		return Binding{Ref: d}, true
	}
	if d == "" {
		return Binding{}, false
	}
	if strings.Contains(d, "/") {
		if bd, err := ParseBindingString(d); err == nil {
			return bd, true
		}
		return Binding{}, false
	}
	return Binding{Ref: d}, true
}

// IsKnownRole: is the model field the client sent a **role key** (a tier or a dynamic role).
// If not, it is reverse-looked-up as a "concrete model name" (docs/04-configuration.md).
func IsKnownRole(s string) bool {
	if IsRole(s) {
		return true
	}
	_, ok := ExtraRoleOf(s)
	return ok
}

// TierLadder: the ladder tiers from weak to strong. vision is an
// **orthogonal** tier (seeing images does not mean being smarter), not on the
// ladder, so it cannot be reached from another tier by "going up one level".
var TierLadder = []string{"light", "mid", "normal", "heavy"}

// ShiftTier moves n levels on the ladder (clamped at the ends). A non-ladder
// tier (vision / unknown) is returned as is. Use: omo slots' `variant`
// (max/high/low) expresses "how hard this slot is driven", and when computing
// the suggested tier it is folded into the heft (docs/04-configuration.md).
func ShiftTier(tier string, n int) string {
	if n == 0 {
		return tier
	}
	i := -1
	for j, t := range TierLadder {
		if t == tier {
			i = j
			break
		}
	}
	if i < 0 {
		return tier
	}
	i += n
	if i < 0 {
		i = 0
	}
	if i >= len(TierLadder) {
		i = len(TierLadder) - 1
	}
	return TierLadder[i]
}
