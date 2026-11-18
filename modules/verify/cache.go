package verify

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// The fingerprint on-disk cache.
//
// # Why it exists
//
// Not to save tokens (verify is explicit, every run really calls), but **the cell in the
// UI**: when the model dropdown opens the user must see "who was verified last time" right
// away. Keep the conclusion in memory and a restart loses it - and the daemon is long-lived
// while the CLI is short-lived, so "run a command, then open the web page" is the usual order.
//
// # Why every read and write goes back to disk
//
// One file, two writers: the daemon behind the web page, and a `newgate verify` the user may
// type in a terminal (every command is a new process). Read the file into memory once and
// never re-check, and what the terminal just verified never shows on the page - yet both are
// our statements about the same thing. The file is tens of KB; re-reading is cheap correctness.

const cacheVersion = 1

type cacheFile struct {
	Version int              `json:"version"`
	Entries map[string]Entry `json:"entries"`
}

type cacheStore struct {
	path string
	mu   sync.Mutex
}

func newCacheStore() *cacheStore {
	return &cacheStore{path: filepath.Join(paths.Root(), "fingerprints", "cache.json")}
}

// dataPath is where the lm-detector reference bank is cached. Under newgate's config root
// rather than the user's `~/.cache/lmfpd`: this data is **this product's** runtime file and
// should be found in one place like providers.json; change NEWGATE_HOME and it moves along.
func (c *cacheStore) dataPath() string {
	return filepath.Join(filepath.Dir(c.path), "data.bin")
}

// dataReady reports whether the bank cache file exists. It only explains "does the first
// verify download"; whether it can really be read is the detector step's answer.
func (c *cacheStore) dataReady() bool {
	_, err := os.Stat(c.dataPath())
	return err == nil
}

// dataCheckedAt is when the bank cache was last downloaded/checked.
//
// It is **the cache file's mtime**: when the bundle is unchanged only the file is touched
// ("I looked"), and it is rewritten only when it changed. So it answers both "how old is the
// ruler" and "when was the last online check" - exactly the sentence the UI shows.
func (c *cacheStore) dataCheckedAt() (time.Time, bool) {
	info, err := os.Stat(c.dataPath())
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

// dataVersion is the detector's sha256 in the cache (the version the bundle declares).
//
// It reads only the bundle.json at the head of the cache, not the whole 4.5 MB detector:
// when checking "is there a new version" this is compared with the remote bundle's sha.
func (c *cacheStore) dataVersion() string {
	f, err := os.Open(c.dataPath())
	if err != nil {
		return ""
	}
	defer f.Close()
	var head [4]byte
	if _, err := io.ReadFull(f, head[:]); err != nil {
		return ""
	}
	n := binary.LittleEndian.Uint32(head[:])
	if n == 0 || n > 4<<20 {
		return ""
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(f, raw); err != nil {
		return ""
	}
	var b referenceBundle
	if json.Unmarshal(raw, &b) != nil {
		return ""
	}
	return b.Files[referenceDetectorFile].SHA256
}

// touchData records "just checked" on the cache file's mtime without touching the content.
func (c *cacheStore) touchData() {
	now := time.Now()
	_ = os.Chtimes(c.dataPath(), now, now)
}

func (c *cacheStore) read() (map[string]Entry, error) {
	out := map[string]Entry{}
	raw, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, i18n.Ef(err, "read fingerprint cache {path}: {err}",
			i18n.A{"path": c.path, "err": err})
	}
	var f cacheFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return out, i18n.Ef(err, "parse fingerprint cache {path}: {err}",
			i18n.A{"path": c.path, "err": err})
	}
	if f.Entries != nil {
		out = f.Entries
	}
	return out, nil
}

// list returns all conclusions sorted by provider/model.
func (c *cacheStore) list() ([]Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := c.read()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Entry, 0, len(keys))
	for _, k := range keys {
		out = append(out, entries[k])
	}
	return out, nil
}

func (c *cacheStore) get(provider, model string) (Entry, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := c.read()
	if err != nil {
		return Entry{}, false, err
	}
	e, ok := entries[provider+"/"+model]
	return e, ok, nil
}

// put merges one conclusion and saves it.
//
// Merge, do not replace the table: verification is concurrent (the CLI verifies several at
// once, the page has a button per row), so a later finish must not erase an earlier one.
// The lock only covers this process; cross-process merging re-reads before writing - two
// processes at once still have a loss window, and losing a cell just means verifying again.
func (c *cacheStore) put(e Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := c.read()
	if err != nil {
		return err
	}
	entries[e.Provider+"/"+e.Model] = e
	f := cacheFile{Version: cacheVersion, Entries: entries}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o2770); err != nil {
		return i18n.Ef(err, "create {dir}: {err}", i18n.A{"dir": dir, "err": err})
	}
	// The temp file name carries a random part: two processes writing at once must not share
	// one inode (same reason as the kernel's store.writeFileAtomic).
	tmp, err := os.CreateTemp(dir, ".cache-*.tmp")
	if err != nil {
		return i18n.Ef(err, "create a temporary file in {dir}: {err}", i18n.A{"dir": dir, "err": err})
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o660); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return i18n.Ef(err, "write {path}: {err}", i18n.A{"path": tmpName, "err": err})
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, c.path); err != nil {
		return i18n.Ef(err, "replace {path}: {err}", i18n.A{"path": c.path, "err": err})
	}
	return nil
}
