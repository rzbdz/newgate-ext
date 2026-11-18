// Package takeover is the only implementation of "takeover".
//
// # Why this layer exists
//
// There are two mechanisms: a PATH shim for env-reading tools like claude, and a config
// rewrite for config-reading tools like opencode. This is an **implementation detail**
// users should not be forced to know - `newgate shim install claude` leaks our internals
// into the interface, and the user must first know which class their tool belongs to.
//
// So outside there are two verbs only: take over (On) and release (Off). The mechanism
// is chosen here from the catalog capability registration (BaseURLEnv -> shim, else a
// config rewrite). CLI / TUI / Web shells all call here - see docs/03-architecture.md
// and docs/07-clients-runtime.md.
//
// # Desired state and actual state
//
// "Whom the user wants taken over" is in state.json (domain.State.Takeover); "whom it is
// actually on" is on disk. They must stay apart, or two symmetric faults appear:
//
//	stop not pulling the shim -> `claude` still hits the shim, the wrapper lazily starts
//	                              the proxy, same as not stopping (the user's original bug)
//	start not plugging back in -> up but not taken over; claude connects direct, silently
//
// So start = plug in, stop = pull out, and neither loses the user's intent.
package takeover

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/store"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/runtime/agentstate"
	"github.com/rzbdz/newgate-ext/modules/runtime/injection"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Mechanism is the takeover mechanism. Users need not know, but troubleshooting wants it.
type Mechanism string

const (
	// MechShim puts a link to newgate in PATH and injects env in the child process.
	// For env-reading tools like claude: shell exports outrank config files, so a config
	// write is silently overridden.
	MechShim Mechanism = "shim"
	// MechConfig rewrites the tool's own config file.
	MechConfig Mechanism = "config"
)

// Status is one agent's takeover state.
type Status struct {
	Agent     string
	Mechanism Mechanism
	Wanted    bool // user intent (state.json)
	// Active is "is there something we did on disk" - shim: is the link there; config: is
	// the file we rewrote there. It is **what we did**, not **the user has the tool**;
	// the latter is Installed, and the split is deliberate (see Installed's note).
	Active    bool
	Installed bool     // whether this client itself is on the machine (`which`, our shim excluded)
	Detail    string   // one plain sentence: how it was taken over, or why not
	Files     []string // files the config mechanism touched
}

// Result is the outcome of one takeover/release.
type Result struct {
	Agent     string
	Mechanism Mechanism
	// Skipped: this agent was deliberately skipped (the user off'd it). Neither success nor
	// failure, and must be shown apart from "taken over" - a ✓ makes the user think it worked.
	Skipped bool
	Lines   []string // one line each, shown to the user as-is
	Warn    []string
	Err     error
}

func mechanismOf(a *agentapi.Agent) Mechanism {
	if a.Config != nil {
		return MechConfig
	}
	if a.BaseURLEnv != "" {
		return MechShim
	}
	return ""
}

// Agents are the known agents, in stable order.
func Agents() []string {
	ids := agentstate.Catalog().Names()
	sort.Strings(ids)
	return ids
}

// List gives each agent's takeover state.
func List() []Status {
	agents := agentstate.Catalog()
	st := store.LoadState()
	var out []Status
	for _, id := range Agents() {
		a, ok := agents.Get(id)
		if !ok {
			continue
		}
		s := Status{
			Agent: id, Mechanism: mechanismOf(a), Wanted: st.TakeoverWanted(id),
			// The criterion is "is this tool on this machine" (minus our own shim, see
			// confighook.InstalledDefault), not "is the file we rewrote still there".
			Installed: agents.Installed(id),
		}
		switch s.Mechanism {
		case MechShim:
			for _, n := range injection.Installed() {
				if n == id {
					s.Active = true
				}
			}
			if s.Active {
				s.Detail = fmt.Sprintf("%s/%s → newgate", injection.Dir(), id)
				if !injection.InPath() {
					s.Detail += i18n.T(" (but the shim dir is not in the current shell's PATH; reopen the shell)", nil)
				}
			} else {
				s.Detail = i18n.T("direct", nil)
			}
		case MechConfig:
			for _, t := range a.Config.Targets() {
				if _, err := os.Stat(t); err != nil {
					continue
				}
				s.Files = append(s.Files, t)
				if a.Config.IsTakenOver(t) {
					s.Active = true
				}
			}
			if s.Active {
				s.Detail = i18n.T("config file rewritten → local proxy", nil)
			} else if len(s.Files) == 0 {
				s.Detail = i18n.T("its config file was not found (not installed?)", nil)
			} else {
				s.Detail = i18n.T("direct", nil)
			}
		}
		if !s.Installed {
			// Not installed, the "how it is taken over" sentence describes nothing meaningful
			// (the file we rewrote is there, the tool is gone). It becomes "what we looked for
			// and did not find": the criterion uses **this process's PATH** - a user may have
			// installed it in another shell this process does not see, so say it plainly.
			s.Detail = i18n.T("{names} is not on PATH — nothing to take over",
				i18n.A{"names": strings.Join(a.Bin, "/")})
		}
		out = append(out, s)
	}
	return out
}

// On takes over an agent and records "the user wants it taken over".
func On(agent string, port int) Result {
	agents := agentstate.Catalog()
	a, ok := agents.Get(agent)
	if !ok {
		return Result{Agent: agent, Err: unknown(agent)}
	}
	if err := store.SetTakeoverWanted(agent, true); err != nil {
		return Result{Agent: agent, Mechanism: mechanismOf(a), Err: err}
	}
	return apply(a, port)
}

// Off releases an agent and records "the user does not want it taken over" - start skips it.
func Off(agent string) Result {
	agents := agentstate.Catalog()
	a, ok := agents.Get(agent)
	if !ok {
		return Result{Agent: agent, Err: unknown(agent)}
	}
	if err := store.SetTakeoverWanted(agent, false); err != nil {
		return Result{Agent: agent, Mechanism: mechanismOf(a), Err: err}
	}
	return release(a)
}

// OnAll takes over everything: agents not explicitly turned off. Changes no intent.
func OnAll(port int) []Result {
	agents := agentstate.Catalog()
	st := store.LoadState()
	var out []Result
	for _, id := range Agents() {
		a, ok := agents.Get(id)
		if !ok {
			continue
		}
		if !st.TakeoverWanted(id) {
			out = append(out, Result{Agent: id, Mechanism: mechanismOf(a), Skipped: true,
				Lines: []string{i18n.T("skipped (you turned it off; newgate on {agent} restores it)",
					i18n.A{"agent": id})}})
			continue
		}
		out = append(out, apply(a, port))
	}
	return out
}

// OffAll releases everything. **Changes no intent** - after newgate stop, start plugs
// back exactly what the user had.
func OffAll() []Result {
	agents := agentstate.Catalog()
	var out []Result
	for _, id := range Agents() {
		a, ok := agents.Get(id)
		if !ok {
			continue
		}
		r := release(a)
		if len(r.Lines) == 0 && r.Err == nil {
			continue // never taken over, nothing to report
		}
		out = append(out, r)
	}
	return out
}

func apply(a *agentapi.Agent, port int) Result {
	res := Result{Agent: a.ID, Mechanism: mechanismOf(a)}
	switch res.Mechanism {
	case MechShim:
		link, err := injection.Install(a.ID)
		if err != nil {
			res.Err = err
			return res
		}
		res.Lines = append(res.Lines, "PATH shim "+link+" → newgate")
		for _, rc := range injection.RCFiles() {
			changed, err := injection.AddToRC(rc)
			if err != nil {
				res.Warn = append(res.Warn, rc+": "+err.Error())
				continue
			}
			if changed {
				res.Lines = append(res.Lines, i18n.T("prepended {dir} to PATH (written into {file})",
					i18n.A{"dir": injection.Dir(), "file": rc}))
			}
		}
		if real, err := a.FindReal(injection.Dir()); err == nil {
			res.Lines = append(res.Lines, i18n.T("real {agent}: {path}", i18n.A{"agent": a.ID, "path": real}))
		} else {
			res.Warn = append(res.Warn, i18n.T("cannot find the real {agent}: {err} — the shim will fail to forward, install it first",
				i18n.A{"agent": a.ID, "err": err}))
		}
		if !injection.InPath() {
			res.Warn = append(res.Warn, i18n.T("the current shell's PATH is not in effect yet; reopen the shell or `exec $SHELL -l`", nil))
		}

	case MechConfig:
		reps, err := a.Config.Apply(port)
		if err != nil {
			res.Err = err
			return res
		}
		for _, rep := range reps {
			if rep.Skipped != "" {
				res.Lines = append(res.Lines, i18n.T("{file} (skipped: {why})",
					i18n.A{"file": rep.File, "why": rep.Skipped}))
				continue
			}
			line := rep.File
			if n := len(rep.Rewrites); n > 0 {
				line += i18n.T(" ({n} rewrites)", i18n.A{"n": n})
			}
			res.Lines = append(res.Lines, line)
			if rep.Fuzzy > 0 {
				res.Warn = append(res.Warn, i18n.T(
					"{file} has {n} spots that missed the exact rules and landed in the middle tier; worth a review",
					i18n.A{"file": rep.File, "n": rep.Fuzzy}))
			}
		}
	}
	return res
}

func release(a *agentapi.Agent) Result {
	res := Result{Agent: a.ID, Mechanism: mechanismOf(a)}
	switch res.Mechanism {
	case MechShim:
		installed := false
		for _, n := range injection.Installed() {
			if n == a.ID {
				installed = true
			}
		}
		if !installed {
			return res
		}
		if err := injection.Uninstall(a.ID); err != nil {
			res.Err = err
			return res
		}
		res.Lines = append(res.Lines, i18n.T(
			"removed the PATH shim (PATH precedence goes back to the real {agent})", i18n.A{"agent": a.ID}))

	case MechConfig:
		restored, err := a.Config.Restore()
		if err != nil {
			res.Err = err
			return res
		}
		for _, f := range restored {
			res.Lines = append(res.Lines, i18n.T("restored {file}", i18n.A{"file": f}))
		}
	}
	return res
}

func unknown(agent string) error {
	return i18n.E("unknown agent {agent} (known: {list})", i18n.A{"agent": agent, "list": Agents()})
}
