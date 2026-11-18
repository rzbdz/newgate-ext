// Package porthub 把「一个端口上挂多个服务」做成一个组件。
//
// 它存在的意义只有一个：让消费者能问出「**这个进程里有没有共享端口这回事**」
// ——有就挂上去（共用 gateway 那个端口），没有就自己监听一个端口（fallback）。
// 表本身住在 lib/porthub（叶子机制）。
//
// 端口归这张表：守护进程入口（modules/gateway/serve.go）把数据面的 handler 交给
// `Root`，拿到根 handler 交给 http.Server。所以「关掉 porthub」不是任何一处的
// 特判——是数据面自己服务端口（今天的样子）vs 交出去让表来分派。
package porthub

import (
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"net/http"

	hub "github.com/rzbdz/newgate-ext/lib/porthub"
	modules "github.com/rzbdz/newgate/component"
)

// Service 是消费者看到的那一面：往表里挂一条（web 界面这类），或者交出这个端口
// 的根 handler（数据面）。
//
// 查询那半边不给出去——「谁接住了这个路径」只有分派处需要知道，多一个人问就多
// 一个「谁排谁前面」的问题。
type Service interface {
	Mount(prefix, owner string, h http.Handler) (func(), error)

	// Root 见 lib/porthub.Registry.Root：登记兜底服务（数据面），换回这个端口
	// 该用的根 handler。
	Root(owner string, fallback http.Handler) (http.Handler, error)
}

// Capability 是「这张挂载表在这个进程里」这件事本身。
var Capability = modules.NewCapability[Service]("porthub")

// New 声明 porthub 组件。
//
// 没有 Requires、不起监听、不打日志、也没有 Stop：Start 在**每一条** `newgate …`
// 命令里都会跑（modules/i18n 在 Start 里打一行日志，结果每条命令都刷屏——那条
// 教训记在它的注释里），所以这里只做「把自己提供出去」这一件事。挂载是进程内
// 注册，不产生 socket、不产生 goroutine，因此在 CLI 进程里被调用也是安全的。
func New() modules.Component {
	return modules.Component{
		Name: "porthub",
		Desc: func() string {
			return i18n.T("several services on one port, or a port of its own when there is no shared one", nil)
		},
		Type:     "infra",
		Provides: []modules.Provision{modules.Provide(Capability, Service(hub.Default()))},
	}
}
