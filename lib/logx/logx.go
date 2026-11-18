package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Rotator is a log writer with a size cap. It rotates
// when full and keeps only keep historical copies.
//
// It is a must: in debug mode a single request can log 8KB+ (opencode's
// system prompt alone is 97KB), and without a cap the disk fills up quickly.
type Rotator struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int
	f        *os.File
	size     int64
}

func New(path string, maxBytes int64, keep int) (*Rotator, error) {
	r := &Rotator{path: path, maxBytes: maxBytes, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Rotator) open() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *Rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.f == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotate(); err != nil {
			// Even if rotation fails, do not drop the log; keep writing to the current file. The
			// `newgate: ` prefix is the program name (a machine marker), kept outside the message.
			fmt.Fprintf(os.Stderr, "newgate: %s\n",
				i18n.T("log rotation failed: {err}", i18n.A{"err": err}))
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate moves x.log -> x.log.1 -> x.log.2 …, dropping whatever exceeds keep.
//
// Failures must be reported: when rotation does not happen there is no symptom,
// the log file just keeps growing -- and the log is this project's only means of
// troubleshooting (CLAUDE.md §5), and a log that quietly grows to a few GB both slows
// grep down and can fill the disk. So every step's error is collected and returned
// here, and Write prints it to stderr (previously every `_ =` was left empty).
//
// "Source file does not exist" is not an error: the generations have
// not reached that far yet, or the previous round already deleted
// it -- that is a normal state, and reporting it would only spam.
func (r *Rotator) rotate() error {
	if r.f != nil {
		_ = r.f.Close()
		r.f = nil
	}
	// Move from oldest to newest to avoid overwriting
	_ = os.Remove(fmt.Sprintf("%s.%d", r.path, r.keep))
	for i := r.keep - 1; i >= 1; i-- {
		from, to := fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return i18n.Ef(err, "rotating {from} → {to}: {err}",
				i18n.A{"from": from, "to": to})
		}
	}
	// This one really matters: if it fails = the current log was not moved away =
	// later writes keep going to the same file, and maxBytes becomes a dead letter.
	if err := os.Rename(r.path, r.path+".1"); err != nil && !os.IsNotExist(err) {
		return i18n.Ef(err, "rotating {from} → {to}.1: {err}",
			i18n.A{"from": r.path, "to": r.path})
	}
	return r.open()
}

func (r *Rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// PruneDir keeps only the newest keep groups of files in a directory
// (grouped by prefix). It is used to cap the number of error-evidence dumps.
func PruneDir(dir string, keep int) { PruneDirBy(dir, "", keep) }

// PruneDirBy prunes only the file groups starting with prefix (an empty
// prefix = all). The dump's req-* and error evidence's err-* share one
// directory, each with its own cap, and neither deletes the other's.
//
// A group = the several files under one base ("err-400-req001840", up to the first ".");
// a group's age is the mtime of the **newest** file in it -- a group has just landed,
// and as long as one file of it remains the group counts as "new", so the newest one
// represents it (using os.Stat, not relying on the directory order the file system gives).
//
// The old version sorted bases lexicographically and deleted names[:len-keep], leaving
// the keep groups with the "largest names", not the "newest" ones. With status codes in
// the names, err-400 < err-402 < … < err-502, and the lexicographically smallest group
// was always the first deleted. The scene on 2026-09-22: the log said 15 times "upstream
// 400, full evidence saved", and on disk not a single err-400-* was left -- because
// saveErrEvidence calls this function at the end, so the just-written err-400-* was
// deleted by itself on the spot; it was exactly the lexicographically smallest group.
//
// Ordering: mtime descending; equal mtime broken by name ascending as a stable tiebreak. A group
// whose os.Stat fails counts as "age unknown" and sorts on the newest side (it will not be pruned
// first): the point of this function is to cap disk usage, and the evidence it protects is gone
// for good once deleted (CLAUDE.md §5 says it is the only thing you can offer when asking "did
// the proxy mangle the request"). Two harms weighed, we would rather keep one extra group (the
// next prune or a manual clean reclaims it, and the total is still bounded by keep) than start
// deleting, with no age evidence at all, the group that may well be the one that just landed.
func PruneDirBy(dir, prefix string, keep int) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	// Group by a prefix like "err-400-req000003"; a group is four files. As long as one file
	// in the group can be stat'ed, the group has an age; only if none can, it is unknown.
	type group struct {
		base     string
		mtime    time.Time
		hasMtime bool
	}
	byBase := map[string]*group{}
	var groups []*group
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if prefix != "" && !strings.HasPrefix(n, prefix) {
			continue
		}
		base := n
		if i := indexAny(n, "."); i > 0 {
			base = n[:i]
		}
		g := byBase[base]
		if g == nil {
			g = &group{base: base}
			byBase[base] = g
			groups = append(groups, g)
		}
		if fi, err := os.Stat(filepath.Join(dir, n)); err == nil {
			if mt := fi.ModTime(); !g.hasMtime || mt.After(g.mtime) {
				g.mtime, g.hasMtime = mt, true
			}
		}
	}
	if len(groups) <= keep {
		return
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.hasMtime != b.hasMtime {
			return !a.hasMtime // unknown age sorts as newest
		}
		if a.hasMtime && !a.mtime.Equal(b.mtime) {
			return a.mtime.After(b.mtime)
		}
		return a.base < b.base
	})
	for _, g := range groups[keep:] {
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			if len(e.Name()) >= len(g.base) && e.Name()[:len(g.base)] == g.base {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
}

func indexAny(s, chars string) int {
	for i := 0; i < len(s); i++ {
		for j := 0; j < len(chars); j++ {
			if s[i] == chars[j] {
				return i
			}
		}
	}
	return -1
}
