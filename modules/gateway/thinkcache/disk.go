package thinkcache

import (
	"encoding/binary"
	"io"
	"os"
	"sync"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// diskMagic is the file header: it identifies this as a thinkcache on-disk file. Change this
// string when the record format changes; old files are then treated as empty (cold start,
// falling back to memory only, no error).
var diskMagic = []byte("NGTC01\n")

// DiskStore is the on-disk cold tier of the three-level cache: an append-only record file plus
// an in-memory index (offsets/timestamps only, not the blobs themselves), surviving daemon
// restarts.
//
// Best-effort, not strongly consistent storage:
//   - Writes go through os.File.Write + O_APPEND (into the OS page cache, no fsync). Process
//     exit/restart loses no data (the page cache still belongs to the kernel); only a full
//     machine power loss loses the last few records -- good enough for "best-effort recovery
//     of the previous process's reasoning text after a restart";
//   - Past maxBytes*2 the whole file is rewritten (dropping expired entries); disk usage is
//     capped near ~2×max, so it neither grows without bound nor fills the disk (when live data
//     itself exceeds max it cannot shrink further and only the TTL holds it down -- 8h really
//     cannot produce 100MB of reasoning).
type DiskStore struct {
	mu    sync.Mutex
	f     *os.File
	path  string
	size  int64 // current file size in bytes (approximate; decides whether to rewrite)
	max   int64
	ttl   time.Duration
	index map[string]rec

	// onErr is the exit for on-disk failures (shaped after breaker.SetErrorHandler). Not
	// installed, failures are dropped: this tier is best-effort, and without it correctness
	// must not be affected -- but only **with it** can one answer "why can't those turns of
	// reasoning be found after the restart". Before 2026-09-18 every failed step of compaction
	// was a `return` / `continue`, and not one was ever spoken.
	onErr func(error)
}

// fail reports one on-disk failure. nil-safe: with no exit installed it is dropped.
func (d *DiskStore) fail(err error) {
	if err != nil && d.onErr != nil {
		d.onErr(err)
	}
}

type rec struct {
	off int64 // start offset of this record in the file
	at  time.Time
}

func openDisk(path string, maxBytes int64, ttl time.Duration) (*DiskStore, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o660)
	if err != nil {
		return nil, err
	}
	d := &DiskStore{f: f, path: path, max: maxBytes, ttl: ttl, index: map[string]rec{}}
	d.scan()
	return d, nil
}

// Close shuts the cold tier down. d.f may already be nil (a failed reopen during compaction
// stops the cold tier, see compactLocked); then there is nothing to do here.
func (d *DiskStore) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f == nil {
		return nil
	}
	err := d.f.Close()
	d.f = nil
	return err
}

// scan rebuilds the index from the file (the caller must hold the lock, or this must run
// single-threaded at startup). A half record (a tail left by a killed process) is truncated.
// Empty or foreign files are reset to "header only".
func (d *DiskStore) scan() {
	if d.f == nil {
		d.size = 0
		return
	}
	fi, err := d.f.Stat()
	if err != nil {
		d.size = 0
		return
	}
	d.size = fi.Size()
	// Empty file / shorter than the header / not our file: reset uniformly, leaving only the
	// header
	reset := d.size < int64(len(diskMagic))
	if !reset {
		hdr := make([]byte, len(diskMagic))
		if _, err := d.f.ReadAt(hdr, 0); err != nil || string(hdr) != string(diskMagic) {
			reset = true
		}
	}
	if reset {
		_ = d.f.Truncate(0)
		if _, err := d.f.Write(diskMagic); err == nil {
			d.size = int64(len(diskMagic))
		}
		return
	}
	now := time.Now()
	off := int64(len(diskMagic))
	good := off
	for {
		key, _, at, n, err := readRecAt(d.f, off)
		if err != nil {
			break
		}
		if d.ttl > 0 && now.Sub(at) > d.ttl {
			off += int64(n)
			continue // expired: do not index, compact collects it
		}
		d.index[key] = rec{off: off, at: at}
		off += int64(n)
		good = off
	}
	if off != d.size {
		_ = d.f.Truncate(good)
		d.size = good
	}
}

// snapshot copies the index (for replay at startup, avoiding reading the map across locks).
func (d *DiskStore) snapshot() map[string]rec {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]rec, len(d.index))
	for k, r := range d.index {
		out[k] = r
	}
	return out
}

// appendKeys attaches one blob to several keys (mirroring Cache.Put's multi-key semantics).
func (d *DiskStore) appendKeys(keys []string, blob []byte, at time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(keys) == 0 || len(blob) == 0 {
		return
	}
	if d.f == nil {
		return // cold tier is disabled (see the reopen-failure branch in compactLocked)
	}
	for _, k := range keys {
		if k == "" {
			continue
		}
		buf := recBytes(k, blob, at)
		if _, err := d.f.Write(buf); err != nil {
			// Skip this entry (best-effort, the in-memory hot tier is still there), but say so:
			// repeated write failures are why "this turn's reasoning is gone after a restart".
			d.fail(i18n.Ef(err, "thinkcache disk write failed (this record is not persisted): {err}", nil))
			continue
		}
		d.index[k] = rec{off: d.size, at: at}
		d.size += int64(len(buf))
	}
	if d.size > d.max*2 {
		d.compactLocked()
	}
}

// get reads one blob back. Expired counts as absent.
func (d *DiskStore) get(key string) ([]byte, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f == nil {
		return nil, false // cold tier is disabled
	}
	r, ok := d.index[key]
	if !ok {
		return nil, false
	}
	if d.ttl > 0 && time.Since(r.at) > d.ttl {
		delete(d.index, key)
		return nil, false
	}
	_, blob, _, _, err := readRecAt(d.f, r.off)
	if err != nil {
		return nil, false
	}
	return blob, true
}

// Record format (per entry):
//
//	[4] keyLen   uint32 LE
//	[4] blobLen  uint32 LE
//	[8] unixnano uint64 LE
//	[keyLen] key
//	[blobLen] blob
func recBytes(key string, blob []byte, at time.Time) []byte {
	buf := make([]byte, 0, len(key)+len(blob)+16)
	var n [8]byte
	binary.LittleEndian.PutUint32(n[0:4], uint32(len(key)))
	binary.LittleEndian.PutUint32(n[4:8], uint32(len(blob)))
	buf = append(buf, n[:]...)
	binary.LittleEndian.PutUint64(n[:], uint64(at.UnixNano()))
	buf = append(buf, n[:]...)
	buf = append(buf, key...)
	buf = append(buf, blob...)
	return buf
}

func readRecAt(f *os.File, off int64) (key string, blob []byte, at time.Time, n int, err error) {
	var hdr [16]byte
	if _, err = f.ReadAt(hdr[:], off); err != nil {
		return
	}
	keyLen := binary.LittleEndian.Uint32(hdr[0:4])
	blobLen := binary.LittleEndian.Uint32(hdr[4:8])
	at = time.Unix(0, int64(binary.LittleEndian.Uint64(hdr[8:16])))
	// Sanity bound: stops a garbage length in a half record from blowing up memory
	if keyLen > 1<<20 || blobLen > 8<<20 {
		err = io.ErrUnexpectedEOF
		return
	}
	body := make([]byte, keyLen+blobLen)
	if _, err = f.ReadAt(body, off+16); err != nil {
		return
	}
	key = string(body[:keyLen])
	blob = body[keyLen:]
	n = 16 + int(keyLen) + int(blobLen)
	return
}

func (d *DiskStore) compactLocked() {
	// Clear expired entries first
	now := time.Now()
	for k, r := range d.index {
		if d.ttl > 0 && now.Sub(r.at) > d.ttl {
			delete(d.index, k)
		}
	}
	tmp := d.path + ".tmp"
	nf, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o660)
	if err != nil {
		d.fail(i18n.Ef(err, "thinkcache compact: cannot open the temp file (still using the old file): {err}", nil))
		return
	}
	if _, err := nf.Write(diskMagic); err != nil {
		_ = nf.Close()
		_ = os.Remove(tmp)
		d.fail(i18n.Ef(err, "thinkcache compact: cannot write the file header (still using the old file): {err}", nil))
		return
	}
	// Compaction **loses records**, so every dropped one must be accounted for. Two ways:
	//   unreadable (that segment of the old file is corrupt) -- this one was already lost;
	//   unwritable (disk full / quota) -- this one was there, and compaction made it vanish.
	// The latter is the one that matters: it is the only explanation for "a record vanished",
	// and the old code made both a bare `continue`, indistinguishable afterwards.
	var unreadable, unwritable int
	for k, r := range d.index {
		if _, blob, _, _, err := readRecAt(d.f, r.off); err != nil {
			unreadable++
			continue
		} else if _, err := nf.Write(recBytes(k, blob, r.at)); err != nil {
			unwritable++
			continue
		}
	}
	// Durability barrier: rename is atomic, but what it makes atomic is the **directory
	// entry**; it does not guarantee the data has left the page cache. The old code had
	// `_ = nf.Sync()` -- rename checked, Sync not, so "the file is swapped in, its content
	// half-written" could happen and nobody would know. On failure, do not rename: the old file
	// is complete, and leaving it in place beats swapping in one that was never flushed.
	if err := nf.Sync(); err != nil {
		_ = nf.Close()
		_ = os.Remove(tmp)
		d.fail(i18n.Ef(err, "thinkcache compact: fsync failed (keeping the old file): {err}", nil))
		return
	}
	if err := nf.Close(); err != nil {
		_ = os.Remove(tmp)
		d.fail(i18n.Ef(err, "thinkcache compact: cannot close the temp file (keeping the old file): {err}", nil))
		return
	}
	if err := os.Rename(tmp, d.path); err != nil {
		_ = os.Remove(tmp)
		d.fail(i18n.Ef(err, "thinkcache compact: cannot replace the file (keeping the old file): {err}", nil))
		return
	}
	if unreadable > 0 || unwritable > 0 {
		d.fail(i18n.E("thinkcache compact lost {lost} records ({unreadable} unreadable, {unwritable} unwritable)",
			i18n.A{"lost": unreadable + unwritable, "unreadable": unreadable, "unwritable": unwritable}))
	}
	_ = d.f.Close()
	nf2, err := os.OpenFile(d.path, os.O_RDWR|os.O_APPEND, 0o660)
	if err != nil {
		// The cold tier ends here: this **must be said out loud**, and the tier must really be
		// stopped -- the old code only cleared the index here while d.f was already the closed
		// handle, so every later write failed and every read missed, all in complete silence.
		// The user saw "all reasoning is gone after a restart", and the cause (the cold tier is
		// dead) could be found neither on disk nor in the logs.
		d.f = nil
		d.index = map[string]rec{}
		d.size = 0
		d.fail(i18n.Ef(err, "thinkcache disk tier disabled (reopen failed; cached reasoning content will not be recoverable after a restart): {err}", nil))
		return
	}
	d.f = nf2
	d.index = map[string]rec{}
	d.scan() // rebuild the index from the new file (offsets have changed)
}
