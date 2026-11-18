package proto

import (
	"crypto/sha256"
	"encoding/hex"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"time"

	paths "github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate/lib/i18n"
)

// DriftReport is how local managed files deviate from "what we last wrote".
//
// # The failure mode it stops
//
// Someone hand-edits providers.json on a replica, believes it took effect, and the next
// poll round **silently** overwrites it. The user's model and reality diverge with **no**
// signal** - a direct violation of this repo's "never silent" rule. The requirement to
// "design the host concept properly or everyone will edit config freely" is answered
// here: replica edits are not errors, but must be seen and handled, never wiped out.
//
// # Why it must hash content, not stat
//
// watch.go's own comment notes mtime+size limits (same-second rewrites can be missed).
// Here the question is "someone changed the content", so only a content hash works.
type DriftReport struct {
	// Modified: a file we wrote had its content changed to something else.
	Modified []string `json:"modified,omitempty"`
	// Deleted: a file we wrote was deleted while the remote still has it (it comes back).
	Deleted []string `json:"deleted,omitempty"`
	// Shadowed: the managed `mappings/x.json` is covered by a **local** `mappings/x.kv` -
	// the remote synced, local behavior unchanged. The nastiest kind; must be reported.
	Shadowed []string `json:"shadowed,omitempty"`
	// Extra: a local file in the managed set that we do not own (= the formal local layer).
	// **Report only, never block**, see the Blocking note below.
	Extra []string `json:"extra,omitempty"`

	At time.Time `json:"at,omitempty"`
}

// Blocking says whether applying is blocked.
//
// Why Extra does **not** block: this design explicitly defines "two layers - shared
// (managed) + machine-local (private, never in the repo, never overwritten)" (see
// docs/13-config-share.md). The local layer on disk is just "extra local files in the
// managed set" (say a machine-specific `mappings/local.json`). Blocking on that would
// make local customization and auto-sync unable to coexist - users would have to give
// up one. So: **report only** (show it in status, avoid the silent-override trap).
//
// The three blocking kinds all mean "materialize would destroy work the user did, or
// do something that takes no effect": overwrite, resurrect, shadow.
func (d *DriftReport) Blocking() bool {
	if d == nil {
		return false
	}
	return len(d.Modified)+len(d.Deleted)+len(d.Shadowed) > 0
}

// Empty: nothing found (a clean replica).
func (d *DriftReport) Empty() bool {
	if d == nil {
		return true
	}
	return len(d.Modified)+len(d.Deleted)+len(d.Shadowed)+len(d.Extra) == 0
}

// Paths are the files involved in blocking drift (archive these; others are untouched).
func (d *DriftReport) Paths() []string {
	if d == nil {
		return nil
	}
	out := append([]string(nil), d.Modified...)
	out = append(out, d.Deleted...)
	out = append(out, d.Shadowed...)
	sort.Strings(out)
	return out
}

// DetectDrift compares "what we wrote" (the applied list), the local state, and what
// this round is about to write.
//
// incoming carries content, not paths, to settle one edge case: the user's hand-edited
// bytes are **exactly equal** to what the remote has now. Writing then destroys nothing
// (the user's version agrees with the authority), so it must not block - blocking would
// force a pointless --force. The criterion is "would materialize change anything".
func DetectDrift(dir string, applied map[string]string, incoming map[string][]byte) DriftReport {
	var rep DriftReport
	rep.At = time.Now()

	ours := make(map[string]bool, len(applied))
	// Sort the list first so the report is stable (tests and status output rely on it).
	paths := make([]string, 0, len(applied))
	for rel := range applied {
		if _, ok := ManagedPath(rel); !ok {
			continue // 老版本写下的、现已不在托管集合里的记录：忽略
		}
		ours[rel] = true
		paths = append(paths, rel)
	}
	sort.Strings(paths)

	for _, rel := range paths {
		live, err := ioutil.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			if isNotExist(err) {
				// Deleted. Remote still has it -> materialize resurrects it, undoing the
				// user's delete; remote does not -> converged, nothing happens.
				if _, still := incoming[rel]; still {
					rep.Deleted = append(rep.Deleted, rel)
				}
				continue
			}
			// Unreadable (permissions, a directory): report as drift, don't skip silently.
			rep.Modified = append(rep.Modified, rel)
			continue
		}
		if hashBytes(live) == applied[rel] {
			continue // 这就是我们写下去的样子
		}
		if want, ok := incoming[rel]; ok && hashBytes(live) == hashBytes(want) {
			continue // 手改的结果与远端一致，写下去等于没变
		}
		rep.Modified = append(rep.Modified, rel)
	}

	// Shadow: the managed mappings/x.json is covered by a local mappings/x.kv.
	incomingPaths := make([]string, 0, len(incoming))
	for rel := range incoming {
		incomingPaths = append(incomingPaths, rel)
	}
	sort.Strings(incomingPaths)
	for _, rel := range incomingPaths {
		sh := shadowOf(rel)
		if sh == "" || ours[sh] {
			continue // 两边都是托管的：宿主自己管着这个优先级，不是漂移
		}
		if fileExists(filepath.Join(dir, sh)) {
			rep.Shadowed = append(rep.Shadowed, rel)
		}
	}

	// Local layer: files in the managed set that we do not own.
	if live, err := ListManagedFiles(dir); err == nil {
		for _, rel := range live {
			if ours[rel] {
				continue
			}
			if _, inSnapshot := incoming[rel]; inSnapshot {
				continue // 远端要开始管它了（首次接管），不算本地层
			}
			rep.Extra = append(rep.Extra, rel)
		}
	}
	return rep
}

// ArchiveFiles copies the given managed files under backups/configshare-<stamp>/
// (keeping the relative structure) and returns the archive dir. Missing ones are skipped.
//
// Follows the backups/<timestamp>/ convention and "never delete" spirit: --force
// overwrites user changes, but must not make them unrecoverable - archiving is why it is safe.
func ArchiveFiles(dir, stamp string, rels []string) (string, error) {
	if len(rels) == 0 {
		return "", nil
	}
	dst := filepath.Join(paths.BackupDir(), "configshare-"+stamp)
	for _, rel := range rels {
		src := filepath.Join(dir, rel)
		data, err := ioutil.ReadFile(src)
		if err != nil {
			if isNotExist(err) {
				continue
			}
			return "", i18n.Ef(err, "cannot archive {path}: {err}", i18n.A{"path": rel})
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o2770); err != nil {
			return "", err
		}
		if err := ioutil.WriteFile(target, data, 0o660); err != nil {
			return "", i18n.Ef(err, "cannot archive {path}: {err}", i18n.A{"path": rel})
		}
	}
	return dst, nil
}

// ---------- small helpers ----------

// readFileIfExists reads a file; missing returns (nil, nil) rather than an error.
//
// Why the distinction matters: drift compares "was this file changed locally", and
// **missing** and **unreadable** are different - missing is a fresh machine (normal),
// unreadable (permissions, a directory) is what to report. Per-call os.IsNotExist gets verbose.
func readFileIfExists(path string) ([]byte, error) {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		if isNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return b, nil
}

// driftOrNil collapses an empty report to nil.
//
// The ledger uses nil for "clean" and non-nil for "something to show": an empty struct
// and a non-nil empty report differ in JSON (`{}` vs absent), and status tests for nil.
// One implementation only, so no path writes "has drift" that is really nothing.
func driftOrNil(d DriftReport) *DriftReport {
	if d.Empty() {
		return nil
	}
	return &d
}

// mergeDrift merges two drift reports into one (for status display).
//
// The two channels (config, secrets) each have their own drift, stored **separately**
// (see SecretsDrift in state.go), merged only for display: users care what is not
// shared from this machine, not which channel reported it.
func mergeDrift(old, add *DriftReport) *DriftReport {
	if add.Empty() {
		return old
	}
	if old.Empty() {
		return add
	}
	return &DriftReport{
		Modified: append(append([]string(nil), old.Modified...), add.Modified...),
		Deleted:  append(append([]string(nil), old.Deleted...), add.Deleted...),
		Shadowed: append(append([]string(nil), old.Shadowed...), add.Shadowed...),
		Extra:    append(append([]string(nil), old.Extra...), add.Extra...),
		At:       add.At,
	}
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isNotExist(err error) bool { return os.IsNotExist(err) }

// stamp is the timestamp in the archive dir name (20260917-214512).
func stamp(t time.Time) string { return t.Format("20060102-150405") }
