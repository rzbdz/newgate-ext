// Package thinkcache helps clients hold on to reasoning they dropped themselves.
//
// # Why this layer must exist
//
// DeepSeek's thinking mode treats reasoning as **conversation state**: once a request carries
// tools, every assistant message in the history must bring that round's reasoning back
// verbatim, even for a round with no tool call, or it is a 400. The docs also say **verbatim**
// - truncation or summarizing is rejected on some versions too.
//
// The problem is clients cannot do it. Clients like Claude Code actively strip thinking blocks
// on non-official endpoints (they assume only officially signed blocks can be sent back), and
// opencode drops the reasoning_content field it does not recognize when serializing assistant
// messages into the context. Neither is fixable by configuration.
//
// Filling an empty string fooled the "is the field there" check, but that is the **worse**
// outcome: next round the model reads that it "thought nothing" last round, and the reasoning
// quality collapses. So the only correct approach is for the gateway to remember the reasoning
// the upstream produced and insert it back verbatim next round.
//
// Boundaries
//
//	Memory only. Reasoning is conversation content, and persisting it is a privacy decision we
//	should not quietly make for the user. A restart loses it, and at that point the caller can
//	only fall back to its own placeholder strategy (see reasoningFallback in
//	modules/deepseek/st-reasoning.go) - that round's reasoning really is lost, and the caller
//	must say so in the log rather than pretend it was filled in.
//
//	No logging. No path logs the content itself, only byte counts and the key's prefix.
//
//	Bounded. Double eviction by total bytes plus TTL, so it can never grow without limit.
package thinkcache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// Cache is an LRU capped by bytes with a TTL. Optionally attaches a disk cold tier (disk):
// on a hot-tier miss it looks in the cold tier, and on a hit promotes back into the hot tier -
// this is level two of the "three-level cache": the thinking block the client itself brings
// back is level one (see pickReasoning in st-deepseek), memory is level two, disk is level
// three.
type Cache struct {
	mu       sync.Mutex
	ll       *list.List // head of the list = most recently used
	items    map[string]*list.Element
	bytes    int64
	maxBytes int64
	ttl      time.Duration
	disk     *DiskStore  // optional on-disk cold tier (nil = memory only)
	onErr    func(error) // exit for cold-tier/parse failures, see SetErrorHandler

	hits, misses, puts, evictions uint64
	// unparsable counts "the assistant message in the request / its content cannot be parsed"
	// (see KeysForAssistantMessage). In the current implementation it ends up the same as
	// misses (neither hits), but the **cause and the handling differ completely**: a miss means
	// the cache really does not have it (look at the upstream and this process's lifecycle),
	// while unparsable means the JSON shape the client sent is one we do not recognize (look at
	// the client). Both only show as "an empty string was filled in" in the log, so they must be
	// counted apart, or that clue is cut.
	unparsable uint64
}

type entry struct {
	key  string
	blob []byte
	at   time.Time
}

// Default is the global instance. 64MB / 8 hours: measured, a full day-long session's
// reasoning is on the order of 1~2MB (see the dump sample: thinking blocks 4~11KB each), so
// 64MB holds dozens of sessions side by side; 8h covers a workday, and sessions from before a
// restart are recoverable within the same day.
var Default = New(64<<20, 8*time.Hour)

// New creates an independent in-memory cache; tests and non-default instances pick their own
// capacity and TTL with it.
func New(maxBytes int64, ttl time.Duration) *Cache {
	return &Cache{
		ll:       list.New(),
		items:    make(map[string]*list.Element),
		maxBytes: maxBytes,
		ttl:      ttl,
	}
}

// fail reports a failure that "does not change this request's result but changes the next
// one's behavior" (the cold tier cannot be written, a message in the request cannot be parsed).
// Dropped when no outlet is installed - this layer is best-effort.
func (c *Cache) fail(err error) {
	if c == nil || err == nil {
		return
	}
	c.mu.Lock()
	fn := c.onErr
	c.mu.Unlock()
	if fn != nil {
		fn(err)
	}
}

// SetErrorHandler injects the outlet for persist/parse failures (the same shape as
// breaker.SetErrorHandler).
//
// Why it is needed: this layer is best-effort and failure does not affect correctness - but
// it decides "can the previous process's reasoning still be found after a restart", which is
// exactly what users come to ask about (CLAUDE.md §3.1: when the cold tier is broken, the
// first big request after a restart gets a 400 "must be passed back" because the chain of
// thought cannot be filled in). Before 2026-09-18 every compaction step's failure was a
// `return` / `continue` that left no trace, and the only symptom was "all reasoning gone after
// a restart".
//
// The daemon calls it after AttachDisk (serve.go); not installing it means dropping, as
// before.
func SetErrorHandler(fn func(error)) {
	Default.mu.Lock()
	Default.onErr = fn
	d := Default.disk
	Default.mu.Unlock()
	if d != nil {
		d.mu.Lock()
		d.onErr = fn
		d.mu.Unlock()
	}
}

// AttachDisk attaches the disk cold tier (level three of the three-level cache) to the global
// Default and backfills the records already on disk into the hot memory tier - the only entry
// that finds the previous process's reasoning again after a daemon restart. The path is
// ~/.config/newgate/thinkcache.bin (see paths.ThinkCacheFile).
//
// On failure (no permission / disk not writable) it returns err, and the caller decides on the
// degradation: continue memory-only, and those rounds' reasoning text is unrecoverable after a
// restart - persisting must never be what takes the proxy down.
func AttachDisk(path string, maxBytes int64) error {
	d, err := openDisk(path, maxBytes, Default.ttl)
	if err != nil {
		return err
	}
	Default.mu.Lock()
	Default.disk = d
	Default.mu.Unlock()

	now := time.Now()
	for k := range d.snapshot() {
		if blob, ok := d.get(k); ok {
			Default.putMem([]string{k}, blob, now)
		}
	}
	return nil
}

// Put hangs one block of reasoning on several keys.
//
// One answer may give several tool_calls at once, and when the client sends the next round
// back we only need to recognize **any one** id to find this reasoning again, so the same
// content hangs on several keys. Extra keys each count their bytes (evict early rather than do
// complicated reference counting).
func (c *Cache) Put(keys []string, blob []byte) {
	if len(keys) == 0 || len(blob) == 0 {
		return
	}
	// Copy it: the caller's buffer gets reused
	cp := make([]byte, len(blob))
	copy(cp, blob)

	now := time.Now()
	c.putMem(keys, cp, now)
	if c.disk != nil {
		c.disk.appendKeys(keys, cp, now)
	}
}

// putMem writes only the hot memory tier. Backfilling the disk cold tier goes through here, to
// avoid the "backfill writes back to disk" loop.
func (c *Cache) putMem(keys []string, cp []byte, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range keys {
		if k == "" {
			continue
		}
		if el, ok := c.items[k]; ok {
			e := el.Value.(*entry)
			c.bytes += int64(len(cp)) - int64(len(e.blob))
			e.blob, e.at = cp, now
			c.ll.MoveToFront(el)
			continue
		}
		el := c.ll.PushFront(&entry{key: k, blob: cp, at: now})
		c.items[k] = el
		c.bytes += int64(len(cp))
	}
	c.puts++
	c.evictLocked(now)
}

// Get fetches reasoning. An expired entry counts as absent; on a hot-tier miss it looks in the
// cold tier and promotes a hit back into the hot tier.
func (c *Cache) Get(key string) ([]byte, bool) {
	if key == "" {
		return nil, false
	}
	c.mu.Lock()
	el, ok := c.items[key]
	if ok {
		e := el.Value.(*entry)
		if c.ttl > 0 && time.Since(e.at) > c.ttl {
			c.removeLocked(el)
			c.misses++
		} else {
			c.ll.MoveToFront(el)
			c.hits++
			c.mu.Unlock()
			return e.blob, true
		}
	}
	c.misses++
	hasDisk := c.disk != nil
	c.mu.Unlock()

	if !hasDisk {
		return nil, false
	}
	blob, ok := c.disk.get(key)
	if !ok {
		return nil, false
	}
	// Cold-tier hit: promote into the hot tier (without persisting - it is already on disk)
	c.putMem([]string{key}, blob, time.Now())
	return blob, true
}

func (c *Cache) evictLocked(now time.Time) {
	// Clear expired ones first (from the tail, where the oldest are)
	if c.ttl > 0 {
		for el := c.ll.Back(); el != nil; {
			prev := el.Prev()
			if now.Sub(el.Value.(*entry).at) <= c.ttl {
				break
			}
			c.removeLocked(el)
			c.evictions++
			el = prev
		}
	}
	for c.bytes > c.maxBytes {
		el := c.ll.Back()
		if el == nil {
			return
		}
		c.removeLocked(el)
		c.evictions++
	}
}

func (c *Cache) removeLocked(el *list.Element) {
	e := el.Value.(*entry)
	c.ll.Remove(el)
	delete(c.items, e.key)
	c.bytes -= int64(len(e.blob))
}

// Stats is for newgate status / st display. No content.
type Stats struct {
	Entries   int
	Bytes     int64
	MaxBytes  int64
	Hits      uint64
	Misses    uint64
	Puts      uint64
	Evictions uint64
	// Unparsable is the number of unparsable assistant messages in requests (see Lookup).
	// Reported apart from Misses: a miss is handled by checking the cache and the upstream, this
	// one by checking the JSON the client sent.
	Unparsable uint64
}

func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{
		Entries: c.ll.Len(), Bytes: c.bytes, MaxBytes: c.maxBytes,
		Hits: c.hits, Misses: c.misses, Puts: c.puts, Evictions: c.evictions,
		Unparsable: c.unparsable,
	}
}

// ToolKey keys by tool_call id.
//
// This is the most stable key: the id is generated by the upstream and the client **must**
// send it back verbatim (tool_result needs it to match up), so it is necessarily identical
// across two requests. And "the request carries tools" is exactly the only case where the
// upstream demands the reasoning back - the two coincide.
func ToolKey(id string) string {
	if id == "" {
		return ""
	}
	return "tool:" + id
}

const originKeyPrefix = "origin:"

// Origin is the actual upstream that produced a tool call. In thinking mode, the assistant's
// reasoning and the whole group of tool calls belong to the same unclosed state; before the
// tool_result comes back they cannot be moved to another upstream. It stores only the routing
// identity, never request or response bodies.
type Origin struct {
	Profile  string `json:"profile,omitempty"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func originKey(id string) string {
	if id == "" {
		return ""
	}
	return originKeyPrefix + id
}

// PutOrigin binds a tool call in the response to the upstream that actually produced it. It
// reuses Cache/DiskStore's TTL, capacity and persistence, so an unclosed tool loop can still
// return to the original upstream after a daemon restart.
func (c *Cache) PutOrigin(toolIDs []string, origin Origin) {
	if c == nil || origin.Provider == "" || origin.Model == "" {
		return
	}
	blob, err := json.Marshal(origin)
	if err != nil {
		return
	}
	var keys []string
	for _, id := range toolIDs {
		if k := originKey(id); k != "" {
			keys = append(keys, k)
		}
	}
	c.Put(keys, blob)
}

// GetOrigin returns the upstream that produced a tool call.
func (c *Cache) GetOrigin(toolID string) (Origin, bool) {
	var origin Origin
	blob, ok := c.Get(originKey(toolID))
	if !ok || json.Unmarshal(blob, &origin) != nil ||
		origin.Provider == "" || origin.Model == "" {
		return Origin{}, false
	}
	return origin, true
}

// TextKey keys on the hash of the assistant body text, for the turns that have **no**
// tool_call.
//
// The doc is explicit: once a request carries tools, every assistant message in the history
// must carry its reasoning content, even if that turn made no tool call. So pure-text turns
// must be findable too, and the only thing on them the client carries back verbatim is the
// body text.
//
// Weakness: if the client rewrites the body (truncates, prefixes) it misses. Accept it -- the
// main path (turns with tool_call) goes through ToolKey, and that one is solid.
func TextKey(text string) string {
	t := strings.TrimSpace(text)
	if t == "" {
		return ""
	}
	h := sha256.Sum256([]byte(t))
	return "text:" + hex.EncodeToString(h[:12])
}
