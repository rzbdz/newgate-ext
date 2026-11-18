package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/buildinfo"
	"github.com/rzbdz/newgate-ext/lib/style"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate/lib/i18n"
)

// cmdDoctor 体检。
//
// **界面在这里一项都不认识**：每一行都由拥有那件事的模块经 RegisterDiagnostics
// 报上来（文件/链路归 config，环境/代理归 gateway，接管/备份归 runtime），界面只
// 做统一的排版与「有没有红的」这个结论。上一版这里写死了六项，于是界面为了它们
// import 了 paths / store / resolve / takeover ——那都是别人的知识。
func cmdDoctor(service *service) int {
	fmt.Println(style.Title("newgate doctor", buildinfo.Version()))
	fmt.Println(style.Rule(64))

	checks := service.moduleDiagnostics()
	fmt.Println()
	bad := 0
	for _, d := range checks {
		mark := style.Skip
		switch d.State {
		case "ok":
			mark = style.OK
		case "warn":
			mark = style.Warn
		case "bad":
			mark = style.Bad
		}
		if mark == style.Bad {
			bad++
		}
		fmt.Println(style.Field(d.Label, style.Mark(mark)+" "+d.Line))
		for _, detail := range d.Details {
			fmt.Println(style.Bullet(style.Dim(detail)))
		}
	}

	fmt.Println()
	if bad == 0 {
		fmt.Println(style.Green(i18n.T("all checks passed", nil)))
		return 0
	}
	// 结论行走 N（带数量的消息）：英文有单复数，中文不分——中文那份只写一条即可。
	// 前面的状态标记留在消息外（它是版式符号，不是文案）。
	fmt.Println(style.Mark(style.Bad) + " " +
		i18n.N("{n} check failed", "{n} checks failed", bad, i18n.A{"n": bad}))
	return 1
}

// cmdAllLogs 诊断包：**把各模块交上来的原文拼起来**。
//
// 界面在这里只做两件事：打自己的那一段（版本、环境、状态），以及把模块交上来的
// 素材按 Rank 顺序拼好。原文从哪来、长什么样，全是各模块自己的事——谁的数据谁
// 自己交（见 cliapi.Dumper），界面不认识 providers.json，也不认识接管改了哪些文件。
//
// 上一版这一整个函数长在界面里，于是它得知道 providers 的结构、profile 的档位表、
// 目标文件在哪、证据文件叫什么、日志在哪。这一轮把那些知识全部还了回去。
func cmdAllLogs(service *service) int {
	line := func(t string) { fmt.Printf("\n===== %s =====\n", t) }

	// 这一段是界面自己的：它讲的是「这个进程跑在哪、什么版本、什么环境」。
	line(i18n.T("Version and environment", nil))
	fmt.Println(VersionLine())
	// 字段标签过目录（它是文案），值不过（那是路径与进程事实）。列宽固定，
	// 三行才对得齐——`env` 是**机器名**（环境变量的原文段），不翻译。
	const dumpLabelW = 12
	field := func(label, value string) { fmt.Println(style.Pad(label, dumpLabelW) + value) }
	field(i18n.T("Config dir", nil), paths.Config())
	field(i18n.T("Log file", nil), paths.LogFile())
	for _, k := range []string{"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY",
		"no_proxy", "NO_PROXY", "NEWGATE_HOME", "NEWGATE_TARGET_DIR", "NEWGATE_DUMP"} {
		if v := os.Getenv(k); v != "" {
			field("env", k+"="+v)
		}
	}

	line(i18n.T("Status", nil))
	cmdStatus(service)

	for _, section := range service.dumpSections() {
		if section.Title != "" {
			line(section.Title)
		}
		fmt.Println(strings.Join(section.Lines, "\n"))
	}
	return 0
}

// cmdStatus 一屏概览。
//
// **这一屏的每一行都归它的所有者**：谁的状态谁自己报（RegisterStatus /
// RegisterStatusBlocks）。界面在这里一行都不认识——代理、接管、配置各由
// gateway / runtime / config 报上来，界面的全部工作是按 Rank 排一下然后打印。
func cmdStatus(service *service) int {
	fmt.Println(style.Title("newgate "+buildinfo.Version(), buildinfo.BuildTimeDisplay()))
	fmt.Println(style.Rule(64))

	for _, line := range service.statusLines() {
		fmt.Println(style.Field(line.Label, line.Value))
	}
	for _, block := range service.statusBlocks() {
		if block.Title != "" {
			fmt.Print(style.Section(block.Title) + "\n")
		}
		for _, l := range block.Lines {
			fmt.Println(l)
		}
	}
	return 0
}

// routingOf 已被 runtime/takeover.List() 取代——那里是接管状态的唯一事实源，
// CLI / TUI / Web 都读同一份，不再各自判断一遍机制。
