package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rzbdz/newgate-ext/lib/httpx"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate/lib/i18n"
)

type Info struct {
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	StartedAt string `json:"started_at"`
	Exe       string `json:"exe"`
}

func ReadPid() (*Info, error) {
	b, err := ioutil.ReadFile(paths.PidFile())
	if err != nil && paths.LegacyPidFile() != "" {
		// Old versions wrote the pid in $HOME - the first stop/status after an upgrade uses
		// it to find a still-running old daemon (and clears it once found).
		b, err = ioutil.ReadFile(paths.LegacyPidFile())
	}
	if err != nil {
		return nil, err
	}
	var i Info
	if err := json.Unmarshal(b, &i); err != nil {
		// Tolerate a plain-number pid file
		if n, e := strconv.Atoi(strings.TrimSpace(string(b))); e == nil {
			return &Info{PID: n}, nil
		}
		return nil, err
	}
	return &i, nil
}

func WritePid(i *Info) error {
	b, _ := json.MarshalIndent(i, "", "  ")
	return ioutil.WriteFile(paths.PidFile(), append(b, '\n'), 0o660)
}

// RemovePid / RemoveLock clear both places (the shared dir + the old $HOME spot):
// when stopping an old daemon a legacy pid points to, the old files must go too,
// or the next read takes them as a live instance again.
func RemovePid() {
	_ = os.Remove(paths.PidFile())
	if p := paths.LegacyPidFile(); p != "" {
		_ = os.Remove(p)
	}
}

func RemoveLock() {
	_ = os.Remove(paths.LockFile())
	if p := paths.LegacyLockFile(); p != "" {
		_ = os.Remove(p)
	}
}

// Alive tells whether the process is alive; pids get reused, so /proc's cmdline is checked too.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		// EPERM: the process **is** there, just not ours to manage (a daemon started by
		// another user in a shared deployment). "Alive" and "can I signal it" differ; the
		// latter falls back to the control endpoint in Stop. Calling it dead means a claude
		// user forever sees "proxy not running" - exactly the "no status" bug being fixed.
		if err != syscall.EPERM {
			return false
		}
	}
	b, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return true // 非 Linux 或读不到，退化为只看 kill(0)
	}
	// An empty cmdline means one of two things, and they must be told apart:
	//   1. the transitional exec state (a /proc read of empty during an image swap) - alive.
	//      Spawn returning straight into Running()/Stop() hits this window; calling it
	//      dead yields "just started, but reports not running";
	//   2. a zombie (dead, uncollected) - dead.
	if len(b) == 0 {
		return !isZombie(pid)
	}
	return strings.Contains(string(b), "newgate")
}

// isZombie reads the state char of /proc/<pid>/stat. stat's comm field may hold spaces
// and parentheses, so take the state char after the last ')'.
func isZombie(pid int) bool {
	st, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	if i := bytes.LastIndexByte(st, ')'); i >= 0 && i+2 < len(st) {
		return st[i+2] == 'Z'
	}
	return false
}

// isServe confirms this pid is **the daemon itself**, not another process also named
// newgate (the CLI itself, another instance). The criterion is `__serve` in cmdline -
// only `newgate __serve` holds the listening socket.
//
// It is used only on the **inference** path deriving the daemon from the lock file, so
// it demands harder evidence than Alive; the pidfile path is the writer proving itself,
// and still uses only Alive. When it cannot tell (not Linux, no /proc) it does not guess.
//
// "Cannot tell" and "insufficient evidence" are handled differently here - do not mix:
// cannot tell = this machine gives no criterion (let it pass); insufficient =
// the criterion exists but says nothing (**refuse**, honestly say "don't know"). The
// latter used to pass too (`len(b) == 0 || ...`), and flaked in CI on 2026-09-20: the
// test's process was exec'ing into another image, cmdline empty, bash taken as the daemon.
func isServe(pid int) bool {
	b, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		// This machine has no /proc (not Linux): no criterion exists, let it pass.
		if _, e := os.Stat("/proc/self"); e != nil {
			return true
		}
		// /proc is there but this pid is unreadable: it died between Alive and here. **Just
		// died is not the daemon** - taking a gone pid as the real one (status reports a
		// fake pid, stop SIGTERMs someone else's process) is exactly what this path guards.
		return false
	}
	// An empty cmdline is the exec transition (see Alive). On the **inference** path it
	// means insufficient evidence, and insufficient evidence says "don't know", never a guess.
	return len(b) > 0 && strings.Contains(string(b), "__serve")
}

// LockHolder reads the pid in the lock file (both locations), 0 when unreadable.
//
// The lock is the **authoritative evidence** of "who is serving": the process that won
// the listener writes it with O_EXCL, and it lives as long as that process; during a
// graceful handoff AdoptRuntime rewrites it to the new process. The pidfile is its cache.
func LockHolder() int {
	for _, f := range []string{paths.LockFile(), paths.LegacyLockFile()} {
		if f == "" {
			continue
		}
		b, err := ioutil.ReadFile(f)
		if err != nil {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
			return pid
		}
	}
	return 0
}

// AcquireLock takes the exclusive lock. A dead lock file is cleaned up automatically.
func AcquireLock() error {
	// Check both locations: an old daemon's live lock is in $HOME, and ignoring it would
	// start a second instance that then fights over the port.
	for _, f := range []string{paths.LockFile(), paths.LegacyLockFile()} {
		if f == "" {
			continue
		}
		if b, err := ioutil.ReadFile(f); err == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			if Alive(pid) {
				return i18n.E("newgate is already running (pid {pid}). Use `newgate restart` or `newgate stop`",
					i18n.A{"pid": pid})
			}
			// Stale lock
			RemoveLock()
			RemovePid()
		}
	}
	f, err := os.OpenFile(paths.LockFile(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o660)
	if err != nil {
		return i18n.Ef(err, "cannot acquire the lock: {err}", nil)
	}
	defer f.Close()
	f.WriteString(strconv.Itoa(os.Getpid()))
	f.WriteString("\n")
	return err
}

// Running returns info about the currently live instance.
func Running() *Info {
	i, _ := Reconcile()
	return i
}

// LastHeal reports the last "reconcile repaired the pidfile" here, "" if none.
//
// Why: once repaired, the pidfile is right and **the next reconcile is all green**. But
// earlier `newgate doctor` items (chain, proxy) read Running(), repair the pidfile first,
// and by the time the "daemon" item runs the mismatch is invisible - all green, and the
// user never learns what happened. This is "not silent" on the self-healing path.
func LastHeal() string {
	healMu.Lock()
	defer healMu.Unlock()
	return healNote
}

var (
	healMu   sync.Mutex
	healNote string
)

// Reconcile answers "who is really running" and repairs the pidfile back to the real body;
// the second value is the mismatch it found (doctor turns it into words; empty if all well).
//
// **Why the pidfile alone is not enough** (2026-09-18, twice, both on restart): it holds
// the pid of a child that "lost the lock race and exited on the spot" while the daemon
// kept serving - see the full chain recorded in WriteOwn. The lock file is authoritative,
// so when the pidfile does not stand, reconcile against the lock.
//
// But the **direction is one-way**: when the pidfile points at a live process, believe it,
// never the lock. At the instant of a graceful handoff the parent rewrites the pidfile to
// itself first and the lock after (AdoptRuntime), so "pidfile = new, lock = old draining" -
// believing the lock would rewrite the pidfile back to the old process, and handoff fails.
//
// "The pidfile points at a dead process" and "really not running" must be kept apart:
// the former is a mismatch (say it, fix it), the latter a normal state (say nothing).
func Reconcile() (*Info, []string) {
	i, err := ReadPid()
	if err == nil && i != nil && Alive(i.PID) {
		return i, nil
	}
	pid := LockHolder()
	if pid <= 0 || !Alive(pid) || !isServe(pid) {
		return nil, nil
	}
	healed := &Info{PID: pid, Port: portOf(i), Exe: exeOf(pid)}
	if t := procStartTime(pid); !t.IsZero() {
		healed.StartedAt = t.Format(time.RFC3339)
	}
	note := i18n.T("The process the pidfile points at is gone ({what}), and the lock file says the real daemon is pid {pid}",
		i18n.A{"what": describePid(i, err), "pid": pid})
	if werr := WritePid(healed); werr != nil {
		note += i18n.T("; the pidfile could not be corrected: {err}", i18n.A{"err": werr.Error()})
	} else {
		note += i18n.T(" (pidfile corrected)", nil)
		healMu.Lock()
		healNote = note
		healMu.Unlock()
	}
	return healed, []string{note}
}

// describePid puts "what the pidfile says" into words, used in the mismatch line above.
func describePid(i *Info, err error) string {
	switch {
	case err != nil || i == nil:
		return i18n.T("the pidfile is unreadable", nil)
	case i.PID <= 0:
		return i18n.T("the pidfile holds no pid", nil)
	default:
		return fmt.Sprintf("pid %d", i.PID)
	}
}

// portOf prefers the port recorded in the pidfile; when the pidfile is unusable, the config's.
func portOf(i *Info) int {
	if i != nil && i.Port > 0 {
		return i.Port
	}
	if st := store.LoadState(); st != nil {
		return st.Port
	}
	return 0
}

func exeOf(pid int) string {
	if p, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
		return p
	}
	return ""
}

// procStartTime derives a process's start time: /proc gives only "ticks since boot" (stat
// field 20 after pid/comm), plus /proc/stat's btime for wall-clock time. Unavailable =
// zero - it only goes into the pidfile for humans, and a guess is worse than a blank.
func procStartTime(pid int) time.Time {
	btime := int64(0)
	if b, err := ioutil.ReadFile("/proc/stat"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "btime ") {
				btime, _ = strconv.ParseInt(strings.TrimSpace(line[len("btime "):]), 10, 64)
				break
			}
		}
	}
	st, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil || btime == 0 {
		return time.Time{}
	}
	rest := st[bytes.LastIndexByte(st, ')')+1:]
	fields := strings.Fields(string(rest))
	if len(fields) < 20 {
		return time.Time{}
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return time.Time{}
	}
	// Linux CLK_TCK is always 100 (os.Sysconf is not in the std lib, and this is just a timestamp)
	return time.Unix(btime+ticks/100, 0)
}

// WriteOwn has the daemon **itself** write the pidfile: it holds the socket.
//
// Two live cases 2026-09-18 (every restart, identical signature): the pidfile held a
// child's pid that "lost the lock race and exited on the spot" (4554, 3080 - two
// zombies) while the real daemon (1556, 2069) kept serving, and the lock named them.
// The chain is:
//
//  1. lazy start -> Spawn a child -> **the parent writes the pidfile first**;
//  2. the child's AcquireLock sees the **stale lock** -> RemoveLock + RemovePid, deleting
//     the pidfile just written in step 1;
//  3. the child takes the lock and serves, and **nobody writes a pidfile again**;
//  4. every later command reads "not running" -> lazy-starts another child doomed to
//     lose the lock race -> its pid lands in the pidfile -> a dead pid stays there.
//
// Moving the write to **after the lock, done by the holder**, leaves just one writer - it lives.
func WriteOwn(port int) error {
	if port <= 0 {
		if st := store.LoadState(); st != nil {
			port = st.Port
		}
	}
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	}
	return WritePid(&Info{PID: os.Getpid(), Port: port,
		StartedAt: time.Now().Format(time.RFC3339), Exe: exe})
}

// Spawn relaunches self in __serve mode, as a background daemon.
//
// **The pidfile is not written here** (2026-09-18): the writer must be the process that
// really took the lock and serves - the child itself (see WriteOwn's full chain: every
// copy a parent writes may be deleted by the child's stale-lock cleanup, never refilled).
// The returned Info is still the child's pid, so "proxy started, pid N" is right.
func Spawn(port int) (*Info, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	// 0660: other users can read the log to troubleshoot (setgid dir keeps the group)
	logf, err := os.OpenFile(paths.LogFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o660)
	if err != nil {
		return nil, err
	}
	defer logf.Close()

	cmd := exec.Command(exe, "__serve", "--port", strconv.Itoa(port))
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // 脱离终端
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	info := &Info{PID: cmd.Process.Pid, Port: port,
		StartedAt: time.Now().Format(time.RFC3339), Exe: exe}
	_ = cmd.Process.Release()
	return info, nil
}

// SpawnHandoff is the core of an nginx-style graceful upgrade: hand the listening socket
// to the new binary as an fd, wait for it to take over (ready byte), return its info.
//
// Why this means zero downtime: socket availability is maintained by the kernel, decoupled
// from process life. The parent dups the fd to the child, both momentarily accept on the
// same socket (kernel spreads), then the parent only drains in-flight requests - the client
// never faces a single millisecond of "connection refused". A Claude Code session
// developing newgate crosses the proxy itself, so restart must never cut the line beneath.
//
// Contract (Serve side cooperates):
//   - fd3 = listening socket; fd4 = ready pipe write end
//   - the child binds the socket and writes one byte to fd4 just before accept
func SpawnHandoff(port int, ln net.Listener) (*Info, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	tcp, ok := ln.(*net.TCPListener)
	if !ok {
		return nil, i18n.E("handoff needs a TCP listener, got {type}",
			i18n.A{"type": fmt.Sprintf("%T", ln)})
	}
	// File() returns a duped new fd - the parent's own listener is unaffected, and the
	// later Shutdown closes the parent's copy.
	listenerFile, err := tcp.File()
	if err != nil {
		return nil, i18n.Ef(err, "cannot extract the listener fd: {err}", nil)
	}
	defer listenerFile.Close()

	// The ready pipe: the child's only proof that it is on the socket. No byte, no handoff.
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer readyR.Close()

	// 0660: other users can read the log to troubleshoot (setgid dir keeps the group)
	logf, err := os.OpenFile(paths.LogFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o660)
	if err != nil {
		return nil, err
	}
	defer logf.Close()

	cmd := exec.Command(exe, "__serve", "--port", strconv.Itoa(port))
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // 脱离终端
	cmd.ExtraFiles = []*os.File{listenerFile, readyW}    // fd3=socket, fd4=ready
	cmd.Env = append(os.Environ(),
		"NEWGATE_LISTENER_FD=3", "NEWGATE_READY_FD=4")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// The parent drops the write end at once: else a dead child leaves the read end waiting.
	readyW.Close()
	if err := readyR.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	var b [1]byte
	if _, err := io.ReadFull(readyR, b[:]); err != nil {
		// The new process never got on the socket: kill it; this process keeps it, service unaffected
		_ = cmd.Process.Kill()
		return nil, i18n.E("the new process did not attach to the listening socket within 5 seconds: {err}",
			i18n.A{"err": err})
	}
	info := &Info{PID: cmd.Process.Pid, Port: port,
		StartedAt: time.Now().Format(time.RFC3339), Exe: exe}
	_ = cmd.Process.Release()
	return info, nil
}

// AdoptRuntime moves the pid file and lock ownership to the new process after a handoff.
// Only the shared dir: the legacy $HOME location migrated during the handoff already.
func AdoptRuntime(i *Info) error {
	if err := WritePid(i); err != nil {
		return err
	}
	return ioutil.WriteFile(paths.LockFile(),
		[]byte(strconv.Itoa(i.PID)+"\n"), 0o660)
}

// Stop stops gracefully, then force-kills if it does not go.
//
// Shared-deployment fallback: the daemon may be another user's (root starts, claude stops).
// The group grants reading files, not kill() - when the signal cannot go out (EPERM),
// use the proxy's own control endpoint (POST /__newgate/stop + ControlToken) to make it quit.
//
// Reconcile decides whom to kill, not the pidfile alone: a dead pid still yields the body.
// Otherwise `newgate stop` reports "not running anyway" at a broken pidfile and deletes
// the live daemon's lock - service still up, lock gone (2026-09-18 live).
func Stop() (int, error) {
	i, _ := Reconcile()
	if i == nil {
		// No live daemon at either place: clear the dead files, so the next read does not see them.
		RemovePid()
		RemoveLock()
		return 0, nil
	}
	if err := syscall.Kill(i.PID, syscall.SIGTERM); err == syscall.EPERM {
		return stopViaHTTP(i)
	}
	for k := 0; k < 40; k++ { // 最多等 2s
		if !Alive(i.PID) {
			RemovePid()
			RemoveLock()
			return i.PID, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(i.PID, syscall.SIGKILL)
	RemovePid()
	RemoveLock()
	return i.PID, nil
}

// stopViaHTTP makes a daemon started by another user exit on its own.
//
// The token lives in state.json, as secret as the providers' keys (0660, group-readable):
// group members able to read it are trusted on this machine, so stop rights are fair.
func stopViaHTTP(i *Info) (int, error) {
	st := store.LoadState()
	if st.ControlToken == "" {
		return 0, i18n.E("the proxy (pid {pid}) runs as another user and has no control token — ask the user who started it to run `newgate stop` once (after that any group member can stop it)",
			i18n.A{"pid": i.PID})
	}
	port := i.Port
	if port <= 0 {
		port = st.Port
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/__newgate/stop", port), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+st.ControlToken)
	resp, err := httpx.LocalClient(3 * time.Second).Do(req)
	if err != nil {
		return 0, i18n.Ef(err, "cannot send the stop request to the proxy (pid {pid}): {err}",
			i18n.A{"pid": i.PID})
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, i18n.E("the proxy refused the stop request (HTTP {code})", i18n.A{"code": resp.StatusCode})
	}
	for k := 0; k < 40; k++ { // 最多等 2s
		if !Alive(i.PID) {
			RemovePid()
			RemoveLock()
			return i.PID, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0, i18n.E("the stop request was accepted, but the proxy (pid {pid}) did not exit within 2 seconds (see the log {log})",
		i18n.A{"pid": i.PID, "log": paths.LogFile()})
}
