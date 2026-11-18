package gateway

import (
	"fmt"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate/lib/i18n"
)

// fallbackCommand 是 `newgate fallback on|off`。
//
// **它住在这里而不是 modules/cli**：fallback 链是网关数据面的一条决策
// （forward 的 steps 循环），「换不换人」是 gateway 的语义，命令也该由
// gateway 提供。理由与 schemaRepairCommand 同一条。
//
// 为什么值得有这个命令（2026-09-28）：链上换人会**掩盖上游的故障**——
// 某个 provider 挂了，请求照常 200，只是走了别人，界面显示的模型名与实际
// 跑的对不上。要判断「到底是 A 家的问题还是我们的问题」必须能一键把所有
// fallback 关掉，让请求**直接撞在上游上**。这是排查手段，不是省 token 的
// 开关（它也会同时关掉超时预算的等待，见 forward.handleProxy 那段短路）。
//
// 与 `--profile=xx` 的关系：那条路天然只走一个 profile，**不需要**这个
// 开关（URL 路径已经把 profile 钉死了，见 forward.Target.Profile）。这个
// 开关管的是其余所有路径——所以默认是**开**（不关），出厂关掉等于把网关
// 的主要能力阉了。
type fallbackCommand struct{}

var (
	_ cliapi.Command    = (*fallbackCommand)(nil)
	_ cliapi.Documented = (*fallbackCommand)(nil)
)

func (fallbackCommand) Names() []string { return []string{"fallback", "fallbacks"} }

func (fallbackCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionMaintenance, Rank: rankMaintenance,
		Usage:   "fallback on|off",
		Summary: i18n.T("Try the next candidate in the chain when one fails", nil),
	}
}

func (fallbackCommand) Run(host cliapi.Host, args []string) int {
	arg := cliapi.Positional(args, 0)
	// 必须显式给 on|off，理由与 schema-repair 字字相同：`truthy("")` 是 false，
	// 不带参数当成 off 的话，「敲一下看看」会把全机的 fallback 静默关掉——
	// 而那是一个**只在出故障时才会被发现**的改动：平时一切正常，等某个
	// provider 挂了才发现请求不再换人。
	if arg == "" {
		return host.Die(64, i18n.T("Usage: newgate fallback on|off (no argument changes anything)", nil))
	}
	on := truthy(arg)
	if err := gatewaystate.SetFallback(on); err != nil {
		return host.Die(70, err.Error())
	}
	if on {
		fmt.Println(style.Item(style.OK, "fallback on") +
			style.Dim(i18n.T("   A failing candidate hands the request to the next one in the chain", nil)))
	} else {
		fmt.Println(style.Item(style.Skip, "fallback off") +
			style.Dim(i18n.T("   Every request stops at the head of the chain, timeouts included", nil)))
	}
	// 改的是转发路径读的那份共享快照，得让 watcher 立刻重载——否则要等下一次
	// 配置变更才生效，而用户以为已经生效了。
	host.NotifyProxy()
	return 0
}
