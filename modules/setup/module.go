package setup

// 本文件把向导接进装配：一条 `newgate config` 命令。
//
// 依赖全是**内核的公开端口**，而且都是 Optional——判据与仓库里别的模块同一条：
//
//	cli 不在    整件事没有入口（这是唯一一条不装就什么都不发生的：向导是键盘的）
//	confighook 不在  不知道有哪些客户端，于是装/接管那一幕整段跳过
//	runtime 不在     同上（没有接管与生命周期）
//
// 缺后两个时向导**仍然**能把第一个上游写出来——那恰好是「只装 web 界面」那种装配
// 里最有用的那一半（网页上再去接管）。所以它不是「缺一个就整个不装」的模块。

import (
	"context"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	runtimeapi "github.com/rzbdz/newgate-ext/modules/runtime"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Name 是这个模块在装配表与 `newgate plugin` 里的名字（= 目录名）。
const Name = "setup"

// New 声明这个组件。
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: Name,
		Desc: func() string {
			return i18n.T("first-run setup: pick clients, one upstream, and go (newgate config)", nil)
		},
		Type: "infra",
		Requires: []modules.Requirement{
			modules.Optional(cliapi.Capability),
			modules.Optional(agentapi.AgentCatalogCapability),
			modules.Optional(runtimeapi.ControlCapability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			ui, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			agents, _ := modules.Get(ctx, agentapi.AgentCatalogCapability)
			control, _ := modules.Get(ctx, runtimeapi.ControlCapability)
			rel, err := ui.RegisterCommand(configCommand{agents: agents, control: control})
			if err != nil {
				return err
			}
			releases = append(releases, rel)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}

// configCommand 是 `newgate config`（别名 `setup`）。
//
// 为什么这个名字此前空着：`newgate config` 在配置模块里没有命令（那里的命令是
// init / profile / tier / agents…），而用户第一反应就是敲它。它现在归这个模块，
// 因为它做的是「把一切**装好**」，不是「改某一项配置」——后者是 tui 与 config 那批
// 命令的事。
type configCommand struct {
	agents  agentapi.AgentCatalog
	control runtimeapi.Control
}

var (
	_ cliapi.Command    = configCommand{}
	_ cliapi.Documented = configCommand{}
	_ cliapi.Unstyled   = configCommand{}
)

func (configCommand) Names() []string { return []string{"config", "setup"} }

// Unstyled：全屏向导，不是 newgate 的版式（见 cliapi.Unstyled）。
func (configCommand) Unstyled([]string) bool { return true }

func (configCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionMaintenance,
		Rank:    5, // 维护那一节的最前面：新装一台机器时第一件要做的事
		Usage:   "config [--reconfigure] [--no-start]",
		Summary: i18n.T("first-run wizard: install and take over clients, write the first upstream, start the proxy", nil),
	}
}

func (c configCommand) Run(host cliapi.Host, args []string) int {
	if c.control == nil {
		// 骨架装配（关掉了 runtime）里没有接管与生命周期。仍然把上游写出来是
		// 有价值的，但 `newgate config` 的承诺是「一把梭」——做不到就明说。
		return host.Die(69, i18n.T("this build has no runtime module, so it cannot take clients over; use newgate init and newgate on instead", nil))
	}
	opt := Options{
		Reconfigure: cliapi.Flag(args, "--reconfigure") || cliapi.Flag(args, "--reset"),
		Yes:         cliapi.Flag(args, "--yes") || cliapi.Flag(args, "-y"),
		NoStart:     cliapi.Flag(args, "--no-start"),
	}
	return Run(Deps{Agents: c.agents, Control: c.control, Out: stdout}, opt)
}
