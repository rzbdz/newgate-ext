// Package gateway 把“选路并转发一次模型请求”实现为独立数据面。
//
// 请求先根据 profile 解析候选链，再依次尝试 binding。上游差异不能散落在这条
// 热路径里，所以 gateway 只公开 Plugin 注册端口：模型、客户端和交叉组件各自
// 注册只对自己成立的修补，网关统一负责排序、执行、记录 notes 和 fail-open。
//
// 组件层只管理插件注册表的所有权。HTTP server、重试、健康状态、协议拼接和
// 字节改写仍由本模块内部 package 负责；通用 component 内核不理解请求概念。
package gateway

import (
	"context"
	"strings"
	"time"

	servingapi "github.com/rzbdz/newgate-ext/lib/serving"
	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	configapi "github.com/rzbdz/newgate-ext/modules/config"
	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	porthubapi "github.com/rzbdz/newgate-ext/modules/porthub"
	modules "github.com/rzbdz/newgate/component"
	entryapi "github.com/rzbdz/newgate/component/entry"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate-ext/modules/gateway/policy"
	"github.com/rzbdz/newgate-ext/modules/gateway/probe"
	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
)

type port struct {
	registry *special.Registry
	// filters 是数据面策略的账本。它**不装在 port 自己身上给外面看**——
	// Gateway 接口只暴露 RegisterFilter，读侧只有 forward.Server（Serve 期）
	// 和 handleStatus 用到。
	filters *policy.Registry
}

var _ Gateway = (*port)(nil)

// New 声明网关控制面组件。对 Config 的 Need 既表达真实依赖，
// 也确保所有配置语义先就绪，再允许插件进入请求路径。
//
// 它对 ui 只声明 **Optional**（弱依赖，见 component.Optional）：
// `newgate st` / `metrics` / `probe` 是这一层的用户界面，插件名与开关语义都是本
// 模块的知识，所以命令由本模块自己注册（见 command_special.go 的说明）。用
// Optional 而不是 Need 是必须的——网关不该因为「没装界面」起不来；而点击进界面
// 的那条边是**排序边**，保证本模块的 Start 跑的时候界面已经就绪。
func New() modules.Component {
	port := &port{registry: special.NewRegistry(), filters: policy.New()}
	var restore func()
	var restoreFilters func()
	var releases []modules.Release
	return modules.Component{
		Name: "gateway",
		Desc: func() string { return i18n.T("forwarding, upstream quirk patches, and reasoning pass-through", nil) },
		Type: "gateway",
		Requires: []modules.Requirement{
			modules.Need(configapi.Capability),
			// state.json 里属于本模块的那一段由 confighook 统一登记（见下面 Start
			// 里的 RegisterStateField）。这条**以前没有声明**，而 Start 里是
			// MustGet——2026-09-21 由 tools/archmap 那个「import 了却没有声明边」
			// 的探针挖出来：摘掉 confighook 时构图期因为**别人**（locale /
			// pluginmanager）先失败，所以这个 panic 一直轮不到，测试全绿。
			// 一旦装配里没有 confighook 而本模块在，就是 Start 期直接 panic。
			modules.Need(confighookapi.ConfigHooksCapability),
			// ui 是**弱依赖**（见 component.Optional）：`newgate st` / `metrics` /
			// `probe` 是本层的用户界面，装着界面就挂上去，没装就跳过——网关本身
			// 照常工作，只是没有入口。界面不依赖本模块（它没有任何出边），所以这
			// 条边不可能成环。
			modules.Optional(cliapi.Capability),
			// porthub 也是弱依赖，而且**只有守护进程入口用它**：`__serve` 起的
			// 那个进程把「这个端口上谁应答」交给它（见 serve.go 的 rootHandler）。
			// 数据面（forward 整个目录）一个字都不认识它——那条由
			// direction_test.go 钉住，跟「数据面不认识任何策略」同一把尺子。
			modules.Optional(porthubapi.Capability),
			// serving 同款：只有守护进程入口用它——「我要开始服务了」这句话由这个
			// 模块说出口，想在服务进程里起来的模块（没装 porthub 时自起端口的
			// 界面）在那上面登记。谁也不认识谁，只认识这本账。
			modules.Optional(servingapi.Capability),
			// web 界面同 ui：弱依赖。装了就报自己那面（计数器），没装就跳过。
			modules.Optional(viewapi.Capability),
			// 入口账本：**守护进程本体申报在它上面**（`newgate __serve`，见 serve.go）。
			// 与 wrapper 同一条理由用 Need 而不是 Optional：摘掉账本，这个进程就
			// 没人起得来了，而那种故障要到第一次 `newgate start` 才显形。声明它让
			// 构图期当场失败并点名端口（见 app/matrix_test.go 的摘除矩阵）。
			modules.Need(entryapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Gateway(port)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			// state.json 里的 "gateway" 段归本模块（补丁开关、debug 到期时间）。
			// 登记它才有人拦「两个模块抢同一个字段」——以前这张表只覆盖了三处
			// 写入里的一半，gateway 与 runtime 的键被占用时不会当场报错。
			hooks := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			fieldRelease, err := hooks.RegisterStateField("gateway", gatewaystate.Key)
			if err != nil {
				return err
			}
			releases = append(releases, fieldRelease)

			restore = special.InstallDefault(port.registry)

			// 数据面的策略账本也装成默认：起数据面的地方有两处（守护进程主循环
			// 走包内字段，testing/system 走这个入口），不装的话后者会自己 New()
			// 一本空账，把「贡献者真的接进数据面了吗」这类回归变成空转——那条
			// 形状判据的测试 2026-09-17 就是这么空转掉的。
			restoreFilters = policy.InstallDefault(port.filters)

			// ui 是**弱依赖**（见 component.Optional）：没装任何 ui 时这些命令就没有
			// 入口，但网关功能照常——本模块不依赖 ui 存在。
			// web 界面那一份（计数器）先注册：它**不依赖 cli**，只装 dashboard
			// 的装配里也要有——下面那段一旦 return，这里就永远不会跑。
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				rel, err := v.Register("gateway",
					viewapi.Title(func() string { return i18n.T("Gateway", nil) }).
						In(func() string { return i18n.T("Data plane", nil) }).
						Of(viewapi.FieldRoutes), gatewayConcepts)
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}

			// 共享端口那张表（装了就取，没装就是 nil → 数据面自己服务端口）。
			// 这里只**取**，怎么用是 serve.go 的事：本模块的其余部分（数据面、
			// 命令、状态行）都不需要知道它存在。
			hub, _ := modules.Get(ctx, porthubapi.Capability)
			// 服务期回调那本账（同上：装了就取，没装就是 nil）。想在「这个进程开
			// 始服务」时才起来的模块登记在它上面——数据面与界面因此都不必认识对方。
			listeners, _ := modules.Get(ctx, servingapi.Capability)

			// 守护进程本体申报为**入口**（下面那段 cli 一旦缺席就 return，所以它
			// 必须在这里）：`disable: ["cli"]` 的装配里没有界面，可守护进程仍然
			// 要有人起——纯 dashboard 的发行版就是这种形状（见 serve.go）。
			//
			// 策略账本原样递进去：**这一层不认识任何一位策略**，谁插进来由各自的
			// Start 决定。
			cli, hasUI := modules.Get(ctx, cliapi.Capability)
			// 没有终端界面时退到**兜底**位：那时 `newgate` 无参数没有别人能回答，
			// 而这个进程唯一说得通的行为就是起服务（见 serve.go 的 headless）。
			//
			// 位置即判据：入口账本按 rank 问，所以「谁更具体谁先答」是自动的。
			// 另一条会兜底的入口（发行版自己的壳，如 hello）要么在 RankPreferred
			// 上、要么与本条互斥（它出现时通常连网关都没装）。
			rank := entryapi.RankShim
			if !hasUI {
				rank = entryapi.DefaultRank
			}
			entries := modules.MustGet(ctx, entryapi.Capability)
			entryRelease, err := entries.Register(
				serveEntry{filters: port.filters, hub: hub, listeners: listeners, headless: !hasUI},
				rank)
			if err != nil {
				return err
			}
			releases = append(releases, entryRelease)

			if !hasUI {
				return nil
			}
			for _, cmd := range []cliapi.Command{
				specialCommand{}, schemaRepairCommand{}, fallbackCommand{}, debugCommand{},
				// 观测面也归数据面自己：计数器怎么分组、探活探出了什么，
				// 都是网关的语义（见 command_metrics.go / command_probe.go）。
				metricsCommand{}, probeCommand{}, logsCommand{},
			} {
				release, err := cli.RegisterCommand(cmd)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			// 补丁开关的状态行、代理那一行、以及两条体检（环境 / 代理）都归
			// 本模块自报（见 status.go / diagnostics.go）。
			for _, register := range []func() (modules.Release, error){
				func() (modules.Release, error) { return cli.RegisterStatus(switchStatus{}) },
				func() (modules.Release, error) { return cli.RegisterStatus(gatewayReporter{}) },
				func() (modules.Release, error) { return cli.RegisterDiagnostics(gatewayReporter{}) },
				func() (modules.Release, error) { return cli.RegisterDump(gatewayReporter{}) },
				func() (modules.Release, error) { return cli.RegisterVerbose(switchStatus{}) },
			} {
				release, err := register()
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			return nil
		},
		Stop: func(context.Context) error {
			err := modules.ReleaseAll(releases)
			if restoreFilters != nil {
				restoreFilters()
			}
			if restore != nil {
				restore()
			}
			return err
		},
	}
}

// Quirks 见 api.go 的说明（数据面用的就是这一个实例）。
func (p *port) Quirks() *quirk.Table { return quirk.Default }

// FallbackOn / SetFallback 见 api.go 的说明。
//
// 这里只做一层转发：开关的值与它的校验是 gatewaystate 的事（它要在**读**的那一侧
// 解析 state.json 的那一段，见那里的 Parse），这一层只把它交给控制面。两份实现
// 会在「网页上点了、命令行敲了」之间分家——而那种分家看得见的样子是两条路各说
// 各话，没人能从界面上分辨谁对。
func (p *port) FallbackOn() bool {
	return !gatewaystate.FallbackOff(store.LoadState())
}

func (p *port) SetFallback(on bool) error { return gatewaystate.SetFallback(on) }

// ProbeBinding 打一发最小请求，然后把结论灌进策略层（见 api.go 的说明）。
//
// 走到策略层那一步复用 `p.filters.ObserveProbes`——**与 `newgate probe` 那条路
// 的终点是同一个函数**（命令行那边走 HTTP 到控制面再进来）。两处各写一遍「记一条
// 探活结论」的话，网页上点出来的结果与终端里敲出来的就会慢慢分家，而那种分家
// 看得见的表现是「网页上说通了、终端说没通」——没人能从界面上分辨谁对。
func (p *port) ProbeBinding(provider, model string) (ProbeOutcome, error) {
	slow := store.LoadState().Timeouts.ClassifierFirstByte()
	var out ProbeOutcome
	_, err := probe.Run(probe.Options{
		OnlyTarget:  &probe.Target{Provider: provider, Model: model},
		Timeout:     slow,
		SlowAfter:   slow,
		Concurrency: 1,
		OnDone: func(_ probe.Target, status int, lat, _ time.Duration, err error) {
			out.Status, out.Latency = status, lat
			out.OK = err == nil && status == 200
			if err != nil {
				out.Err = err.Error()
			}
		},
	})
	if err != nil {
		return ProbeOutcome{}, err
	}
	// 阈值取的是**分类器首字节**那一档：探活发的是 4-token 的极小请求，超过这个
	// 阈值仍未完成就说明它进不了交互 fallback 链——与命令行那条同一条判据
	// （见 policy.ProbeObservation.SlowAfter 的注释）。
	// 策略层的回话**交回给调用方**，不在这里打日志：这一次探活是**有人点的**，
	// 而那句话（「这条的闸开了」）正是点的人要看的反馈。打在日志里等于让反馈
	// 跑到一个他没在看的地方。
	var notes []string
	for _, ack := range p.filters.ObserveProbes([]policy.ProbeObservation{{
		Provider: provider, Model: model, Status: out.Status,
		Latency: out.Latency, ContextBytes: 1, Error: out.Err, SlowAfter: slow,
	}}) {
		if ack.Note != "" {
			notes = append(notes, ack.Note)
		}
	}
	out.Note = strings.Join(notes, " · ")
	return out, nil
}

// RegisterRequestHook 把插件注册限制在 gateway owner 内部，并把撤销权交还调用组件。
func (p *port) RegisterRequestHook(hook Plugin) (modules.Release, error) {
	return p.registry.Register(hook)
}

// RegisterFilter 让策略把决策逻辑插进数据面的四个决策点。
//
// 这是**唯一的插入口**：数据面 handler、内部 registry、以及「谁插进来了」这件事
// 都不越过这个接口。所以 gateway 可以完全不认识那张健康表（binding 的可用性与
// 延迟账本），健康表只要声明 Need(gateway) 就能把自己的状态机挂上去。形状与
// RegisterRequestHook 并列，理由见 modules/gateway/policy 的包注释。
func (p *port) RegisterFilter(f Filter) (modules.Release, error) {
	return p.filters.Register(f)
}
