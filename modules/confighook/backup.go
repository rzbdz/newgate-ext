package confighook

import (
	"bytes"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate/lib/i18n"
)

// This file is **the few steps every config takeover must take**: save an
// original before takeover, write back atomically, and restore from the original.
//
// # Why it is in the kernel rather than in some client's module
//
// Before 2026-09-21 it grew inside the distribution's modules/opencodeomo, while codex's
// takeover writes the same set of things (back up before changing, write back atomically,
// an escape hatch that really restores). The criterion is still that one -- **would this
// still belong here under another distribution?** Yes: for any config file of any client,
// whoever takes it over must do these three steps, and the steps themselves contain no one's
// knowledge (which file, what counts as "already taken over", are given by the caller).
//
// The cost of leaving it in opencodeomo was measured: codex's takeover
// had to copy the three steps, and **the copy drifts** -- here it is the write
// protection on `original/`, and after drifting the symptom is "the user's
// original config is lost permanently", while in tests it makes not one sound.

// TakenOver judges whether a file's **current content** has already been taken over by us.
//
// The criterion must come from the caller, because what "taken over" looks like is each
// format's business: in opencode.json it is a `"newgate"` provider key, in codex's config.toml a
// `[model_providers.newgate]` section. The kernel does not know these two formats, and should not.
type TakenOver func(content []byte) bool

// OriginalPath is where this target file's "untouched copy" lives.
//
// Indexed by **file name** (without the directory): target files are often
// scattered (`~/.codex/config.toml`, `~/.config/opencode/opencode.json`),
// and moving the whole path under backups/ would grow a tree isomorphic to
// the user's home, while that tree is internal bookkeeping only for `off`.
func OriginalPath(target string) string {
	return filepath.Join(paths.BackupDir(), "original", filepath.Base(target))
}

// BackupFile saves the original file as original/ before takeover
// (for restore), and also keeps a timestamped history every time.
//
// The crucial protection: **original/ may only be written with "not yet taken over"
// content**. Otherwise, once original/ is deleted by mistake, the next takeover would store the
// already-taken-over file as the "original", and what is later restored is the polluted version
// -- the user's original config is lost permanently. This is not a theoretical risk: `off`
// then `on` again, or the user manually deleting some directory in backups/, both reach here.
func BackupFile(target string, isTakenOver TakenOver) error {
	b, err := ioutil.ReadFile(target)
	if err != nil {
		return err
	}

	orig := OriginalPath(target)
	if err := os.MkdirAll(filepath.Dir(orig), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(orig); os.IsNotExist(err) {
		if isTakenOver != nil && isTakenOver(b) {
			return i18n.E("refusing to back up: {file} has already been taken over by newgate, but {orig} does not exist.\n"+
				"  Using it as the original backup would lose your original config for good.\n"+
				"  Either put a clean copy from backups/<timestamp>/ back into original/,\n"+
				"  or revert the config by hand and run newgate start again",
				i18n.A{"file": filepath.Base(target), "orig": orig})
		}
		if err := ioutil.WriteFile(orig, b, 0o600); err != nil {
			return err
		}
	}

	ts := filepath.Join(paths.BackupDir(), time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(ts, 0o700); err != nil {
		return err
	}
	if err := ioutil.WriteFile(filepath.Join(ts, filepath.Base(target)), b, 0o600); err != nil {
		return err
	}
	PruneSnapshots(10)
	return nil
}

// PruneSnapshots keeps only the most recent keep timestamped snapshots, so
// the backup directory does not grow forever. original/ is never deleted
// -- that is the escape hatch, and deleting it leaves nothing to restore.
func PruneSnapshots(keep int) {
	ents, err := ioutil.ReadDir(paths.BackupDir())
	if err != nil {
		return
	}
	var stamps []string
	for _, e := range ents {
		if e.IsDir() && e.Name() != "original" {
			stamps = append(stamps, e.Name())
		}
	}
	if len(stamps) <= keep {
		return
	}
	sort.Strings(stamps) // the timestamp format sorts lexicographically as-is
	for _, s := range stamps[:len(stamps)-keep] {
		_ = os.RemoveAll(filepath.Join(paths.BackupDir(), s))
	}
}

// RestoreFile writes original/'s copy back to the target. Returns whether it really restored.
//
// No backup = never taken over, not an error (`off` on a file we never touched should do nothing).
func RestoreFile(target string) (bool, error) {
	b, err := ioutil.ReadFile(OriginalPath(target))
	if err != nil {
		return false, nil
	}
	return true, WriteAtomic(target, b, 0o600)
}

// WriteAtomic writes a file atomically: first write a temporary file
// in the same directory, then rename to swap the directory entry.
//
// Why not overwrite directly: these files are **being read by someone else's
// program** (codex reads config.toml on each start). Read halfway through a
// write, at best that one start reports a baffling parse error, at worst we
// truncated the user's config -- and the truncated copy is the one we wrote.
func WriteAtomic(path string, content []byte, mode os.FileMode) error {
	tmp := path + ".newgate.tmp"
	if err := ioutil.WriteFile(tmp, content, mode); err != nil {
		return err
	}
	// WriteFile's mode gets stripped by umask (a root-created directory handed to claude is
	// 0640), so Chmod once explicitly -- same origin as the multi-user permission pit in the docs.
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ContainsMarker is a small helper for TakenOver: is this piece of bytes in the content.
//
// It exists only so each caller need not write bytes.Contains itself (that line has no information,
// and the wrong forms -- going through a string conversion, say -- add a full copy on big files).
func ContainsMarker(content []byte, marker string) bool {
	return bytes.Contains(content, []byte(marker))
}
