// Package dialect records "which API dialects this (provider, model) understands".
//
// # Why it is needed
//
// newgate's forwarding layer is a pure byte passthrough: whatever dialect the client sends
// goes upstream as-is (claude → anthropic /messages, opencode → openai /chat/completions),
// with no protocol conversion. The premise is that **upstream understands the
// client's dialect** - the aggregator (api.rvcompute.com, measured 2026-09)
// accepts both dialects, but anthropic's private endpoints may not be implemented:
//
//	POST /v1/messages → 200 (anthropic dialect proper) POST
//	/v1/messages/count_tokens → 404 "Invalid URL" (not implemented)
//
// count_tokens is Claude Code's context meter / auto-compaction threshold, and
// only upstream knows the true value. Understood, we forward and take the real
// value; otherwise we fall back to a local rough estimate - "is it understood" is
// recorded here per (provider, model), from two sources, both writing one registry:
//
//	Mark*() a 404 / 200 hit while forwarding, learned on the spot (a probe at the
//	          gate level: the first count_tokens is itself a probe, costing one
//	trip) probe pkg `newgate probe` actively sends both dialects + count_tokens,
//	          saving that first collision, and making it visible in the report
//
// The registry itself is **in memory only**, the same philosophy as quirk:
// a restart forgets it from this package's point of view, and re-learning
// costs one request (the daemon relearns on the first request).
//
// But one layer of persistence exists, and it lives in probe, not here: `newgate
// probe` writes what it learned into ~/.config/newgate/probe-capabilities.json
// (`probe.LoadCachedCapabilities` loads it when the daemon starts). So the accurate
// wording is "this package persists nothing; the probe persists what was learned" -
// before 2026-09-18 this comment said "forgotten on restart", but it does load back, and
// treating that as fact leads to wrong conclusions (such as re-probing on every restart).
//
// Both sides must be recorded: recording only "supported" means every "unsupported" upstream
// is hit with another 404 on every request. So Cap is both a capability bit and a probed bit -
// a zero bit in Known means "probed, explicitly unsupported", distinct from "not probed yet".
package dialect

import (
	"sort"
	"strings"
	"sync"
)

// Cap is one dialect capability. A bitmask; one (provider, model) can have several.
type Cap uint32

const (
	// CapOpenAI: /chat/completions (the openai dialect) is available.
	CapOpenAI Cap = 1 << iota
	// CapAnthropic: /messages (the anthropic dialect) is available.
	CapAnthropic
	// CapCountTokens: the anthropic dialect's /messages/count_tokens is available.
	CapCountTokens
)

func (c Cap) String() string {
	var parts []string
	if c&CapOpenAI != 0 {
		parts = append(parts, "openai")
	}
	if c&CapAnthropic != 0 {
		parts = append(parts, "anthropic")
	}
	if c&CapCountTokens != 0 {
		parts = append(parts, "count_tokens")
	}
	return strings.Join(parts, "+")
}

type entry struct {
	supported Cap
	known     Cap // bits that are 0 in known = probed and definitely unsupported
}

var (
	mu   sync.RWMutex
	caps = map[string]entry{}
)

func key(provider, model string) string { return provider + "/" + model }

// SplitKey is the inverse of key(): split "provider/model" back into two halves.
//
// Split at the **first** '/', not the last: provider is a flat key in config (measured ark
// / smt-deepseek / kimi ..., see providers.json), while model is upstream's own model id
// and **may contain '/'** (the `vendor/model` style used by openrouter). Before 2026-09-18
// Snapshot used LastIndexByte, so `a/openrouter/x` was reported as provider="a/openrouter"
// - while probe's cache restore used another implementation splitting at the
// first '/', so the two gave different (provider, model) for the same keys.
func SplitKey(k string) (provider, model string, ok bool) {
	i := strings.IndexByte(k, '/')
	if i <= 0 || i == len(k)-1 {
		return "", "", false
	}
	return k[:i], k[i+1:], true
}

// Mark records "supported". A true return means this was newly learned.
func Mark(provider, model string, c Cap) bool {
	return mark(provider, model, c, true)
}

// MarkUnsupported records "probed, explicitly unsupported". Supports then always returns false
// - unless upstream upgrades some day (then restart the daemon to re-probe, costing one request).
func MarkUnsupported(provider, model string, c Cap) bool {
	return mark(provider, model, c, false)
}

func mark(provider, model string, c Cap, ok bool) bool {
	k := key(provider, model)
	mu.Lock()
	defer mu.Unlock()
	e := caps[k]
	if ok {
		// changing from "unsupported" to "supported" is new information too (upstream upgraded)
		if e.supported&c == c && e.known&c == c {
			return false
		}
		e.supported |= c
		e.known |= c
		caps[k] = e
		return true
	}
	if e.known&c == c {
		return false
	}
	e.known |= c
	e.supported &^= c
	caps[k] = e
	return true
}

// Supports looks up one capability. known=false means not probed yet - the caller chooses
// the default behavior (count_tokens' default is to try once, see forward's lazy learning).
func Supports(provider, model string, c Cap) (ok, known bool) {
	mu.RLock()
	defer mu.RUnlock()
	e := caps[key(provider, model)]
	if e.known&c == 0 {
		return false, false
	}
	return e.supported&c != 0, true
}

// Entry is for newgate status / probe report display.
type Entry struct {
	Provider string
	Model    string
	Supports Cap
	Known    Cap
}

func Snapshot() []Entry {
	mu.RLock()
	defer mu.RUnlock()
	var out []Entry
	for k, e := range caps {
		provider, model, ok := SplitKey(k)
		if !ok {
			continue
		}
		out = append(out, Entry{Provider: provider, Model: model, Supports: e.supported, Known: e.known})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// Reset is for tests only.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	caps = map[string]entry{}
}
