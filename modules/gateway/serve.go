package gateway

// This file is the **daemon's main loop**: `newgate __serve` (spawned by daemon.Spawn).
//
// **Why it lives here** (2026-09-18): what it runs is the data plane itself - the atomic
// page swap of config hot reload, thinkcache's disk cold tier, starting and stopping the
// forwarding service (forward.New), draining for a graceful takeover. None of that is a
// "command line" matter. While it lived in modules/cli, the UI had to know forward /
// thinkcache / the health table / config,store / runtime,daemon just to run - so half of the
// UI's dependency table was the data plane.
//
// After the move: the UI does not know the process lifecycle, and the process does not know
// the UI. `__serve` is a command the gateway registers into the UI itself in Start (this
// module has a weak Optional(cli) dependency on ui), and users do not see it (deliberately
// no HelpLine).
//
// Dependency direction: gateway → cli/extension (a contract leaf), not → modules/cli.

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rzbdz/newgate-ext/lib/buildinfo"
	"github.com/rzbdz/newgate-ext/lib/logx"
	servingapi "github.com/rzbdz/newgate-ext/lib/serving"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/forward"
	"github.com/rzbdz/newgate-ext/modules/gateway/policy"
	"github.com/rzbdz/newgate-ext/modules/gateway/thinkcache"
	porthubapi "github.com/rzbdz/newgate-ext/modules/porthub"
	"github.com/rzbdz/newgate-ext/modules/runtime/daemon"
	entryapi "github.com/rzbdz/newgate/component/entry"
	"github.com/rzbdz/newgate/lib/i18n"
)

// serveEntry is the daemon itself: `newgate __serve`.
//
// **It is an entry declaration (component/entry), not a UI command** (changed 2026-09-20).
//
// Why: `__serve` answers "who does this process invocation belong to", which is exactly the
// entry ledger's question; and when hung as a cli command, **turning off the UI turned off
// the daemon itself** - a `disable: ["cli"]` assembly compiles but cannot start a daemon
// (measured: `newgate: no entry claimed this call (asked: wrapper did not claim)`). A pure
// dashboard distribution (no terminal UI, just a web console) was therefore impossible, and
// it is a **legitimate** product shape: the daemon still has to be started by someone for
// the web page to have anything to connect to. The wrapper reporting "argv0 is a taken-over
// client" takes this same road.
//
// One declaration less, too: as a command it had to report Unstyled (the output is not a
// layout), as an entry it need not - where an entry's output goes is the entry's own
// business.
//
// It carries only filters (the data plane policy ledger) in, no concrete policy: the ledger
// exists at Bind time (created in gateway's New), contributors write into it in their own
// Start, and the data plane reads it at Serve time.
type serveEntry struct {
	filters *policy.Registry
	// hub is the shared-port table (nil when porthub is not installed).
	//
	// It appears only here: **the daemon entry** is the only place in this process that knows
	// "someone else is on the port". The data plane (forward) gets a handler and does not know
	// where it came from; porthub gets a handler and does not know whose it is - neither side
	// knows the other.
	hub porthubapi.Service
	// listeners is the ledger of "this process has started serving" (nil when serving is not
	// installed).
	//
	// It appears in the same place as hub, for the same reason: **the daemon entry is the only
	// place in this process that can say "I am serving"**. The data plane gets a process
	// that is already set up, the UI gets a "you may start now" notification - neither side
	// knows the other, nor the other half of this module.
	listeners servingapi.Service

	// headless records "this assembly has no terminal UI", computed by Start from what is
	// installed (see module.go), and decides **what this process is when given no arguments**.
	//
	// With a UI: a bare `newgate` is answered by the UI (help / status), and the daemon claims
	// only an explicit `__serve`. Without a UI (a pure dashboard distribution has this shape):
	// this process **is** the daemon - there is no second answer to give, and `newgate start`
	// went away with the UI (it is a command runtime registers into the UI), so without a
	// fallback such a build compiles but can never start (`no entry claimed this call`).
	headless bool
}

var _ entryapi.Handler = (*serveEntry)(nil)

// Name goes into the log: the composition root's `[entry] resolve: … → <Name>` line.
func (serveEntry) Name() string { return "__serve" }

// Claims claims two things: an explicit `newgate __serve …`, and **"only this entry's own
// flags" when there is no UI** (`newgate --port 8907`).
//
// The first is literal equality with no spelling tolerance: `__serve` is an internal entry
// (assembled by daemon.Spawn), not something a human types, and accepting a near miss only
// makes the real error harder to see.
//
// The second holds only without a UI (see headless): there is no sub-command to dispatch to,
// so the flags can only be the daemon's own and "start serving" is the only behavior that
// makes sense. **Anything else is passed through** - `newgate frobnicate` should get the
// plain-language "no entry claimed this call", not a daemon quietly started (that kind of
// mistake is only discovered once the port is taken or nothing can connect).
//
// It only reads Process and has no side effects - the entry ledger asks several declarers,
// and the order it asks in must not change the answer.
func (e serveEntry) Claims(p entryapi.Process) bool {
	if len(p.Args) > 0 && p.Args[0] == "__serve" {
		return true
	}
	return e.headless && onlyOwnFlags(p.Args)
}

// onlyOwnFlags reports that this argument string consists **only** of this entry's own flags.
//
// The one it knows is `--port`: that is the daemon's parameter, and "which parameters are
// mine" is something only the daemon itself knows. When it gains a flag, a line is added
// here - the honest cost, and far safer than "anything starting with a dash is mine" (which
// would swallow `--help` too, so `newgate --help` on a build without a UI would start a
// daemon).
func onlyOwnFlags(args []string) bool {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--port":
			i++ // consume its value; a missing value makes intFlag fall back to the default port
		case strings.HasPrefix(a, "--port="):
		default:
			return false
		}
	}
	return true
}

func (c serveEntry) Handle(p entryapi.Process) int {
	return Serve(c.filters, c.hub, c.listeners, intFlag(p.Args, "--port", 0))
}

// intFlag takes the integer in `--name N` or `--name=N`, with a default when absent.
func intFlag(args []string, name string, def int) int {
	for i, a := range args {
		v := ""
		switch {
		case a == name && i+1 < len(args):
			v = args[i+1]
		case strings.HasPrefix(a, name+"="):
			v = strings.TrimPrefix(a, name+"=")
		default:
			continue
		}
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

// Serve is the daemon's main loop.
//
// Hot reload lands here (the foundation of service stability): a watcher monitors the config
// directory in the background and atomically swaps the page when it changes. Already running
// agents are **completely unaffected** - changing claude's profile does not interrupt a
// running opencode, because:
//   - each agent reads its own bindings (per-agent profile)
//   - the swap is an atomic pointer replacement; there is no "config changed mid-read" state
//   - when the new config fails to load, **the old snapshot is kept**, and everything running
//     keeps working
//
// Dependency direction: the data plane knows only the **policy ledger** (policy.Registry),
// no individual policy. The ledger is built by gateway's New (Bind time), and policies
// register into it in their own Start.
func Serve(filters *policy.Registry, hub porthubapi.Service, listeners servingapi.Service, port int) int {
	rot, rerr := logx.New(paths.LogFile(), 16<<20, 3) // 16MB x 4 files
	var lg *log.Logger
	if rerr != nil {
		lg = log.New(os.Stdout, "", log.LstdFlags)
		lg.Printf("%s", i18n.T("Log rotation init failed, falling back to stdout: {err}", i18n.A{"err": rerr}))
	} else {
		defer rot.Close()
		lg = log.New(rot, "", log.LstdFlags)
	}

	// A process taken over gracefully: pid/lock were already rewritten to us by the parent
	// (AdoptRuntime), so taking the lock now would read our own pid and wrongly conclude
	// "already running". The parent is the guarantor; skip.
	inherited := os.Getenv("NEWGATE_LISTENER_FD") != ""
	if !inherited {
		if err := daemon.AcquireLock(); err != nil {
			lg.Printf("%s", i18n.T("Failed to acquire the lock: {err}", i18n.A{"err": err}))
			return 69
		}
		defer daemon.RemoveLock()
		// After taking the lock, **we** write the pidfile: only the process holding the
		// listening right should appear there. Written by the parent, it gets deleted by the
		// "stale lock cleanup" that follows (the RemovePid in AcquireLock) and nobody puts it
		// back - for the full chain of the two field cases on 2026-09-18 see daemon.WriteOwn.
		// Failing to write only warns: the proxy keeps serving, just status/metrics cannot see
		// it (doctor's "daemon" item says so at that point).
		if err := daemon.WriteOwn(port); err != nil {
			lg.Printf("%s", i18n.T("Cannot write the pidfile (status/metrics will not see this process): {err}", i18n.A{"err": err}))
		}
	} else {
		lg.Printf("%s", i18n.T("Graceful handover: taking over the listening socket from the parent process", nil))
	}

	// The control token must be on disk before the watcher: other users' `newgate stop`
	// cannot send a signal and can only go through /__newgate/stop, so the daemon must be able
	// to verify it as soon as it starts. This is the last fallback (cmdStart / launch.Launch
	// each do it once before Spawn; normally it is long there).
	if st, err := store.EnsureControlToken(); err != nil {
		// The daemon starts as usual: no token only means cross-user stop is unavailable, and
		// that should not hold up the whole proxy.
		lg.Printf("%s", i18n.T("Cannot write the control token (cross-user stop unavailable): {err}", i18n.A{"err": err}))
	} else if st.ControlToken != "" {
		lg.Printf("%s", i18n.T("Control endpoint /__newgate/stop is ready (cross-user stop available)", nil))
	}

	watcher, err := store.NewWatcher(time.Second)
	if err != nil {
		lg.Printf("%s", i18n.T("Failed to load the configuration: {err}", i18n.A{"err": err}))
		return 65
	}
	watcher.OnChange(func(old, nw *store.Snapshot) {
		changes := store.DiffSummary(old, nw)
		if len(changes) == 0 {
			lg.Printf("[config] %s", i18n.T("Configuration reloaded (generation {gen}), no structural change",
				i18n.A{"gen": watcher.Generation()}))
			return
		}
		lg.Printf("[config] %s", i18n.T("Configuration hot-reloaded (generation {gen}): {changes} — running sessions are unaffected",
			i18n.A{"gen": watcher.Generation(), "changes": changes}))
	})
	watcher.OnError(func(e error) {
		// Two shapes arrive here: a load that failed outright (the whole old snapshot keeps
		// serving), and a load that went through with some profiles substituted by their last
		// usable version (resilience.go). Either way the affected part is unchanged, so the
		// wording must not claim the new file is in effect.
		lg.Printf("[config] %s", i18n.T("Configuration problem — the affected part still serves its last usable version: {err}",
			i18n.A{"err": e}))
	})
	watcher.Start()
	defer watcher.Close()

	if port <= 0 {
		port = watcher.Current().State.Port
	}
	// thinkcache's disk cold tier: find the previous process's reasoning again after a daemon
	// restart. On failure it degrades to memory only (the text of the rounds before the
	// restart cannot be recovered), and persisting must never take the proxy down.
	if err := thinkcache.AttachDisk(paths.ThinkCacheFile(), 100<<20); err != nil {
		lg.Printf("%s", i18n.T("thinkcache disk layer disabled (memory-only): {err}", i18n.A{"err": err}))
	}
	// Failures after the cold tier is running (compaction dropping records, flush failures,
	// the cold tier being disabled) must be spoken: the user-observable symptom is "reasoning
	// cannot be recovered after a restart", exactly the hardest kind to trace back.
	thinkcache.SetErrorHandler(func(err error) {
		lg.Printf("[thinkcache] %v", err)
	})
	srv := forward.New(watcher.Current().State.BindHost(), port, lg, watcher, filters)

	// The root handler of this port: with porthub installed it goes there (the mount table
	// wins, and unclaimed paths fall back to the data plane); without it, it is the data plane
	// itself - i.e. the way things looked before this mechanism existed.
	//
	// Why the composition lives here and not in the data plane: **this is composition, not data
	// plane logic**. The data plane is the "forward one model request" chain and should not know
	// who else lives on this process's port; the daemon entry is by nature the place that
	// "assembles this process" (logging, locks, signals, draining all live here). The dependency
	// itself is declared (Optional in module.go), so it is visible on the dependency graph.
	if hub != nil {
		root, err := hub.Root("gateway", srv.Handler())
		if err != nil {
			lg.Printf("%s", i18n.T("Cannot share the port with other services, the data plane will serve it alone: {err}",
				i18n.A{"err": err}))
		} else {
			srv.SetRoot(root)
			lg.Printf("%s", i18n.T("This port is shared: services mounted on it take their paths first, everything else is the data plane", nil))
		}
	}

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	// requested records "someone asked for this exit"; it only affects the exit code.
	//
	// The shutdown path has **exactly one exit**: the goroutine below only calls `srv.Shutdown()`
	// so Serve() returns, and pid/lock cleanup and the exit code are all done in the one place
	// after srv.Start(). There used to be three exits each doing `RemoveLock + RemovePid +
	// os.Exit(0)`, racing the main path's `return 70`: measured (2026-09-17, chapter 11 of
	// mock/e2e_claude.sh) the main path finished first, `RemoveLock` was covered by a defer,
	// `RemovePid` was not, so .newgate.pid stayed on disk - and the next start/restart reads a
	// dead pid.
	var requested int32

	go func() {
		for s := range sig {
			if s == syscall.SIGHUP {
				// SIGHUP is the traditional "reload config" semantics
				lg.Printf("%s", i18n.T("Received SIGHUP, forcing a configuration reload", nil))
				watcher.Reload(true)
				continue
			}
			if srv.Draining() {
				// Interrupted by a signal during draining: pid/lock already belong to the new
				// process, do not clear them
				lg.Printf("%s", i18n.T("Received {sig} while draining, exiting now (pid/lock untouched)",
					i18n.A{"sig": s}))
				os.Exit(0)
			}
			lg.Printf("%s", i18n.T("Received {sig}, exiting", i18n.A{"sig": s}))
			atomic.StoreInt32(&requested, 1)
			srv.Shutdown()
			return
		}
	}()

	// Control-endpoint shutdown (another user's `newgate stop`): users who cannot send a signal
	// stop it this way. It likewise only closes the listener; cleanup is done in the one place
	// below.
	go func() {
		<-srv.StopRequested()
		if srv.Draining() {
			// Shutdown received during draining: likewise, do not touch the new process's
			// pid/lock
			lg.Printf("%s", i18n.T("Received a control stop while draining, exiting now (pid/lock untouched)", nil))
			os.Exit(0)
		}
		lg.Printf("%s", i18n.T("Control stop (token verified), exiting", nil))
		atomic.StoreInt32(&requested, 1)
		srv.Shutdown()
	}()

	lg.Printf("%s", i18n.T("newgate {version} (built {built}) started, default profile={profile}, config hot-reload enabled",
		i18n.A{"version": buildinfo.Version(), "built": buildinfo.BuildTimeDisplay(),
			"profile": watcher.Current().State.DefaultProfile}))
	// "This process has started serving": modules that want to ride along (a web UI that mounts
	// on the shared port when porthub is installed, or listens on its own port otherwise) come
	// up at this moment.
	//
	// **Why it cannot go in a module's Start**: Start runs for every single `newgate …`
	// command, and starting a listener there means one `newgate status` opens a server. And this
	// is the only place that can say it - this process is really about to hand the port to the
	// kernel.
	//
	// It sits before Start (rather than after accept): it says "I am about to begin". The cost
	// of that one beat is "a listener that is already up when binding fails" - which the stop
	// below collects, and in that case the process leaves at once anyway (see the failure
	// branch below).
	if listeners != nil {
		stop, err := listeners.Notify()
		if err != nil {
			// fail-open: one UI failing to start must not drag down the data plane (the log
			// names who).
			lg.Printf("%s", i18n.T("Some services did not start: {err}", i18n.A{"err": err}))
		}
		defer stop()
	}

	if err := srv.Start(); err != nil {
		// Draining for a graceful takeover: the listener has been handed to the new process, so
		// Serve returns - but this is not the time to exit. Wait for in-flight requests to finish
		// (Drained), then exit directly (os.Exit skips the deferred RemoveLock: pid/lock already
		// belong to the new process).
		if srv.Draining() {
			lg.Printf("%s", i18n.T("Listening socket handed over to the new process, waiting for in-flight requests to drain (up to 10 minutes)", nil))
			<-srv.Drained()
			lg.Printf("%s", i18n.T("Drain complete, the old process is stepping down", nil))
			os.Exit(0)
		}
		lg.Printf("%s", i18n.T("Proxy exiting: {err}", i18n.A{"err": err}))
		// The only exit: the process is leaving, pid/lock are its own, and must go with it.
		// The deferred RemoveLock only handles lock; pid must be added here.
		daemon.RemoveLock()
		daemon.RemovePid()
		if atomic.LoadInt32(&requested) == 1 {
			return 0
		}
		return 70
	}
	return 0
}
