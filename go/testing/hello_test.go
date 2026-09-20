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
	"testing"

	"github.com/rzbdz/newgate-ext/go/modules/hello"
	"github.com/rzbdz/newgate/go/testing/testkit"
)

// TestHelloLoadsStandalone 是「零依赖的模块独立加载」这条不变式在**发行版这一侧**
// 的样例：一个不依赖任何人的模块，装进一张只有它自己的图也该装配成功。
//
// 核心仓库那边有一条同样的矩阵测试（app/matrix_test.go）跑**每个**自带模块；
// 这一条存在的意义是让「发行版也可以有自己的测试」这件事有个可抄的样板。
func TestHelloLoadsStandalone(t *testing.T) {
	testkit.Sandbox(t)
	g := testkit.Start(t, hello.New())
	if names := g.Names(); len(names) != 1 || names[0] != "hello" {
		t.Fatalf("图里应该是 hello 一个组件，拿到 %v", names)
	}
}
