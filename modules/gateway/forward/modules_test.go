package forward_test

import (
	"context"
	"os"
	"testing"

	"github.com/rzbdz/newgate-ext/manifest"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate/app"
)

// TestMain 是这个包唯一的进程级装配点（internal 与 external 两个测试包共用同一个
// 测试二进制，所以 TestMain 全包只能有一个）。
//
// 它做两件事，**顺序不能反**：
//
//  1. 把全部测试关进一个临时 NEWGATE_HOME 并铺一份最小配置；
//  2. 起一张真组件图（有些用例要验模块注册进来了没有）。
//
// 第 1 步是 2026-09-18 补的，起因是 CI 连续三次红：这个包里凡是走
// `newTestServer()` 的用例都没有 Watch，handler 于是退化成 `store.Load()` 直接
// 读盘——读的是**开发机上真实的那份 ~/.config/newgate**。开发机永远有配置，所以
// 本地一直绿；干净的 runner 上 Load 失败、`snap()` 返回 nil，每一发请求都变成
// 400「[newgate] 配置读不出」，于是整包一起挂。
//
// 教训不是「CI 环境要补配置」，而是**测试不该依赖跑它的那台机器**：这台机器上有
// 配置只是巧合，用例真正需要的是「一份能解析的配置」，那就自己铺一份。
// 个别用例仍可以用 sandboxState(t, …) 覆盖成自己那一份。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "newgate-forward-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("NEWGATE_HOME", dir)
	if _, err := store.Init(false); err != nil {
		_ = os.RemoveAll(dir)
		panic("铺测试配置失败: " + err.Error())
	}

	// The graph is the distribution's flagship spec: this package tests how a
	// module behaves when the product actually installs it (an upstream patch's
	// signature is registered by another module, so it has to be present).
	sel := manifest.Specs()[manifest.DefaultSpec]
	built, err := app.New(context.Background(), sel)
	if err != nil {
		_ = os.RemoveAll(dir)
		panic(err)
	}
	code := m.Run()
	if err := built.Stop(context.Background()); err != nil && code == 0 {
		code = 1
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
