package cli

import (
	"fmt"

	"github.com/rzbdz/newgate/lib/i18n"
)

// 本文件把界面**自己的**那批命令也做成 Command，注册进同一个账本。
//
// 为什么（2026-09-18）：这些命令还住在界面里（它们要读配置、接管状态、守护进程
// 生命周期，而那些依赖还没从界面搬走），但它们的**声明**没必要和界面绑在一起。
// 做成 Command 之后：
//
//   - `run()` 里那个巨大的 switch 消失了，只剩编排（argv0 分发、包装启动、账本
//     查表、未知命令兜底）；
//   - `--help` 自动补全——每个命令自己声明 HelpLine，不必再在 usageText 里维护
//     一张并列的清单（那两张表错开过一次：命令搬走了、help 还留着）；
//   - 每条命令都变成了一个**可以整体搬走**的单元：哪天它的依赖也离开了界面，
//     把这个类型连同它的 cmdXxx 一起挪到拥有它的模块即可。
//
// 留在这里的判断标准写在 CLAUDE.md §4：把它删掉，业务模块还成立吗？成立就不该
// 留在界面——反过来说，这些命令**此刻**留在界面是因为它们依赖的东西还在界面手上。

// 帮助行的位置。数字留出空档，方便插队；节内按 Rank 再按 Usage 排。
const (
	rankTakeover = 10 // 接管
	rankRunOnce  = 20 // 跑一次
	rankRouting  = 30 // 路由与配置
	rankObserve  = 40 // 探测与观测
	rankMaint    = 50 // 维护
	rankSystem   = 60 // 界面自身（help / version）
)

type statusCommand struct{ s *service }

func (statusCommand) Names() []string { return []string{"status"} }
func (statusCommand) Help() HelpLine {
	return HelpLine{Section: SectionTakeover, Rank: rankTakeover,
		Usage: "status", Summary: i18n.T("who is going through newgate, and which profile each client uses", nil)}
}
func (c statusCommand) Run(_ Host, _ []string) int { return cmdStatus(c.s) }

// ---------- 探测与观测 ----------

type doctorCommand struct{ s *service }

func (doctorCommand) Names() []string { return []string{"doctor"} }
func (doctorCommand) Help() HelpLine {
	return HelpLine{Section: SectionObserve, Rank: rankObserve,
		Usage: "doctor", Summary: i18n.T("health check", nil)}
}
func (c doctorCommand) Run(_ Host, _ []string) int { return cmdDoctor(c.s) }

type allLogsCommand struct{ s *service }

func (allLogsCommand) Names() []string { return []string{"alllogs", "all-logs"} }

// Unstyled：诊断包是**原始转储**（日志原文 + 配置原文），不是给人扫的版式。
func (allLogsCommand) Unstyled([]string) bool { return true }
func (allLogsCommand) Help() HelpLine {
	return HelpLine{Section: SectionObserve, Rank: rankObserve,
		Usage: "alllogs", Summary: i18n.T("full diagnostic bundle", nil)}
}
func (c allLogsCommand) Run(_ Host, _ []string) int { return cmdAllLogs(c.s) }

// ---------- 界面自身 ----------

type versionCommand struct{}

func (versionCommand) Names() []string {
	return []string{"version", "--version", "-v"}
}
func (versionCommand) Help() HelpLine {
	return HelpLine{Section: SectionUI, Rank: rankSystem,
		Usage: "version", Summary: ""}
}
func (versionCommand) Run(_ Host, _ []string) int {
	fmt.Println(VersionLine())
	return 0
}

type helpCommand struct{ s *service }

func (helpCommand) Names() []string { return []string{"help", "--help", "-h"} }
func (helpCommand) Help() HelpLine {
	return HelpLine{Section: SectionUI, Rank: rankSystem,
		Usage: "help", Summary: i18n.T("this screen", nil)}
}
func (c helpCommand) Run(_ Host, _ []string) int {
	fmt.Print(usageText(c.s))
	return 0
}

// ownCommands 是界面自己的命令。
//
// 注册进同一个账本（而不是留在 run() 的 switch 里）：查重、分派、help 组装走
// 同一条路，run() 于是只剩编排。哪天某条命令的依赖离开了界面，把它从这里删掉、
// 挪到拥有它的模块即可——见文件头的说明。
func ownCommands(s *service) []Command {
	return []Command{
		statusCommand{s},
		doctorCommand{s},
		allLogsCommand{s},
		versionCommand{}, helpCommand{s},
	}
}

// registerOwnCommands 把界面自己的命令挂进账本。撞名即装配期错误——两个模块
// 抢同一个命令名必须是当场报错，而不是先到先得。
func (s *service) registerOwnCommands() error {
	for _, c := range ownCommands(s) {
		if _, err := s.RegisterCommand(c); err != nil {
			return err
		}
	}
	return nil
}
