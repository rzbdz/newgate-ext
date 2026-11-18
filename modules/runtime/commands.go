package runtime

// This file holds the commands **takeover owns**: start / stop / on / off / restart / shim.
// Moved wholesale from modules/cli/{lifecycle,shim}.go on 2026-09-18.
//
// Why: takeover is runtime's semantics - installing the shim, rewriting client config,
// backing up the original, releasing it back, are all this module's work. Left in the UI,
// the UI needed takeover / injection / confighook, when all it should do is hand argv over.
//
// This module registers the commands into the UI in Start: it declares a **weak dependency**
// on ui (Optional(cli), see component.Optional), so it starts after the UI - by the time
// Start gets here the UI's ledger is ready; without a UI it skips, takeover still works.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/lib/httpx"
	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlpath"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
	"github.com/rzbdz/newgate-ext/modules/runtime/daemon"
	"github.com/rzbdz/newgate-ext/modules/runtime/injection"
	"github.com/rzbdz/newgate-ext/modules/runtime/takeover"
	"github.com/rzbdz/newgate/lib/i18n"
)

// die is the single error exit (layout lives in lib/style).
func die(code int, msg string) int { return style.Die(code, msg) }

// pingProxy asks whether the control plane on the port is alive.
func pingProxy(port int) bool { return controlplane.Ping(port) }

// cmdShim is the **low-level escape hatch**, not a daily command.
//
// For daily takeover use newgate on/off <agent>: users should not have to know whether
// their tool is taken over via a PATH shim or a config rewrite - that is runtime/takeover's
// business (docs/08-operations.md: an escape hatch is first-class, but sits where it should).
//
// Only three things the takeover layer does not do remain here:
//   - status: lay out the real state of the shim dir, for "installed but not working"
//   - uninstall: delete the PATH line in rc too (takeover only pulls links; an empty dir
//     in PATH is harmless, and it spares the next on from touching rc again)
//   - install/off: aliases of the old commands, forwarded to on/off for muscle memory
func cmdShim(agents confighookapi.AgentCatalog, sub, which string) int {
	switch sub {
	case "install", "add", "on":
		if which == "" {
			return die(64, i18n.T("Usage: newgate shim install <agent> (known: {names})",
				i18n.A{"names": strings.Join(agents.Names(), ", ")}))
		}
		fmt.Println(style.Hint(i18n.T("Just use newgate on {agent}; newgate picks the mechanism itself",
			i18n.A{"agent": which})))
		fmt.Println()
		return cmdTakeover(agents, which)

	case "off", "remove", "rm":
		if which == "" {
			return die(64, i18n.T("Usage: newgate shim off <agent> (known: {names})",
				i18n.A{"names": strings.Join(agents.Names(), ", ")}))
		}
		fmt.Println(style.Hint(i18n.T("Just use newgate off {agent}", i18n.A{"agent": which})))
		fmt.Println()
		return cmdRelease(which)

	case "uninstall", "purge":
		for _, n := range injection.Installed() {
			if err := injection.Uninstall(n); err != nil {
				fmt.Fprintln(os.Stderr, style.Item(style.Warn, n+": "+err.Error()))
				continue
			}
			fmt.Println(style.Item(style.OK, i18n.T("Removed the shim {name}", i18n.A{"name": n})))
		}
		for _, rc := range injection.RCFiles() {
			changed, err := injection.RemoveFromRC(rc)
			if err != nil {
				fmt.Fprintln(os.Stderr, style.Item(style.Warn, rc+": "+err.Error()))
				continue
			}
			if changed {
				fmt.Println(style.Item(style.OK, i18n.T("Removed the PATH line from {file}", i18n.A{"file": rc})))
			}
		}
		if fg := injection.Foreign(); len(fg) > 0 {
			// Speak only when we **recognize this agent name**: then a foreign file of the
			// same name would shadow our shim and is worth a look; a claude-bak lying in the
			// directory is the user's own business, reporting it is just noise.
			var shadow []string
			for _, n := range fg {
				if _, known := agents.Get(n); known {
					shadow = append(shadow, n)
				}
			}
			if len(shadow) > 0 {
				fmt.Println(style.Hint(i18n.T("an external file is still in {dir}: {names} (it shadows newgate's shim)",
					i18n.A{"dir": injection.Dir(), "names": strings.Join(shadow, ", ")})))
			}
		}
		fmt.Println()
		fmt.Println(style.Hint(i18n.T("PATH traces are cleared; reopen the shell to take effect", nil)))
		fmt.Println(style.Hint(i18n.T("This does not change takeover intent: the next newgate start reinstalls it", nil)))
		return 0

	default:
		return shimStatus(agents)
	}
}

// shimStatus lays out the shim directory's real state, for "installed but not working".
func shimStatus(agents confighookapi.AgentCatalog) int {
	fmt.Println(style.Title("newgate shim", injection.Dir()))
	fmt.Println(style.Rule(72))

	if injection.InPath() {
		fmt.Println(style.Field(i18n.T("In PATH", nil), style.Green(i18n.T("yes", nil))))
	} else {
		fmt.Println(style.Field(i18n.T("In PATH", nil), style.Yellow(i18n.T("no", nil))+
			style.Dim("   "+i18n.T("reopen the shell or exec $SHELL -l", nil))))
	}

	inst := injection.Installed()
	if len(inst) == 0 {
		fmt.Println(style.Field(i18n.T("Installed", nil), style.Dim(i18n.T("none", nil))))
	} else {
		t := style.NewTable("shim", i18n.T("points to", nil))
		for _, n := range inst {
			a, ok := agents.Get(n)
			if !ok {
				t.Row(n, style.Dim(i18n.T("unknown agent", nil)))
				continue
			}
			if real, err := a.FindReal(injection.Dir()); err == nil {
				t.Row(n, real)
			} else {
				t.Row(n, style.Red(i18n.T("the real executable was not found (forwarding will fail)", nil)))
			}
		}
		fmt.Println(style.Field(i18n.T("Installed", nil),
			i18n.N("{n} shim", "{n} shims", len(inst), i18n.A{"n": len(inst)})))
		fmt.Print(t.String())
	}

	t := style.NewTable(i18n.T("rc file", nil), i18n.T("PATH line", nil))
	for _, rc := range injection.RCFiles() {
		mark := style.Dim(i18n.T("none", nil))
		if injection.HasBlock(rc) {
			mark = style.Green(i18n.T("yes", nil))
		}
		t.Row(rc, mark)
	}
	fmt.Print(t.String())

	// Desired vs actual: only agents using the shim mechanism; the rest is unrelated to shim.
	var rows [][2]string
	for _, s := range takeover.List() {
		if s.Mechanism != takeover.MechShim {
			continue
		}
		state := style.Mark(style.OK) + " " + i18n.T("in effect", nil)
		switch {
		case s.Wanted && !s.Active:
			state = style.Mark(style.Bad) + " " + i18n.T("declared takeover but not in effect", nil)
		case !s.Wanted && s.Active:
			state = style.Mark(style.Warn) + " " + i18n.T("not declared but still installed", nil)
		case !s.Wanted:
			state = style.Mark(style.Skip) + " " + i18n.T("not taken over", nil)
		}
		rows = append(rows, [2]string{s.Agent, state})
	}
	if len(rows) > 0 {
		fmt.Print(style.Section(i18n.T("Takeover intent", nil)) + "\n")
		t := style.NewTable("agent", i18n.T("status", nil))
		for _, r := range rows {
			t.Row(r[0], r[1])
		}
		fmt.Print(t.String())
	}

	fmt.Println()
	fmt.Println(style.Hint(i18n.T("Low-level escape hatch; day to day use newgate on|off <agent> · newgate shim uninstall also clears the rc files", nil)))
	return 0
}

func cmdStart(agents confighookapi.AgentCatalog, force bool) int {
	// Implemented in control.go's Start: the command and the `newgate config` wizard run
	// **one copy** of the code, since "start the proxy + take over all" has one correct
	// behavior (token first, no key no takeover, write back TakenOver). Here: exit code + hint.
	rep, err := (&service{}).Start(StartOptions{Force: force})
	if err != nil {
		switch {
		case errors.Is(err, ErrConfigUnusable):
			return 65
		case errors.Is(err, ErrProxyDidNotStart):
			return 70
		}
		return die(70, err.Error())
	}
	for _, r := range rep.Takeover {
		if r.Mechanism == takeover.MechShim && r.Err == nil && !r.Skipped && len(r.Lines) > 0 {
			warnShellEnvConflict(agents, r.Agent)
		}
	}
	return 0
}

// printResults prints takeover/release results as plain language.
//
// Three markers must not be mixed: ✓ taken over, · skipped on purpose, ✗ failed. Names are
// padded to a fixed width - only then can one scan a multi-agent takeover at a glance.
func printResults(rs []takeover.Result) {
	const nameW = 10
	for _, r := range rs {
		switch {
		case r.Err != nil:
			fmt.Fprintln(os.Stderr, style.Item(style.Bad, style.Pad(r.Agent, nameW)+r.Err.Error()))
		case len(r.Lines) == 0:
			continue
		default:
			mark := style.OK
			if r.Skipped {
				mark = style.Skip
			}
			fmt.Println(style.Item(mark, style.Pad(r.Agent, nameW)+r.Lines[0]))
			for _, l := range r.Lines[1:] {
				fmt.Println("    " + style.Pad("", nameW) + style.Dim(l))
			}
		}
		for _, w := range r.Warn {
			fmt.Println(style.Bullet(style.Mark(style.Warn) + " " + w))
		}
	}
}

// cmdTakeover / cmdRelease take over or release a single agent.
//
// The user should not care about mechanism (claude via PATH shim, opencode via config
// rewrite); outside there is only take over/release, and runtime/takeover picks the rest.
func cmdTakeover(agents confighookapi.AgentCatalog, agent string) int {
	if agent == "" {
		return die(64, i18n.T("Which agent to take over? e.g. newgate on claude", nil))
	}
	st := store.LoadState()
	r := takeover.On(agent, st.Port)
	if r.Err != nil {
		return die(65, r.Err.Error())
	}
	printResults([]takeover.Result{r})
	if r.Mechanism == takeover.MechShim {
		warnShellEnvConflict(agents, agent)
	}
	if daemon.Running() == nil {
		fmt.Println(style.Hint(i18n.T("Proxy is not running · it takes effect after newgate start", nil)))
	}
	return 0
}

func cmdRelease(agent string) int {
	if agent == "" {
		return die(64, i18n.T("Which agent to release? e.g. newgate off claude", nil))
	}
	r := takeover.Off(agent)
	if r.Err != nil {
		return die(65, r.Err.Error())
	}
	if len(r.Lines) == 0 {
		fmt.Println(style.Item(style.Skip, style.Pad(agent, 10)+i18n.T("was not taken over in the first place", nil)))
	} else {
		printResults([]takeover.Result{r})
	}
	fmt.Println(style.Hint(i18n.T("{agent} is back to a direct connection; newgate start will no longer take it over (newgate on {agent} restores it)",
		i18n.A{"agent": agent})))
	if r.Mechanism == takeover.MechShim {
		fmt.Println(style.Hint(i18n.T("This shell may have cached the path: hash -r (zsh: rehash)", nil)))
	}
	return 0
}

func cmdStop() int {
	// Order matters: pull the PATH shim first. Otherwise `claude` still hits
	// ~/.config/newgate/bin/claude -> newgate, the wrapper **lazily starts the proxy**, and
	// stop means nothing - the direct cause of "stopped but still going through newgate".
	//
	// OffAll does not change takeover intent: the next start plugs them back in as they were.
	printResults(takeover.OffAll())

	pid, err := daemon.Stop()
	if err != nil {
		return die(70, err.Error())
	}
	if pid > 0 {
		fmt.Println(style.Item(style.OK, i18n.T("Proxy stopped   pid {pid}", i18n.A{"pid": pid})))
	} else {
		fmt.Println(style.Item(style.Skip, i18n.T("The proxy was not running", nil)))
	}

	st := store.LoadState()
	st.TakenOver = false
	// Same: if this write fails, the next start thinks "the user never let go".
	if err := store.SaveState(st); err != nil {
		return style.Die(70, i18n.T("Cannot write the takeover state back to state.json: {err}",
			i18n.A{"err": err.Error()}))
	}

	fmt.Println()
	fmt.Println(style.Hint(i18n.T("All tools are back to direct connections; the PATH line still points at the empty shim dir, which is harmless", nil)))
	fmt.Println(style.Hint(i18n.T("Remove it completely: newgate shim uninstall", nil)))
	return 0
}

// cmdRestart restarts the proxy and keeps the takeover scene.
//
// Prefers graceful handoff (nginx upgrade semantics): the old daemon hands the listening
// socket to the new binary and drains in-flight requests - sessions crossing the proxy
// (a Claude Code developing newgate, say) are untouched, and takeover state stays.
// When the running daemon is an old version (no handoff), it falls back to stop+start:
// a short gap, with one line telling the user.
func cmdRestart(agents confighookapi.AgentCatalog, force bool) int {
	if tryHandoff() {
		return 0
	}
	cmdStop()
	fmt.Println()
	return cmdStart(agents, force)
}

// tryHandoff makes the running daemon hand the listening socket to the new binary on disk.
// Returns true when it succeeded (fully in place); otherwise prints why and returns false,
// and the caller falls back to stop+start.
func tryHandoff() bool {
	i := daemon.Running()
	if i == nil {
		return false // 没在跑，restart 退化为 start
	}
	st := store.LoadState()
	if st.ControlToken == "" {
		fmt.Println(style.Item(style.Skip, i18n.T("The daemon has no control token (started before the upgrade), falling back to stop+start", nil)))
		return false
	}
	port := i.Port
	if port <= 0 {
		port = st.Port
	}
	// Probe capability first: old daemons never registered /__newgate/upgrade, and a blind
	// post goes to the upstream via catch-all. status's handoff field is a side-effect-free probe.
	supports, err := handoffSupported(port)
	if err != nil || !supports {
		if err != nil {
			fmt.Println(style.Item(style.Skip, i18n.T("Cannot probe the daemon handoff capability ({err}), falling back to stop+start",
				i18n.A{"err": err})))
		} else {
			fmt.Println(style.Item(style.Skip, i18n.T("The running daemon is an old version (no graceful handoff), falling back to stop+start", nil)))
		}
		return false
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d%s", port, controlpath.Upgrade), nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+st.ControlToken)
	// The old daemon answers 200 only once the new process is ready; allow enough time
	resp, err := httpx.LocalClient(20 * time.Second).Do(req)
	if err != nil {
		fmt.Println(style.Item(style.Skip, i18n.T("Cannot send the handoff request ({err}), falling back to stop+start",
			i18n.A{"err": err})))
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := ioutil.ReadAll(io.LimitReader(resp.Body, 512))
		fmt.Println(style.Item(style.Skip, i18n.T("Handoff refused (HTTP {code}: {body}), falling back to stop+start",
			i18n.A{"code": resp.StatusCode, "body": string(b)})))
		return false
	}
	// 200 = new process on the socket, pid/lock rewritten. Here we only confirm it is fully in place.
	for k := 0; k < 100; k++ { // 最多 5s
		if j := daemon.Running(); j != nil && j.PID != i.PID && pingProxy(j.Port) {
			fmt.Println(style.Item(style.OK, i18n.T("Proxy gracefully restarted   pid {old} → {new} · 127.0.0.1:{port}",
				i18n.A{"old": i.PID, "new": j.PID, "port": j.Port})))
			fmt.Println(style.Hint(i18n.T("Socket handed over seamlessly: in-flight requests drain in the old process, sessions are not interrupted; takeover state is untouched", nil)))
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Printf("%s %s\n", style.Mark(style.Warn), i18n.T("Handoff sent, but the new process was not in place within 5 seconds · log {log}",
		i18n.A{"log": paths.LogFile()}))
	return false
}

// handoffSupported asks the daemon's status endpoint: does it support graceful handoff?
func handoffSupported(port int) (bool, error) {
	resp, err := httpx.LocalClient(3 * time.Second).
		Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, controlpath.Status))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var out struct {
		Handoff bool `json:"handoff"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.Handoff, nil
}

// activeProblems checks only providers that **will actually be used** - don't block
// startup because some unused profile lacks a key.
func activeProblems(st *domain.State) []string {
	snap, err := store.Load()
	if err != nil {
		return []string{err.Error()}
	}
	used := map[string]bool{}
	check := func(profile string) {
		for _, p := range snap.Profiles {
			if p.Name != profile {
				continue
			}
			for _, cands := range p.Roles {
				for _, b := range cands {
					used[b.Provider] = true
				}
			}
		}
	}
	check(st.DefaultProfile)
	for _, p := range st.Active {
		check(p)
	}
	var out []string
	for name := range used {
		prov, ok := snap.Providers.Providers[name]
		if !ok {
			out = append(out, i18n.T("Provider {name} is not defined", i18n.A{"name": name}))
			continue
		}
		if prov.Key() == "" {
			hint := i18n.T("fill in api_key in providers.json", nil)
			if prov.APIKeyEnv != "" {
				hint = i18n.T("set the environment variable {env} or fill in api_key", i18n.A{"env": prov.APIKeyEnv})
			}
			out = append(out, i18n.T("Provider {name} has no key → {hint}", i18n.A{"name": name, "hint": hint}))
		}
	}
	return out
}

func warnShellEnvConflict(agents confighookapi.AgentCatalog, toolID string) {
	a, ok := agents.Get(toolID)
	if !ok {
		return
	}
	var conflict []string
	for _, k := range append([]string{a.BaseURLEnv, a.AuthEnv}, a.UnsetEnv...) {
		if os.Getenv(k) != "" {
			conflict = append(conflict, k)
		}
	}
	if len(conflict) == 0 {
		return
	}
	fmt.Println(style.Hint(i18n.T("Already exported in the shell: {names}", i18n.A{"names": strings.Join(conflict, ", ")})))
	fmt.Println(style.Hint(i18n.T("Takeover overrides it inside the child process, without affecting newgate; it applies again after off (i.e. back to a direct connection)", nil)))
}

// ---------- command declarations ----------
//
// Each one only does "parse argv -> call the implementation above". Help line and slots
// are UI metadata, declared by the module that owns the command (see cliapi.Documented).

// Position of the takeover section: as it was in the UI, so the user's muscle memory holds.
const rankTakeover = 10

type startCommand struct{ agents confighookapi.AgentCatalog }

var (
	_ cliapi.Command    = (*startCommand)(nil)
	_ cliapi.Documented = (*startCommand)(nil)
)

func (startCommand) Names() []string { return []string{"start"} }

func (startCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionTakeover, Rank: rankTakeover,
		Usage: "start", Summary: i18n.T("Start the proxy + take over every agent", nil)}
}

func (c startCommand) Run(_ cliapi.Host, args []string) int {
	if a := cliapi.Positional(args, 0); a != "" {
		return cmdTakeover(c.agents, a)
	}
	return cmdStart(c.agents, cliapi.Flag(args, "--force"))
}

type stopCommand struct{ agents confighookapi.AgentCatalog }

var (
	_ cliapi.Command    = (*stopCommand)(nil)
	_ cliapi.Documented = (*stopCommand)(nil)
)

func (stopCommand) Names() []string { return []string{"stop"} }

func (stopCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionTakeover, Rank: rankTakeover,
		Usage: "stop", Summary: i18n.T("Stop the proxy + every agent back to a direct connection", nil)}
}

func (stopCommand) Run(_ cliapi.Host, args []string) int {
	if a := cliapi.Positional(args, 0); a != "" {
		return cmdRelease(a)
	}
	return cmdStop()
}

type takeoverCommand struct{ agents confighookapi.AgentCatalog }

var (
	_ cliapi.Command    = (*takeoverCommand)(nil)
	_ cliapi.Documented = (*takeoverCommand)(nil)
)

func (takeoverCommand) Names() []string { return []string{"on", "takeover", "take"} }

func (takeoverCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionTakeover, Rank: rankTakeover,
		Usage: "on <agent>", Summary: i18n.T("Take over just one", nil)}
}

func (c takeoverCommand) Run(_ cliapi.Host, args []string) int {
	return cmdTakeover(c.agents, cliapi.Positional(args, 0))
}

type releaseCommand struct{}

var (
	_ cliapi.Command    = (*releaseCommand)(nil)
	_ cliapi.Documented = (*releaseCommand)(nil)
)

func (releaseCommand) Names() []string { return []string{"off", "release", "free"} }

func (releaseCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionTakeover, Rank: rankTakeover,
		Usage: "off <agent>", Summary: i18n.T("Release just one; later start runs will leave it alone", nil)}
}

func (releaseCommand) Run(_ cliapi.Host, args []string) int {
	if a := cliapi.Positional(args, 0); a != "" {
		return cmdRelease(a)
	}
	return cmdStop()
}

type restartCommand struct{ agents confighookapi.AgentCatalog }

var (
	_ cliapi.Command    = (*restartCommand)(nil)
	_ cliapi.Documented = (*restartCommand)(nil)
)

func (restartCommand) Names() []string { return []string{"restart"} }

func (restartCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionTakeover, Rank: rankTakeover,
		Usage: "restart", Summary: i18n.T("Restart the proxy, keeping the takeover scene as it is", nil)}
}

func (c restartCommand) Run(_ cliapi.Host, args []string) int {
	return cmdRestart(c.agents, cliapi.Flag(args, "--force"))
}

type shimCommand struct{ agents confighookapi.AgentCatalog }

var (
	_ cliapi.Command    = (*shimCommand)(nil)
	_ cliapi.Documented = (*shimCommand)(nil)
)

func (shimCommand) Names() []string { return []string{"shim"} }

func (shimCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionMaintenance, Rank: 50,
		Usage: "shim …", Summary: i18n.T("Low-level escape hatch; on/off is enough day to day", nil)}
}

func (c shimCommand) Run(_ cliapi.Host, args []string) int {
	// No default agent: client ids are **each client module's own keys**; the UI should know none.
	return cmdShim(c.agents, cliapi.Positional(args, 0), cliapi.Positional(args, 1))
}

// commands is every command this module injects into the UI.
func commands(agents confighookapi.AgentCatalog) []cliapi.Command {
	return []cliapi.Command{
		startCommand{agents}, stopCommand{agents}, takeoverCommand{agents},
		releaseCommand{}, restartCommand{agents}, shimCommand{agents},
	}
}
