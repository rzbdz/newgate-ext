package upgrade

import (
	"context"
	"fmt"
	"strings"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
)

// 本文件是 `newgate upgrade`：把所有模块登记的升级动作跑一遍，或者只看清单。
//
// 二进制本身由 self-update 登记成其中一个动作（见 api.go 的包注释）；本命令对它
// 一无所知，只负责解析 `@版本` / `--check` / `--force`、挑出该跑的动作、汇总结果。

type upgradeCommand struct{ svc Service }

var (
	_ cliapi.Command    = (*upgradeCommand)(nil)
	_ cliapi.Documented = (*upgradeCommand)(nil)
)

func (upgradeCommand) Names() []string { return []string{"upgrade"} }

func (upgradeCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionMaintenance, Rank: 60,
		Usage:   "upgrade[@version] [action] [--check] [--force] [--list]",
		Summary: i18n.T("upgrade newgate and the data its modules own; @version pins a tag, branch or commit", nil),
	}
}

// upgradeArgs 是解析过的参数。`@值` 来自界面把 `upgrade@v0.9.0` 拆开的那一刀
// （内核 cli 的 splitAtVerb），也接受分开写的 `upgrade @v0.9.0`。
type upgradeArgs struct {
	only string
	req  Request
}

func parseArgs(args []string) (upgradeArgs, error) {
	var out upgradeArgs
	out.req.Check = cliapi.Flag(args, "--check") || cliapi.Flag(args, "--dry-run")
	out.req.Force = cliapi.Flag(args, "--force")
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
	if cliapi.Flag(args, "--help") || cliapi.Flag(args, "-h") {
		h := upgradeCommand{}.Help()
		fmt.Println("usage: newgate " + h.Usage)
		fmt.Println(h.Summary)
		return 0
	}
	actions := c.svc.Actions()
	if cliapi.Flag(args, "--list") {
		if len(actions) == 0 {
			fmt.Println(style.Hint(i18n.T("no module registered an upgrade action in this build", nil)))
			return 0
		}
		t := style.NewTable(i18n.T("action", nil), i18n.T("@version", nil), i18n.T("what it refreshes", nil))
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
		return 0
	}

	parsed, err := parseArgs(args)
	if err != nil {
		return host.Die(64, err.Error())
	}
	req := parsed.req

	if parsed.only != "" {
		found := false
		for _, a := range actions {
			if a.ID == parsed.only {
				actions = []Action{a}
				found = true
				break
			}
		}
		if !found {
			names := make([]string, 0, len(actions))
			for _, a := range actions {
				names = append(names, a.ID)
			}
			return host.Die(64, i18n.T("unknown upgrade action {id} (available: {names})",
				i18n.A{"id": parsed.only, "names": joinOrNone(names)}))
		}
		if req.Version != "" && !actions[0].Versioned {
			return host.Die(64, i18n.T("upgrade action {id} has no versions; drop @{version}",
				i18n.A{"id": parsed.only, "version": req.Version}))
		}
	}
	if len(actions) == 0 {
		fmt.Println(style.Hint(i18n.T("no module registered an upgrade action in this build", nil)))
		return 0
	}

	// 点了版本：只跑认识版本的那几个，其余的说一声再跳过（不静默）。
	var skipped []string
	if req.Version != "" {
		kept := actions[:0:0]
		for _, a := range actions {
			if a.Versioned {
				kept = append(kept, a)
			} else {
				skipped = append(skipped, a.ID)
			}
		}
		actions = kept
		if len(actions) == 0 {
			return host.Die(64, i18n.T("no upgrade action in this build takes a version (@{version})",
				i18n.A{"version": req.Version}))
		}
	}
	if req.Check {
		fmt.Println(style.Hint(i18n.T("--check: nothing is downloaded, replaced or restarted", nil)))
		fmt.Println()
	}

	failed := 0
	for _, a := range actions {
		fmt.Println(style.Bold(a.Title()))
		fmt.Printf("  %s\n", style.Dim(a.Describe()))
		msg, err := a.Run(context.Background(), req)
		if err != nil {
			failed++
			fmt.Println("  " + style.Item(style.Bad, err.Error()))
			continue
		}
		if msg != "" {
			fmt.Println("  " + style.Item(style.OK, msg))
		}
	}
	if len(skipped) > 0 {
		fmt.Println(style.Item(style.Skip, i18n.T("skipped (no versions): {ids}",
			i18n.A{"ids": joinOrNone(skipped)})))
	}
	fmt.Println()
	if failed > 0 {
		fmt.Println(style.Item(style.Bad, i18n.T("upgrade finished: {ok} succeeded, {failed} failed",
			i18n.A{"ok": len(actions) - failed, "failed": failed})))
		return 1
	}
	fmt.Println(style.Item(style.OK, i18n.T("upgrade finished: {n} succeeded",
		i18n.A{"n": len(actions)})))
	return 0
}

func joinOrNone(names []string) string {
	if len(names) == 0 {
		return i18n.T("none", nil)
	}
	return strings.Join(names, ", ")
}
