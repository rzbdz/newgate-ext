// Package webdashboard 是 web 界面 + 它的 BFF。
//
// 它挂在 gateway 那个端口上（默认 8899）的 /ui 前缀下——**共用一个端口**是部署
// 上的硬要求（防火墙、反代、ACL 都按端口配），而这件事的机制在内核的 porthub：
// 这里只负责「把我的 handler 挂上去」，分派是 gateway 的事。
//
// BFF 不认识任何具体模块：它读的是共享叶子（config/store、gateway/metrics、
// gateway/controlplane…），要展示模块自己的东西时，模块通过 view capability 把
// **结构化的概念**贡献进来（见 bff.go 的说明）。所以加一个模块的界面不需要改这
// 个包，也不需要改前端——前端只认 concept 的形状。
package webdashboard

import (
	"context"
	"embed"
	"io/fs"
	"net/http"

	modules "github.com/rzbdz/newgate/component"
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
//   - porthub 不在 → **什么也不做**。CLI 进程里没有 HTTP 服务，而模块的 Start 在
//     每一条 `newgate …` 命令里都会跑（modules/i18n 的教训），所以这里绝不自己
//     起监听。自起端口的 fallback 只该发生在「正在服务的那个进程」里，判据要用
//     入口账本认（见本包 README 的待办），v1 先不做。
//   - cliapi 在 → 以后挂 `newgate web`（打印 URL / 打开浏览器）；现在还没做。
func New() modules.Component {
	var release func()
	return modules.Component{
		Name:     "web-dashboard",
		Type:     "cli", // 「界面壳」这一类的既有取值（tui / simple-cli 也是它）
		Requires: []modules.Requirement{modules.Optional(porthubapi.Capability), modules.Optional(cliapi.Capability)},
		Provides: nil,
		Start: func(_ context.Context, ctx modules.Context) error {
			sub, err := fs.Sub(assets, AssetDir)
			if err != nil {
				return err
			}
			// 挂载是进程内注册（不产生 socket、不产生 goroutine），所以即便在
			// CLI 进程里跑也没有副作用：那个进程根本没有 HTTP 服务在读这张表。
			hub, ok := modules.Get(ctx, porthubapi.Capability)
			if !ok {
				return nil
			}
			rel, err := hub.Mount(Prefix, "web-dashboard", http.StripPrefix(Prefix, NewHandler(sub)))
			if err != nil {
				return err
			}
			release = rel
			return nil
		},
		Stop: func(context.Context) error {
			if release != nil {
				release()
				release = nil
			}
			return nil
		},
	}
}
