// Package wrapper 拥有「PATH shim 接管」这条通路的策略。
//
// 为什么它是一个模块而不是 cmd 里的几行代码
//
// 被接管后的行为是：用户敲 `claude` → 内核 exec 的是 PATH 里那个**名叫
// claude 的符号链接**（指向 newgate）→ 我们的进程醒来，argv[0] 是 `claude`。
// 这件事有两半：
//
//	装链接：newgate on claude 时把符号链接建出去（runtime/injection 的字节操作）
//	解析：  进程醒来后认出「我是替 claude 跑的那一份」，注入 env 再 exec 真实
//	        的 claude（原来是 cmd/newgate 里的 argv0 判断）
//
// 两半分开的代价是静默断链：链接装了但没人解析 argv0，症状是 `claude` 启动了
// 但完全没走 newgate——用户以为接管生效了。所以它们同属一个模块，模块的名字
// 就是这件事的名字。
//
// 依赖方向：wrapper → runtime（要 Launch）、wrapper → confighook（要按名字查
// Agent 描述符）。反过来不成立——runtime 不知道 shim 的存在，它只负责「给一个
// Agent，启动它」。
package wrapper

import (
	"context"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	runtimeapi "github.com/rzbdz/newgate-ext/modules/runtime"
	"github.com/rzbdz/newgate-ext/modules/runtime/injection"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
)

type service struct {
	agents  agentapi.AgentCatalog
	runtime runtimeapi.Runtime
	entry   modules.Release
}

var (
	_ Wrapper       = (*service)(nil)
	_ entry.Handler = (*service)(nil)
)

// New 声明 shim 接管组件。它不提供任何跨模块扩展点——外部只需要「分发一次
// 调用」和「装/摘链接」，没有第三个模块往它里面注册东西。
func New() modules.Component {
	instance := &service{}
	return modules.Component{
		Name: "wrapper",
		Desc: func() string { return i18n.T("argv0 dispatch: what runs when we are called by a client's name", nil) },
		Type: "client",
		Requires: []modules.Requirement{
			modules.Need(runtimeapi.Capability),
			modules.Need(agentapi.AgentCatalogCapability),
			// 入口账本（root 提供）。**声明它不是为了排序**——root 没有出边，
			// 谁先谁后没有悬念。声明它是为了让「root 是唯一的 built-in」这条主张
			// 变成可检查的事实：有人把 root 摘掉，构图期就当场失败并点名这个端口，
			// 而不是让进程起得来、却在第一次调用时才发现没有任何入口认领
			// （见 app/matrix_test.go 的摘除矩阵）。
			modules.Need(entry.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Wrapper(instance)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			instance.agents = modules.MustGet(ctx, agentapi.AgentCatalogCapability)
			instance.runtime = modules.MustGet(ctx, runtimeapi.Capability)
			// 申报自己为**有条件**的入口（entry.RankShim）：argv0 是某个被接管的
			// client 时先问本模块，不是就放过去让默认入口接手。
			//
			// 为什么这件事必须由 wrapper 自己做：哪些名字算 client、什么条件才认领
			// 一次调用，是本模块的策略。以前它写在 cmd/newgate/main.go 里，判据和
			// 数据分居两地，而且组合根因此 import 了本模块（删掉就编译不过）。
			registry := modules.MustGet(ctx, entry.Capability)
			release, err := registry.Register(instance, entry.RankShim)
			if err != nil {
				return err
			}
			instance.entry = release
			return nil
		},
		Stop: func(context.Context) error {
			if instance.entry == nil {
				return nil
			}
			return instance.entry()
		},
	}
}

// Claims 见 entry.Handler：argv0（basename，剥掉 .exe）是某个已接管 client 时认领。
//
// 判据只读 Process，不碰 os.Args —— 「这次进程是怎么被调起来的」由组合根装好
// 递进来（见 entry.Process 的注释）。
func (s *service) Claims(p entry.Process) bool {
	if p.Argv0 == "" {
		return false
	}
	_, ok := s.agents.Get(p.Argv0)
	return ok
}

// Handle 执行这次替 client 跑的调用。成功路径不会返回：进程被 syscall.Exec 换掉了。
func (s *service) Handle(p entry.Process) int {
	agent, ok := s.agents.Get(p.Argv0)
	if !ok {
		return 1
	}
	// profile 传空 = 动态模式（注入档位名而不是真实模型名），这是 argv0 分发
	// 的既定语义：用户没在命令行上钉 profile，就让代理按请求时的当前配置解析。
	// facts 从目录端口取来交给它：槽位此刻走哪儿是**客户端模块**的知识
	// （用户改过的映射住在那边），wrapper 只是转交，不解释。
	return s.runtime.Launch(agent, s.agents.Facts(agent.ID), p.Args, "")
}

// Name 进日志与诊断。
func (s *service) Name() string { return "wrapper" }

// Install/Uninstall/Installed/Foreign 把 shim 的字节操作转给 injection。
//
// 这四项刻意不做成 capability 端口：它们的消费者是 CLI 命令（newgate on/off/
// shim），走的是 package 级调用而不是组件间依赖。本模块在这里提供的是**归属**
// ——shim 是 wrapper 的东西，别处要动它得先问 wrapper，而不是各自去戳 injection
// 这个库。
func (*service) Install(agentID string) (string, error) { return injection.Install(agentID) }
func (*service) Uninstall(agentID string) error         { return injection.Uninstall(agentID) }
func (*service) Installed() []string                    { return injection.Installed() }
func (*service) Foreign() []string                      { return injection.Foreign() }
