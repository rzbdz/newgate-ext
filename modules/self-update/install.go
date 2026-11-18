package selfupdate

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This file is the "swap it in" half: stage, smoke-test, keep the old, rename, hand off
// the daemon, roll back. Both sources arrive here with a file, identical from here on.

// installTarget is the file to replace: the running binary (symlinks resolved).
//
// Replace "itself", not some conventional path: wherever the user installed it (~/.local/bin,
// /data/home2/.../WorkSpace/bin), and the PATH shim points there too.
func installTarget() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	// Replacing a temporary binary built by `go run` is pointless: the next go run is a fresh one.
	if strings.Contains(exe, string(filepath.Separator)+"go-build") {
		return "", i18n.E("this binary was started by `go run` ({exe}); build one with build/build.sh first", i18n.A{"exe": exe})
	}
	return exe, nil
}

// stage creates the candidate in the target's **same directory**: rename is atomic only
// within one filesystem. Permissions come from the old file (the daemon may run as
// another user and must be able to execute the new one).
//
// The caller holds the upgrade lock, so leftover candidates here (a previous upgrade
// killed mid-download or mid-build) are unused; clean them, or each break leaves a file.
func stage(target string) (*os.File, error) {
	pattern := "." + filepath.Base(target) + ".new-"
	if stale, _ := filepath.Glob(filepath.Join(filepath.Dir(target), pattern+"*")); len(stale) > 0 {
		for _, f := range stale {
			_ = os.Remove(f)
		}
	}
	f, err := os.CreateTemp(filepath.Dir(target), pattern)
	if err != nil {
		return nil, i18n.Ef(err, "cannot write next to {target}: {err}", i18n.A{"target": target})
	}
	mode := os.FileMode(0o755)
	if fi, err := os.Stat(target); err == nil {
		mode = fi.Mode().Perm() | 0o100
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	return f, nil
}

// stageFrom copies an artifact already elsewhere (source build output) into a candidate.
func stageFrom(src, target string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	f, err := stage(target)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, in); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// smoke runs the candidate's `version` under the name newgate, returning the version.
//
// The **only** place to stop a bad artifact before swapping: wrong architecture (exec
// format error), truncated download, another distribution - after the swap everything
// breaks, including the command to swap back (rollback exists, but one must find .prev).
//
// argv0 must be newgate: the multi-call binary dispatches on it, another name = a profile.
func smoke(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	cmd.Args[0] = "newgate"
	cmd.Env = append(os.Environ(), "NEWGATE_LANG=en", "NO_COLOR=1")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	f := strings.Fields(line)
	if len(f) < 2 || f[0] != "newgate" {
		return "", i18n.E("unexpected `version` output: {line}", i18n.A{"line": firstLine(line)})
	}
	return f[1], nil
}

// install swaps the candidate in, keeping the old one as <target>.prev. **No old version,
// no swap**: every failure after the swap (daemon handoff, new process will not start)
// falls back to .prev, and without it there is no way back.
//
// The old version is kept via a **hard link**: link, then rename, so the target path
// always holds a complete binary (moving target to .prev first leaves the PATH shim
// briefly with no command). Running processes are unaffected: they hold the old inode,
// and rename only swaps the directory entry (Text file busy, kernel CLAUDE.md §3.1).
func install(candidate, target string) (string, error) {
	prev := target + ".prev"
	_ = os.Remove(prev)
	if err := os.Link(target, prev); err != nil {
		if err := copyFile(target, prev); err != nil {
			_ = os.Remove(candidate)
			return "", i18n.Ef(err, "cannot keep the current binary as {prev}, so there would be no way back; nothing was replaced: {err}",
				i18n.A{"prev": prev})
		}
	}
	if err := os.Rename(candidate, target); err != nil {
		_ = os.Remove(candidate)
		return "", i18n.Ef(err, "cannot replace {target}: {err}", i18n.A{"target": target})
	}
	return prev, nil
}

// rollback swaps <target>.prev back in, and the replaced one becomes the new .prev (rollback
// again swaps it back - it is a switch, not a one-way street).
func rollback(ctx context.Context, target string) (string, error) {
	prev := target + ".prev"
	if _, err := os.Stat(prev); err != nil {
		return "", i18n.E("there is no previous binary to roll back to ({prev} is missing)", i18n.A{"prev": prev})
	}
	ver, err := smoke(ctx, prev)
	if err != nil {
		return "", i18n.Ef(err, "the previous binary does not run: {err}", nil)
	}
	swap := target + ".swap-" + strconv.Itoa(os.Getpid())
	_ = os.Remove(swap)
	if err := os.Link(target, swap); err != nil {
		if err := copyFile(target, swap); err != nil {
			return "", err
		}
	}
	if err := os.Rename(prev, target); err != nil {
		_ = os.Remove(swap)
		return "", i18n.Ef(err, "cannot replace {target}: {err}", i18n.A{"target": target})
	}
	if err := os.Rename(swap, prev); err != nil {
		_ = os.Remove(swap)
	}
	return ver, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// restartDaemon moves the running daemon onto the new binary: run `restart` **with the
// new copy** (graceful handoff: old process hands the socket over, draining).
//
// Why not send /__newgate/upgrade ourselves: that protocol (probe, token, ready, then
// stop+start) is fully implemented in the kernel runtime's restart; a second copy here
// would be two. Running it with the new binary makes the handoff logic the new version.
//
// A daemon not started from target (another install) is left alone - this swaps this
// one; restarting that one keeps it on the old build, thinking it upgraded.
func restartDaemon(target string, log io.Writer) (string, error) {
	info, _ := controlplane.State()
	if info == nil {
		return i18n.T("the daemon is not running; its next start uses the new binary", nil), nil
	}
	if info.Exe != "" && !sameFile(info.Exe, target) {
		return i18n.T("the daemon (pid {pid}) runs from {exe}, not {target}; it was left alone",
			i18n.A{"pid": info.PID, "exe": info.Exe, "target": target}), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, target, "restart")
	cmd.Args[0] = "newgate"
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil {
		return "", i18n.Ef(err, "`newgate restart` failed: {err}", nil)
	}
	// Not just "a process exists": it must **answer** (the control plane's status responds).
	// A new version that starts but faults in its serving loop cannot be caught by `version`.
	// restart itself waits up to 5s; a few more seconds of margin here.
	var after *controlplane.Info
	var doc *controlplane.Doc
	for i := 0; i < 20; i++ {
		if after, doc = controlplane.State(); after != nil && doc != nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if after == nil {
		return "", i18n.E("after the restart no daemon is running; see newgate logs", nil)
	}
	if doc == nil {
		return "", i18n.E("after the restart the daemon (pid {pid}) does not answer on its control port; see newgate logs",
			i18n.A{"pid": after.PID})
	}
	if runtime.GOOS == "linux" && !sameFile("/proc/"+strconv.Itoa(after.PID)+"/exe", target) {
		return "", i18n.E("the daemon (pid {pid}) is still running an old binary; see newgate logs",
			i18n.A{"pid": after.PID})
	}
	return i18n.T("daemon now runs the new binary (pid {old} → {new})",
		i18n.A{"old": info.PID, "new": after.PID}), nil
}

func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

// lockUpgrade ensures only one upgrade swaps files at a time. flock: the lock dies with
// the process, leaving no stale lock for a human to clean up.
func lockUpgrade() (func(), error) {
	if err := os.MkdirAll(paths.Root(), 0o2770); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(paths.Root(), ".self-update.lock"), os.O_CREATE|os.O_RDWR, 0o660)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, i18n.E("another upgrade is already running", nil)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
