package upgrade

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This file is `newgate upgrade`: run the registered upgrade actions, or just list them.
//
// The binary itself is registered by self-update as one of the actions (see api.go's package
// comment); this command knows nothing about it, only parsing `@version` / `--check` / `-y` /
// `-f`, picking what to run, asking, and summarizing.
//
// # Not silent (2026-10-29)
//
// The first version ran on the keystroke. With the binary in this table, "type upgrade" means
// replacing the serving program and restarting the daemon - the user must look first. Default:
//
//  1. First **check only** (Request.Check), printing each action's "would do / up to date";
//  2. Ask y/N **twice, by Kind**: once for newgate itself, once for module data - different
//     weight, and the user must be able to take just half;
//  3. Run only the approved ones with real work to do.
//
// `-y` skips the prompts; `-f` skips them and allows reinstall/rollback. With no terminal
// and no `-y` it **does not run** and exits 1: an unanswered prompt is not consent (same as
// the kernel runtime's confirm); scripts must see "nothing upgraded" in the exit code.

type upgradeCommand struct{ svc Service }

var (
	_ cliapi.Command    = (*upgradeCommand)(nil)
	_ cliapi.Documented = (*upgradeCommand)(nil)
)

func (upgradeCommand) Names() []string { return []string{"upgrade"} }

func (upgradeCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionMaintenance, Rank: 60,
		Usage:   "upgrade[@version] [action] [--check] [-y] [-f] [--list]",
		Summary: i18n.T("upgrade newgate and the data its modules own, asking first; @version pins a tag, branch or commit", nil),
	}
}

// upgradeArgs is the parsed arguments. `@value` comes from the UI splitting `upgrade@v0.9.0`
// (the kernel cli's splitAtVerb), and the split `upgrade @v0.9.0` is accepted too.
type upgradeArgs struct {
	only string
	yes  bool
	req  Request
}

func parseArgs(args []string) (upgradeArgs, error) {
	var out upgradeArgs
	out.req.Check = cliapi.Flag(args, "--check") || cliapi.Flag(args, "--dry-run")
	out.req.Force = cliapi.Flag(args, "--force") || cliapi.Flag(args, "-f")
	out.yes = out.req.Force || cliapi.Flag(args, "--yes") || cliapi.Flag(args, "-y")
	for i := 0; ; i++ {
		a := cliapi.Positional(args, i)
		if a == "" {
			break
		}
		switch {
		case strings.HasPrefix(a, "@"):
			if out.req.Version != "" {
				return out, i18n.E("give one version, not {a} and {b}",
					i18n.A{"a": "@" + out.req.Version, "b": a})
			}
			out.req.Version = strings.TrimPrefix(a, "@")
			if out.req.Version == "" {
				return out, i18n.E("@ needs a version after it, e.g. upgrade@v0.9.0", nil)
			}
		case out.only == "":
			out.only = a
		default:
			return out, i18n.E("unexpected argument {arg}", i18n.A{"arg": a})
		}
	}
	return out, nil
}

func (c upgradeCommand) Run(host cliapi.Host, args []string) int {
	actions := c.svc.Actions()
	if cliapi.Flag(args, "--help") || cliapi.Flag(args, "-h") {
		printHelp(os.Stdout, actions)
		return 0
	}
	if cliapi.Flag(args, "--list") {
		printList(actions)
		return 0
	}
	parsed, err := parseArgs(args)
	if err != nil {
		return host.Die(64, err.Error())
	}
	actions, skipped, err := selectActions(actions, parsed)
	if err != nil {
		return host.Die(64, err.Error())
	}
	if len(actions) == 0 {
		fmt.Println(style.Hint(i18n.T("no module registered an upgrade action in this build", nil)))
		return 0
	}
	s := session{
		ctx:         context.Background(),
		out:         os.Stdout,
		in:          bufio.NewReader(os.Stdin),
		interactive: stdinIsTerminal(),
	}
	code := s.run(actions, parsed)
	if len(skipped) > 0 {
		fmt.Println(style.Item(style.Skip, i18n.T("skipped (no versions): {ids}",
			i18n.A{"ids": joinOrNone(skipped)})))
	}
	return code
}

// selectActions picks the actions to run from "named actions" and "version given or not";
// the second return value is those skipped for having no version concept (say so).
func selectActions(actions []Action, parsed upgradeArgs) ([]Action, []string, error) {
	req := parsed.req
	if parsed.only != "" {
		var hit []Action
		names := make([]string, 0, len(actions))
		for _, a := range actions {
			names = append(names, a.ID)
			if a.ID == parsed.only {
				hit = []Action{a}
			}
		}
		if hit == nil {
			return nil, nil, i18n.E("unknown upgrade action {id} (available: {names})",
				i18n.A{"id": parsed.only, "names": joinOrNone(names)})
		}
		if req.Version != "" && !hit[0].Versioned {
			return nil, nil, i18n.E("upgrade action {id} has no versions; drop @{version}",
				i18n.A{"id": parsed.only, "version": req.Version})
		}
		return hit, nil, nil
	}
	if req.Version == "" {
		return actions, nil, nil
	}
	var kept []Action
	var skipped []string
	for _, a := range actions {
		if a.Versioned {
			kept = append(kept, a)
		} else {
			skipped = append(skipped, a.ID)
		}
	}
	if len(kept) == 0 {
		return nil, nil, i18n.E("no upgrade action in this build takes a version (@{version})",
			i18n.A{"version": req.Version})
	}
	return kept, skipped, nil
}

// session is one `newgate upgrade` run's live context. In/out are fields; tests swap them.
type session struct {
	ctx         context.Context
	out         io.Writer
	in          *bufio.Reader
	interactive bool

	done, current, declined, failed int
}

func (s *session) println(a ...any) { fmt.Fprintln(s.out, a...) }

func (s *session) run(actions []Action, parsed upgradeArgs) int {
	if parsed.req.Check {
		s.println(style.Hint(i18n.T("--check: nothing is downloaded, replaced or restarted", nil)))
		s.println()
		for _, a := range actions {
			s.title(a)
			s.report(a, parsed.req)
		}
		return s.finish()
	}
	// Ask for newgate itself first, then data: after the program is swapped, data actions still
	// run in this (old) process, which is fine - they work on files, not the binary's version.
	for _, kind := range []Kind{KindProgram, KindData} {
		var group []Action
		for _, a := range actions {
			if a.Kind == kind {
				group = append(group, a)
			}
		}
		if len(group) > 0 {
			s.group(kind, group, parsed)
		}
	}
	return s.finish()
}

// group handles one action class: run directly with -y; else check, ask once, run those with work.
func (s *session) group(kind Kind, group []Action, parsed upgradeArgs) {
	if parsed.yes {
		for _, a := range group {
			s.title(a)
			s.execute(a, parsed.req)
		}
		return
	}
	look := parsed.req
	look.Check = true
	var pending []Action
	for _, a := range group {
		s.title(a)
		if res, ok := s.report(a, look); ok && !res.UpToDate {
			pending = append(pending, a)
		}
	}
	if len(pending) == 0 {
		return
	}
	if !s.interactive {
		s.println("  " + style.Item(style.Bad, i18n.T("not asking without a terminal; run again with -y to go ahead", nil)))
		s.failed += len(pending)
		return
	}
	if !s.confirm(kind, len(pending)) {
		s.println("  " + style.Item(style.Skip, i18n.T("left as it is", nil)))
		s.declined += len(pending)
		return
	}
	for _, a := range pending {
		if len(pending) > 1 {
			s.println("  " + style.Dim(a.Title()))
		}
		s.execute(a, parsed.req)
	}
}

func (s *session) title(a Action) {
	s.println(style.Bold(a.Title()))
	s.println("  " + style.Dim(a.Describe()))
}

// report runs the check pass and prints the conclusions. Second return = "this pass had no error".
func (s *session) report(a Action, req Request) (Result, bool) {
	res, err := a.Run(s.ctx, req)
	switch {
	case err != nil:
		s.failed++
		s.println("  " + style.Item(style.Bad, err.Error()))
		return res, false
	case res.UpToDate:
		s.current++
		s.println("  " + style.Item(style.OK, res.Message))
	case req.Check && res.Message != "":
		s.println("  " + style.Item(style.Skip, res.Message))
	}
	return res, true
}

func (s *session) execute(a Action, req Request) {
	res, err := a.Run(s.ctx, req)
	switch {
	case err != nil:
		s.failed++
		s.println("  " + style.Item(style.Bad, err.Error()))
	case res.UpToDate:
		s.current++
		s.println("  " + style.Item(style.OK, res.Message))
	default:
		s.done++
		if res.Message != "" {
			s.println("  " + style.Item(style.OK, res.Message))
		}
	}
}

// confirm asks a y/N. Accepts only y/yes; unreadable (EOF) counts as no.
func (s *session) confirm(kind Kind, n int) bool {
	q := i18n.N("Refresh this module data now? [y/N] ", "Refresh these {n} module data items now? [y/N] ", n, i18n.A{"n": n})
	if kind == KindProgram {
		q = i18n.T("Replace the newgate binary now? [y/N] ", nil)
	}
	fmt.Fprint(s.out, "  "+style.Bold(q))
	line, err := s.in.ReadString('\n')
	if err != nil && line == "" {
		s.println()
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

func (s *session) finish() int {
	s.println()
	parts := []string{i18n.T("{n} upgraded", i18n.A{"n": s.done}), i18n.T("{n} already current", i18n.A{"n": s.current})}
	if s.declined > 0 {
		parts = append(parts, i18n.T("{n} declined", i18n.A{"n": s.declined}))
	}
	if s.failed > 0 {
		parts = append(parts, i18n.T("{n} failed", i18n.A{"n": s.failed}))
		s.println(style.Item(style.Bad, i18n.T("upgrade finished: {summary}", i18n.A{"summary": strings.Join(parts, " · ")})))
		return 1
	}
	s.println(style.Item(style.OK, i18n.T("upgrade finished: {summary}", i18n.A{"summary": strings.Join(parts, " · ")})))
	return 0
}

func printList(actions []Action) {
	if len(actions) == 0 {
		fmt.Println(style.Hint(i18n.T("no module registered an upgrade action in this build", nil)))
		return
	}
	t := style.NewTable(i18n.T("action", nil), i18n.T("@version", nil), i18n.T("what it upgrades", nil))
	for _, a := range actions {
		pin := "—"
		if a.Versioned {
			pin = "✓"
		}
		t.Row(a.ID, pin, a.Describe())
	}
	fmt.Println(style.Title("newgate upgrade", i18n.N(
		"{n} action", "{n} actions", len(actions), i18n.A{"n": len(actions)})))
	fmt.Print(t.String())
}

// printHelp is `newgate upgrade --help`: usage, flow, options, examples, and the actions **this
// build** actually registered (the help screen has one summary line, too small for "asks twice").
func printHelp(w io.Writer, actions []Action) {
	h := upgradeCommand{}.Help()
	fmt.Fprintln(w, "usage: newgate "+h.Usage)
	fmt.Fprintln(w)
	fmt.Fprintln(w, style.WrapLine(i18n.T("Upgrades newgate itself and the data its modules own. Nothing happens silently: it first looks (like --check), then asks once for the newgate binary and once for the module data, and only does what you confirmed.", nil), ""))
	fmt.Fprintln(w, style.Section(i18n.T("options", nil)))
	for _, o := range [][2]string{
		{"@<version>", i18n.T("pin the binary to a tag, branch or commit (a release if one exists, otherwise built from source); actions without versions are skipped", nil)},
		{"<action>", i18n.T("only this action (see --list)", nil)},
		{"--check", i18n.T("only look: what would change; nothing is downloaded, replaced or restarted", nil)},
		{"-y, --yes", i18n.T("do not ask; go ahead with everything that has something to do", nil)},
		{"-f, --force", i18n.T("like -y, and also reinstall the same version or go back to an older one", nil)},
		{"--list", i18n.T("list the upgrade actions in this build", nil)},
	} {
		// Two lines per item (like long usage lines in the main help): option name lengths vary
		// a lot, and in one column short ones float far and long ones hug the description.
		fmt.Fprintln(w, "  "+style.Bold(o[0]))
		fmt.Fprintln(w, "      "+style.WrapLine(o[1], "      "))
	}
	fmt.Fprintln(w, style.Section(i18n.T("examples", nil)))
	for _, e := range []string{
		"newgate upgrade",
		"newgate upgrade --check",
		"newgate upgrade binary -y",
		"newgate upgrade@v0.9.0",
		"newgate upgrade fingerprint-data",
	} {
		fmt.Fprintln(w, "  "+e)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, style.Hint(i18n.T("If a new binary fails to take over the daemon it is rolled back automatically; newgate self-update rollback swaps back by hand.", nil)))
	if len(actions) > 0 {
		fmt.Fprintln(w, style.Section(i18n.T("actions in this build", nil)))
		for _, a := range actions {
			fmt.Fprintln(w, "  "+style.Bold(a.ID))
			fmt.Fprintln(w, "      "+style.WrapLine(a.Describe(), "      "))
		}
	}
}

func joinOrNone(names []string) string {
	if len(names) == 0 {
		return i18n.T("none", nil)
	}
	return strings.Join(names, ", ")
}
