// Package serving 是「**这个进程开始服务了**」这条边：想在服务进程里做点什么的
// 模块，把自己挂在这里。
//
// # 为什么需要它（而不是在 Start 里直接做）
//
// 每一个 `newgate …` 进程都会跑一遍模块的 Start——敲一条命令、进一个 shell、跑一次
// 体检，都是。所以「起一个监听」这种**只有服务进程才该做**的事，在 Start 里做等于
// 把每一条 CLI 调用都变成一台服务器（端口冲突、僵尸进程、`newgate status` 顺手开了
// 一个 web 界面）。modules/i18n 的教训是这条规矩的来源，而 web 界面是它的第二个
// 受害者：它要挂的那条路（porthub）恰好可能没装，那时它得自己监听一个端口。
//
// # 谁通知，谁听
//
//   - **通知方是拥有端口的那个模块**（今天：gateway 的 Serve，在进入 accept 之前）。
//     它不必知道有谁在听——这正是这个方法存在的理由：端口的所有者说一句「我开始了」，
//     不需要认识任何想搭车的人。
//   - **听方是想在服务进程里干活的模块**。它们在 Start 里登记，回调只在通知那一刻跑。
//
// # 为什么不是入口账本
//
// 「这次调用是不是 `__serve`」看起来该问 entry，但 entry 只在**装配之后**被组合根
// 问一次，而 Start 在装配**之中**——顺序不允许。更要紧的是判据：模块自己去读
// os.Args 判断这件事是被内核明文禁止的（见 component/entry 的说明）。所以这里换一
// 个问法：不问「我是谁」，只等「有人开始服务了」。那个事实只有端口的主人说得出来。
package serving

import (
	"errors"
	"sync"

	"github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Starter 是「服务开始了，请你开始工作」。
//
// 返回的 stop 在服务结束时调用（通常是 Serve 返回时）。**必须幂等**：进程退出路径
// 与正常收尾可能都会走到它。
type Starter func() (stop func(), err error)

// Service 是听方看到的、也是通知方看到的那一面。
type Service interface {
	// OnServe 登记一个回调。它在**服务进程**里、Notify 的那一刻跑一次。
	OnServe(name string, start Starter) (component.Release, error)

	// Notify 由拥有端口的模块调用：跑一遍所有登记的回调，返回一个停机函数。
	//
	// 某个回调失败**不会**让整次服务失败（fail-open）：一个界面起不来不该拖垮
	// 数据面。失败的会出现在返回的 error 里，由调用方写进日志。
	Notify() (stop func(), err error)
}

// Capability 是这条边的身份。
var Capability = component.NewCapability[Service]("serving")

// Registry 是回调账本。
type Registry struct {
	mu       sync.Mutex
	starters []entry
	notified bool
}

type entry struct {
	name  string
	start Starter
	seq   int
}

func NewRegistry() *Registry { return &Registry{} }

func (r *Registry) OnServe(name string, start Starter) (component.Release, error) {
	if name == "" {
		return nil, i18n.E("serving: a listener registered with no name", nil)
	}
	if start == nil {
		return nil, i18n.E("serving: {name} registered a nil starter", i18n.A{"name": name})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := entry{name: name, start: start, seq: len(r.starters)}
	r.starters = append(r.starters, e)
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		for i, cur := range r.starters {
			if cur.seq == e.seq {
				r.starters = append(r.starters[:i:i], r.starters[i+1:]...)
				return nil
			}
		}
		return nil
	}, nil
}

// Notify 跑一遍登记的回调。**幂等**：同一个进程里被叫两次时，第二次什么都不做
// （已经起来的监听不该被再起一遍）。
func (r *Registry) Notify() (func(), error) {
	r.mu.Lock()
	if r.notified {
		r.mu.Unlock()
		return func() {}, nil
	}
	r.notified = true
	starters := append([]entry(nil), r.starters...)
	r.mu.Unlock()

	var stops []func()
	var failures []error
	for _, e := range starters {
		stop, err := e.start()
		if err != nil {
			// 一个界面起不来不该拖垮数据面。名字带上，日志里才认得出是谁。
			failures = append(failures, i18n.Ef(err, "serving: {name} could not start: {err}",
				i18n.A{"name": e.name, "err": err}))
			continue
		}
		if stop != nil {
			stops = append(stops, stop)
		}
	}
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			for i := len(stops) - 1; i >= 0; i-- {
				stops[i]()
			}
		})
	}
	return stop, errors.Join(failures...)
}
