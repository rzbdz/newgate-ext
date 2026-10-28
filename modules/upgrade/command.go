package upgrade

import (
	"context"
	"fmt"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
)

// 本文件是 `newgate upgrade`：把所有模块登记的升级动作跑一遍，或者只看清单。
//
// 它**不换二进制**。换二进制是另一件事（cp + mv + `newgate restart` 的优雅
// 交接），需要处理运行中的进程与在途请求；这里只处理「拿一份动态数据/配置」，
// 第一版不把两件事混在一起。绑定 git remote 自动 fetch/编译/安装同样没有做
// ——见下面输出里那句说明。

type upgradeCommand struct{ svc Service }

var (
	_ cliapi.Command    = (*upgradeCommand)(nil)
	_ cliapi.Documented = (*upgradeCommand)(nil)
)

func (upgradeCommand) Names() []string { return []string{"upgrade"} }

func (upgradeCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionMaintenance, Rank: 60,
		Usage:   "upgrade [action] [--list]",
		Summary: i18n.T("refresh the dynamic data/configuration modules own (not the binary)", nil),
	}
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
		t := style.NewTable(i18n.T("action", nil), i18n.T("what it refreshes", nil))
		for _, a := range actions {
			t.Row(a.ID, a.Describe())
		}
		fmt.Println(style.Title("newgate upgrade", i18n.N(
			"{n} action", "{n} actions", len(actions), i18n.A{"n": len(actions)})))
		fmt.Print(t.String())
		return 0
	}

	only := cliapi.Positional(args, 0)
	if only != "" {
		found := false
		for _, a := range actions {
			if a.ID == only {
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
				i18n.A{"id": only, "names": joinOrNone(names)}))
		}
	}
	if len(actions) == 0 {
		fmt.Println(style.Hint(i18n.T("no module registered an upgrade action in this build", nil)))
		return 0
	}

	fmt.Println(style.Hint(i18n.T(
		"this refreshes module-owned dynamic data/configuration only; the newgate binary is not replaced "+
			"(git-remote self-upgrade is not supported yet)", nil)))
	fmt.Println()

	failed := 0
	for _, a := range actions {
		fmt.Println(style.Bold(a.Title()))
		fmt.Printf("  %s\n", style.Dim(a.Describe()))
		msg, err := a.Run(context.Background())
		if err != nil {
			failed++
			fmt.Println("  " + style.Item(style.Bad, err.Error()))
			continue
		}
		if msg != "" {
			fmt.Println("  " + style.Item(style.OK, msg))
		}
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
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
