// Package serving 把「这个进程开始服务了」这条边做成一个**普通模块**：它提供的那
// 本账，一边由拥有端口的模块（gateway）通知，另一边由想在服务进程里干活的模块
// （web 界面自己监听一个端口）登记。
//
// 为什么是一个模块而不是内核里的一行：这条边的两端都不该认识对方。做成模块之后，
// 箭头是「gateway → serving ← web-dashboard」，谁都不 import 谁；把本模块摘掉，
// 两边都退化成「没有服务期回调」——数据面照常服务，界面只是没有自起端口那条路。
// 这与 porthub 的形状一模一样（那个是「共享端口」，这个是「服务期」）。
//
// 它**自己不监听任何东西**：账本而已。真正的监听由登记的模块在回调里做——端口是
// 谁的、绑哪个号，只有那个模块知道。
package serving

import (
	"context"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	"github.com/rzbdz/newgate-ext/lib/serving"
	modules "github.com/rzbdz/newgate/component"
)

type service struct{ *serving.Registry }

func New() modules.Component {
	registry := serving.NewRegistry()
	return modules.Component{
		Name: "serving",
		Desc: func() string { return i18n.T("the edge that says this process is serving", nil) },
		Type: "infra",
		// 一条出边都没有：通知方与听方都只是可选地用它。这也是它能被摘掉的前提
		// （见 app/matrix_test.go 的摘除矩阵）。
		Provides: []modules.Provision{modules.Provide(serving.Capability, serving.Service(service{registry}))},
		Start:    func(context.Context, modules.Context) error { return nil },
		Stop:     func(context.Context) error { return nil },
	}
}
