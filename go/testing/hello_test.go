// Package disttesting 装**这个发行版自己**的测试。
//
// # 为什么测试写在发行版仓库里
//
// 它是这个仓库能被称为「发行版」而不是「一堆模块目录」的分界线之一：模块的正确性、
// 发行版级的不变式（哪些模块必须同时在、哪些必须互斥），只有发行版作者知道边界在
// 哪。核心仓库的测试测的是内核——它不该认识 hello，更不该认识「本发行版启用了哪几个」。
//
// # 它怎么跑
//
// 这些包住在发行版**自己的 module** 里（`go/go.mod`，用一条 `replace` 指到 `core/`
// 那份 submodule），所以它们直接 import 内核的测试设施与模块：
//
//	cd go && go test ./...
//
// 在本仓库里就能跑，不需要内核那边配合。反过来也成立——**内核的测试不会跑这些**：
// 它的装配清单只看自己的 `modules/`。「测试跟着拥有者走」这条边界是两个方向都成立的。
package disttesting

import (
	"context"
	"testing"

	"github.com/rzbdz/newgate-ext/go/modules/hello"
	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/component/entry"
	entrymod "github.com/rzbdz/newgate/go/modules/entry"
	"github.com/rzbdz/newgate/go/testing/testkit"
)

// TestHelloClaimsTheDefaultEntry 是骨架配置（dist-hello.json）的全部行为：
// 「框架 + hello」起来之后，`newgate` 这次调用归 hello，它跑一句 hello world。
//
// 这条测试 2026-09-20 之前断言的是「零依赖模块能独立加载」——那时 hello 谁也不
// 依赖。现在它往**入口账本**申报自己，于是它有了一个硬依赖（`Need(entry.Capability)`，
// 见 go/modules/hello），必须和 entry 那个模块一起装。判据也跟着改成断言它**实际
// 做的事**：申报 → 被 Resolve 选中 → Handle 返回 0。
//
// 「零依赖模块能单独装」那条不变式没有丢：它由内核的摘除矩阵逐个模块守着
// （core 的 app/matrix_test.go 里的 TestDependencyFreeModulesLoadStandalone）。
func TestHelloClaimsTheDefaultEntry(t *testing.T) {
	testkit.Sandbox(t)
	g := testkit.Start(t, entrymod.New(), hello.New())

	registry := testkit.Get(g, entry.Capability)
	process := entry.Process{Argv0: "newgate"}
	h, why, ok := registry.Resolve(process)
	if !ok {
		t.Fatalf("没有任何入口认领 `newgate`（%s）——这个发行版的二进制跑起来会直接退出", why)
	}
	if got := h.Name(); got != "hello" {
		t.Fatalf("认领这次调用的是 %q，想要 hello", got)
	}
	if code := h.Handle(process); code != 0 {
		t.Errorf("hello world 的退出码 = %d，想要 0", code)
	}
}

// TestHelloUnregistersOnStop 锁住内核那条规矩在本模块这一侧也成立：
// **Stop 必须撤销 Start 做过的注册**。
//
// 为什么值得一条自己的测试：账本是**进程级**状态，漏掉那次 Release 的症状是第二张
// 图拿到第一张图的残留——而且不报错（内核的 TestGraphCanBeRebuiltAfterStop 就是被
// 这类残留咬出来的）。发行版的模块以后会越来越多，这条是给它们抄的样板。
func TestHelloUnregistersOnStop(t *testing.T) {
	testkit.Sandbox(t)

	comps := []modules.Component{entrymod.New(), hello.New()}
	manager, err := modules.NewContext(context.Background(), staticLoader(comps))
	if err != nil {
		t.Fatalf("起图：%v", err)
	}
	// 账本对象在 Stop 之后仍然在手上（它就是那个 Table），所以能直接问它「还剩谁」。
	registry := modules.MustGet(manager.Context(), entry.Capability)
	process := entry.Process{Argv0: "newgate"}
	if _, _, ok := registry.Resolve(process); !ok {
		t.Fatal("图起来之后 hello 没申报入口")
	}

	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("停止：%v", err)
	}
	if h, why, ok := registry.Resolve(process); ok {
		t.Fatalf("停掉之后账本里还留着 %q（%s）——Stop 漏了那次 Release", h.Name(), why)
	}
}
