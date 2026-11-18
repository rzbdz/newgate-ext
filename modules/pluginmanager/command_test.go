package pluginmanager

import (
	"testing"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/store"
)

// stubHost is the minimal Host. The command uses only Die and NotifyProxy; the rest is noise.
type stubHost struct {
	died     int
	msg      string
	notified int
}

func (h *stubHost) Die(code int, message string) int { h.died, h.msg = code, message; return code }

func (h *stubHost) DaemonRunning() bool { return false }

func (h *stubHost) NotifyProxy() { h.notified++ }

// Verb empty: the test calls Run directly, no dispatcher - no "which name found me".
func (h *stubHost) Verb() string { return "" }

var _ cliapi.Host = (*stubHost)(nil)

// TestRunGetsArgsWithoutTheVerb guards the command's argument index base.
//
// The dispatcher **strips the verb** before handing off: `newgate plugin X off 90s`
// gives Run `["X","off","90s"]`. That contract was not on the contract package
// before 2026-09-18, so code indexed from 1 by os.Args intuition, off by one:
//
//	newgate plugin deepseek          -> printed the list (not the expansion)
//	newgate plugin deepseek.tail-shape off 90s -> "unknown verb "90s""
//
// The unit tests were green - they built args directly, bypassing the dispatcher;
// running the real binary found it. So this test drives the **shape the dispatcher
// really passes** (args has no "plugin"), not a convenient hand-made slice.
func TestRunGetsArgsWithoutTheVerb(t *testing.T) {
	manager := start(t)
	if _, err := store.Init(false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	release, err := manager.RegisterSelf("deepseek", []Switch{goodSwitch("deepseek.tail-shape")})
	if err != nil {
		t.Fatalf("registering: %v", err)
	}
	defer func() { _ = release() }()

	cmd := &command{manager: manager}

	// Expanding a module: args = ["deepseek"]. Off by one it prints the list.
	if host := (&stubHost{}); cmd.Run(host, []string{"deepseek"}) != 0 {
		t.Fatalf("plugin deepseek failed: %s", host.msg)
	}

	// Switch + duration: off by one, "90s" is taken as the verb.
	host := &stubHost{}
	if code := cmd.Run(host, []string{"deepseek.tail-shape", "off", "90s"}); code != 0 {
		t.Fatalf("plugin deepseek.tail-shape off 90s failed (%d): %s", code, host.msg)
	}
	if host.notified == 0 {
		t.Fatal("the switch was flipped but the proxy was not notified - the daemon only converges on the next poll")
	}
	st := store.LoadState()
	if !Off(st, "deepseek.tail-shape") {
		t.Fatal("off did not land in state.json")
	}
	if until := Remaining(st, "deepseek.tail-shape"); until.IsZero() {
		t.Fatal("90s was not parsed as a time limit - the duration argument is still being eaten")
	}

	// Module-level switch: args = ["deepseek","on"].
	if host := (&stubHost{}); cmd.Run(host, []string{"deepseek", "on"}) != 0 {
		t.Fatalf("plugin deepseek on failed: %s", host.msg)
	}
	if Off(store.LoadState(), "deepseek.tail-shape") {
		t.Fatal("the module-level on did not restore the switch points")
	}

	// Second symptom: a wrong real verb must be named in the error.
	host = &stubHost{}
	if code := cmd.Run(host, []string{"deepseek.tail-shape", "bogus"}); code != 64 {
		t.Fatalf("an unknown verb should exit with 64, got %d", code)
	}
	if host.msg == "" {
		t.Fatal("no reason was given on rejection")
	}
}
