package claudecode

import (
	"testing"
	"time"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// nakedHost is the minimal Host implementation. naked only uses Die and NotifyProxy.
type nakedHost struct {
	died     int
	msg      string
	notified int
}

func (h *nakedHost) Die(code int, message string) int { h.died, h.msg = code, message; return code }

func (h *nakedHost) DaemonRunning() bool { return false }

func (h *nakedHost) NotifyProxy() { h.notified++ }

// Verb empty: the test calls Run directly, with no
// dispatcher, so there is no "by which name do you find me".
func (h *nakedHost) Verb() string { return "" }

var _ cliapi.Host = (*nakedHost)(nil)

// nakedNow reads the current naked-mode config.
func nakedNow(t *testing.T) (NakedConfig, bool) {
	t.Helper()
	return ParseNakedConfig(store.LoadState().ModuleConfig[NakedConfigKey])
}

// TestNakedRunGetsArgsWithoutTheVerb guards the command's argument-index base.
//
// The dispatcher strips the verb before handing to Run: `newgate naked on`
// -> Run receives `["on"]`. Before 2026-09-18 the index here started at
// 1 (`argAt(args, 1)`), so it got the **empty string**, and fell into the
// `case "", "off"` branch -- `newgate naked on` actually executed **off**.
//
// That bug fooled three layers: unit tests all green (nobody drove it with the dispatcher's
// shape), `newgate naked`'s output looked normal (it was printing "off"), and even the
// end-to-end assertion "naked mode finally turned off" passed **for the wrong reason**.
// In the end it was the e2e "with it on the upstream receives 0 requests" that went red
// and flushed it out -- running the real binary once is worth more than any assertion.
func TestNakedRunGetsArgsWithoutTheVerb(t *testing.T) {
	testkit.Sandbox(t)
	if _, err := store.Init(false); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// on: it must really turn on, not be executed as off.
	host := &nakedHost{}
	if code := (nakedCommand{}).Run(host, []string{"on"}); code != 0 {
		t.Fatalf("naked on failed (%d): %s", code, host.msg)
	}
	if host.notified == 0 {
		t.Fatal("naked mode was turned on but the proxy was not notified - the daemon only converges on the next poll")
	}
	cfg, active := nakedNow(t)
	if !active {
		t.Fatal("naked on did not land - this is that swap bug: on was executed as off")
	}
	if cfg.Mode != "on" || cfg.ExpiresAt.IsZero() {
		t.Fatalf("wrong config: %+v", cfg)
	}
	if d := time.Until(cfg.ExpiresAt); d <= 0 || d > 2*time.Minute {
		t.Fatalf("the default window should be on the order of 60s, got %s", d)
	}

	// With a duration: when misaligned, this argument
	// is taken as the subcommand (an unknown duration).
	host = &nakedHost{}
	if code := (nakedCommand{}).Run(host, []string{"2m"}); code != 0 {
		t.Fatalf("naked 2m failed (%d): %s", code, host.msg)
	}
	if cfg, _ := nakedNow(t); time.Until(cfg.ExpiresAt) <= 90*time.Second {
		t.Fatalf("2m was not parsed as a time limit, got %s", time.Until(cfg.ExpiresAt))
	}

	// off: clear the fields.
	host = &nakedHost{}
	if code := (nakedCommand{}).Run(host, []string{"off"}); code != 0 {
		t.Fatalf("naked off failed (%d): %s", code, host.msg)
	}
	if _, active := nakedNow(t); active {
		t.Fatal("naked off did not clear the config")
	}

	// No arguments = show the current state, which should change nothing.
	if code := (nakedCommand{}).Run(&nakedHost{}, nil); code != 0 {
		t.Fatal("naked with no args should print the current state and exit successfully")
	}
	if _, active := nakedNow(t); active {
		t.Fatal("looking at the current state changed the config")
	}

	// An unknown duration must error, not silently be treated as on.
	host = &nakedHost{}
	if code := (nakedCommand{}).Run(host, []string{"wait a moment"}); code != 64 {
		t.Fatalf("an unknown duration should exit with 64, got %d", code)
	}
	if _, active := nakedNow(t); active {
		t.Fatal("the error path must not write the config")
	}
}
