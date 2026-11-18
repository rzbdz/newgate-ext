package gateway

// This file is `newgate logs`: paging and following the proxy log.
//
// **Why it lives here** (2026-09-18): it reads **the file the daemon writes** - log
// rotation (16MB x 4) is configured where serve.go starts, and this module writes it.
// A UI borrowing it as a pager is fine, but where the file is should not be its knowledge.
//
// The pager (pageOut) stays here by design: it is this command's output style, not a
// general UI ability - other commands do not page.

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate/lib/i18n"
)

type logsCommand struct{}

var (
	_ cliapi.Command    = (*logsCommand)(nil)
	_ cliapi.Documented = (*logsCommand)(nil)
	_ cliapi.Unstyled   = (*logsCommand)(nil)
)

func (logsCommand) Names() []string { return []string{"logs", "log"} }

// Unstyled: byte-for-byte log output (pager / `-f` follow), not formatted.
func (logsCommand) Unstyled([]string) bool { return true }

func (logsCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionObserve, Rank: rankObserve,
		Usage: "logs [N] [-f]", Summary: i18n.T("Proxy log: paged in the terminal, -f keeps following", nil)}
}

func (logsCommand) Run(host cliapi.Host, args []string) int {
	return runLogs(host, logCount(args), cliapi.Flag(args, "-f") || cliapi.Flag(args, "--follow"))
}

// logTailDefault is the line count for non-terminal output (pipe / redirect). Terminals
// get everything, held by the pager - like journalctl, only redirects need narrowing.
const logTailDefault = 40

// cmdLogs is logs. Behavior matches journalctl:
//
//	newgate logs          full log on a terminal, via the pager (short output just printed)
//	newgate logs 200      last 200 lines only
//	newgate logs -n 200   same
//	newgate logs -f       follow (tail -F)
//
// Paging happens only when stdout is a terminal: `newgate logs > f` or a pipe must print
// plainly, or scripts hang waiting for a keypress.
func runLogs(host cliapi.Host, n int, follow bool) int {
	auto := n <= 0
	if follow {
		// -F rather than -f: logx rotates at 16MB, and -f loses the file on rotation,
		// showing up as "the log suddenly stopped".
		k := n
		if k <= 0 {
			k = logTailDefault
		}
		c := exec.Command("tail", "-n", strconv.Itoa(k), "-F", paths.LogFile())
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		_ = c.Run()
		return 0
	}
	b, err := ioutil.ReadFile(paths.LogFile())
	if err != nil {
		return host.Die(69, i18n.T("Cannot read the log {path}: {err}",
			i18n.A{"path": paths.LogFile(), "err": err.Error()}))
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		fmt.Println(style.Dim(i18n.T("  The log is empty", nil)))
		return 0
	}
	limit := n
	if auto {
		limit = logTailDefault
		if style.TTY() {
			limit = 0 // terminal: hand everything to the pager
		}
	}
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return pageOut(strings.Join(lines, "\n") + "\n")
}

// pageOut outputs through a pager. PAGER first; else less (-FRX: quit if shorter than a
// screen, keep colors, no termcap changes); no less, fall back to more.
//
// The pager is only a **display** aid; any step failing falls back to plain printing -
// a missing pager must never hide the logs.
func pageOut(s string) int {
	if !style.TTY() {
		fmt.Print(s)
		return 0
	}
	pager := os.Getenv("PAGER")
	if pager == "" {
		switch {
		case haveCmd("less"):
			pager = "less -FRX"
		case haveCmd("more"):
			pager = "more"
		default:
			fmt.Print(s)
			return 0
		}
	}
	c := exec.Command("/bin/sh", "-c", pager)
	c.Stdin = strings.NewReader(s)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		fmt.Print(s)
	}
	return 0
}

func haveCmd(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// logCount parses the line count: `newgate logs 200` or `newgate logs -n 200`.
// Neither = 0, meaning automatic (all on a terminal, tail on a pipe).
func logCount(args []string) int {
	if v := cliapi.FlagValue(args, "-n", "--lines"); v != "" {
		var k int
		if _, err := fmt.Sscanf(v, "%d", &k); err == nil && k > 0 {
			return k
		}
	}
	if v := cliapi.Positional(args, 0); v != "" {
		var k int
		if _, err := fmt.Sscanf(v, "%d", &k); err == nil && k > 0 {
			return k
		}
	}
	return 0
}
