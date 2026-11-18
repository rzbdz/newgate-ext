package wrapper_test

import (
	"reflect"
	"testing"

	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	entrymod "github.com/rzbdz/newgate-ext/modules/entry"
	runtimeapi "github.com/rzbdz/newgate-ext/modules/runtime"
	wrapperapi "github.com/rzbdz/newgate-ext/modules/wrapper"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
)

// launchCall 记录一次 Runtime.Launch 的入参。
type launchCall struct {
	agent   string
	args    []string
	profile string
}

// fakeRuntime 是 Runtime 端口的桩：只记录调用，不 exec 任何东西。
// 真实的 Launch 会 syscall.Exec 掉整个测试进程，所以这里必须换成桩。
type fakeRuntime struct{ calls []launchCall }

func (f *fakeRuntime) Launch(agent *agentapi.Agent, _ agentapi.AgentFacts, args []string, profile string) int {
	id := ""
	if agent != nil {
		id = agent.ID
	}
	f.calls = append(f.calls, launchCall{agent: id, args: args, profile: profile})
	return 0
}

var _ runtimeapi.Runtime = (*fakeRuntime)(nil)

// harness 装出「wrapper + 它的两个依赖」这张最小图。
//
// 这正是 testkit 存在的理由：被测模块不必知道依赖从哪来，只要图上有人提供
// 那两个端口就能启动。这里用桩替掉 runtime（真 Launch 会 exec 掉进程），
// 但 wrapper 模块本身是**真的**——它的 Requires/Provides/Start 全走真实路径。
func harness(t *testing.T, agents ...*agentapi.Agent) (entry.Registry, *fakeRuntime) {
	t.Helper()
	runtime := &fakeRuntime{}
	catalog := testkit.NewCatalog().Add(agents...)
	graph := testkit.Start(t,
		modules.Component{
			Name:     "stub-runtime",
			Type:     "test",
			Provides: []modules.Provision{modules.Provide(runtimeapi.Capability, runtimeapi.Runtime(runtime))},
		},
		modules.Component{
			Name:     "stub-catalog",
			Type:     "test",
			Provides: []modules.Provision{modules.Provide(agentapi.AgentCatalogCapability, catalog.AsCatalog())},
		},
		// 入口账本的提供者是 modules/entry——内核唯一认识、也唯一摘不掉的那个
		// 模块：本模块在自己的 Start 里往它申报，所以测试图里必须有它——缺了
		// 就该当场 panic，那正是「申报口没了」的现场。
		entrymod.New(),
		wrapperapi.New(),
	)
	// 依赖顺序：wrapper 必须在两个提供者之后启动，否则 Start 里的 MustGet 会 panic。
	graph.Before("stub-runtime", "wrapper")
	graph.Before("stub-catalog", "wrapper")
	// 交出去的是**入口账本**而不是 Wrapper 接口：本模块暴露给进程的那一面现在是
	// 「我申报过一次入口」，而测试要走的就是这条路（问账本 → 拿到认领者 → 调它），
	// 不是绕过账本直接点模块——那样测出来的行为线上根本不会发生。
	return testkit.Get(graph, entry.Capability), runtime
}

func TestDispatchClaimsAgentInvocations(t *testing.T) {
	registry, runtime := harness(t,
		&agentapi.Agent{ID: "claude"},
		&agentapi.Agent{ID: "opencode"},
	)

	tests := []struct {
		name     string
		argv     []string
		claimed  bool
		wantID   string
		wantArgs []string
	}{
		{
			name:     "PATH shim 转过来的绝对路径",
			argv:     []string{"/home/u/.config/newgate/bin/claude", "--resume", "abc"},
			claimed:  true,
			wantID:   "claude",
			wantArgs: []string{"--resume", "abc"},
		},
		{
			name:     "argv0 就是裸名字",
			argv:     []string{"opencode"},
			claimed:  true,
			wantID:   "opencode",
			wantArgs: []string{},
		},
		{
			// npm 装的兜底脚本会以 claude.exe 出现，剥后缀是历史行为。
			// 用 POSIX 路径而不是 `C:\bin\claude.exe`：反斜杠只在 Windows 上是
			// 分隔符，在 Linux 上 filepath.Base 会把整串当文件名——那样测的是
			// Windows 语义，在 Linux 上必然失败，属于测试写错而不是代码写错。
			name:     "Windows 风格 .exe 后缀要剥掉",
			argv:     []string{"/usr/local/bin/claude.exe", "hi"},
			claimed:  true,
			wantID:   "claude",
			wantArgs: []string{"hi"},
		},
		{
			name:     "裸的 .exe 名字也要认",
			argv:     []string{"claude.exe"},
			claimed:  true,
			wantID:   "claude",
			wantArgs: []string{},
		},
		{
			name:    "控制 CLI 自己不该被认领",
			argv:    []string{"/usr/local/bin/newgate", "status"},
			claimed: false,
		},
		{
			name:    "不认识的命令名不该被认领",
			argv:    []string{"/usr/bin/ls", "-la"},
			claimed: false,
		},
		{
			// 空 argv 是内核理论上不会给但防御性要处理的情况（比如测试里手构造）。
			name:    "空 argv 不 panic",
			argv:    nil,
			claimed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(runtime.calls)
			// argv0 归一是**组合根那一趟**（entry.MakeProcess）做的，所以这里走
			// 真管道而不是手构造 Process：绝对路径、.exe 后缀这些形态的归一
			// 才算被测到。
			p := entry.MakeProcess(tt.argv, nil, "", "", "")
			handler, why, claimed := registry.Resolve(p)
			if claimed != tt.claimed {
				t.Fatalf("Resolve(%v) claimed = %v, want %v（%s）", tt.argv, claimed, tt.claimed, why)
			}
			if !tt.claimed {
				if len(runtime.calls) != before {
					t.Fatalf("未被认领却调了 Launch: %v", runtime.calls[before:])
				}
				return
			}
			if handler.Name() != "wrapper" {
				t.Fatalf("认领者 = %q, want wrapper", handler.Name())
			}
			if code := handler.Handle(p); code != 0 {
				t.Fatalf("Handle 退出码 = %d, want 0", code)
			}
			if len(runtime.calls) != before+1 {
				t.Fatalf("认领了但 Launch 调用次数 = %d, want %d", len(runtime.calls)-before, 1)
			}
			call := runtime.calls[before]
			if call.agent != tt.wantID {
				t.Errorf("Launch agent = %q, want %q", call.agent, tt.wantID)
			}
			if !reflect.DeepEqual(call.args, tt.wantArgs) {
				t.Errorf("Launch args = %v, want %v", call.args, tt.wantArgs)
			}
			// argv0 分发是**动态模式**：不钉 profile，让代理按请求时的当前配置解析。
			// 钉死了的话 newgate --set-profile 对跑着的会话就不生效了。
			if call.profile != "" {
				t.Errorf("Launch profile = %q, want 空（argv0 分发必须走动态模式）", call.profile)
			}
		})
	}
}

// TestDispatchStripsArgv0 锁住「argv0 不进透传参数」。
// 真实启动是 syscall.Exec(real, [real]+args, env)，多带一个 argv0 会让
// claude 把 `claude` 当成要打开的目录。
func TestDispatchStripsArgv0(t *testing.T) {
	registry, runtime := harness(t, &agentapi.Agent{ID: "claude"})
	p := entry.MakeProcess([]string{"claude", "a", "b"}, nil, "", "", "")
	handler, why, ok := registry.Resolve(p)
	if !ok {
		t.Fatalf("没有任何入口认领 %v（%s）", p.Argv0, why)
	}
	handler.Handle(p)
	if len(runtime.calls) != 1 {
		t.Fatalf("Launch 调用次数 = %d, want 1", len(runtime.calls))
	}
	if got := runtime.calls[0].args; !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("透传参数 = %v, want [a b]（argv0 不该出现）", got)
	}
}
