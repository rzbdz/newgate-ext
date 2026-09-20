// Package webdashboard 是 web 界面 + 它的 BFF。
//
// 它挂在 gateway 那个端口上（默认 8899）的 /ui 前缀下——**共用一个端口**是部署
// 上的硬要求（防火墙、反代、ACL 都按端口配），而这件事的机制在内核的 porthub：
// 这里只负责「把我的 handler 挂上去」，分派是 porthub 的事。
//
// # 它不认识任何模块
//
// BFF 读的是一本**概念账本**（core/lib/view）：各模块自己 Optional 依赖这个能力，
// 在自己的 Start 里注册「我这一面长什么样、怎么改」。所以加一个模块的界面不需要
// 改这个包，也不需要改前端——这跟 cli 与它的注入点（modules/cli/extension）是
// 同一条规矩的两份实现。
//
// 提供出去的 Service 只有 Register：查询那半边留给界面自己（它要渲染）。
package webdashboard

import (
	"context"
	"embed"
	"io/fs"
	"net/http"

	modules "github.com/rzbdz/newgate/component"
	servingapi "github.com/rzbdz/newgate/lib/serving"
	viewapi "github.com/rzbdz/newgate/lib/view"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	porthubapi "github.com/rzbdz/newgate/modules/porthub"
)

// assets 是前端构建产物。**产物进版本控制**（与 manifest/modules_gen.go 同一套
// 做法）：这样 `go build` 与 CI 仍然完全离线，前端工具链只在开发机上出现。
//
//go:embed all:web/dist
var assets embed.FS

const (
	// Prefix 是它在共享端口上的落脚点。改这里要同时改前端的 base 路径。
	Prefix = "/ui"
	// AssetDir 是构建产物目录。**只有一处真相**：embed 读它、前端构建写它、
	// 以后那条「产物是否过期」的检查也读它（同 modules/i18n 的 CatalogDir）。
	AssetDir = "web/dist"
)

// New 声明 web 界面组件。
//
// 依赖全是 Optional：
//   - porthub 在 → 挂到共享端口（这是常规形态）；
//   - porthub 不在、serving 在 → 退路：自己监听一个端口（见 standalone.go）。
//     它**只在服务进程里**起来——登记到 serving 那本账上，等拥有端口的那位说
//     「我要开始服务了」。为什么不在这里自己判断：模块的 Start 每一条
//     `newgate …` 命令都会跑（modules/i18n 的教训），自己起监听等于敲一次
//     `newgate status` 就开一台服务器；而模块读 os.Args 判断「我是不是服务进程」
//     是内核明文禁止的。
//   - cliapi 在 → 挂 `newgate web`（告诉用户界面上哪儿找）。
func New() modules.Component {
	views := viewapi.NewRegistry()
	// self 是这次装配的事实：挂上了没有、挂上之后怎么撤。命令要用它如实回答
	// 「界面上哪儿找」——装了本模块**不等于**它有入口（porthub 缺席时就没有），
	// 让命令按实际挂载结果说话，而不是按「我装了没有」猜。
	self := &instance{views: views}
	return modules.Component{
		Name: "web-dashboard",
		Type: "cli", // 「界面壳」这一类的既有取值（tui / simple-cli 也是它）
		Requires: []modules.Requirement{
			// 共享端口（常态）。
			modules.Optional(porthubapi.Capability),
			// 服务期回调（退路：没装 porthub 时自己监听一个端口）。它**只有这一
			// 处用**——判据不是「我是谁」，而是「有人开始服务了吗」。
			modules.Optional(servingapi.Capability),
			modules.Optional(cliapi.Capability),
		},
		// 这本账就是**它提供出去的东西**：别的模块 Optional 依赖它，在自己的
		// Start 里往里注册概念。所以它必须在 Bind 期就存在（New 里建），而不是
		// Start 里——后者的话，比它先 Start 的模块就注册不进来了。
		Provides: []modules.Provision{modules.Provide(viewapi.Capability, viewapi.Service(views))},
		Start: func(_ context.Context, ctx modules.Context) error {
			sub, err := fs.Sub(assets, AssetDir)
			if err != nil {
				return err
			}
			handler := NewHandler(sub, views)
			// 两条路，只能走一条，而且都要先问「这条路存不存在」：
			//
			//   - **porthub 在**：挂到共享端口上（常态）。挂载是进程内注册（不产生
			//     socket、不产生 goroutine），所以即便在 CLI 进程里跑也没有副作用
			//     ——那个进程根本没有 HTTP 服务在读这张表。
			//   - **porthub 不在、serving 在**：退路，自己监听一个端口。但**只在
			//     服务进程里**（见 serving 包）：登记回调，等拥有端口的那位说
			//     「我要开始服务了」。
			//   - 两个都不在：这次装配里界面没有入口（`newgate web` 会说清楚）。
			hub, hasHub := modules.Get(ctx, porthubapi.Capability)
			listeners, hasServing := modules.Get(ctx, servingapi.Capability)
			switch {
			case hasHub:
				// 挂在 Prefix 上（不是 Prefix+"/"）：porthub 的 matches 认「正好等于
				// 前缀」与「前缀 + /」两种，都由这一条挂载接住（见 lib/porthub）。
				// 于是 `/ui` 落进来时，StripPrefix 交出来的是**空路径**。
				//
				// 空路径本身能画（static 把它当 index.html），但**浏览器解析相对地址
				// 时会拿当前文档的路径去截**：从 `/ui` 出发，`api/snapshot` 成了
				// `/api/snapshot`——那个路径没人认领，落进数据面的 catch-all，被当成
				// 一次形状不认识的转发请求（400，还算进了数据面流量）。所以空路径先
				// 重定向到带斜杠的那一份。
				//
				// 302 而不是 301：永久重定向会被浏览器近乎永久地缓存，将来 Prefix 一
				// 改，见过 301 的机器就再也纠正不回来了（standalone.go 那边同理）。
				stem := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "" {
						http.Redirect(w, r, Prefix+"/", http.StatusFound)
						return
					}
					handler.ServeHTTP(w, r)
				})
				rel, err := hub.Mount(Prefix, "web-dashboard",
					http.StripPrefix(Prefix, stem))
				if err != nil {
					return err
				}
				// Mount 的放销是 func()，OnServe 的是 component.Release
				// （func() error）——归一成后者，停机路径只认一种形状。
				self.mode = modeShared
				self.release = func() error { rel(); return nil }
			case hasServing:
				rel, err := listeners.OnServe("web-dashboard", func() (func(), error) {
					return serveStandalone(handler)
				})
				if err != nil {
					return err
				}
				self.mode, self.release = modeStandalone, rel
			default:
				self.mode = modeNone
			}
			// 命令是**弱依赖**：没装任何界面时本模块的功能一个都不少（概念照常
			// 注册，模块照常工作），只是没人能敲 `newgate web`。
			if ui, ok := modules.Get(ctx, cliapi.Capability); ok {
				rel, err := ui.RegisterCommand(&webCommand{self: self})
				if err != nil {
					return err
				}
				self.releases = append(self.releases, rel)
			}
			return nil
		},
		Stop: func(context.Context) error {
			for _, rel := range self.releases {
				_ = rel()
			}
			self.releases = nil
			if self.release != nil {
				_ = self.release()
				self.release = nil
			}
			self.mode = modeNone
			return nil
		},
	}
}

// instance 是这次装配里本模块的事实，Start 与命令共享它。
//
// 为什么命令不能自己判断「porthub 在不在」：那是**装配期**的事实，而命令跑在
// 「这次调用」里——两者在同一个进程里（每条命令都会装配一遍），但让命令去问
// porthub 等于让它重复一遍装配期的判断，还会在「装配期挂失败、命令期看着像在」
// 时给出错误答案。
type instance struct {
	views    *viewapi.Registry
	mode     string
	release  func() error
	releases []func() error
}

// 这次装配里界面**入口**的三种形态。命令要按它说话：装了本模块不等于它有入口。
const (
	// modeShared：挂在共享端口上（porthub 在）。
	modeShared = "shared"
	// modeStandalone：自己监听一个端口（porthub 不在、serving 在），且只在服务
	// 进程里真的起来。
	modeStandalone = "standalone"
	// modeNone：这次装配里没有任何入口（两个都没有）。
	modeNone = "none"
)
