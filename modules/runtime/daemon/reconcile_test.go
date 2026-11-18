package daemon

// This file pins the 2026-09-18 live case: **the daemon serves, every command says it is not**.
//
// Twice (every WSL restart, identical signature): the pidfile held the pid of a child that
// "lost the lock race and exited" (4554 and 3080, two zombies) while the real daemon (1556
// and 2069) kept serving, and the lock kept naming them. See WriteOwn's note for the chain.
//
// So there are two distinct guarantees here, and missing one is not a fix:
//   - **written right**: the socket-holding process writes the pidfile itself, after the
//     lock (WriteOwn); the parent no longer ghosts it - the cleanup would delete that one;
//   - **read back**: when the pidfile does not stand, recover the body from the lock
//     (Reconcile), not report "not running" - a scene already broken on disk must heal.

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newgateServeProcess starts a process with the real daemon's argv0: `newgate __serve`.
// Both criteria must hit - Alive wants "newgate" in cmdline, and Reconcile's **inference**
// path additionally wants "__serve" (see isServe: proof that this is a daemon, not just
// another newgate process).
func newgateServeProcess(t *testing.T) int {
	t.Helper()
	// argv0 is set by execve directly (`cmd.Args[0]`), **no shell at all**.
	//
	// This used to be `/bin/bash -c 'exec -a "newgate __serve --port 1" sleep 30'`: the
	// extra hop (bash starts, then execs into sleep) is itself a window - /proc cmdline
	// is then bash's own `-c ...` line (which holds newgate and __serve too, so both
	// criteria seem to hold), and bash may exit any time. On 2026-09-20 the distribution
	// CI saw this fixture red twice in a row (TestReconcileHealsFromTheLock,
	// TestLastHealRecordsTheRepair), both at 0.00s, both reporting "should have found a
	// mismatch". With argv0 set directly, the cmdline is right from the moment it exists.
	cmd := exec.Command("sleep", "30")
	cmd.Args[0] = "newgate __serve --port 1"
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start the test process: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	waitForProcess(t, cmd.Process.Pid,
		func(p int) bool { return Alive(p) && isServe(p) }, "a __serve process")
	return cmd.Process.Pid
}

// waitForProcess waits until this pid really satisfies **the criterion under test**, then asserts.
//
// This used to wait for "cmdline non-empty" - not enough. The instant `cmd.Start()` returns
// the child is still bash, `exec -a` has not run, and /proc/<pid>/cmdline reads empty;
// Alive / isServe take cmdline as proof, so the test stood on a transition. An empty
// string is the **easy** bad shape: if it reads bash's `-c ...` line both criteria seem to
// hold, but the shell may exit any moment (no sleep in PATH, exec refused, slow start),
// and the assertion stands on a vanishing process - that is how it flaked on the
// distribution CI on 2026-09-20: TestReconcileHealsFromTheLock at 0.00s said "the lock has
// a live daemon, should not report not running", while kernel CI and this machine were green.
//
// So it now waits for the criterion itself. When it cannot tell, **fail with evidence**,
// not skip: a fixture that will not start usually means the criterion does not hold here.
func waitForProcess(t *testing.T, pid int, want func(int) bool, what string) {
	t.Helper()
	if _, err := os.Stat("/proc/self"); err != nil {
		t.Skipf("this machine has no /proc (not Linux?): Alive/isServe both degrade to kill(0), nothing can be tested")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if want(pid) {
			return
		}
		if time.Now().After(deadline) {
			b, _ := ioutil.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
			t.Fatalf("pid %d is still not %s after 3 seconds (Alive=%v isServe=%v cmdline=%q) - "+
				"either the fixture cannot start, or the criterion does not hold on this machine",
				pid, what, Alive(pid), isServe(pid), b)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// why puts "the scene the proxy saw" on one line, appended to assertion failures.
//
// Such flakes always root in the state of pidfile / lock / process, and CI's "should have
// found a mismatch" is unworkable - the two 2026-09-20 cases were guessed at via logs.
func why() string {
	pid, err := ReadPid()
	pidDesc := "nil"
	if pid != nil {
		pidDesc = fmt.Sprintf("%d(alive=%v)", pid.PID, Alive(pid.PID))
	}
	lock := LockHolder()
	return fmt.Sprintf("pidfile=%s err=%v; lock=%d alive=%v isServe=%v; LastHeal=%q",
		pidDesc, err, lock, Alive(lock), isServe(lock), LastHeal())
}

// zombiePid makes a **zombie**: the child exited but nobody collects it (no Wait). That is
// exactly what the live pidfile recorded - Alive says "not there" (empty cmdline +
// isZombie), yet it never disappears, so the wrong state stays on disk.
func zombiePid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/true")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start the test process: %v", err)
	}
	pid := cmd.Process.Pid
	deadline := time.Now().Add(2 * time.Second)
	for !isZombie(pid) {
		if time.Now().After(deadline) {
			_ = cmd.Wait()
			t.Skip("never saw the zombie state")
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Cleanup(func() { _ = cmd.Wait() }) // 收尸，别把僵尸留给测试进程
	return pid
}

// freePid picks a pid that does not exist. Not hard-coded 999999: that number really might.
// Alive is false for it (kill says ESRCH), the most common way a pidfile goes bad.
func freePid(t *testing.T) int {
	t.Helper()
	for pid := 3999000; pid > 399000; pid -= 7919 {
		if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); os.IsNotExist(err) {
			return pid
		}
	}
	t.Skip("cannot find a free pid (abnormal process count on this machine)")
	return 0
}

func writePidFile(t *testing.T, pid, port int) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".config", "newgate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"pid":` + strconv.Itoa(pid) + `,"port":` + strconv.Itoa(port) + `}`
	if err := ioutil.WriteFile(filepath.Join(dir, ".newgate.pid"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeLockFile(t *testing.T, pid int) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".config", "newgate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(dir, ".newgate.lock"),
		[]byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReconcileHealsFromTheLock is that scene: pidfile points at a zombie, lock at the body.
// The result must be "running, pid is it", and the pidfile repaired on the spot - else
// status/metrics stay blind, and every lazy start births another child doomed to lose the race.
func TestReconcileHealsFromTheLock(t *testing.T) {
	sandbox(t)
	live := newgateServeProcess(t)
	writePidFile(t, zombiePid(t), 8899) // 现场：pidfile 里是个僵尸
	writeLockFile(t, live)              // 锁一直是对的

	info, notes := Reconcile()
	if info == nil {
		t.Fatal("there is a live daemon in the lock, so it must not report 'not running':" + why())
	}
	if info.PID != live {
		t.Errorf("it identified the wrong process: got pid %d, want %d", info.PID, live)
	}
	// The port can only come from pidfile/config - the lock has none, don't lose it.
	if info.Port != 8899 {
		t.Errorf("the port should be taken from the pidfile: got %d", info.Port)
	}
	if len(notes) == 0 {
		t.Error("it found an inconsistency but said nothing - the doctor item relies on it for readable output")
	}
	// Repaired: the next command need not re-derive, nor decide from a dead pid again.
	back, err := ReadPid()
	if err != nil || back.PID != live {
		t.Errorf("the pidfile was not repaired to the real process: %+v (err %v)", back, err)
	}
	// After the repair Running hits directly, no mismatch left to report.
	if Running() == nil {
		t.Fatal("after the repair Running still says not running")
	}
	if _, again := Reconcile(); len(again) != 0 {
		t.Errorf("it still reports an inconsistency after they agree: %v", again)
	}
}

// TestReconcileHealsWhenPidfileIsMissing: the pidfile is gone entirely (the instant stale
// lock cleanup deleted it, see WriteOwn) - the lock must recover the body just the same.
func TestReconcileHealsWhenPidfileIsMissing(t *testing.T) {
	sandbox(t)
	live := newgateServeProcess(t)
	writeLockFile(t, live)

	info, notes := Reconcile()
	if info == nil || info.PID != live {
		t.Fatalf("with the pidfile missing it should identify the process from the lock: %+v", info)
	}
	if len(notes) == 0 {
		t.Error("an inconsistency must be stated")
	}
}

// TestReconcileLeavesALivePidfileAlone: one-way. When the pidfile points at a live process,
// believe it. At a graceful handoff's instant the pidfile is the new process and the lock
// still the old draining one (AdoptRuntime writes the pidfile first) - believing the lock
// would rewrite the pidfile back to the old process and fail the handoff on the spot.
func TestReconcileLeavesALivePidfileAlone(t *testing.T) {
	sandbox(t)
	fresh := newgateServeProcess(t) // pidfile 说的：新进程
	draining := newgateServeProcess(t)
	writePidFile(t, fresh, 8899)
	writeLockFile(t, draining) // 锁说的：还在排空的旧进程

	info, notes := Reconcile()
	if info == nil || info.PID != fresh {
		t.Fatalf("when the pidfile points at a live process it should trust the pidfile: %+v", info)
	}
	if len(notes) != 0 {
		t.Errorf("an inconsistency inside the handoff window must not alarm (it resolves itself within milliseconds): %v", notes)
	}
	back, _ := ReadPid()
	if back == nil || back.PID != fresh {
		t.Errorf("the pidfile was modified - the handoff would fail because of it: %+v", back)
	}
}

// TestReconcileSaysNothingWhenNobodyRuns: "really not running" is not a mismatch: with
// no live daemon at either place, say nothing (doctor reports skip, not warn).
func TestReconcileSaysNothingWhenNobodyRuns(t *testing.T) {
	t.Run("both sides are dead pids", func(t *testing.T) {
		sandbox(t)
		dead := freePid(t)
		writePidFile(t, dead, 8899)
		writeLockFile(t, dead)
		if info, notes := Reconcile(); info != nil || len(notes) != 0 {
			t.Errorf("when not running it should give (nil, empty): %+v %v", info, notes)
		}
	})
	t.Run("only a stale lock", func(t *testing.T) {
		sandbox(t)
		writeLockFile(t, freePid(t))
		if info, notes := Reconcile(); info != nil || len(notes) != 0 {
			t.Errorf("when not running it should give (nil, empty): %+v %v", info, notes)
		}
	})
}

// TestReconcileRefusesALockThatIsNotTheDaemon: "a newgate process exists" is not enough -
// the lock's pid may be a recycled number taken by another newgate process (the CLI).
// Using it as the daemon cascades: status reports a fake pid, stop SIGTERMs the user's
// shell process. So the inference path demands harder proof (__serve), else says "don't know".
func TestReconcileRefusesALockThatIsNotTheDaemon(t *testing.T) {
	sandbox(t)
	notADaemon := newgateNamedProcess(t) // argv0 带 newgate，但不是 __serve
	writePidFile(t, freePid(t), 8899)
	writeLockFile(t, notADaemon)

	info, notes := Reconcile()
	if info != nil {
		t.Fatalf("with insufficient evidence (not __serve) it must not be taken as the real process: %+v", info)
	}
	if len(notes) != 0 {
		t.Errorf("if it was not identified, do not alarm: %v", notes)
	}
	// Nor may it touch the pidfile along the way - that would cover facts with a guess.
	back, _ := ReadPid()
	if back == nil || back.PID == notADaemon {
		t.Errorf("the pidfile was rewritten by a guess: %+v", back)
	}
}

// TestStopTakesTheLockHolderWhenPidfileIsStale: stop must also pick the right body. The old
// code reported "not running anyway" at a dead pidfile and **deleted the live daemon's lock**
// - service up, lock gone, and the next start can birth a second instance fighting for the port.
func TestStopTakesTheLockHolderWhenPidfileIsStale(t *testing.T) {
	sandbox(t)
	live := newgateServeProcess(t)
	writePidFile(t, freePid(t), 8899)
	writeLockFile(t, live)

	got, err := Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got != live {
		t.Fatalf("stopped the wrong process: got pid %d, want %d", got, live)
	}
	for _, f := range []string{".config/newgate/.newgate.pid", ".config/newgate/.newgate.lock"} {
		if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), f)); !os.IsNotExist(err) {
			t.Errorf("%s was not cleared", f)
		}
	}
}

// TestStaleLockCleanupThenWriteOwnIsTheFix pins the **order** of the fix: stale lock cleanup
// (RemovePid inside AcquireLock) must happen **before** writing the pidfile. The live case
// was the reverse: the parent wrote first, the child then cleared the stale lock and the
// fresh pidfile together, leaving "daemon alive, lock naming it, pidfile absent", never refilled.
//
// Here we walk the child's two real actions: AcquireLock (clears stale files), then
// WriteOwn (writes itself). After that the pidfile must name this process.
func TestStaleLockCleanupThenWriteOwnIsTheFix(t *testing.T) {
	sandbox(t)
	stale := freePid(t)
	writePidFile(t, stale, 8899) // 上个进程留下的两样东西都指向死 pid
	writeLockFile(t, stale)

	if err := AcquireLock(); err != nil {
		t.Fatalf("a stale lock should be cleaned automatically; taking the lock must not fail: %v", err)
	}
	t.Cleanup(RemoveLock)
	if err := WriteOwn(18899); err != nil {
		t.Fatalf("WriteOwn: %v", err)
	}
	i, err := ReadPid()
	if err != nil {
		t.Fatalf("the pidfile was not written: %v", err)
	}
	if i.PID != os.Getpid() || i.Port != 18899 {
		t.Errorf("the pidfile should record this process: %+v", i)
	}
	if i.StartedAt == "" {
		t.Error("started_at is empty - `newgate status` uses it to show how long it has been up")
	}
}

// TestProcStartTimeMatchesPs: /proc gives only "ticks since boot"; btime must be added for
// wall-clock time. A wrong derivation shows a nonsensical start time in status, and that is
// just what one looks at to answer "did it really restart or not".
func TestProcStartTimeMatchesPs(t *testing.T) {
	got := procStartTime(os.Getpid())
	if got.IsZero() {
		t.Skip("this kernel cannot read btime/starttime")
	}
	// Self-check: this process's start is between "long ago" and "now", and not 1970.
	if got.After(time.Now().Add(time.Minute)) || got.Before(time.Now().Add(-10*365*24*time.Hour)) {
		t.Fatalf("the inferred start time is unreasonable: %v", got)
	}
}

// TestLastHealRecordsTheRepair: once repaired, the pidfile is right, and the next reconcile
// is all green - the mismatch is gone. Earlier `newgate doctor` items (chain, proxy) read
// Running(), so they flatten the scene first and the "daemon" item has nothing to say. This
// record is where "not silent" lands on the self-healing path: repaired quietly, but sayable.
func TestLastHealRecordsTheRepair(t *testing.T) {
	sandbox(t)
	live := newgateServeProcess(t)
	writePidFile(t, freePid(t), 8899)
	writeLockFile(t, live)

	if _, notes := Reconcile(); len(notes) == 0 {
		t.Fatal("this pass should have found an inconsistency:" + why())
	}
	h := LastHeal()
	if h == "" {
		t.Fatal("it repaired but did not record it - the doctor would report a just-fixed inconsistency as all green")
	}
	for _, want := range []string{"pidfile", strconv.Itoa(live)} {
		if !strings.Contains(h, want) {
			t.Errorf("that record should contain %q: %q", want, h)
		}
	}
	// The second pass already agrees: no new entry (else every check reports the same line).
	before := LastHeal()
	if _, notes := Reconcile(); len(notes) != 0 {
		t.Fatalf("it still reports after they agree: %v", notes)
	}
	if after := LastHeal(); after != before {
		t.Errorf("no new inconsistency, yet the record changed: %q -> %q", before, after)
	}
}
