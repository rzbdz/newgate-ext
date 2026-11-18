package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Baseline-carrying writes (compare-and-swap): **whichever
// copy you read, that is the copy you must still be writing**.
//
// # Why it is needed
//
// One config file has two writers: the command line, and the web UI. During the few minutes the
// user edits a tier in the browser, he may well have typed `newgate tier` in another terminal --
// both write, the later one wins, and the earlier person's change **vanishes silently**. This is
// not theoretical: writeJSON's own comment says "there is currently no cross-process write lock,
// and with several writers updating the same file concurrently the last writer still wins".
//
// # Why detect rather than prevent
//
// Making every config-writing path take a lock amounts to requiring every kernel module
// to change how it writes (today they read-modify-write directly in their own Start).
// And **detecting** gives something more valuable: it can put both conflicting copies
// in front of the user and let him decide -- "your change and someone else's change
// collided on the same passage" is far more useful than "save failed, please retry".
//
// So the criterion is: **baseline comparison handles correctness (finding the conflict), flock
// handles atomicity (only one writer is reading-comparing-writing at any instant)**. The lock only
// works for writers that take the same lock; the command line does not, and conflicts it causes are
// found by the baseline. The tiny window between the check and the rename is an acknowledged cost.

// revPrefix makes the baseline recognizable at a glance as a
// content hash (and a version slot for a future algorithm change).
const revPrefix = "sha256:"

// lockName is the write lock file, under the config root.
//
// Separate from the daemon's pid/lock: that one is "who is serving this port", this one
// is "who is changing this config"; sharing one file would drag on each other (and the
// daemon's lock lives in the runtime directory, with completely different semantics).
const lockName = ".write.lock"

// ErrStale is "the baseline does not match". Callers use
// errors.As to get a *StaleError with both source texts.
var ErrStale = errors.New("store: the file changed on disk since it was loaded")

// StaleError carries **both source texts**.
//
// Only by laying both copies out can the user decide what to do: his change
// and the other person's may have just happened to collide on the same passage, or may be
// completely different. Replying with just "it is stale" leaves that to the user's guess.
type StaleError struct {
	Path    string
	Base    string // the copy you loaded
	Current string // the copy now on disk
	Disk    []byte // the raw on-disk content (for showing the user, and for diffing)
}

func (e *StaleError) Error() string {
	return fmt.Sprintf("%s (loaded %s, on disk %s)", e.Path, shortRev(e.Base), shortRev(e.Current))
}
func (e *StaleError) Unwrap() error { return ErrStale }

func shortRev(rev string) string {
	if len(rev) > 15 {
		return rev[:15]
	}
	if rev == "" {
		return "(absent)"
	}
	return rev
}

// Revision is the file content hash; the empty string when
// the file does not exist ("baseline = nothing at all").
func Revision(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return RevisionOf(b)
}

// RevisionOf is one piece of content's identity (for when the
// caller has already read the content, saving one disk read).
func RevisionOf(b []byte) string {
	sum := sha256.Sum256(b)
	return revPrefix + hex.EncodeToString(sum[:])
}

// WriteIfUnchanged writes data when base still equals
// the current disk state, returning the new baseline.
//
// On a mismatch it **writes nothing** and returns a *StaleError (with the
// disk source text). The read, compare and write steps complete inside one
// flock -- otherwise two writers would each read the old content, each pass
// the comparison, and the later rename would overwrite the earlier change.
func WriteIfUnchanged(path, base string, data []byte) (string, error) {
	var newRev string
	_, err := WithLock(func() error {
		disk, err := os.ReadFile(path)
		switch {
		case err == nil:
		case os.IsNotExist(err):
			disk = nil // file absent: the baseline must be the empty string, or it counts as a conflict
		default:
			return err
		}
		current := ""
		if disk != nil {
			current = RevisionOf(disk)
		}
		if current != base {
			return &StaleError{Path: path, Base: base, Current: current, Disk: disk}
		}
		if err := writeFileAtomic(path, data); err != nil {
			return err
		}
		newRev = RevisionOf(data)
		return nil
	})
	return newRev, err
}

// RemoveIfUnchanged deletes a file, provided it is still **the version at load**
// (base is the baseline the snapshot reported; the empty string = it is expected not
// to exist -- which is exactly "not created yet", so deleting is idempotent success).
//
// Why deleting a file needs CAS too: when the UI presses "delete this
// tier", the command line may have just changed or deleted the same file. Without
// baseline comparison the UI thinks it is deleting something it saw, while it may
// already be other content -- one delete wipes config someone just wrote. Report
// StaleError, and let the UI go through the conflict path that lays both sides out.
func RemoveIfUnchanged(path, base string) error {
	_, err := WithLock(func() error {
		disk, err := os.ReadFile(path)
		switch {
		case err == nil:
		case os.IsNotExist(err):
			if base == "" {
				return nil // we expected it to be absent: the delete succeeds
			}
			return &StaleError{Path: path, Base: base}
		default:
			return err
		}
		current := ""
		if disk != nil {
			current = RevisionOf(disk)
		}
		if current != base {
			return &StaleError{Path: path, Base: base, Current: current, Disk: disk}
		}
		return os.Remove(path)
	})
	return err
}

// WithLock takes the config directory's write lock and runs fn.
//
// flock rather than a pidfile: the lock is gone as soon as the fd closes, and a process killed
// with kill -9 leaves no stale lock needing manual cleanup -- this repository already had one
// scene where a stale pidfile made it judge "not running", no need to create another. flock
// is cross-process and cross-goroutine (two fds of the same file are two independent open
// file descriptions), so a UI and a command line inside one process exclude each other too.
//
// If it cannot get it, wait a while -- the user pressed save,
// and waiting two seconds is better than replying "busy".
func WithLock(fn func() error) (bool, error) {
	path := filepath.Join(paths.Root(), lockName)
	if err := os.MkdirAll(filepath.Dir(path), 0o2770); err != nil {
		return false, err
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o660)
		if err != nil {
			return false, err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			defer func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}()
			return true, fn()
		}
		_ = f.Close()
		if err != syscall.EWOULDBLOCK {
			return false, err
		}
		if !time.Now().Before(deadline) {
			return false, i18n.E("another save is in progress, try again in a moment", nil)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// writeFileAtomic: a temporary file in the same directory +
// rename, so readers only ever see a complete old or new version.
//
// The temporary name carries a random suffix, **not a fixed name**: a
// fixed name would make two concurrent writers collide on the same inode -- one
// renames away, and the other renames a copy the first one had written halfway.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o2770); err != nil {
		return err
	}
	// First copy **the current copy** into the history ring (see history.go). Before
	// the replacement, and it does not refuse the write when it fails -- making
	// the user's save fail to keep a backup turns the safety net into a roadblock.
	snapshotBeforeWrite(path)
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	// 0600 is too strict (the config directory is shared
	// for group reading); land 0660 and then rename.
	if err := f.Chmod(0o660); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
