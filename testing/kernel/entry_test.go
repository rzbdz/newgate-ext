package kernel

import (
	"context"
	"testing"

	"github.com/rzbdz/newgate-ext/testing/testkit"
	modules "github.com/rzbdz/newgate/component"
	entryapi "github.com/rzbdz/newgate/component/entry"
)

// TestTheDaemonEntrySurvivesARemovedUI 是 2026-09-20 那个 bug 的棘轮。
//
// 现场：`__serve`（守护进程本体）原本注册成 cli 的一条命令，于是
// `disable: ["cli"]` 的装配**编得出来、却永远起不来**——进程一问入口账本，
// 得到的是「no entry claimed this call」。纯网页的发行版（没有终端界面，全靠
// 浏览器）因此根本不成立，而那是规格书允许要的一种形状。
//
// **矩阵测试看不见这类错**：它只问「摘掉这个模块还装得起来吗」，而这一条问的是
// 「装起来之后，这个进程还能不能当守护进程」。装得起来与起得来是两件事，中间
// 隔着入口账本——所以棘轮打在这里。
//
// 判据走**入口账本**而不是「有没有 cli」：将来守护进程换一种申报方式，这条测试
// 照样守得住。
func TestTheDaemonEntrySurvivesARemovedUI(t *testing.T) {
	testkit.Sandbox(t)
	all := kernelGraph(t)

	manager, err := modules.NewContext(context.Background(), staticLoader(without(all, "cli")))
	if err != nil {
		t.Fatalf("摘掉 cli 装不起来: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })

	reg, ok := modules.Get(manager.Context(), entryapi.Capability)
	if !ok {
		t.Fatal("入口账本不在图里——它是唯一摘不掉的那个模块（见 modules/entry）")
	}

	// 认领得了的：显式的内部入口，以及**没有界面时**的无子命令调用
	// （那时没有子命令可派，flag 只可能是守护进程自己的）。
	for _, args := range [][]string{
		{"__serve", "--port", "8899"},
		{"--port", "8899"},
		{"--port=8899"},
		nil,
	} {
		h, why, claimed := reg.Resolve(entryapi.Process{Argv0: "newgate", Args: args})
		if !claimed {
			t.Errorf("没有界面时 %v 没人认领——这种装配编得出来却起不来（%s）", args, why)
			continue
		}
		// 名字从 handler 上取：Resolve 的第二个返回值是「问过谁」的过程说明
		// （进日志用的那句话），不是身份。
		if name := h.Name(); name != "__serve" {
			t.Errorf("%v 被 %q 认领了，期望守护进程本体（__serve）", args, name)
		}
	}

	// 认领不了的：**认多了比认少了更难发现**——它表现为「敲错一条命令却起了一个
	// 守护进程」，要等端口被占、或者根本连不上才会露头。
	for _, args := range [][]string{
		{"frobnicate"},
		{"--help"},
		{"--port", "8899", "status"},
	} {
		if h, why, claimed := reg.Resolve(entryapi.Process{Argv0: "newgate", Args: args}); claimed {
			t.Errorf("%v 被 %q 认领了——没有界面时敲这些东西该得到「没人认领」（%s）",
				args, h.Name(), why)
		}
	}
}

// TestABareInvocationIsStillTheUIs 是上一条的**反面**：装着界面时，无参数调用
// 仍然是界面的（帮助 / 状态），不能被守护进程吞掉。
//
// 两条合起来才是完整的判据：守护进程的兜底只对「没有别人能回答」的装配成立。
func TestABareInvocationIsStillTheUIs(t *testing.T) {
	testkit.Sandbox(t)
	manager, err := modules.NewContext(context.Background(), staticLoader(kernelGraph(t)))
	if err != nil {
		t.Fatalf("装不起来: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })

	reg, ok := modules.Get(manager.Context(), entryapi.Capability)
	if !ok {
		t.Fatal("入口账本不在图里")
	}
	for _, args := range [][]string{nil, {"status"}, {"--help"}} {
		h, why, claimed := reg.Resolve(entryapi.Process{Argv0: "newgate", Args: args})
		if !claimed {
			t.Errorf("装着界面时 %v 该由界面认领，实际没人认领", args)
			continue
		}
		if h.Name() == "__serve" {
			t.Errorf("装着界面时 %v 被守护进程吞了——无参数该是给界面回答的（%s）", args, why)
		}
	}
	// 显式的内部入口照旧归守护进程。
	if h, _, _ := reg.Resolve(entryapi.Process{Argv0: "newgate", Args: []string{"__serve"}}); h.Name() != "__serve" {
		t.Errorf("__serve 该归守护进程，实际 %q", h.Name())
	}
}
