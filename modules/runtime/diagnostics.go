package runtime

// This file hands over **takeover's own** two checks: which target files were really
// rewritten (takeover), and whether the escape hatch is ready (backups, restored by `stop`).
//
// **Why they live here** (2026-09-18): takeover state and the backup dir are this module's
// output; they used to be in modules/cli/diag.go, so the UI read original/ on takeover's
// behalf and judged each target file. Whoever writes it reports it - the UI only lays out
// (see cli/extension.Diagnostic).
//
// Dependency direction: runtime -> cli/extension (a leaf contract), not -> modules/cli.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
	"github.com/rzbdz/newgate-ext/modules/runtime/daemon"
	"github.com/rzbdz/newgate-ext/modules/runtime/takeover"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Position of the check and the status line (smaller Rank first). Takeover sits after proxy
// and before config: it answers "who is going through newgate".
const (
	rankStatusTakeover = 20
	rankCheckDaemon    = 35
	rankCheckTakeover  = 40
	rankCheckBackups   = 50

	// Position in the diagnostics bundle (see cliapi.DumpSection).
	rankDumpTargets = 40
)

type runtimeReporter struct{ agents confighookapi.AgentCatalog }

var (
	_ cliapi.DiagnosticProvider = runtimeReporter{}
	_ cliapi.StatusProvider     = runtimeReporter{}
	_ cliapi.Dumper             = runtimeReporter{}
)

func (r runtimeReporter) Diagnostics() []cliapi.Diagnostic {
	return []cliapi.Diagnostic{checkDaemon(), r.checkTakeover(), checkBackups()}
}

// checkDaemon: the three statements of "who is serving" must agree: pidfile, lock, body.
//
// **Why a separate item** (2026-09-18 live): after the pidfile went bad ("a child that lost
// the lock race and exited"), the proxy item said "not running" while the daemon served -
// neither item lied, but nobody said "your pidfile and lock disagree", so one guessed via ps.
// This item is that sentence, and it repairs the pidfile along the way (daemon.Reconcile).
//
// "Not running" is skip, not bad: a normal state, not a fault.
func checkDaemon() cliapi.Diagnostic {
	d := cliapi.Diagnostic{Rank: rankCheckDaemon, Label: i18n.T("Daemon", nil)}
	info, notes := daemon.Reconcile()
	if len(notes) == 0 {
		// Earlier checks (chain, proxy) read Running() and may already have repaired the
		// pidfile - the mismatch is invisible by now. If this process healed it, still say so.
		if h := daemon.LastHeal(); h != "" {
			notes = []string{h}
		}
	}
	switch {
	case len(notes) > 0:
		d.State = "warn"
		d.Line = notes[0]
		d.Details = append(d.Details, notes[1:]...)
		if info != nil {
			d.Details = append(d.Details,
				i18n.T("Trusting the lock: pid {pid} · 127.0.0.1:{port}",
					i18n.A{"pid": info.PID, "port": info.Port}),
				i18n.T("pidfile is rewritten to the real process; status/metrics recover at once, no manual file editing needed", nil))
		}
	case info == nil:
		d.State = "skip"
		d.Line = i18n.T("Not running", nil)
		d.Details = append(d.Details, "newgate start")
	default:
		d.State = "ok"
		d.Line = i18n.T("pid {pid} · 127.0.0.1:{port} · pidfile and lock agree",
			i18n.A{"pid": info.PID, "port": info.Port})
		// At a graceful handoff's instant the pidfile is the new process and the lock the old
		// one (AdoptRuntime writes the pidfile first). Both alive but different pids has only
		// this one explanation; say it, so the reader does not think it broke again.
		if pid := daemon.LockHolder(); pid > 0 && pid != info.PID && daemon.Alive(pid) {
			d.Details = append(d.Details, i18n.T(
				"The lock file currently says pid {pid} (normal within the graceful handoff window, it lines up by itself within milliseconds)",
				i18n.A{"pid": pid}))
		}
	}
	return d
}

// Status says in one line who goes through newgate, expanding only on anomaly.
//
// Desired state (what was on/off'd) vs reality (what is on disk) disagreeing is exactly
// the two symmetric faults' scene: taken over but not in effect (the tool connects direct).
func (r runtimeReporter) Status() []cliapi.StatusLine {
	var on, off, missing []string
	wanted, active, stale := 0, 0, 0
	for _, s := range takeover.List() {
		// The four-state criterion comes from phaseOf (view.go), same source as the web table:
		// two UIs answering differently is worse than one not showing it; this only lays it out.
		switch phaseOf(s) {
		case phaseActive:
			active++
			on = append(on, s.Agent+style.Dim(" ")+style.Mark(style.OK))
		case phasePending:
			on = append(on, s.Agent+style.Dim(" ")+style.Mark(style.Bad))
		case phaseStale:
			// The user turned it off, but disk was not released (release failed). Shown on the
			// `on` side - it **really** still goes through newgate; ⚠ says "not what you asked".
			active++
			stale++
			on = append(on, s.Agent+style.Dim(" ")+style.Mark(style.Warn))
		case phaseAbsent:
			// **Not folded into off**: off means "the tool is here, just direct", while here
			// the machine does not have it at all. Mixed in, users think they need `newgate
			// on` once (before 2026-09-21 machines without opencode showed ✓).
			missing = append(missing, s.Agent)
			continue // 没装的东西不计入 wanted：它接不上管，也不该触发下面那句提醒
		default:
			off = append(off, s.Agent)
		}
		if s.Wanted {
			wanted++
		}
	}
	line := takeoverLine(on, off, missing)
	// The proxy runs but some agent wanted takeover got none: a missed step after
	// start/build; without it that tool silently connects directly.
	if _, doc := controlplane.State(); doc != nil && active < wanted {
		line += "\n" + style.Hint(style.Yellow(i18n.T("An agent declares takeover but it is not in effect; rerun newgate start", nil)))
	}
	// The other half (⚠ class): the user turned it off, yet disk still has it - release
	// failed (the permission trap, see CLAUDE.md §3.1). **Must come with an explanation**: a
	// bare ⚠ makes one guess, and it says "you think it is direct, it still goes via the proxy".
	if stale > 0 {
		line += "\n" + style.Hint(style.Yellow(i18n.T("An agent was released but the takeover is still in effect (the release did not take); rerun: newgate off <agent>", nil)))
	}
	return []cliapi.StatusLine{{Rank: rankStatusTakeover, Label: i18n.T("Takeover", nil), Value: line}}
}

func takeoverLine(on, off, missing []string) string {
	tail := ""
	if len(off) > 0 {
		tail += "   " + style.Dim(strings.Join(off, " ")+" off")
	}
	// Not-installed is its own section: neither "connected" nor "direct", just a fact
	// (see phaseAbsent). Hidden, users hunt for why a thing not on this machine was not
	// taken over - a hunt for something that does not exist here.
	if len(missing) > 0 {
		tail += "   " + style.Dim(strings.Join(missing, " ")+" "+i18n.T("not installed", nil))
	}
	if len(on) == 0 {
		line := style.Dim(i18n.T("All direct", nil))
		if len(off) > 0 {
			line += "   " + style.Dim("newgate start / on <agent>")
		}
		return line + tail
	}
	return "   " + strings.Join(on, "   ") + tail
}

// checkTakeover: the target files that were rewritten.
func (r runtimeReporter) checkTakeover() cliapi.Diagnostic {
	d := cliapi.Diagnostic{Rank: rankCheckTakeover, Label: i18n.T("Takeover", nil)}
	var on, off []string
	for _, id := range r.agents.Names() {
		agent, ok := r.agents.Get(id)
		if !ok || agent.Config == nil {
			continue
		}
		for _, target := range agent.Config.Targets() {
			if _, err := os.Stat(target); err != nil {
				continue
			}
			if agent.Config.IsTakenOver(target) {
				on = append(on, filepath.Base(target))
			} else {
				off = append(off, filepath.Base(target))
			}
		}
	}
	if len(on) == 0 {
		d.State = "skip"
		d.Line = i18n.T("No file was rewritten", nil)
		return d
	}
	d.State = "ok"
	d.Line = strings.Join(on, " · ")
	if len(off) > 0 {
		d.Details = append(d.Details, i18n.T("Not taken over: {names}", i18n.A{"names": strings.Join(off, " · ")}))
	}
	return d
}

// checkBackups: is the escape hatch ready - with something in original/, `newgate stop` restores.
func checkBackups() cliapi.Diagnostic {
	d := cliapi.Diagnostic{Rank: rankCheckBackups, Label: i18n.T("Backups", nil)}
	orig := filepath.Join(paths.BackupDir(), "original")
	ents, err := os.ReadDir(orig)
	if err != nil || len(ents) == 0 {
		d.State = "skip"
		d.Line = i18n.T("No original backups", nil)
		d.Details = append(d.Details, i18n.T("Created only after a config file has been taken over", nil))
		return d
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	d.State = "ok"
	d.Line = i18n.N("{n} original config · newgate stop can restore it",
		"{n} original configs · newgate stop can restore them", len(ents), i18n.A{"n": len(ents)})
	d.Details = append(d.Details, "backups/original/ "+strings.Join(names, " · "))
	return d
}

// ---------- raw material for the diagnostics bundle ----------

// Dump hands over the newgate-related excerpts of **the files takeover rewrote**.
//
// Only lines containing "newgate": the target is "the user's original config", which may
// run to hundreds of unrelated lines, and the bundle must fit in an issue. Raw text.
func (r runtimeReporter) Dump() []cliapi.DumpSection {
	s := cliapi.DumpSection{Rank: rankDumpTargets, Title: i18n.T("Target files after takeover (newgate-related fragments)", nil)}
	runtimeDump := s
	var targets []string
	for _, id := range r.agents.Names() {
		agent, ok := r.agents.Get(id)
		if !ok || agent.Config == nil {
			continue
		}
		targets = append(targets, agent.Config.Targets()...)
	}
	sort.Strings(targets)
	for _, t := range targets {
		b, err := os.ReadFile(t)
		if err != nil {
			s.Lines = append(s.Lines, fmt.Sprintf("  %s : %v", t, err))
			continue
		}
		s.Lines = append(s.Lines, "  "+i18n.T("--- {file} ({n} bytes) ---", i18n.A{"file": t, "n": len(b)}))
		for _, ln := range strings.Split(string(b), "\n") {
			if strings.Contains(ln, "newgate") {
				s.Lines = append(s.Lines, "    "+strings.TrimSpace(ln))
			}
		}
	}
	_ = runtimeDump
	return []cliapi.DumpSection{s}
}
