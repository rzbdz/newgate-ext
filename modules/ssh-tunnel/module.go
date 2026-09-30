package sshtunnel

import (
	"context"
	"net/http"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	servingapi "github.com/rzbdz/newgate/lib/serving"
	viewapi "github.com/rzbdz/newgate/lib/view"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	porthubapi "github.com/rzbdz/newgate/modules/porthub"
)

// Name 是这个模块在装配表、概念账本与 `newgate plugin` 里的名字（= 目录名）。
//
// 它同时是 state.json 里 ModuleConfig 那一格的键（见 StateKey）。
const Name = "ssh-tunnel"

// New 声明这个组件。
//
// 依赖**全是 Optional**：没有界面、没有共享端口、没有命令行时它仍然成立——它的
// 功能是「一条穿过 SSH 的通道」，而通道本身不需要谁看着。这与内核那条「业务模块
// 不依赖任何 ui」是同一条规矩：谁来操作它，谁就装上；不装，它照常待着。
//
// 它也**不声明 config 依赖**：这个模块读的是 state.json 里属于自己的那一格，
// 而那是 `store.LoadState()` 直接读盘的事，不是 config 那个组件的语义（与
// web-dashboard 读皮肤选择同一条路）。
func New() modules.Component {
	p := newPool(newSSHDialer())
	m := newManager(p, Load)
	// 同一个 proxy 实例既挂到共享端口上，也交给 manager（停机时要关它的空闲连接，
	// 见 manager.shutdown）。建在这里而不是 Start 里：Start 每次调用都会跑，
	// 而连接池应当是**每个进程一份**的东西。
	px := newProxy(p, Load)
	m.proxy = px

	var (
		mount    func()
		releases []modules.Release
	)
	return modules.Component{
		Name: Name,
		Desc: func() string {
			return i18n.T("reaches a remote machine's newgate interface over SSH", nil)
		},
		// 「基础设施」这一类：它不是界面、不是客户端、不是模型，也不经手任何一次
		// 转发请求。它是一条**通道**——与 porthub / i18n 同一档。
		Type: "infra",
		Requires: []modules.Requirement{
			// 共享端口上那条 route（常态）。不在时模块照常工作：本地转发端口走
			// serving 那条路，两条路各自独立。
			modules.Optional(porthubapi.Capability),
			// 本地转发端口 + 那个后台循环。**只有守护进程会调它**（见 manager 的
			// 说明），所以短命的 CLI 进程不会因为装了这个模块而多开一堆东西。
			modules.Optional(servingapi.Capability),
			modules.Optional(cliapi.Capability),
			modules.Optional(viewapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			// 挂载是进程内注册（不起 socket、不起 goroutine），所以 CLI 进程里挂
			// 一次无害——那个进程没有 HTTP 服务在读这张表。与 web-dashboard 同一
			// 条理由、同一条形状：porthub **不剥前缀**，我们自己在 handler 外面剥。
			if hub, ok := modules.Get(ctx, porthubapi.Capability); ok {
				rel, err := hub.Mount(Prefix, Name, http.StripPrefix(Prefix, px))
				if err != nil {
					return err
				}
				mount = rel
			}
			// 转发端口与后台循环：登记在 serving 上，等真正拥有端口的那位说
			// 「我要开始服务了」。**不能**在 Start 里直接起——那条路的尽头是
			// 「敲一次 `newgate status` 就开一台服务器」。
			if listeners, ok := modules.Get(ctx, servingapi.Capability); ok {
				rel, err := listeners.OnServe(Name, m.Serve)
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}
			if ui, ok := modules.Get(ctx, cliapi.Capability); ok {
				rel, err := ui.RegisterCommand(&tunnelCommand{m: m, pool: p})
				if err != nil {
					return err
				}
				releases = append(releases, rel)
				// 状态里一行、体检里每条 target 一项。两者都只读内存，不拨号。
				if rel, err := ui.RegisterStatus(tunnelStatus{m}); err != nil {
					return err
				} else {
					releases = append(releases, rel)
				}
				if rel, err := ui.RegisterDiagnostics(tunnelDiagnostics{m}); err != nil {
					return err
				} else {
					releases = append(releases, rel)
				}
			}
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				rel, err := registerView(v, m)
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}
			return nil
		},
		Stop: func(context.Context) error {
			err := modules.ReleaseAll(releases)
			releases = nil
			if mount != nil {
				mount()
				mount = nil
			}
			return err
		},
	}
}
