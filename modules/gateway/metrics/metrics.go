// Package metrics holds the gateway's counters: every kind of "thing the gateway did to it"
// a request ran into on the way gets a tick -- interception (count_tokens answered locally,
// special rewrites, classifier reroutes), timeouts (first byte), swaps (chain fallback),
// cancellation.
//
// Why it is needed: newgate does more and more on the path (tier switching, disabling thinking,
// rerouting, count_tokens fallback, timeout swaps); each looks like an isolated case in the log,
// and only together do they show "is this chain healthy today, who is being killed wrongly".
// Tuning (say, whether 12s for a non-streaming first byte is too tight) needs data, not a hunch
// (the direction the user set on 2026-09-09).
//
// In memory only, back to zero when the daemon restarts -- the granularity is "this daemon's
// journey", the same philosophy as health/quirk/dialect. Names use dot-separated namespaces;
// anywhere else just Inc by name, no pre-registration.
package metrics

import (
	"sort"
	"sync"
)

// Default is the gateway process's global counters. Every hot path in the daemon shares it.
var Default = New()

type counters struct {
	mu sync.Mutex
	m  map[string]uint64
}

// New builds a separate set of counters (for tests; production uses Default).
func New() *counters { return &counters{m: map[string]uint64{}} }

// Inc records one tick. Concurrency safe; names need no pre-registration.
func (c *counters) Inc(name string) {
	c.mu.Lock()
	c.m[name]++
	c.mu.Unlock()
}

// Add accumulates a value.
func (c *counters) Add(name string, n uint64) {
	c.mu.Lock()
	c.m[name] += n
	c.mu.Unlock()
}

// Snapshot returns a snapshot sorted by name (stable display, easy to diff).
func (c *counters) Snapshot() map[string]uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]uint64, len(c.m))
	for k, v := range c.m {
		out[k] = v
	}
	return out
}

// Reset is for tests only.
func (c *counters) Reset() {
	c.mu.Lock()
	c.m = map[string]uint64{}
	c.mu.Unlock()
}

// SortedKeys gives the names in order (companion display for Snapshot).
func SortedKeys(m map[string]uint64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
