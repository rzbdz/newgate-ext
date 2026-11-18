package daemon

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/lib/i18n"
)

// sandbox confines the whole path set (incl. the legacy $HOME spots) to a temp dir.
// Note: must not set NEWGATE_HOME - that disables the legacy fallback; changing HOME is:
// root() and LegacyPidFile() both land in the sandbox, and no live daemon is touched.
func sandbox(t *testing.T) string {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	_ = os.Unsetenv("NEWGATE_HOME")
	_ = os.Unsetenv("XDG_CONFIG_HOME")
	return dir
}

// newgateNamedProcess starts a process with "newgate" in /proc/<pid>/cmdline
// (Alive uses that string for identity; a plain sleep counts as unrelated) and returns its pid.
//
// argv0 is set by execve directly, not through a shell (see newgateServeProcess). It used
// to be `bash -c 'exec -a newgate-sleep sleep 30'`: that hop left a window and forced bash
// - this machine's dash has no `exec -a`, so sh just exits, and probing becomes a race with
// zombies (see Stop's failure live record). Now there is no shell to choose.
func newgateNamedProcess(t *testing.T) int {
	cmd := exec.Command("sleep", "30")
	cmd.Args[0] = "newgate-sleep"
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start the test process: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	// Wait for the criterion itself, not for a "cmdline non-empty" middle state (see
	// waitForProcess): this test wants exactly that - Alive true, isServe false.
	waitForProcess(t, cmd.Process.Pid,
		func(p int) bool { return Alive(p) && !isServe(p) }, "a process that looks like newgate but is not __serve")
	return cmd.Process.Pid
}

// TestReadPidFallsBackToLegacy is the post-upgrade shape: the pid is at the old location
// ($HOME), the new one (shared dir) has none. Unreadable, a running old daemon "vanishes".
func TestReadPidFallsBackToLegacy(t *testing.T) {
	dir := sandbox(t)
	_ = os.MkdirAll(filepath.Join(dir, ".config", "newgate"), 0o755)
	if err := ioutil.WriteFile(filepath.Join(dir, ".newgate.pid"),
		[]byte(`{"pid":4242,"port":8899}`), 0o644); err != nil {
		t.Fatal(err)
	}
	i, err := ReadPid()
	if err != nil || i == nil {
		t.Fatalf("it should fall back to reading the legacy pid: %v", err)
	}
	if i.PID != 4242 || i.Port != 8899 {
		t.Fatalf("read wrong: %+v", i)
	}
}

// TestRemoveCleansBothLocations: stopping an old daemon a legacy pid points to must take
// both files, or the next read takes the dead file as a live instance again.
func TestRemoveCleansBothLocations(t *testing.T) {
	dir := sandbox(t)
	cfg := filepath.Join(dir, ".config", "newgate")
	_ = os.MkdirAll(cfg, 0o755)
	_ = ioutil.WriteFile(filepath.Join(cfg, ".newgate.pid"), []byte("1"), 0o644)
	_ = ioutil.WriteFile(filepath.Join(dir, ".newgate.pid"), []byte("2"), 0o644)
	_ = ioutil.WriteFile(filepath.Join(cfg, ".newgate.lock"), []byte("1"), 0o644)
	_ = ioutil.WriteFile(filepath.Join(dir, ".newgate.lock"), []byte("2"), 0o644)
	RemovePid()
	RemoveLock()
	for _, p := range []string{
		filepath.Join(cfg, ".newgate.pid"),
		filepath.Join(dir, ".newgate.pid"),
		filepath.Join(cfg, ".newgate.lock"),
		filepath.Join(dir, ".newgate.lock"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s was not cleared", p)
		}
	}
}

// TestAcquireLockHonorsLiveLegacyLock: a live lock at the old location must block a new
// instance - ignoring it starts a second daemon that fights over port 8899.
func TestAcquireLockHonorsLiveLegacyLock(t *testing.T) {
	dir := sandbox(t)
	pid := newgateNamedProcess(t)
	if err := ioutil.WriteFile(filepath.Join(dir, ".newgate.lock"),
		[]byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := AcquireLock()
	if err == nil {
		RemoveLock()
		t.Fatal("there is a live lock in the old location, so taking it must not succeed")
	}
}

// TestStopViaLegacyPid runs the whole upgrade path: pid only at the old location, process
// alive -> Stop kills it and clears both files.
func TestStopViaLegacyPid(t *testing.T) {
	dir := sandbox(t)
	pid := newgateNamedProcess(t)
	if err := ioutil.WriteFile(filepath.Join(dir, ".newgate.pid"),
		[]byte(`{"pid":`+strconv.Itoa(pid)+`,"port":18899}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(dir, ".newgate.lock"),
		[]byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Stop()
	if err != nil || got != pid {
		t.Fatalf("Stop via legacy pid should kill %d: got %d, err %v", pid, got, err)
	}
	for _, p := range []string{
		filepath.Join(dir, ".newgate.pid"),
		filepath.Join(dir, ".newgate.lock"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s was not cleared", p)
		}
	}
}

// TestStopViaHTTPToken is the cross-user stop path: when the signal cannot go out (EPERM),
// use the control endpoint. A fake daemon that only knows the token checks stopViaHTTP -
// right token stops it and cleans pid/lock; missing or rejected token errors, files untouched.
func TestStopViaHTTPToken(t *testing.T) {
	dir := sandbox(t)
	stateFile := filepath.Join(dir, ".config", "newgate", "state.json")
	_ = os.MkdirAll(filepath.Dir(stateFile), 0o755)
	writeState := func(tok string) {
		s := `{"default_profile":"ds","port":0`
		if tok != "" {
			s += `,"control_token":"` + tok + `"`
		}
		s += `}`
		if err := ioutil.WriteFile(stateFile, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// startFakeDaemon starts a fake daemon: the HTTP face knows only the expect token, and a
	// stop request with the right token kills its own process (simulating "daemon exits").
	startFakeDaemon := func(expect string) (url string, seen chan string, pid, port int) {
		cmd := exec.Command("/bin/bash", "-c", "exec -a newgate-serve sleep 30")
		if err := cmd.Start(); err != nil {
			t.Skipf("cannot start the test process: %v", err)
		}
		seen = make(chan string, 4)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen <- r.Header.Get("Authorization")
			if r.Header.Get("Authorization") != "Bearer "+expect {
				w.WriteHeader(403)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
			_ = cmd.Process.Kill()
		}))
		t.Cleanup(func() {
			srv.Close()
			_ = cmd.Process.Kill()
		})
		port, _ = strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])
		return srv.URL, seen, cmd.Process.Pid, port
	}

	pidFile := filepath.Join(dir, ".config", "newgate", ".newgate.pid")
	lockFile := filepath.Join(dir, ".config", "newgate", ".newgate.lock")
	writePidLock := func() {
		_ = os.MkdirAll(filepath.Dir(pidFile), 0o755)
		_ = ioutil.WriteFile(pidFile, []byte("9"), 0o644)
		_ = ioutil.WriteFile(lockFile, []byte("9"), 0o644)
	}

	t.Run("no token must give a readable message", func(t *testing.T) {
		writeState("")
		_, err := stopViaHTTP(&Info{PID: 1, Port: 1})
		// Assert message identity (language-free): the point is "no control token", not the language.
		if err == nil || !strings.Contains(i18n.ID(err), "no control token") {
			t.Fatalf("it should report 'no control token', got: %v", err)
		}
	})

	t.Run("the token was refused but nothing was cleaned up", func(t *testing.T) {
		writeState("tok-a")
		_, seen, _, port := startFakeDaemon("tok-b")
		writePidLock()
		if _, err := stopViaHTTP(&Info{PID: 1, Port: port}); err == nil {
			t.Fatal("403 should error")
		}
		if a := <-seen; a != "Bearer tok-a" {
			t.Fatalf("the token sent is wrong: %q", a)
		}
		if _, err := os.Stat(pidFile); err != nil {
			t.Fatalf("on refusal the pid file must not disappear: %v", err)
		}
	})

	t.Run("the token is right, stop and clean up", func(t *testing.T) {
		writeState("tok-right")
		_, seen, dpid, port := startFakeDaemon("tok-right")
		writePidLock()
		got, err := stopViaHTTP(&Info{PID: dpid, Port: port})
		if err != nil || got != dpid {
			t.Fatalf("it should stop the fake daemon (pid %d): got %d, err %v", dpid, got, err)
		}
		if a := <-seen; a != "Bearer tok-right" {
			t.Fatalf("the token sent is wrong: %q", a)
		}
		for _, p := range []string{pidFile, lockFile} {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Errorf("%s was not cleared", p)
			}
		}
	})
}
