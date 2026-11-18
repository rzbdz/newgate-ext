package store

import (
	"crypto/sha256"
	"encoding/hex"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/roleprov"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Watcher holds the config snapshot in memory, watches disk
// changes in the background, and swaps the page atomically.
//
// Why it is needed (this is the foundation of service stability):
//
//  1. **Do not interrupt running sessions.** Change claude's profile and a running opencode
//     is not affected in the least -- they read their own agent's bindings, and the swap is
//     atomic, so there is no intermediate state of "the config changed halfway through a read".
//
//  2. **A broken config is not fatal.** Hand-break a JSON, and when the new snapshot
//     fails to load the **old snapshot is kept**, with a loud warning. Everything
//     running keeps working. If every request read the disk directly, one typo could
//     make every agent error at once -- that is not an acceptable failure mode.
//
//  3. **Zero IO on the hot path.** The request side does one
//     atomic pointer read and never touches the filesystem.
//
// Polling mtime rather than inotify: zero external dependencies, and inotify is inherently
// unreliable on NFS / container mounts anyway, while the config directory often is on one of those.
type Watcher struct {
	cur      atomic.Value // *Snapshot
	interval time.Duration

	mu       sync.Mutex
	lastSig  string
	stop     chan struct{}
	stopped  bool
	onChange func(old, new *Snapshot)
	onError  func(error)
	// generation increments on every successful page swap, for logs and status.
	generation int64
}

// NewWatcher does one synchronous load immediately, returning
// an error on failure (problems should be found at start-up).
func NewWatcher(interval time.Duration) (*Watcher, error) {
	if interval <= 0 {
		interval = time.Second
	}
	w := &Watcher{interval: interval, stop: make(chan struct{})}
	snap, err := Load()
	if err != nil {
		return nil, err
	}
	w.cur.Store(snap)
	w.lastSig = signature()
	w.generation = 1
	return w, nil
}

// OnChange / OnError register callbacks. Must be called before Start.
func (w *Watcher) OnChange(f func(old, new *Snapshot)) { w.onChange = f }
func (w *Watcher) OnError(f func(error))               { w.onError = f }

// Current the current snapshot. Called on the hot path, no lock, no IO.
func (w *Watcher) Current() *Snapshot { return w.cur.Load().(*Snapshot) }

// Generation how many successful page swaps.
func (w *Watcher) Generation() int64 { return atomic.LoadInt64(&w.generation) }

// Start starts the background polling.
func (w *Watcher) Start() {
	go func() {
		t := time.NewTicker(w.interval)
		defer t.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-t.C:
				w.Reload(false)
			}
		}
	}()
}

func (w *Watcher) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.stopped {
		w.stopped = true
		close(w.stop)
	}
}

// Reload checks whether the disk changed and swaps the
// page. When force is true, reloads unconditionally.
//
// The key point: a failed load **does not touch** the current snapshot. Better to keep
// running on the old config than to make every agent fail at once because of one typo.
func (w *Watcher) Reload(force bool) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	sig := signature()
	if !force && sig == w.lastSig {
		return false
	}

	snap, err := Load()
	if err != nil {
		// Record the signature, so the same bad file does not spam repeated errors; but
		// **do not** replace the snapshot -- running sessions keep using the old config.
		w.lastSig = sig
		if w.onError != nil {
			w.onError(err)
		}
		return false
	}

	old := w.cur.Load().(*Snapshot)
	w.cur.Store(snap)
	w.lastSig = sig
	atomic.AddInt64(&w.generation, 1)
	if w.onChange != nil {
		w.onChange(old, snap)
	}
	return true
}

// signature the config directory's fingerprint: a
// hash of (path, size, mtime) for all relevant files.
//
// mtime+size rather than a content hash: config files may number in the hundreds, and
// reading every content once a second is unnecessary. The cost is that an in-place
// rewrite within the same second may be missed -- but our own writes all go through
// atomic rename (mtime always changes), and hand editors almost always rename too.
func signature() string {
	h := sha256.New()
	var files []string
	// omo-slots.json is config too: it is the only source of a module-contributed
	// role key (which tier omo-sisyphus follows). Taking over omo writes it,
	// and a change should take effect within 1 second (see core/roleprov).
	files = append(files, paths.ProvidersFile(), paths.StateFile())
	files = append(files, roleprov.WatchFiles()...)
	if ents, err := ioutil.ReadDir(paths.Mappings()); err == nil {
		var names []string
		for _, e := range ents {
			if !e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names) // 顺序必须稳定，否则指纹会假变化
		for _, n := range names {
			files = append(files, filepath.Join(paths.Mappings(), n))
		}
	}
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			h.Write([]byte(f + "|missing\n"))
			continue
		}
		h.Write([]byte(f + "|" +
			fi.ModTime().UTC().Format(time.RFC3339Nano) + "|" +
			itoa(fi.Size()) + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// DiffSummary a human-readable change summary, for logs.
// Reports only **structural** changes, and prints no key.
func DiffSummary(old, new *Snapshot) []string {
	var out []string
	if old == nil || new == nil {
		return out
	}
	if old.State.DefaultProfile != new.State.DefaultProfile {
		out = append(out, i18n.T("default profile: {from} → {to}", i18n.A{
			"from": old.State.DefaultProfile, "to": new.State.DefaultProfile}))
	}
	for agent, p := range new.State.Active {
		if old.State.Active[agent] != p {
			out = append(out, i18n.T("agent {agent} → profile {profile}",
				i18n.A{"agent": agent, "profile": p}))
		}
	}
	for agent := range old.State.Active {
		if _, still := new.State.Active[agent]; !still {
			out = append(out, i18n.T("agent {agent} falls back to the default profile",
				i18n.A{"agent": agent}))
		}
	}
	if len(old.Profiles) != len(new.Profiles) {
		out = append(out, i18n.T("the number of profiles changed", nil))
	}
	if len(old.Providers.Providers) != len(new.Providers.Providers) {
		out = append(out, i18n.T("the number of providers changed", nil))
	}
	return out
}
