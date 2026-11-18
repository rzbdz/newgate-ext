package runtime

// This file is **wrapped launch**: `newgate claude ...` / `newgate run claude ...` /
// `newgate --profile ds claude`. Moved wholesale from modules/cli/launch.go on 2026-09-18.
//
// Why: "bring up a client and assemble the env/argv it needs" is this module's definition
// (see package runtime). The old note said "runtime only finds the real binary hidden by
// the shim, builds the minimal env override and launches it" - and this code is that
// rule's caller, left in the UI only because the UI was where commands were typed.
//
// So the UI no longer needs to know "which agents exist" to launch one: a client id is
// vocabulary of this module and config-hook. It hands `newgate claude ...` to the command
// named claude in the ledger - the launchCommand below, whose name it declares itself.

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate/lib/i18n"
)

// launchCommand launches an agent wrapped: `newgate claude ...` / `newgate run claude ...` /
// `newgate --profile ds claude`.
//
// **One command, verbs looked up on the spot** (rewritten 2026-09-18). The last version
// was "one instance per known agent, each remembering its id", so registration had to know
// every verb; but verbs **grow at runtime** - client modules register agents in Start. So
// launchCommands could only enumerate the catalog in Start, when others might not be
// registered yet: measured (`Optional(cli)` moved cli to the front), runtime starts before
// claudecode, the table is empty, and `newgate claude` says "unknown command". 70 e2e
// assertions went red that way.
//
// The root cause is not order but **reading others' output in Start** - the graph cannot
// express "everyone finished" (docs/02-component-framework.md's three-phase rule). So
// the verb table is looked up **at dispatch**, and registration hands over one command.
//
// The host adds back the missing bit: the args the dispatcher hands the command **exclude
// the verb**, so `newgate claude --profile=ds` gets `["--profile=ds"]`. The host knows
// which name found us (cliapi.Host.Verb), prepends the id, then splitLaunch runs it.
type launchCommand struct {
	rt     Runtime
	agents confighookapi.AgentCatalog
}

var (
	_ cliapi.Command    = (*launchCommand)(nil)
	_ cliapi.Documented = (*launchCommand)(nil)
	_ cliapi.Handoff    = (*launchCommand)(nil)
)

// Names looks up the catalog now: one verb per known agent, plus an explicit `run`.
//
// **Looked up on every dispatch** (no cache): the catalog grows after assembly, and a new
// agent must be dispatchable at once - no restart, no betting you start after the clients.
func (c launchCommand) Names() []string {
	names := append([]string(nil), c.agents.Names()...)
	return append(names, "run")
}

// Help declares only the one line `run <agent> [args...]`.
//
// **Deliberately no per-agent line**: agent names are client modules' keys, listing them
// would make the UI know the clients again. Users never learned `newgate claude` from help.
//
// The last version did this via "one instance per agent, returning empty Usage per
// agentID", at the price of knowing all agents at registration (see above). One line now.
func (c launchCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionRunOnce, Rank: 20,
		Usage: "run <agent> [args…]", Summary: i18n.T("Run once with a given profile", nil)}
}

func (c launchCommand) Run(host cliapi.Host, args []string) int {
	// The verb slot was stripped by the dispatcher: a name-is-the-agent call puts the id
	// back first, so splitLaunch takes the same path (it also validates the id exists).
	if verb := host.Verb(); verb != "" && verb != "run" {
		args = append([]string{verb}, args...)
	}
	return runLaunch(c.rt, c.agents, args)
}

// launchCommands has only one: the verb table is looked up at dispatch (see launchCommand).
func launchCommands(rt Runtime, agents confighookapi.AgentCatalog) []cliapi.Command {
	return []cliapi.Command{launchCommand{rt: rt, agents: agents}}
}

// HandsOff: this command then hands control to the launched client, and newgate's layout
// audit must not inspect that process's output (see cliapi.Handoff).
func (launchCommand) HandsOff() {}

// cmdLaunch launches an agent wrapped: parses newgate's own options, passes the rest through.
//
// Supported (docs/08-operations.md):
//
//	newgate claude
//	newgate claude --profile=ds
//	newgate --profile ds claude
//	newgate run claude --profile=ds
//	newgate claude --resume abc          # --resume abc is passed through to claude
func runLaunch(rt Runtime, agents confighookapi.AgentCatalog, args []string) int {
	agentName, profile, passthrough, err := splitLaunch(agents, args)
	if err != nil {
		return style.Die(64, err.Error())
	}
	a, _ := agents.Get(agentName)

	// A tool that is not installed: this call is **ours** (see offerInstall and takeYes).
	// Installed ones are untouched, not one character of their command line.
	if !agents.Installed(agentName) {
		if handled, rest := offerInstall(agents, a, passthrough); handled {
			passthrough = rest
		}
	}

	// A profile that does not exist errors at once - never a silent fallback to the default.
	if profile != "" {
		if _, err := store.LoadProfile(profile); err != nil {
			avail, _ := store.ListProfiles()
			return style.Die(65, i18n.T("No such profile {profile} (available: {list})",
				i18n.A{"profile": profile, "list": strings.Join(avail, ", ")}))
		}
	}

	return rt.Launch(a, agents.Facts(a.ID), passthrough, profile)
}

// splitLaunch splits the launch argv into (agent, profile, passthrough args).
//
// Rules (docs/08-operations.md applied; the one relaxation: `--profile`/`--preset` are
// newgate's own options and are consumed even after the agent - else `newgate claude
// --profile=ds` would pass the profile through to claude):
//
//  1. scan left to right;
//  2. `--profile`/`--preset` consume a value by arity (`--profile ds` or `--profile=ds`);
//  3. the first token that is neither a newgate option nor its value is the agent;
//  4. every token after the agent that is not a newgate option is passed through verbatim;
//  5. unknown `-` options before the agent is fixed are typos (error).
func splitLaunch(agents confighookapi.AgentCatalog, args []string) (agent, profile string, passthrough []string, err error) {
	if len(args) > 0 && args[0] == "run" {
		args = args[1:]
	}

	var rest []string
	agentKnown := false

	for i := 0; i < len(args); i++ {
		a := args[i]

		// newgate's own profile option (`--preset` is a historical alias for the same thing).
		if a == "--profile" || a == "--preset" || strings.HasPrefix(a, "--profile=") || strings.HasPrefix(a, "--preset=") {
			v := ""
			if j := strings.Index(a, "="); j >= 0 {
				v = a[j+1:]
			} else if i+1 < len(args) {
				v = args[i+1]
				i++
			}
			if v == "" {
				return "", "", nil, i18n.E("{option} needs a profile name", i18n.A{"option": a})
			}
			if profile != "" && profile != v {
				return "", "", nil, i18n.E("Both profile {first} and {second} were given, and they disagree",
					i18n.A{"first": profile, "second": v})
			}
			profile = v
			continue
		}

		if strings.HasPrefix(a, "-") {
			if !agentKnown {
				return "", "", nil, i18n.E(
					"Unknown option {option} (an unknown option before the tool counts as a typo, docs/08-operations.md rule 5)",
					i18n.A{"option": a})
			}
			rest = append(rest, a)
			continue
		}

		if !agentKnown {
			agent = a
			agentKnown = true
		} else {
			rest = append(rest, a)
		}
	}

	if agent == "" {
		return "", "", nil, i18n.E("Which agent to launch? e.g. newgate claude or newgate run claude", nil)
	}
	if _, ok := agents.Get(agent); !ok {
		return "", "", nil, i18n.E("Unknown agent {agent} (known: {list})",
			i18n.A{"agent": agent, "list": strings.Join(agents.Names(), ", ")})
	}
	return agent, profile, rest, nil
}

// takeYes strips `-y` / `--yes` from the passthrough args.
//
// **Called only when "this tool is not installed"** (see runLaunch): the command does not
// exist, so the args after it cannot be for it - `-y` in `newgate opencode -y` can only
// mean "confirm install". An installed tool gets everything verbatim: then `-y` is
// **its own** argument, and stripping it changes the user's command line. One criterion: installed.
func takeYes(args []string) (yes bool, rest []string) {
	for _, a := range args {
		if a == "-y" || a == "--yes" {
			yes = true
			continue
		}
		rest = append(rest, a)
	}
	return yes, rest
}

// confirm asks y/N. Unreadable input (no terminal, a pipe) always means "no" - a question
// nobody answered must not count as consent; that is "silently did what the user never approved".
func confirm(prompt string) bool {
	fmt.Fprint(os.Stderr, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(os.Stderr)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// offerInstall takes over this call when "this tool is not installed": ask, install, launch.
//
// Why launch right after installing instead of making the user type it again: the
// command was "run it", and installing is a step along the way. Sending them back turns
// our implementation detail into their operating step.
//
// Returns (handled?, remaining args). No installer = false, back to the old path (FindReal).
func offerInstall(agents confighookapi.AgentCatalog, a *confighookapi.Agent, passthrough []string) (bool, []string) {
	inst := agents.Installer(a.ID)
	if inst == nil {
		return false, passthrough
	}
	yes, rest := takeYes(passthrough)
	cmdline := joinArgv(inst.Command())
	if !yes {
		if !confirm(i18n.T("{agent} is not installed. Run `{cmd}` to install it? [y/N] ",
			i18n.A{"agent": a.ID, "cmd": cmdline})) {
			fmt.Fprintln(os.Stderr, i18n.T("Not installing. Do it yourself with: {cmd}",
				i18n.A{"cmd": cmdline}))
			os.Exit(0)
		}
	}
	fmt.Fprintf(os.Stderr, "newgate: %s\n", i18n.T("running: {cmd}", i18n.A{"cmd": cmdline}))
	if err := inst.Install(os.Stderr); err != nil {
		// best-effort: a failed install is not a fault (no npm, no network, no rights are
		// normal), but **must be said** - "did not install, looks installed" makes one hunt
		// for hours on a tool that does not exist. Carry the command and its error verbatim.
		fmt.Fprintf(os.Stderr, "newgate: %s\n", i18n.T("install failed: {err}", i18n.A{"err": err}))
		os.Exit(69)
	}
	if !agents.Installed(a.ID) {
		// Installed, but this process's PATH still cannot find it (the package manager's bin
		// dir is not in this PATH, or it went elsewhere). **Do not pretend success**: say so
		// and let the user open a new shell - that works; going on fails at launch, worse.
		fmt.Fprintf(os.Stderr, "newgate: %s\n", i18n.T(
			"{agent} was installed but is still not on PATH — open a new shell and try again",
			i18n.A{"agent": a.ID}))
		os.Exit(69)
	}
	return true, rest
}

// joinArgv joins argv into one line for display (no shell escaping: it is hint text).
func joinArgv(argv []string) string { return strings.Join(argv, " ") }
