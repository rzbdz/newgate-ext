package upgrade

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
)

// 本文件是 `newgate upgrade`：把模块登记的升级动作跑一遍，或者只看清单。
//
// 二进制本身由 self-update 登记成其中一个动作（见 api.go 的包注释）；本命令对它
// 一无所知，只负责解析 `@版本` / `--check` / `-y` / `-f`、挑出该跑的动作、问人、
// 汇总结果。
//
// # 不静默（2026-10-29）
//
// 第一版敲下去就跑。二进制也进了这张表之后，「敲一下 upgrade」意味着换掉正在服务
// 的程序、重启 daemon——那必须是用户看过之后点头的事。所以缺省是：
//
//  1. 先**只看**一遍（Request.Check），把每个动作「会做什么 / 已经是最新」打出来；
//  2. 按 Kind **分两次问** y/N：一次问 newgate 本身，一次问模块的数据——它们分量
//     不同，用户要能只要其中一半；
//  3. 只执行点了头、而且确实有事可做的那几个。
//
// `-y` 跳过提问；`-f` 跳过提问并允许重装/倒退。没有终端又没给 `-y` 时**不执行**
// 并以 1 退出：没人回答的提问不能当成同意（与内核 runtime 的 confirm 同一条规矩），
// 而脚本需要从退出码上看出「其实没升」。

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

// upgradeArgs 是解析过的参数。`@值` 来自界面把 `upgrade@v0.9.0` 拆开的那一刀
// （内核 cli 的 splitAtVerb），也接受分开写的 `upgrade @v0.9.0`。
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

// selectActions 按「点名的动作」与「点没点版本」筛出这一次要跑的动作；
// 第二个返回值是因为没有版本概念而被跳过的那些（要说一声）。
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

// session 是一次 `newgate upgrade` 的执行现场。输入输出是字段，测试换掉它们。
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
	// 先问 newgate 本身，再问数据：程序换完之后数据动作仍在这个（旧）进程里跑，
	// 这没问题——它们拿的是文件，不依赖二进制的版本。
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

// group 处理一类动作：-y 时直接跑；否则先只看、再问一次、再跑有事可做的那几个。
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

// report 跑只看的那一遍并打出结论。第二个返回值是「这一遍没出错」。
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

// confirm 问一句 y/N。只认 y/yes；读不到（EOF）当「不」。
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

// printHelp 是 `newgate upgrade --help`：用法、流程、选项、例子，以及**这份构建**
// 里实际登记了哪些动作（help 屏只有一行摘要，装不下「会问你两次」这种事）。
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
		// 两行一条（与主 help 里过长的用法同一种排法）：选项名长短差得多，挤成
		// 一列时短的空一大截、长的贴着说明。
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
