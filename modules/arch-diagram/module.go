// Package archdiagram 把架构图挂到网关那个端口上（`/arch`），给开发时看。
//
// 它服务的就是 `tools/archmap` 生成的那张图——同一个 `Build`、同一个渲染器，
// 只是从「跑一次测试写进 dist/」变成「浏览器里点一下就有一张当下的」。
//
// # 为什么是一个模块，而不是测试顺手起个 http server
//
// 因为它要的正是 porthub 提供的那件事：**共享端口上的一个路径**。做成模块之后，
// 它跟别的服务一样被装配、被摘除、被 `newgate plugin` 列出来；不需要的时候
// 不装这份规格书（见 dist-dev.json），它就从产品里整个消失——而不是在某处留一段
// 「开发时请手动跑这个脚本」。
//
// # 它是**开发期**的东西，两条后果写在这里
//
//  1. **import 那一半要源码树**：`go list` 得在有 go.mod 的树上跑。跑不动时不报错，
//     装配那一半照画，import 那半张图写成一句解释（见 archmap.Unavailable）——
//     部署出去的二进制里没有源码，那时它只有装配图，那是有用的。
//  2. **它认得发行版自己的 manifest**：规格书表（`manifest.Specs()`）编在二进制里，
//     所以「这个构建装了什么」在任何机器上都答得出来。这也是它必须住在发行版仓库
//     的原因——内核不知道有哪些规格书。
package archdiagram

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	modules "github.com/rzbdz/newgate/component"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	porthubapi "github.com/rzbdz/newgate/modules/porthub"
)

// Prefix 是它在共享端口上的落脚点。
//
// 不用 `/a`（那是数据面前缀，porthub 会当场拒掉），也不用 `__newgate` 那一族
// （控制面命名空间）。`/arch` 是一块空地。
const Prefix = "/arch"

// SrcEnv 指向源码树的根：import 那一半在那里跑 `go list`。
//
// 不给就猜当前工作目录（开发机上多半就是在仓库里敲的命令）。两处都取不到，
// 或者取到的树不成立，就只画装配图——**不报错**：图少一半比一个 500 有用。
const SrcEnv = "NEWGATE_ARCH_SRC"

// service 是这次装配的事实：源码树在哪儿，以及装配完成后内核递来的组件名单。
type service struct {
	root string

	mu    sync.Mutex
	comps []modules.Component
}

// SetCatalog 由内核在**所有 Start 之后**递一次（component.CatalogAware）。
//
// 在线这张图只能这么拿名单：它要回答的是「这个进程此刻装着谁」，而那份名单内核
// 本来就拥有。**不是**去读发行版的规格书表——那张表长在模块们的上面（生成的清单
// import 了每个模块），模块抬头看它就是编译环，dist-dev.json 那一次构建实测撞上过：
//
//	manifest → arch-diagram → tools/archmap → manifest
//
// 少了一张「还有哪些装配编得出来」的下拉，换来的是零内核改动、以及一张更贴近
// 正在发生的事情的图（离线那份 `go test ./tools/archmap` 仍然画全部规格书）。
func (s *service) SetCatalog(comps []modules.Component) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.comps = comps
}

func (s *service) Components() []modules.Component {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.comps
}

// 编译期断言：内核按这个接口找人（component.CatalogAware）。
var _ modules.CatalogAware = (*service)(nil)

// Service 是本模块**露出去**的那一面。
//
// 它没有别的消费者，存在的唯一理由是内核交付组件名单的通道只认「已经 Provide
// 出去的值」（见 component.deliverCatalog 的注释）——不露出去，SetCatalog 永远
// 不会被调用，图上就是零个节点（实测踩过一次）。这不是仪式：那条规矩换来的是
// 内核不认识任何模块，代价就是「想被递东西，先把自己交出去」。
type Service interface {
	// Components 是装配完成后内核递来的名单（可能为空：还没递到）。
	Components() []modules.Component
}

// Capability 是这一面的身份。
var Capability = modules.NewCapability[Service]("arch-diagram")

func New() modules.Component {
	svc := &service{root: sourceRoot()}
	var release func()
	return modules.Component{
		Name: "arch-diagram",
		// 「其它」：它不是内核、网关、界面、客户端、模型，也不是它们之间的桥。
		// 一个开发期的观测工具，硬塞进上面任何一类都会让 `newgate plugin` 的分组说谎。
		Type: "others",
		Requires: []modules.Requirement{
			modules.Optional(porthubapi.Capability),
			modules.Optional(cliapi.Capability),
		},
		// 露出去，只为了能被内核递到那份组件名单（见 Service 的说明）。
		Provides: []modules.Provision{modules.Provide(Capability, Service(svc))},
		Start: func(_ context.Context, ctx modules.Context) error {
			// 挂载是进程内注册（不起 socket），所以 CLI 进程里挂一次无害——那个
			// 进程没有 HTTP 服务在读这张表（与 web-dashboard 同一条）。
			if hub, ok := modules.Get(ctx, porthubapi.Capability); ok {
				rel, err := hub.Mount(Prefix, "arch-diagram",
					http.StripPrefix(Prefix, NewHandler(svc)))
				if err != nil {
					return err
				}
				release = rel
			}
			if ui, ok := modules.Get(ctx, cliapi.Capability); ok {
				if _, err := ui.RegisterCommand(archCommand{svc: svc}); err != nil {
					return err
				}
			}
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

// sourceRoot 是这次要去哪儿扫 import。
//
// 判据是**那份树成不成立**（有 go.mod 与 dist.json），不是「环境变量给没给」：
// 给错了目录时，`go list` 会在一棵不相干的树上跑出一些看起来正常的包名，而图上
// 于是出现一堆没人认识的节点。宁可说「扫不了」。
func sourceRoot() string {
	candidates := []string{}
	if v := os.Getenv(SrcEnv); v != "" {
		candidates = append(candidates, v)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}
	for _, dir := range candidates {
		if looksLikeSourceTree(dir) {
			return dir
		}
	}
	return ""
}

func looksLikeSourceTree(dir string) bool {
	if dir == "" {
		return false
	}
	for _, name := range []string{"go.mod", "dist.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}
