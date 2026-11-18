// Package launch is the only implementation of "launch a taken-over agent".
//
// It serves two entries at once:
//
//   - argv0 dispatch: `claude` (the PATH shim points at the newgate binary) -> main sees
//     the basename is an agent and forwards here;
//   - CLI wrapped launch: `newgate claude --profile=ds` -> cli resolves agent and profile,
//     and forwards here too.
//
// What it does (docs/07-clients-runtime.md): resolve the real executable, lazily start the
// proxy if needed, inject env into the child, exec - no file touched, no network request.
//
// Slot naming is this layer's core decision (settled 2026-09-09, see buildInject):
// by default inject the **tier name** (heavy/mid/...) - the session sends the tier name
// back, the proxy resolves it against the config current at request time, so
// `newgate --set-profile` applies at once to running sessions (dynamic);
// only an explicit --profile pins this call and injects the **real model name**
// (glm-5.3), showing what is really used. Window decls (CLAUDE_CODE_*) go in both modes.
package launch

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/rzbdz/newgate-ext/lib/httpx"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/runtime/daemon"
	"github.com/rzbdz/newgate-ext/modules/runtime/injection"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Options are one launch's overrides. Zero value = the agent's default profile.
type Options struct {
	// Profile is this call's profile override (no global state written). Empty = the agent default.
	Profile string
}

// Launch injects env and execs the real agent. On success the process is replaced and this
// never returns; the int returned is the exit code (returned only on failure paths).
func Launch(a *agentapi.Agent, facts agentapi.AgentFacts, args []string, o Options) int {
	depth, _ := strconv.Atoi(os.Getenv("NEWGATE_DEPTH"))
	if depth >= 2 || os.Getenv("NEWGATE_DISABLE") == "1" {
		return execReal(a, args, nil) // straight passthrough, no injection
	}

	st := store.LoadState()
	if _, err := a.FindReal(injection.Dir()); err != nil {
		fmt.Fprintf(os.Stderr, "newgate: %v\n", err)
		return 69
	}

	// Lazy-start the proxy; else the agent gets connection refused on entry.
	// (EnsureControlToken: others must be able to stop this lazily started daemon too.)
	if daemon.Running() == nil {
		// A failed write only warns, no block: the user wants the client up, and refusing over
		// a token is worse. But say the cost - that daemon cannot be stopped by others.
		if _, err := store.EnsureControlToken(); err != nil {
			fmt.Fprintf(os.Stderr, "newgate: %s\n", i18n.T(
				"Cannot write the control token (a cross-user newgate stop will not work): {err}",
				i18n.A{"err": err.Error()}))
		}
		fmt.Fprintf(os.Stderr, "newgate: proxy not running, starting…\n")
		if _, err := daemon.Spawn(st.Port); err != nil {
			fmt.Fprintf(os.Stderr, "newgate: failed to start proxy: %v\n"+
				"  (NEWGATE_DISABLE=1 bypasses newgate entirely)\n", err)
			return 69
		}
		waitProxy(st.Port)
	}

	// Resolve this call's chain head: command-line --profile > NEWGATE_PROFILE/PRESET > agent default.
	explicit := o.Profile
	if explicit == "" {
		explicit = firstNonEmpty(os.Getenv("NEWGATE_PROFILE"), os.Getenv("NEWGATE_PRESET"))
	}
	active := explicit
	if active == "" {
		active = st.ActiveFor(a.ID)
	}

	inject, warns := buildInject(a, facts, st, active, explicit)
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "newgate: %s\n", w)
	}
	inject["NEWGATE_DEPTH"] = strconv.Itoa(depth + 1)
	return execReal(a, args, inject)
}

// buildInject computes the whole env set to layer onto the child (except NEWGATE_DEPTH).
//
// facts are **runtime facts** the client reports (agentapi.AgentFacts), fetched by the
// caller from the catalog port - this function does not know how they are derived.
// nil = all slots use the descriptor defaults. Separate to be testable: disk + inputs only.
//
// Slot naming has two modes (settled 2026-09-09):
//
//   - dynamic (default): slots keep the **tier name** (heavy/mid/...). The session sends
//     the tier name back and the proxy resolves it against the config current **at
//     request time** - `newgate --set-profile` applies at once to running sessions. The
//     UI shows the semantic name; the actual model is in X-Newgate-Route / `newgate tier`.
//   - pinned (explicit --profile / NEWGATE_PROFILE / argv0 dispatch): inject the **real**
//     model name. This call is pinned to that profile, so what is shown is what is used.
//
// Window declarations are injected in both modes: the window env takes the client's
// "unknown model" branch (both names are unknown there); compact env has top priority.
// **Both variable names come from the agent definition** (`ContextWindowEnv` /
// `AutoCompactEnv`): client knowledge; the kernel knows no vendor's names (2026-09-20).
// The second value is "should have been injected but was not", printed to stderr by the
// caller. A config read failure does not block launch (fail-open: tier routing still
// works), but it drops the real model name and the profile's windows from this call -
// without them Claude Code assumes 200k and compacts early, which the user feels. So it
// must not be silent (like the control token above in this file).
func buildInject(a *agentapi.Agent, facts agentapi.AgentFacts, st *domain.State, active, explicit string) (map[string]string, []string) {
	// Slot default = the tier name (dynamic mode). facts are the runtime facts this client
	// reports (user-modified slot mappings live there); nil = all use the descriptor defaults.
	inject := a.BuildEnv(st.Port, "newgate-local", facts)
	var warns []string

	if snap, err := store.Load(); err != nil {
		warns = append(warns, i18n.T(
			"cannot read the config, so this run injects no real model names and no window declaration (the proxy still routes by tier name): {err}",
			i18n.A{"err": err}))
	} else {
		// Pinned mode: slots become the profile chain head's real model names. Unresolvable
		// keeps the tier name (fail-open, docs/05-gateway.md): the proxy still routes by tier.
		// Both go through Slot.Apply - client capability markers (`[1m]` and such) must ride
		// along, else pinned mode loses exactly what the user named (--profile=xx).
		if explicit != "" {
			for _, s := range a.EnvSlots() {
				if b, ok := resolve.PrimaryBinding(agentapi.TierOf(facts, s), snap.Profiles, snap.Providers, active); ok {
					inject[s.EnvVar] = s.Apply(b.Model)
				}
			}
		}

		// The main model's real window (declared in the profile). Without it Claude Code
		// assumes a 200k window for model names it does not know (tier or real, same) and
		// compacts early (measured 2026-09). Both envs are injected only for a value > 0;
		// the client may cap them in its descriptor (ContextWindowCap / AutoCompactCap), and
		// then the cap is used even if the profile declares nothing - 200k is smaller, and
		// this client explicitly accepts a bigger window. Other agents (opencode) ignore
		// these variables, harmless.
		//
		// These two are **numbers**, so they never go through Slot.Apply: appending the
		// client's model marker to a window writes an unparseable value, and the failure
		// looks like "the window declaration silently died, back to 200k" - as if absent.
		for _, p := range snap.Profiles {
			if p.Name != active {
				continue
			}
			// The client's cap (agentapi.Agent.ContextWindowCap). **Say so** when the profile
			// declares more: quietly swapping the user's 1M for 512K is the hardest-to-place
			// kind of fault - "config looks right, behavior differs".
			window := p.ContextWindow
			if a.ContextWindowCap > 0 && (window == 0 || window > a.ContextWindowCap) {
				if window > a.ContextWindowCap {
					warns = append(warns, i18n.T(
						"profile {name} declares a {window}-token context window; {client} is capped at {cap} so a compaction request still fits under the upstream limit",
						i18n.A{"name": p.Name, "window": window, "client": a.ID, "cap": a.ContextWindowCap}))
				}
				window = a.ContextWindowCap
			}
			if window > 0 && a.ContextWindowEnv != "" {
				inject[a.ContextWindowEnv] = strconv.Itoa(window)
			}
			compact := p.AutoCompactWindow
			if a.AutoCompactCap > 0 && compact > a.AutoCompactCap {
				compact = a.AutoCompactCap
			}
			if compact > 0 && a.AutoCompactEnv != "" {
				inject[a.AutoCompactEnv] = strconv.Itoa(compact)
			}
			break
		}
	}

	// per-invocation override: encode the profile into the base URL path (/p/<profile>).
	// The proxy reads the override from the path and writes no global state (docs/07).
	if explicit != "" && a.BaseURLEnv != "" {
		inject[a.BaseURLEnv] = fmt.Sprintf("http://127.0.0.1:%d/a/%s/p/%s", st.Port, a.ID, active)
	}
	return inject, warns
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// execReal finds the real executable and replaces the process with the injected env.
func execReal(a *agentapi.Agent, args []string, inject map[string]string) int {
	real, err := a.FindReal(injection.Dir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "newgate: %v\n", err)
		return 69
	}

	// The controlled env allowlist: inherit -> drop interference -> layer ours on top.
	drop := map[string]bool{}
	for _, k := range a.UnsetEnv {
		drop[k] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		k := kv
		if i := indexByte(kv, '='); i >= 0 {
			k = kv[:i]
		}
		if drop[k] {
			continue
		}
		if _, overridden := inject[k]; overridden {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range inject {
		env = append(env, k+"="+v)
	}

	execArgs := append([]string{real}, args...)
	if err := syscall.Exec(real, execArgs, env); err != nil {
		// A script with no shebang (npm's claude.exe fallback, or a CRLF-broken
		// shebang) makes the kernel return ENOEXEC (exec format error). Hand it to
		// /bin/sh so it prints the script's own clear error, instead of handing the
		// user a mysterious "exec format error".
		if errors.Is(err, syscall.ENOEXEC) {
			shArgs := append([]string{"/bin/sh", real}, args...)
			if err2 := syscall.Exec("/bin/sh", shArgs, env); err2 != nil {
				fmt.Fprintf(os.Stderr, "newgate: %s\n", i18n.T(
					"running {file} through /bin/sh failed too: {err}",
					i18n.A{"file": real, "err": err2}))
				return 70
			}
			return 0 // unreachable: Exec does not return on success
		}
		fmt.Fprintf(os.Stderr, "newgate: exec %s failed: %v\n", real, err)
		return 70
	}
	return 0 // unreachable: Exec does not return on success
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// waitProxy polls the proxy until it answers, or gives up after about 3s.
func waitProxy(port int) {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if tcpAlive(port) {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func tcpAlive(port int) bool { return httpx.TCPAlive("127.0.0.1", port, 500*time.Millisecond) }
