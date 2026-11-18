package setup

// This file is the wizard's first act: **install the chosen clients and make them go
// through newgate**.
//
// It does only two things, both "what this machine looks like right now", no questions:
//
//	install  client absent -> run its own install command (see confighook.AgentInstaller)
//	takeover route this client's traffic through us (see the kernel's runtime/takeover)
//
// # Why it does not ask "which package to install"
//
// Install methods belong to the client module (`npm i -g @openai/codex`, hermes'
// curl|bash...): that is the whole reason confighook.AgentInstaller exists. Here it
// only **runs them in the chosen order** and reports each step's outcome truthfully.
//
// # Why "cannot install" is not the wizard's failure
//
// A user may lack npm, curl, a working network or sudo - that is their machine, not a
// config error. Note a failure, continue, recite each at the end: "looked installed" is fatal.

import (
	"fmt"
	"io"
	"os"

	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	runtimeapi "github.com/rzbdz/newgate-ext/modules/runtime"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Step is the outcome of one "install + takeover".
type Step struct {
	Agent string
	// Installed reports whether it is on this machine after the run.
	Installed bool
	// DidInstall = the install command really ran this time.
	DidInstall bool
	// Takeover is the takeover act's outcome (empty = never reached: not on the machine).
	Takeover runtimeapi.Result
	// TakenOver reports "has the disk really been changed by us right now".
	//
	// It is not the same as len(Takeover.Lines)>0: a config-type client (codex / hermes)
	// only grows its config file after one run, and that takeover **skips** with a note.
	// Reading that line as "taken over" is the one thing this repo cannot accept
	// ("looks installed"): requests in codex go direct.
	TakenOver bool
	// Err is the install step's error (takeover's own error is in Takeover.Err).
	Err error
}

// EnsureAgents, for each chosen agent in turn: install if absent, take over if present.
//
// out collects the install output (best-effort "trying" must be visible, see install.go).
func EnsureAgents(agents agentapi.AgentCatalog, rt runtimeapi.Control, ids []string, port int, out io.Writer) []Step {
	status := map[string]bool{}
	for _, st := range rt.TakeoverStatus() {
		status[st.Agent] = st.Active
	}
	var steps []Step
	for _, id := range ids {
		st := Step{Agent: id}
		if _, ok := agents.Get(id); !ok {
			st.Err = i18n.E("unknown agent {agent}", i18n.A{"agent": id})
			steps = append(steps, st)
			continue
		}
		if !agents.Installed(id) {
			inst := agents.Installer(id)
			if inst == nil {
				st.Err = i18n.E("{agent} is not installed, and this build knows no way to install it; install it yourself, then run this again",
					i18n.A{"agent": id})
				steps = append(steps, st)
				continue
			}
			fmt.Fprintf(out, "newgate: %s\n", i18n.T("running: {cmd}",
				i18n.A{"cmd": joinArgv(inst.Command())}))
			st.DidInstall = true
			if err := inst.Install(out); err != nil {
				st.Err = err
				steps = append(steps, st)
				continue
			}
			// The install finished, but this process's PATH may still not find it (the package
			// manager put bin elsewhere). No pretending: Installed tells the truth again.
			st.Installed = agents.Installed(id)
			if !st.Installed {
				st.Err = i18n.E("{agent} was installed but is still not on PATH — open a new shell and run newgate config again",
					i18n.A{"agent": id})
				steps = append(steps, st)
				continue
			}
		} else {
			st.Installed = true
		}
		// Take over right after installing: takeover writes **config files on this machine**;
		// uninstalled, those files do not exist and takeover just says "no such file".
		st.Takeover = rt.TakeOver(id, port)
		st.TakenOver = st.Takeover.Err == nil && takenOver(agents, id, status)
		if !st.TakenOver && st.Takeover.Err == nil {
			// A config-type client's (codex / hermes) file appears only after one run.
			// That is a **next step to tell the user**, not a failure - but never "taken over".
			if msg := firstStartHint(agents, id); msg != "" {
				st.Takeover.Warn = append(st.Takeover.Warn, msg)
			}
		}
		steps = append(steps, st)
	}
	return steps
}

// takenOver asks the disk: is this client **really** pointed at the local proxy now?
//
// Config-type goes through its own IsTakenOver (its criterion, each slightly
// different: codex looks at that header block, hermes at the providers.newgate line);
// shim-type reads the kernel's takeover state (link present or not). Both are answered
// by the module owning it; a guessed "taken over" is the worst error here.
func takenOver(agents agentapi.AgentCatalog, id string, status map[string]bool) bool {
	a, ok := agents.Get(id)
	if !ok {
		return false
	}
	if a.Config == nil {
		return status[id]
	}
	for _, t := range a.Config.Targets() {
		if _, err := os.Stat(t); err != nil {
			continue
		}
		if a.Config.IsTakenOver(t) {
			return true
		}
	}
	return false
}

// firstStartHint is the sentence for "installed, but its config file does not exist yet".
//
// The criterion uses the descriptor mechanism (has Config = config-type), not an agent
// list - a list is forgotten when a client is added, and the symptom is "user thinks taken over".
func firstStartHint(agents agentapi.AgentCatalog, id string) string {
	a, ok := agents.Get(id)
	if !ok || a.Config == nil {
		return ""
	}
	return i18n.T("{agent} writes its config file on first run; start it once ({cmd}), then run newgate on {agent}",
		i18n.A{"agent": id, "cmd": firstBin(a)})
}

func firstBin(a *agentapi.Agent) string {
	if len(a.Bin) > 0 {
		return a.Bin[0]
	}
	return a.ID
}

// joinArgv joins argv into one display line (no shell escaping: it is hint text).
func joinArgv(argv []string) string {
	s := ""
	for i, a := range argv {
		if i > 0 {
			s += " "
		}
		s += a
	}
	if s == "" {
		return i18n.T("(no command declared)", nil)
	}
	return s
}

// Report writes one "install + takeover" as lines for humans. w is usually stdout.
func Report(w io.Writer, steps []Step) {
	for _, s := range steps {
		switch {
		case s.Err != nil:
			fmt.Fprintln(w, i18n.T("  ✗ {agent}: {err}", i18n.A{"agent": s.Agent, "err": s.Err.Error()}))
		case s.Takeover.Err != nil:
			fmt.Fprintln(w, i18n.T("  ✗ {agent}: installed, but taking it over failed: {err}",
				i18n.A{"agent": s.Agent, "err": s.Takeover.Err.Error()}))
		default:
			what := i18n.T("already installed", nil)
			if s.DidInstall {
				what = i18n.T("installed", nil)
			}
			line := i18n.T("  ✓ {agent}: {what}",
				i18n.A{"agent": s.Agent, "what": what})
			switch {
			case s.TakenOver && len(s.Takeover.Lines) > 0:
				line += i18n.T(", taken over ({first})", i18n.A{"first": s.Takeover.Lines[0]})
			case !s.TakenOver:
				line += i18n.T(", not taken over yet", nil)
			}
			fmt.Fprintln(w, line)
		}
		for _, warn := range s.Takeover.Warn {
			fmt.Fprintln(w, "    "+i18n.T("note: {warn}", i18n.A{"warn": warn}))
		}
	}
}

// StepsFailed reports whether any step really failed (for the exit code).
func StepsFailed(steps []Step) bool {
	for _, s := range steps {
		if s.Err != nil || s.Takeover.Err != nil {
			return true
		}
	}
	return false
}

// stdout is the wizard's default output (a buffer in tests).
var stdout io.Writer = os.Stdout
