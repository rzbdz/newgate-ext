// Package tui 是 menuconfig 风格的终端界面：改 profile / 档位归属用的。
//
// # 它是一个**独立的 ui 模块**（2026-09-18 从 modules/cli 的里层子包抽出来）
//
// 界面不是只有 cli 一个。tui 与将来的 web 都是同级的东西：各自是一个模块，各自
// 往**当前装着的** ui 里注入自己的入口（见 CLAUDE.md §4「ui 只是一类普通模块」）。
//
// 抽出来的直接好处是 modules/cli 里少了一块和「命令行解析」无关的东西——它原来
// 住在 cli/tui/ 里，只因为它是从 cli 敲出来的，而不是因为它属于命令行。
//
// # 它依赖什么
//
// 只依赖 config/store 与 gateway/controlplane 两个**共享叶子**（前者读写 profile、
// 后者写完之后通知守护进程重读）。**不依赖 cli**：它只是把一个命令注入进去，而那
// 是可选的（没装任何 ui 时这个模块什么也不做，自己的功能不受影响）。
//
// 为什么可以直接用 store：它是被 gateway / runtime / config 共同使用的叶子包（与
// config/paths 同级），不是「某个模块的内部结构」——所以这条 import 不是依赖边，
// Requires 里看不见它是对的（棘轮只看 Requires，这一点是**有意**的：store 与
// paths 属于「共享基础设施」那一档，跟 lib/ 同类）。
//
// 写完之后的 controlplane.Notify() 也不是正确性必需（watcher 的指纹覆盖
// state.json，最迟 1 秒自己会读），但仓库里每个写配置的点都这么通知——理由见
// tui.go 里的注释。
package tui

import (
	"context"
	"fmt"

	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/lib/i18n"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
)

// New 声明终端界面模块。
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "tui",
		// Type 是产品层的分类词，取值由编排者约定（见 modules/pluginmanager）。
		Type: "cli",
		Requires: []modules.Requirement{
			// ui 是**弱依赖**（见 component.Optional）：装着界面就把 `newgate tui`
			// 挂上去，没装就跳过——本模块没有别的职责，于是什么也不做。
			modules.Optional(cliapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			ui, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			release, err := ui.RegisterCommand(tuiCommand{})
			if err != nil {
				return err
			}
			releases = append(releases, release)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}

// tuiCommand 是 `newgate tui`（别名 menuconfig）。
type tuiCommand struct{}

var (
	_ cliapi.Command    = tuiCommand{}
	_ cliapi.Documented = tuiCommand{}
	_ cliapi.Unstyled   = tuiCommand{}
)

func (tuiCommand) Names() []string { return []string{"tui", "menuconfig"} }

// Unstyled：全屏界面，不是 newgate 的版式（见 cliapi.Unstyled）。
func (tuiCommand) Unstyled([]string) bool { return true }

func (tuiCommand) Help() cliapi.HelpLine {
	// Rank 50 = 维护那一节（与界面自己的那批同节）。数字是约定，留了空档给插队。
	//
	// Summary 在渲染 help 时现算，不在声明期算：这里的 T 必须晚于装语言。
	return cliapi.HelpLine{
		Section: cliapi.SectionMaintenance,
		Rank:    50,
		Usage:   "tui",
		Summary: i18n.T("menuconfig-style UI", nil),
	}
}

func (tuiCommand) Run(host cliapi.Host, _ []string) int {
	if err := Run(); err != nil {
		return host.Die(70, fmt.Sprintf("tui: %v", err))
	}
	return 0
}
