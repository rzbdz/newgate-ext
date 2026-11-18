// Package quirk records "what is wrong with this upstream", learned from its own error site.
//
// # Why it is needed
//
// special_treatment plugins claim requests via Match, and Match only sees **static**
// information: model name, provider name, URL. Some quirks cannot be seen statically:
//
//	[1210][this model always thinks and cannot disable thinking; use low, high or max.]
//
// That sentence came back from glm-5.3 on a request **with tools**. Measured (the
// api.rvcompute.com aggregator + glm-5.3):
//
//	tools + no reasoning_effort          → 400 / 1210
//	tools + reasoning_effort=low|high    → 200
//	glm-5.2 / glm-4.7 with the same tools → 200
//
// The cause is basically certain: the aggregator sees tools and stuffs "disable thinking"
// in for the upstream itself (many models cannot do thinking and tools together), and
// glm-5.3 always thinks, so it refuses. We cannot change the aggregator, but one
// **explicit** reasoning_effort overrides the value it stuffed in.
//
// The model name alone cannot tell which model "always thinks" - that is a property of the
// model version, it changes, and no interface exposes it. So the strategy here is:
// **recognize the signature, write it down, carry the patch next time**. Two sources, both
// writing into the same registry:
//
//	Learn()  a forward hit a 4xx and the signature matched, so record it (one failure buys
//	         permanent immunity)
//	Probe()  newgate probe actively sends one shot and finds it, skipping that failure
//
// Memory only. A restart forgets it, and learning again costs one request. To make it more
// durable it should go into providers.json - but that is the user's config file, and we
// should not quietly write into it.
package quirk

import (
	"fmt"
	"strings"
	"sync"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Flag is one known quirk. A bitmask, so one (provider, model) can have several at once.
type Flag uint32

const (
	// NoThinkingDisable: this model does not accept "disable thinking"; it needs an explicit
	// thinking effort.
	NoThinkingDisable Flag = 1 << iota
)

func (f Flag) String() string {
	if f&NoThinkingDisable != 0 {
		return i18n.T("thinking cannot be disabled (an explicit reasoning_effort is required)", nil)
	}
	return i18n.T("unknown", nil)
}

// AllFlags is every known quirk bit, in definition order.
//
// **A new Flag constant must be added here at the same time.** There are two consumers, and
// both go wrong silently when one is missing:
//
//   - probe's capability cache (capture in cache.go) asks bit by bit "does this upstream
//     have this one" and persists it. The restore side (apply) reads the whole bitmask and
//     is generic - so a missing bit does not error, it means **that bit can never be
//     stored**: after a restart/reprobe the upstream quirk must be learned again by hitting
//     a 400.
//   - `Flag.String()`'s rendering (which bits get plain language).
//
// Before 2026-09-18 capture hardcoded NoThinkingDisable in one if line, which is exactly
// this shape: the day a second bit was added, persisting would silently lose one bit.
func AllFlags() []Flag {
	return []Flag{NoThinkingDisable}
}

// Table is this "upstream quirks" table.
//
// **It is an instance, not a package-level variable** (changed 2026-09-18): a package-level
// map is the classic service locator - another module (thinking's st-always) calling
// `quirk.Has(...)` directly would depend on state the gateway data plane wrote in, and that
// dependency is invisible in `Requires`, in the dependency graph and in the ratchet tests.
// docs/03-architecture.md §5 says outright "no new service locators", and this repo fixed
// exactly the same problem for breaker (see the note in modules/breaker/shape.go: the health
// table became a table instance precisely because tests need many, without polluting each
// other).
//
// Now gateway owns the single Default and **hands it over with each request** (see
// `Request.Quirks` in modules/gateway/special) - a module that wants it reads the field on
// the request in hand, i.e. what the gateway itself handed out, not a global it could route
// around.
type Table struct {
	mu         sync.RWMutex
	flags      map[string]Flag
	signatures []Signature
}

// Default is gateway's own copy (probe / forward / filling Request all use it).
//
// **It is gateway's internal convention, not for other modules**. To read this table from
// elsewhere, go through `special.Request.Quirks` (the one the gateway carries on each
// request) - that is a **declared** dependency (the consumer must already Need gateway's
// port), whereas writing `quirk.Default.Has(...)` here would reintroduce a service locator
// invisible in the dependency graph.
//
// Why not make it unexported: probe and forward are **sibling packages** of this module and
// want the same state, and Go's internal/ keeps gophers out while also keeping them from
// naming this type; what really must be prevented is "another module calling it directly",
// and that is handled by the proper route `special.Request.Quirks` plus the note below.
var Default = NewTable()

// NewTable builds an empty table (for isolation in tests).
func NewTable() *Table { return &Table{flags: map[string]Flag{}} }

func key(provider, model string) string { return provider + "/" + model }

// Mark records one entry. Returning true means this is **newly learned** - the caller logs
// on that, so the same fact does not print a line per request.
func (t *Table) Mark(provider, model string, f Flag) bool {
	k := key(provider, model)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.flags[k]&f == f {
		return false
	}
	t.flags[k] |= f
	return true
}

// RegisterSignature registers one criterion. A name collision (same Label) fails right there
// rather than silently going first-come-first-served - the same shape as the registration
// ports in gateway/special and breaker.
func (t *Table) RegisterSignature(sig Signature) (modules.Release, error) {
	if sig.Label == "" {
		return nil, fmt.Errorf("quirk: signature label is required")
	}
	if len(sig.Any) == 0 {
		return nil, fmt.Errorf("quirk: signature %s has no patterns", sig.Label)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, existing := range t.signatures {
		if existing.Label == sig.Label {
			return nil, fmt.Errorf("quirk: signature %s already registered", sig.Label)
		}
	}
	t.signatures = append(t.signatures, sig)
	return func() error {
		t.mu.Lock()
		defer t.mu.Unlock()
		// Find it by label and delete (indices shift as others unregister, so they are
		// unreliable).
		for i, existing := range t.signatures {
			if existing.Label == sig.Label {
				t.signatures = append(t.signatures[:i], t.signatures[i+1:]...)
				return nil
			}
		}
		return nil
	}, nil
}

// Has asks whether this (provider, model) has a certain quirk.
func (t *Table) Has(provider, model string, f Flag) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.flags[key(provider, model)]&f != 0
}

// Entry is for newgate status / doctor display.
type Entry struct {
	Provider string
	Model    string
	Flags    Flag
}

func (t *Table) Snapshot() []Entry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []Entry
	for k, f := range t.flags {
		i := strings.LastIndexByte(k, '/')
		if i < 0 {
			continue
		}
		out = append(out, Entry{Provider: k[:i], Model: k[i+1:], Flags: f})
	}
	return out
}

// Reset clears the table (test isolation; production code should not call it - this table is
// "one failure buys permanent immunity", and surviving across assemblies is **intended
// semantics**, not pollution).
func (t *Table) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.flags = map[string]Flag{}
}

// Signature is one rule: "the error reads like this → it means this quirk".
//
// **It is registered by the module owning that patch** (`RegisterSignature`), and quirk
// itself knows not one of them (changed 2026-09-18). This is the same rule as
// `gateway/special`'s st-<upstream>.go and `breaker.RegisterShapeDetector`, with the reason
// written in modules/deepseek/shape.go:
//
//	"Criteria stay in the module that knows the truth, so that module can test and evolve
//	 them, and no upstream-proprietary string ever appears in core."
//
// This used to hardcode the error text of GLM / DeepSeek / Kimi, so the only way to "add a
// new upstream" was to edit a gateway file - and that cost was paid once: before kimi's
// wording was added, "no signature matched, quirk could never learn it", and every
// background call hit the 400 again.
//
// The bar for a new rule (for the registering side): it must be **reproduced live** error
// text, and the patch must make semantic sense. Guessed rules add fields to a pile of
// unrelated requests - worse than no fix.
type Signature struct {
	// Flag is the quirk bit to record on a match (bits are defined in quirk, the registering
	// side explains their meaning).
	Flag Flag
	// Any matches when any one of these appears in the error text (lowercase comparison).
	Any []string
	// Label is the plain language written to the log once learned.
	Label string
}

// Learn learns from one failed forward. It only looks at 4xx - a 5xx is the upstream itself
// failing, unrelated to request shape.
//
// It returns what was learned (plain language, ready for the log); nil when nothing was
// learned. An unrecognized error is never guessed at: better to show the user the raw error
// than to add fields blindly.
func (t *Table) Learn(provider, model string, status int, body []byte) []string {
	if status < 400 || status >= 500 || len(body) == 0 {
		return nil
	}
	low := strings.ToLower(string(body))
	t.mu.RLock()
	sigs := append([]Signature(nil), t.signatures...)
	t.mu.RUnlock()
	var learned []string
	for _, sg := range sigs {
		hit := false
		for _, pat := range sg.Any {
			if strings.Contains(low, strings.ToLower(pat)) {
				hit = true
				break
			}
		}
		if hit && t.Mark(provider, model, sg.Flag) {
			learned = append(learned, sg.Label)
		}
	}
	return learned
}
