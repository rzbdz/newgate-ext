package selfupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/rzbdz/newgate/lib/buildinfo"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	"github.com/rzbdz/newgate/modules/gateway/controlplane"

	"github.com/rzbdz/newgate-ext/manifest/distinfo"
)

// 本文件是 `newgate self-update`：看/改「走哪条路」，以及回滚。
//
// **升级本身不在这里**——那是 `newgate upgrade`（本模块往那里登记了 `binary`
// 动作）。两条命令各管一件事：upgrade 是「去拿新的」，self-update 是「这台机器
// 怎么拿」。合成一条的话，`self-update` 既是动词又是设置，参数会互相打架。

type selfUpdateCommand struct{}

var (
	_ cliapi.Command    = selfUpdateCommand{}
	_ cliapi.Documented = selfUpdateCommand{}
)

func (selfUpdateCommand) Names() []string { return []string{"self-update"} }

func (selfUpdateCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionMaintenance, Rank: 61,
		Usage:   "self-update [git [dir] | release [owner/repo] | rollback]",
		Summary: i18n.T("show or choose where newgate upgrade gets new binaries; rollback restores the previous one", nil),
	}
}

var ownerRepo = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+$`)

func (c selfUpdateCommand) Run(host cliapi.Host, args []string) int {
	if cliapi.Flag(args, "--help") || cliapi.Flag(args, "-h") {
		h := c.Help()
		fmt.Println("usage: newgate " + h.Usage)
		fmt.Println(h.Summary)
		return 0
	}
	switch sub := cliapi.Positional(args, 0); sub {
	case "", "status":
		printStatus()
		return 0
	case "git":
		return setGit(host, cliapi.Positional(args, 1))
	case "release":
		return setRelease(host, cliapi.Positional(args, 1))
	case "rollback":
		return doRollback(host)
	default:
		return host.Die(64, i18n.T("unknown self-update subcommand {sub} (git, release, rollback)", i18n.A{"sub": sub}))
	}
}

func printStatus() {
	st, saved := ensureSettings()
	info := distinfo.Current()
	fmt.Println(style.Title("newgate self-update", info.Distribution+" · "+info.Spec))

	mode := st.Mode
	if st.Mode == modeGit {
		mode += "  " + st.Repo
	}
	switch {
	case !saved:
		mode += style.Dim("  (" + i18n.T("inferred, not saved yet", nil) + ")")
	case st.DecidedBy == "first-run":
		mode += style.Dim("  (" + i18n.T("decided on first run", nil) + ")")
	}
	fmt.Println(style.Field(i18n.T("mode", nil), mode))
	gh := st.github()
	if gh == "" {
		gh = style.Dim(i18n.T("none declared", nil))
	} else if st.GitHub != "" {
		gh += style.Dim("  (" + i18n.T("overrides the specification", nil) + ")")
	}
	fmt.Println(style.Field("GitHub", gh))
	fmt.Println(style.Field(i18n.T("version", nil), buildinfo.Version()))
	if target, err := installTarget(); err == nil {
		bin := target
		if _, err := os.Stat(target + ".prev"); err == nil {
			bin += style.Dim("  (" + i18n.T("previous kept as .prev", nil) + ")")
		}
		fmt.Println(style.Field(i18n.T("binary", nil), bin))
	}
	if info, _ := controlplane.State(); info != nil {
		fmt.Println(style.Field(i18n.T("daemon", nil), fmt.Sprintf("pid %d · %s", info.PID, info.Exe)))
	}
	fmt.Println()
	fmt.Println(style.Hint(i18n.T("newgate upgrade --check shows whether there is something newer; newgate upgrade@<tag> pins a version", nil)))
}

func setGit(host cliapi.Host, dir string) int {
	if dir == "" {
		dir = distinfo.Source()
		if dir == "" || underTemp(dir) {
			wd, err := os.Getwd()
			if err != nil {
				return host.Die(1, err.Error())
			}
			dir = wd
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return host.Die(64, err.Error())
	}
	if top, err := git(context.Background(), abs, "rev-parse", "--show-toplevel"); err == nil && top != "" {
		abs = top
	}
	if err := checkRepo(abs, distinfo.Spec()); err != nil {
		return host.Die(65, err.Error())
	}
	st, _ := ensureSettings()
	st.Mode, st.Repo, st.DecidedBy = modeGit, abs, "user"
	if err := saveSettings(st); err != nil {
		return host.Die(1, err.Error())
	}
	fmt.Println(style.Item(style.OK, i18n.T("newgate upgrade now builds from {dir}", i18n.A{"dir": abs})))
	return 0
}

func setRelease(host cliapi.Host, repo string) int {
	if repo != "" && !ownerRepo.MatchString(repo) {
		return host.Die(64, i18n.T("{repo} is not owner/repo (no https://, no .git)", i18n.A{"repo": repo}))
	}
	st, _ := ensureSettings()
	st.Mode, st.Repo, st.DecidedBy = modeRelease, "", "user"
	if repo != "" {
		st.GitHub = repo
	}
	if st.github() == "" {
		return host.Die(65, i18n.T("this distribution declares no GitHub repository; give one: newgate self-update release <owner/repo>", nil))
	}
	if err := saveSettings(st); err != nil {
		return host.Die(1, err.Error())
	}
	fmt.Println(style.Item(style.OK, i18n.T("newgate upgrade now installs GitHub releases of {repo}", i18n.A{"repo": st.github()})))
	return 0
}

func doRollback(host cliapi.Host) int {
	target, err := installTarget()
	if err != nil {
		return host.Die(1, err.Error())
	}
	unlock, err := lockUpgrade()
	if err != nil {
		return host.Die(1, err.Error())
	}
	defer unlock()
	ver, err := rollback(context.Background(), target)
	if err != nil {
		return host.Die(1, err.Error())
	}
	fmt.Println(style.Item(style.OK, i18n.T("rolled back to {version}; the replaced binary is now {prev}",
		i18n.A{"version": ver, "prev": target + ".prev"})))
	msg, err := restartDaemon(target, os.Stdout)
	if err != nil {
		return host.Die(1, err.Error())
	}
	fmt.Println(style.Item(style.OK, msg))
	return 0
}
