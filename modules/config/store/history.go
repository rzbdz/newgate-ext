package store

// Keep a copy of "the previous version" before writing.
//
// # Why it is needed
//
// The scene on 2026-09-20: a front-end bug wrote away the tiers of one
// of the user's profiles (a `mid=/model-name` binding with an empty provider landed
// in the config). By the time it was noticed, **nowhere could retrieve the copy from
// before the write** -- `backups/` is for opencode/claude config at takeover time
// and has nothing to do with config files; git has none either (the config directory
// is not under version control). So "broke it" equaled "rewrite it from memory".
//
// The criterion is **asymmetric cost**: keeping a backup costs one more
// small-file copy per write (a few KB), while losing one costs the config in the
// user's hands. So this should not be something the caller remembers to do -- it
// must be on **the single exit point of writing**, which nobody can go around.
//
// # Shape
//
// One directory per file ever written (the `/` in the path becomes `__`), named by time
// inside, **keeping only the most recent 20 versions**. 20 is the trade-off between "enough to
// go back to before the problem was noticed" and "not inflating the config directory": an editing
// session usually has a few saves, and 20 versions covers going back half an hour to a few hours.
//
// # Direction of failure
//
// A failed backup **does not block the write** (fail-open). Making the user's
// save fail in order to keep a history turns the safety net into a roadblock
// -- and the point of a safety net is "still there when the main flow breaks".

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
)

const (
	// historyDirName goes under the config root (the same approach as `.write.lock`:
	// leading dot, so it does not mix with the directories people go poking through).
	historyDirName = ".history"
	// historyKeep is the number of versions kept per file.
	historyKeep = 20
)

// snapshotBeforeWrite copies path's content **at this moment** into history.
//
// The call site must be before "the target has already been replaced". When
// the file does not exist (creating) or cannot be read, it does nothing
// -- there is no "previous version" to keep, and that is not an error.
func snapshotBeforeWrite(path string) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return
	}
	dir, err := historyDirFor(path)
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0o2770); err != nil {
		return
	}
	// File names are ordered by time: UnixNano is monotonic and sortable, and
	// two writes within the same nanosecond would collide -- on collision,
	// step one notch back (one nanosecond more), no need for a lock.
	name := time.Now().UnixNano()
	for i := 0; i < 100; i++ {
		p := filepath.Join(dir, fmt.Sprintf("%d.bak", name+int64(i)))
		if _, err := os.Stat(p); os.IsNotExist(err) {
			_ = os.WriteFile(p, b, 0o660)
			break
		}
	}
	pruneHistory(dir)
}

// pruneHistory keeps only the most recent historyKeep versions. Deletion failures
// are ignored: keeping a few extra versions is better than deleting the wrong one.
func pruneHistory(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var stamps []int64
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".bak") {
			continue
		}
		// The name is UnixNano; one that cannot be parsed (something a human put there)
		// is **left alone** -- we did not write it, so there is no reason to delete it.
		var n int64
		if _, err := fmt.Sscanf(e.Name(), "%d.bak", &n); err == nil {
			stamps = append(stamps, n)
		}
	}
	if len(stamps) <= historyKeep {
		return
	}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i] < stamps[j] })
	for _, n := range stamps[:len(stamps)-historyKeep] {
		_ = os.Remove(filepath.Join(dir, fmt.Sprintf("%d.bak", n)))
	}
}

// historyDirFor maps a config file path to its history directory.
func historyDirFor(path string) (string, error) {
	root, err := filepath.Abs(paths.Root())
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	// Join the path into one directory name: `mappings/demo.kv` ->
	// `mappings__demo.kv`. Using a separator that cannot appear in a
	// file name (the path separator itself has already been replaced).
	flat := strings.ReplaceAll(filepath.ToSlash(rel), "/", "__")
	if flat == "" || strings.HasPrefix(flat, "..") {
		return "", fmt.Errorf("store: %s is outside the config root", path)
	}
	return filepath.Join(root, historyDirName, flat), nil
}

// HistoryDir is some config file's history directory (for tests and for "where do I find backups").
func HistoryDir(path string) (string, error) { return historyDirFor(path) }

// HistoryEntries lists some config file's history versions, **newest to oldest**.
func HistoryEntries(path string) []string {
	dir, err := historyDirFor(path)
	if err != nil {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".bak") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}
