package gateway

// 本文件是 `newgate logs`：代理日志的分页与跟随。
//
// **为什么它住在这里**（2026-09-18）：它读的是**守护进程写的那个文件**——
// 日志轮转（16MB × 4）在 serve.go 的启动处配置，写它的也是本模块。界面借这个
// 命令做一个分页器没问题，但「日志文件在哪、怎么写、轮转几份」不该由它知道。
//
// 分页器（pageOut）留在这儿是刻意的：它是这个命令的输出方式，不是界面的通用
// 能力——别的命令不需要翻页。

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

// Unstyled：逐字节日志（分页器/`-f` 跟随），不是版式。
func (logsCommand) Unstyled([]string) bool { return true }

func (logsCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionObserve, Rank: rankObserve,
		Usage: "logs [N] [-f]", Summary: i18n.T("Proxy log: paged in the terminal, -f keeps following", nil)}
}

func (logsCommand) Run(host cliapi.Host, args []string) int {
	return runLogs(host, logCount(args), cliapi.Flag(args, "-f") || cliapi.Flag(args, "--follow"))
}

// logTailDefault 非终端输出（管道 / 重定向）时的行数。终端上给全量，
// 由分页器兜着——和 journalctl 一样，重定向时才需要收敛。
const logTailDefault = 40

// cmdLogs 日志。行为对齐 journalctl：
//
//	newgate logs          终端上给全量日志，经分页器（内容不满一屏直接给）
//	newgate logs 200      只看最后 200 行
//	newgate logs -n 200   同上
//	newgate logs -f       跟随（tail -F）
//
// 分页只在 stdout 是终端时发生：`newgate logs > f` 或管道里必须老实打印，
// 否则脚本会挂在等用户按键上。
func runLogs(host cliapi.Host, n int, follow bool) int {
	auto := n <= 0
	if follow {
		// -F 而不是 -f：logx 按 16MB 轮转，换文件后 -f 会跟丢，
		// 表现为「日志突然不动了」。
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
			limit = 0 // 终端：全量交给分页器
		}
	}
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return pageOut(strings.Join(lines, "\n") + "\n")
}

// pageOut 经分页器输出。PAGER 优先；否则 less（-FRX：不满一屏自动退、
// 保留颜色、不动 termcap），没有 less 就退回 more。
//
// 分页器只是**显示**手段，任何一步失败都退回直接打印——不能因为没装
// 分页器就让用户看不到日志。
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

// logCount 解析行数：`newgate logs 200` 或 `newgate logs -n 200`。
// 都没给返回 0 = 自动（终端全量，管道取尾巴）。
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
