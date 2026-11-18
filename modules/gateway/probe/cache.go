package probe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/gateway/dialect"
	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

type capabilityEntry struct {
	Supports uint32    `json:"supports"`
	Known    uint32    `json:"known"`
	Quirks   uint32    `json:"quirks"`
	Checked  time.Time `json:"checked_at"`
}

type capabilityCache struct {
	mu      sync.Mutex
	Targets map[string]capabilityEntry `json:"targets"`
}

// loadCapabilityCache reads back the dialect/quirk facts learned by the previous probe.
//
// A read failure (bad file, wrong permissions) returns an error but **still gives an empty
// cache**: the caller decides from that whether to "probe again (spending tokens)" or "run
// as is". This error used to be swallowed by `_ =`, and the symptom was "we probed, yet
// every time it probes again" - with the only clue (why it cannot be read) gone.
func loadCapabilityCache() (*capabilityCache, error) {
	c := &capabilityCache{Targets: map[string]capabilityEntry{}}
	raw, err := os.ReadFile(paths.ProbeCacheFile())
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil // never probed: perfectly normal
		}
		return c, i18n.Ef(err, "read capability cache {path}: {err}",
			i18n.A{"path": paths.ProbeCacheFile()})
	}
	if err := json.Unmarshal(raw, c); err != nil {
		return c, i18n.Ef(err, "parse capability cache {path}: {err}",
			i18n.A{"path": paths.ProbeCacheFile()})
	}
	if c.Targets == nil {
		c.Targets = map[string]capabilityEntry{}
	}
	return c, nil
}

func (c *capabilityCache) apply(t Target) bool {
	c.mu.Lock()
	entry, ok := c.Targets[t.String()]
	c.mu.Unlock()
	if !ok {
		return false
	}
	for _, cap := range []dialect.Cap{
		dialect.CapOpenAI, dialect.CapAnthropic, dialect.CapCountTokens,
	} {
		switch {
		case dialect.Cap(entry.Known)&cap == 0:
		case dialect.Cap(entry.Supports)&cap != 0:
			dialect.Mark(t.Provider, t.Model, cap)
		default:
			dialect.MarkUnsupported(t.Provider, t.Model, cap)
		}
	}
	if flags := quirk.Flag(entry.Quirks); flags != 0 {
		quirk.Default.Mark(t.Provider, t.Model, flags)
	}
	return true
}

// QuirksOf reports "which quirks of this target we know right now" in a shape that can be
// handed to other modules.
//
// Who needs it: the data plane itself (a 4xx hit while forwarding is as valuable as a
// conclusion we **probed**, and the probed copy is persisted while the hit copy used to live
// only in memory - see Shutdown in forward).
//
// The reported amount is itself "explored" (`quirk.Flags`) - ask the known quirk bits one by
// one instead of reading that table ourselves. **The table belongs to gateway/quirk**; we
// only borrow its criterion here, not copy an internal shape of it: when the table changes,
// this follows, and they cannot split.
//
// It only covers the in-memory table (`quirk.Default`), neither reading nor writing disk -
// persisting is the Load/Save pair's job, and "read the disk once to answer an in-memory
// question" would become IO on a hot path here (see the call site in forward.learnQuirks: it
// is on every upstream 4xx).
func QuirksOf(t Target) quirk.Flag {
	var flags quirk.Flag
	for _, f := range quirk.AllFlags() {
		if quirk.Default.Has(t.Provider, t.Model, f) {
			flags |= f
		}
	}
	return flags
}

// MergeQuirks merges "the quirks known right now" into the persisted cache.
//
// It is a **union, not an overwrite**: those bits in the cache may come from a real probe
// (that conclusion is fresher, because a probe hits the protocol the provider declares),
// while the merged-in ones come from errors really hit while forwarding - both hold for the
// same (provider, model), and neither should erase the other. Grow-only is the only safe way
// here: **recording one bit too few** means one more 400 after a restart (recoverable),
// while **erasing one bit too many** disables this patch for good (unrecoverable, and
// nothing points at it).
//
// When the disk was never read and holds no such target, a new entry is created: leaving
// `supports`/`known` at 0 is **correct** - those two describe dialect capability, and here
// there is not a byte of conclusion. Dropping it cannot make apply do the wrong thing: with
// `known` at 0 apply marks no dialect bit (see the first branch of that switch above).
//
// It writes this one cache only and never touches providers.json: that is the user's config
// file, and we should not quietly write into it (the same judgment as the note about "why
// the table is not persisted" at the top of gateway/quirk).
//
// It returns the number of targets left in this cache, for the caller to log.
func MergeQuirks(entries map[Target]quirk.Flag) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	c, _ := loadCapabilityCache()
	for t, flags := range entries {
		if flags == 0 {
			continue
		}
		// Keep the two dialect fields of an entry already on disk (see above); otherwise create one.
		e := c.Targets[t.String()]
		e.Quirks |= uint32(flags)
		e.Checked = time.Now()
		c.Targets[t.String()] = e
	}
	if err := c.save(); err != nil {
		return 0, err
	}
	return len(c.Targets), nil
}

// PendingQuirks are quirks that are **known but not yet persisted**.
//
// Why this layer exists: quirks hit while forwarding (learnQuirks) used to live only in
// memory, and this path has a window that **always recurs** - process restart. After a
// restart the in-memory table is empty, so "the first shot after an upgrade/restart" hits the
// same 400 again (what the user reported on 2026-09-22: the first Codex → GLM shot,
// `stream disconnected before completion`).
//
// It records only "not yet persisted": targets already written into the cache should not be
// rewritten over and over. The bits already on disk count as "persisted" here, so after a
// normal run the pending set is empty and persisting happens once, only when something new
// is really learned.
type PendingQuirks struct {
	mu      sync.Mutex
	entries map[Target]quirk.Flag
}

// Record notes "this target has these quirks right now". Returning true means this is
// newly learned (the caller logs on that, so the same fact does not print a line per request).
func (p *PendingQuirks) Record(t Target, flags quirk.Flag) bool {
	if t.Provider == "" || t.Model == "" || flags == 0 {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries == nil {
		p.entries = map[Target]quirk.Flag{}
	}
	if p.entries[t]&flags == flags {
		return false
	}
	p.entries[t] |= flags
	return true
}

// Flush persists the pending entries.
//
// Called once by the data plane at shutdown (see forward.Server.Shutdown). **It must be one
// atomic write**: the process is already at shutdown, and a file interrupted halfway reads
// back as a broken cache - exactly when "right after an upgrade" needs it to be right (the
// same reason as the "persist at shutdown" note in forward).
func (p *PendingQuirks) Flush() error {
	p.mu.Lock()
	entries := p.entries
	p.entries = nil
	p.mu.Unlock()
	if len(entries) == 0 {
		return nil
	}
	_, err := MergeQuirks(entries)
	return err
}

// DiskQuirks reports the targets on disk that carry quirk bits - startup uses it to answer
// "is there anything in the cache worth restoring", spending no tokens (same as
// LoadCachedCapabilities).
func DiskQuirks() []Target {
	c, _ := loadCapabilityCache()
	var out []Target
	for raw, e := range c.Targets {
		if e.Quirks != 0 {
			out = append(out, splitTarget(raw))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

func (c *capabilityCache) capture(t Target) {
	var supported, known dialect.Cap
	for _, cap := range []dialect.Cap{
		dialect.CapOpenAI, dialect.CapAnthropic, dialect.CapCountTokens,
	} {
		if ok, seen := dialect.Supports(t.Provider, t.Model, cap); seen {
			known |= cap
			if ok {
				supported |= cap
			}
		}
	}
	// Ask bit by bit rather than hardcoding one: `Flag` is a bitmask and the restore side
	// (apply) reads the whole mask, so missing one bit here means **that bit can never be
	// stored** (see quirk.AllFlags).
	var flags quirk.Flag
	for _, f := range quirk.AllFlags() {
		if quirk.Default.Has(t.Provider, t.Model, f) {
			flags |= f
		}
	}
	c.mu.Lock()
	c.Targets[t.String()] = capabilityEntry{
		Supports: uint32(supported), Known: uint32(known),
		Quirks: uint32(flags), Checked: time.Now(),
	}
	c.mu.Unlock()
}

func (c *capabilityCache) save() error {
	c.mu.Lock()
	raw, err := json.MarshalIndent(c, "", "  ")
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(paths.ProbeCacheFile()), 0o2770); err != nil {
		return err
	}
	tmp := paths.ProbeCacheFile() + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o660); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o660); err != nil {
		return err
	}
	return os.Rename(tmp, paths.ProbeCacheFile())
}

// LoadCachedCapabilities restores learned dialect/quirk facts without spending
// tokens. The daemon calls this on startup; probe calls it before deciding
// whether auxiliary checks are needed.
//
// On error it **restores whatever it can** (bad file = empty cache) and only hands back "why
// it could not be installed". The caller is the daemon, wired to the log outlet - if it stays
// quiet, the symptom is "after a restart every upstream must hit its 404 / 400 again before
// it is learned", and nobody knows the cause (the cache cannot be read).
func LoadCachedCapabilities() error {
	c, err := loadCapabilityCache()
	for raw := range c.Targets {
		parts := splitTarget(raw)
		c.apply(parts)
	}
	return err
}

// splitTarget is the inverse of Target.String(). The split rule is defined by dialect (both
// sides must agree: the same keys must yield the same (provider, model) in cache restore and
// in report display).
func splitTarget(raw string) Target {
	provider, model, ok := dialect.SplitKey(raw)
	if !ok {
		return Target{}
	}
	return Target{Provider: provider, Model: model}
}
