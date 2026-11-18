package runtime

// This file is the **public port for lifecycle and takeover**.
//
// # Why it exists (2026-11-17)
//
// The distribution's `newgate config` does "install -> start proxy -> take over" in one
// wizard. The first two have their own ports (confighook's AgentInstaller, daemon.Spawn),
// while **takeover** had only commands: `start`/`on <agent>` called the internal package.
//
// So a distribution had two roads: copy takeover logic (= import kernel internals, break
// in another repo on refactor), or shell out to `newgate start` (= spawn a child process
// and parse its output; one wording change kills it silently). Neither is the answer.
//
// So takeover is promoted into part of the Runtime port - **it is this module's
// definition** (package runtime's first sentence is "turn an Agent descriptor into a
// real client process"); commands are just its facade, and what is under it is askable.
//
// The return types are the commands' (takeover.Result/Status), so "what one takeover
// changed" has one shape: the wizard prints the same facts as `newgate on`.

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/rzbdz/newgate-ext/lib/httpx"
	"github.com/rzbdz/newgate-ext/lib/style"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
	"github.com/rzbdz/newgate-ext/modules/runtime/daemon"
	"github.com/rzbdz/newgate-ext/modules/runtime/takeover"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Status is one agent's takeover state (the same facts as the `newgate status` line).
type Status = takeover.Status

// Result is the outcome of one takeover/release.
type Result = takeover.Result

// TakeOver takes over one agent and records "the user wants it" - what `newgate on <agent>` does.
func (*service) TakeOver(agent string, port int) Result { return takeover.On(agent, port) }

// Release frees one agent and records "the user does not want it" - `newgate off <agent>`.
func (*service) Release(agent string) Result { return takeover.Off(agent) }

// TakeOverAll takes over agents not explicitly turned off - the last step of `start`.
func (*service) TakeOverAll(port int) []Result { return takeover.OnAll(port) }

// TakeoverStatus lists the takeover state of every known agent.
func (*service) TakeoverStatus() []Status { return takeover.List() }

// StartOptions holds Start's switches.
type StartOptions struct {
	// Force skips the "no usable config, no takeover" gate (`newgate start --force`).
	//
	// The wizard does not need it - it just wrote usable providers itself. Kept because the
	// port must mirror the commands: miss one switch and the next person opens a bypass for it.
	Force bool
	// SkipTakeover only brings the proxy up, **not** a full takeover.
	//
	// The wizard uses it: the user named the clients on the multi-select screen, and we
	// must never take over the unselected ones - that is that screen's "skip leaves them
	// unchanged" promise. `newgate start` does not pass it, so command semantics are unchanged.
	SkipTakeover bool
	// Quiet prints nothing.
	//
	// The wizard uses it: it is drawing a screen, and the kernel's "proxy started" lines
	// would fight the layout. Facts go out via StartReport anyway - quiet is not "less".
	Quiet bool
}

// StartReport is Start's outcome.
type StartReport struct {
	// Running reports whether the proxy is up when this returns.
	Running bool
	// AlreadyRunning = this call started no new process (the proxy was already up).
	AlreadyRunning bool
	// PID / Port identify the running process.
	PID  int
	Port int
	// Profile is its current global default chain head.
	Profile string
	// Takeover is this run's takeover result (empty if AlreadyRunning - that was last time).
	Takeover []Result
	// Problems lists each "config unusable" reason (non-empty = takeover refused, and Force false).
	Problems []string
}

// Start brings the proxy up and takes over everything - `newgate start` semantics, with
// the outcome handed back to the caller.
//
// It runs **the same code** as the command (cmdStart calls it), so a proxy started by the
// wizard and by `newgate start` behaves the same: token, key gate, OnAll, TakenOver write.
func (s *service) Start(opts StartOptions) (StartReport, error) {
	var rep StartReport
	say := func(format string, a ...any) {
		if !opts.Quiet {
			fmt.Printf(format, a...)
		}
	}
	sayErr := func(format string, a ...any) {
		if !opts.Quiet {
			fmt.Fprintf(os.Stderr, format, a...)
		}
	}

	if _, err := os.Stat(paths.ProvidersFile()); os.IsNotExist(err) {
		say("%s\n", style.Dim(i18n.T("First run, initializing the config", nil)))
		if _, err := store.Init(false); err != nil {
			return rep, err
		}
	}
	if i := daemon.Running(); i != nil {
		rep = StartReport{Running: true, AlreadyRunning: true, PID: i.PID, Port: i.Port,
			Profile: store.LoadState().DefaultProfile}
		say("%s\n", style.Item(style.OK, i18n.T("Proxy already running   pid {pid} · 127.0.0.1:{port}",
			i18n.A{"pid": i.PID, "port": i.Port})))
		return rep, nil
	}

	// The token is written before Spawn: the daemon must verify /__newgate/stop right away.
	// Also against a race - st is written back by SaveState after Spawn below, so ensure
	// st carries the token first, and the write-back cannot wipe the daemon's token.
	st, err := store.EnsureControlToken()
	if err != nil {
		// Not blocking: the user wants the proxy up, refusing over a token is worse. But say the cost.
		sayErr("%s %s\n", style.Mark(style.Warn), i18n.T(
			"Cannot write the control token (a cross-user newgate stop will not work): {err}",
			i18n.A{"err": err.Error()}))
	}
	// No key, no takeover - every request would error, and the user's config is already changed
	if probs := activeProblems(st); len(probs) > 0 && !opts.Force {
		rep.Problems = probs
		sayErr("newgate: %s\n", i18n.T("Config is unusable, refusing to take over", nil))
		for _, p := range probs {
			sayErr("  %s %s\n", style.Mark(style.Bad), p)
		}
		sayErr("%s\n", style.Hint(i18n.T("Put a key in {file}, or set the matching environment variable",
			i18n.A{"file": paths.ProvidersFile()})))
		sayErr("%s\n", style.Hint(i18n.T("Check: newgate doctor    Force takeover: newgate start --force", nil)))
		return rep, ErrConfigUnusable
	}

	info, err := daemon.Spawn(st.Port)
	if err != nil {
		return rep, i18n.Ef(err, "Cannot start the daemon: {err}", nil)
	}
	ok := false
	for k := 0; k < 60; k++ {
		if controlplane.Ping(st.Port) {
			ok = true
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if !ok {
		// Tell "process did not start" from "up but unreachable" - never kill the latter
		if httpx.TCPAlive("127.0.0.1", st.Port, time.Second) {
			sayErr("newgate: %s\n", i18n.T(
				"Port {port} is listening but the HTTP probe failed; the proxy process is kept",
				i18n.A{"port": st.Port}))
			sayErr("  %s\n", i18n.T(
				"Most likely cause: an outbound proxy is hijacking loopback requests. Run newgate doctor", nil))
		} else {
			sayErr("newgate: %s\n", i18n.T(
				"The proxy did not come up (nothing is listening on port {port}); see the log {log}",
				i18n.A{"port": st.Port, "log": paths.LogFile()}))
			_, _ = daemon.Stop()
			return rep, ErrProxyDidNotStart
		}
	}
	rep = StartReport{Running: true, PID: info.PID, Port: st.Port, Profile: st.DefaultProfile}
	say("%s\n", style.Item(style.OK, i18n.T("Proxy started   pid {pid} · 127.0.0.1:{port} · profile {profile}",
		i18n.A{"pid": info.PID, "port": st.Port, "profile": st.DefaultProfile})))

	// Full takeover: every agent not explicitly off'd, plugged in by its own mechanism.
	if !opts.SkipTakeover {
		say("%s\n", "")
		rep.Takeover = s.TakeOverAll(st.Port)
		if !opts.Quiet {
			printResults(rep.Takeover)
		}
	}

	st.TakenOver = true
	// **Never swallow this error**: above we printed "proxy started", below "config changes
	// hot-reload within 1s". If the write fails (CLAUDE.md §3.1's umask trap is the scene),
	// TakenOver never lands, the next start does the opposite of what the user expects - all green.
	if err := store.SaveState(st); err != nil {
		return rep, i18n.Ef(err, "Cannot write the takeover state back to state.json: {err}", nil)
	}
	say("%s\n", "")
	say("%s\n", style.Hint(i18n.T("Config changes hot-reload within 1 second; no restart needed", nil)))
	say("%s\n", style.Hint(i18n.T("Original config backup: {dir}/original/", i18n.A{"dir": paths.BackupDir()})))
	return rep, nil
}

// Two failure reasons callers can tell apart (command -> exit 65 / 70; wizard picks next line).
var (
	// ErrConfigUnusable = config unusable, takeover refused (`start` exit code 65).
	ErrConfigUnusable = errors.New("config is unusable")
	// ErrProxyDidNotStart = the proxy did not come up (`start` exit code 70).
	ErrProxyDidNotStart = errors.New("the proxy did not come up")
)

// Compile-time confirmation of the port's shape.
var (
	_ Runtime = (*service)(nil)
	_ Control = (*service)(nil)
)
