// Package themedark 是「黑」这套皮肤：真黑底 + 中性灰 + 蓝强调。
//
// # 它与出厂那套的关系
//
// 出厂令牌（web/src/app.css 的 `:root`）是**暖色**的——底色偏棕、强调是暖橙。这一套
// 刻意走另一个方向：底色是真黑（`hsl(0 0% 0%)`，OLED 上不发光），中间层是中性灰，
// 强调换成蓝。两者不是「同一套的两个亮度」，而是两种语气：出厂那套像纸，这一套像
// 终端。用户选它是因为他在夜里看，而不是因为系统的深浅设置变了。
package themedark

import (
	"context"

	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Name 是这个模块在装配表与 `newgate plugin` 里的名字（= 目录名）。
const Name = "theme-dark"

// ID 是这套皮肤的机器标记（存进 state.json 的就是它）。
//
// 与目录名差一个 `theme-` 前缀：目录名要让「这是一套皮肤」一眼看得出来（见
// webapi.Theme.ID 那条约定），而 ID 是给人和报文看的，多一个前缀反而啰嗦。
const ID = "dark"

// New 声明这个组件：界面在时把自己这套皮肤登记进去，不在时什么都不做。
//
// # 为什么是 Optional（而**不是** Requires）
//
// 与 modules/i18n 依赖语言层是同一条：没装任何界面时（骨架发行版、或者
// `dist-dashboard.json` 之外的装配）这个模块**照常装着、功能一个不少**——它做的
// 全部事情就是往界面的表里放一份 CSS，没有人来取就等于没做。装不上的话，一个纯
// 皮肤模块会把整张图拖下来。
//
// 依赖声明成 Optional 还买到了**启动顺序**：界面在时它一定排在界面之后（那条边
// 就是顺序边），于是登记发生在登记处建好之后。
func New() modules.Component {
	return modules.Component{
		Name: Name,
		Desc: func() string { return i18n.T("a true-black theme for the web interface", nil) },
		// Type 取 "cli"：与 web-dashboard / tui / simple-cli 同一类（「界面壳」那
		// 一类的既有取值）。皮肤是界面的一部分，它不是业务模块。
		Type:     "cli",
		Requires: []modules.Requirement{modules.Optional(webapi.ThemeCapability)},
		Start:    start,
		Stop:     stop,
	}
}

// release 是登记时拿回的放销。**模块实例只有一份**（New 一次装配跑一次），所以
// 一个包级变量就够——不需要锁，Start 与 Stop 由框架按顺序调用。
var release modules.Release

func start(_ context.Context, ctx modules.Context) error {
	port, ok := modules.Get(ctx, webapi.ThemeCapability)
	if !ok {
		return nil // 这次装配里没有界面：没处可登记，也不是错误。
	}
	rel, err := port.RegisterTheme(webapi.Theme{
		ID:   ID,
		Name: func() string { return i18n.T("Black", nil) },
		Dark: true,
		// 只有令牌，没有选择器（见 webapi.Theme.CSS）。少给一个不会报错——它会
		// 落回出厂的值，而半套皮肤的难看是作者一眼看得见的。
		CSS: `
  --bg: hsl(0 0% 2%);
  --panel: hsl(0 0% 6%);
  --panel-2: hsl(0 0% 11%);
  --line: hsl(0 0% 18%);
  --ink: hsl(0 0% 94%);
  --dim: hsl(0 0% 60%);
  --accent: hsl(212 92% 62%);
  --ok: hsl(142 52% 54%);
  --warn: hsl(38 92% 60%);
  --danger: hsl(0 72% 64%);
`,
	})
	if err != nil {
		return err
	}
	release = rel
	return nil
}

// stop 把登记撤回去（框架只要求「拿到了什么就还回去」）。
//
// 它看着像摆设——进程都要退了，一张表撤不撤有什么区别？区别在**测试与嵌入**：
// testing/testkit 会在同一个进程里反复装图、停图，不还回去的话第二次装配会撞上
// 「theme dark is already registered」，而那个错误看起来像皮肤写坏了。
func stop(context.Context) error {
	if release != nil {
		_ = release()
		release = nil
	}
	return nil
}
