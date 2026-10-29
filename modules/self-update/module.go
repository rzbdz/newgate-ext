// Package selfupdate 换 newgate 自己的二进制：从 GitHub Release 下产物，或者在
// 本地仓库里编一份，然后优雅地换上去（daemon 走 socket 交接，不断流）。
//
// # 它挂在哪
//
// 本模块**不新开一条升级命令**：它往 modules/upgrade 的登记处交一个 ID 为
// `binary` 的动作，于是 `newgate upgrade` 连二进制带数据一起升，
// `newgate upgrade@v0.9.0` 把二进制钉到某个 tag。upgrade 不知道怎么下 Release、
// 怎么编、怎么交接——那些都在这里；这里也不知道参考库之类的数据怎么刷新。
// 自己只多带一条 `newgate self-update`：看/改「走哪条路」，以及回滚。
//
// # 两条路（mode），装第一次时定下来
//
//   - **release**：去规格书里声明的 GitHub 仓库（dist.json 的 `github`）拿最新
//     Release 里本平台的产物，对 SHA256SUMS，换上。给不编代码的机器（snode1 那类）。
//   - **git**：在一份本地 checkout 里 fetch → 能快进就快进 → `build/build.sh` →
//     换上。给开发者和尝鲜的人：他们手上本来就有仓库，要的是「我这份树的最新」。
//
// 走哪条记在 state.json 的 `self-update` 段里，**第一次运行这个二进制时**按构建
// 期注入的源码目录推断（见 settings.go）：编它的那份 checkout 在本机、是本仓库
// → git；不在 → release。之后只认那一段，用户可以 `newgate self-update git|release`
// 改它。推断而不是每次重算，是因为「这台机器走哪条路」是一个**决定**，不该随着
// 某个目录被挪走就悄悄翻转。
//
// # `@版本`
//
// `newgate upgrade@<ref>` 不分 mode：先找这个 tag 的 Release；有、且有本平台产物
// → 下它；没有（只打了 tag 没发版、或者是分支/提交、或者本平台没有产物，比如
// riscv64）→ 在临时目录里 clone 那个 ref、编、换上。开发者的本地 checkout 永远
// 不被切换分支——那是他正在改的东西。
//
// # 稳妥（每一条都是「换坏了比不换糟得多」）
//
//   - 新二进制先**按 newgate 的名字跑一次 `version`**，跑不起来（架构不对、被截断、
//     发行版不对）就不换；
//   - 替换是同目录临时文件 + rename，旧的那份硬链接成 `<路径>.prev`，
//     `newgate self-update rollback` 一步换回去；
//   - 没给 `--force` 时不倒退：Release 比手上的旧（开发机常态）就只说一声；
//   - 同一时刻只有一个升级在跑（配置目录里的 flock）；
//   - daemon 在跑就用新二进制执行 `restart`（优雅交接），换完核对它的 /proc/<pid>/exe。
package selfupdate

import (
	"context"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"

	upgradeapi "github.com/rzbdz/newgate-ext/modules/upgrade"
)

// Name 是这个模块在装配表与 `newgate plugin` 里的名字（= 目录名）。
const Name = "self-update"

// actionID 是登记进 upgrade 的那个动作的 ID。
const actionID = "binary"

// New 声明这个组件。
//
// 两条依赖都是 Optional：没装 upgrade 时没有 `newgate upgrade` 里的那一项，但
// `newgate self-update` 照样能看、能改、能回滚；没装界面时两个入口都没有，模块
// 什么也不做——它没有后台行为（不定时检查更新：换二进制是用户要知道的事）。
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: Name,
		Desc: func() string {
			return i18n.T("replace the newgate binary from a GitHub release or a local build, with a graceful daemon handoff", nil)
		},
		Type: "infra",
		Requires: []modules.Requirement{
			modules.Optional(upgradeapi.Capability),
			modules.Optional(cliapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			// 第一次运行时把「走哪条路」写下来（见包注释）。失败不挡启动：
			// 写不进去就每次现推，等用户第一次真的升级时再试一次。
			_, _ = ensureSettings()

			if up, ok := modules.Get(ctx, upgradeapi.Capability); ok {
				rel, err := up.Register(upgradeapi.Action{
					ID:        actionID,
					Title:     func() string { return i18n.T("newgate binary", nil) },
					Describe:  describeAction,
					Run:       runAction,
					Versioned: true,
				})
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}
			if ui, ok := modules.Get(ctx, cliapi.Capability); ok {
				rel, err := ui.RegisterCommand(selfUpdateCommand{})
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
