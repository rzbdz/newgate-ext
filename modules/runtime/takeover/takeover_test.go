package takeover

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/runtime/injection"
)

// isolate points newgate's config dir, the target config dir and HOME at a temp dir.
//
// All three must be isolated, or one missing touches real user files:
//   - NEWGATE_HOME       state.json / shim dir
//   - NEWGATE_TARGET_DIR the rewritten opencode config (**unaffected by NEWGATE_HOME**)
//   - HOME               injection.RCFiles() goes through os.UserHomeDir()
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEWGATE_HOME", dir)
	t.Setenv("NEWGATE_TARGET_DIR", target)
	t.Setenv("HOME", filepath.Join(dir, "home")) // 不创建 → 没有 rc 文件可动
	return dir
}

func installed(t *testing.T, agent string) bool {
	t.Helper()
	for _, n := range injection.Installed() {
		if n == agent {
			return true
		}
	}
	return false
}

// TestPlugUnplugCycle is the regression test for the bug the user reported.
//
// Original symptom: after newgate stop, claude still went through newgate. Root cause:
// stop did not pull the PATH shim, and the wrapper lazily starts the proxy - same as not
// stopping. Fixing it at once produced the mirror fault: start did not plug it back.
//
// So this test must run the whole round trip and verify both directions.
func TestPlugUnplugCycle(t *testing.T) {
	isolate(t)

	if installed(t, "claude") {
		t.Fatal("there is already a shim in a clean environment? isolation did not take effect")
	}

	// Take over
	if r := On("claude", 8899); r.Err != nil {
		t.Fatalf("On: %v", r.Err)
	}
	if !installed(t, "claude") {
		t.Fatal("after On the shim is missing - takeover did not land")
	}

	// stop: must pull the shim, else `claude` still hits the wrapper's lazy proxy
	OffAll()
	if installed(t, "claude") {
		t.Fatal("stop did not remove the shim - that is 'still going through newgate after stop'")
	}

	// But stop must not change intent, so the next start must plug it back in as it was
	if st := store.LoadState(); !st.TakeoverWanted("claude") {
		t.Fatal("stop changed the takeover wish, so start would never take over again")
	}
	OnAll(8899)
	if !installed(t, "claude") {
		t.Fatal("start did not put the shim back - claude would silently go direct")
	}
}

// TestOffPersistsIntent: an agent explicitly off'd must be skipped by OnAll.
// Else the user's "I don't want newgate managing claude" is quietly overturned by the next start.
func TestOffPersistsIntent(t *testing.T) {
	isolate(t)

	if r := On("claude", 8899); r.Err != nil {
		t.Fatalf("On: %v", r.Err)
	}
	if r := Off("claude"); r.Err != nil {
		t.Fatalf("Off: %v", r.Err)
	}
	if installed(t, "claude") {
		t.Fatal("after Off the shim is still there")
	}
	if st := store.LoadState(); st.TakeoverWanted("claude") {
		t.Fatal("Off did not remember the wish")
	}

	OnAll(8899)
	if installed(t, "claude") {
		t.Fatal("take-over-all ignored the user's explicit off - the user's decision was overruled")
	}

	// Back on: intent must be restored
	if r := On("claude", 8899); r.Err != nil {
		t.Fatalf("On again: %v", r.Err)
	}
	if !installed(t, "claude") {
		t.Fatal("On again did not install it")
	}
	OffAll()
	OnAll(8899)
	if !installed(t, "claude") {
		t.Fatal("once the wish is restored, start should take over again")
	}
}

// TestOffAllLeavesForeignEntriesAlone: whatever the user put in the shim dir stays.
//
// The scene: to work around this bug, a user hand-renamed the shim to claude-bak and
// kept it. If fixing the bug deleted it, we would have destroyed his emergency tool.
func TestOffAllLeavesForeignEntriesAlone(t *testing.T) {
	isolate(t)

	if r := On("claude", 8899); r.Err != nil {
		t.Fatalf("On: %v", r.Err)
	}
	// A backup link the user kept by hand, and a script of his own
	bak := filepath.Join(injection.Dir(), "claude-bak")
	self, _ := os.Executable()
	if err := os.Symlink(self, bak); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(injection.Dir(), "my-wrapper")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec claude \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// The name is not a known agent -> not ours
	if installed(t, "claude-bak") || installed(t, "my-wrapper") {
		t.Fatal("Installed() counted the user's own file as our shim")
	}
	fg := injection.Foreign()
	if len(fg) != 2 {
		t.Fatalf("Foreign() = %v, want claude-bak and my-wrapper", fg)
	}

	OffAll()
	for _, p := range []string{bak, script} {
		if _, err := os.Lstat(p); err != nil {
			t.Fatalf("stop deleted the user's own %s: %v", filepath.Base(p), err)
		}
	}
	if err := injection.Uninstall("claude-bak"); err == nil {
		t.Fatal("Uninstall is willing to delete entries whose agent name it does not know")
	}
}

// TestUnknownAgent: a typo must list what is available, never succeed silently.
func TestUnknownAgent(t *testing.T) {
	isolate(t)

	r := On("claud", 8899) // 少个 e
	if r.Err == nil {
		t.Fatal("it took over a nonexistent agent")
	}
	if r := Off("claud"); r.Err == nil {
		t.Fatal("it released a nonexistent agent")
	}
	// A typo must not leave garbage in state.json
	if st := store.LoadState(); len(st.Takeover) != 0 {
		t.Fatalf("a misspelled name was written into state: %v", st.Takeover)
	}
}

// TestListReportsWantedVsActual: status must show "wanted" vs "really taken over"
// - they disagreeing is the fault scene, and hiding it kills troubleshooting.
func TestListReportsWantedVsActual(t *testing.T) {
	isolate(t)

	find := func(agent string) Status {
		t.Helper()
		for _, s := range List() {
			if s.Agent == agent {
				return s
			}
		}
		t.Fatalf("%s is missing from List()", agent)
		return Status{}
	}

	s := find("claude")
	if s.Mechanism != MechShim {
		t.Fatalf("the mechanism of claude should be a shim, got %q", s.Mechanism)
	}
	if !s.Wanted || s.Active {
		t.Fatalf("initially it should be want=true actual=false, got %+v", s)
	}

	On("claude", 8899)
	if s := find("claude"); !s.Active {
		t.Fatal("the shim is installed, yet List says not taken over")
	}

	// After stop: wanted still true, actual false - exactly the "should be refilled" scene
	OffAll()
	if s := find("claude"); !s.Wanted || s.Active {
		t.Fatalf("after stop it should be want=true actual=false, got %+v", s)
	}
}
