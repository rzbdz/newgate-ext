// Package themeanthropic 是 anthropic 那套配色：米白纸底 + 陶土色强调。
//
// # 色值从哪来
//
// 用的是 Anthropic 对外那套品牌色（米白 `#FAF9F5`/`#F0EEE6`、深墨 `#141413`、
// 陶土橙 `#D97757`、"book cloth" `#CC785C`、燕麦 `#A09E96`）。**这些是十六进制而
// 不是 HSL**，与另外两套皮肤（hsl 三元的灰阶）刻意不同：它们是**抄来的具体值**，
// 换算成 HSL 会引入一份不该有的舍入，而下一个来核对的人要在两个色空间之间来回倒。
// 灰阶那种「我现推一个」的颜色才写成 hsl。
//
// # 它与 theme-light 的关系
//
// 两者都是浅色，但语气不同：theme-light 是纯白 + 中性灰（要对比度），这一套是米白 +
// 暖褐（要氛围）。所以它**不是** theme-light 的换色版——同一张卡上，一边是医院，
// 一边是书桌。
package themeanthropic

import (
	"context"

	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Name 是这个模块在装配表与 `newgate plugin` 里的名字（= 目录名）。
const Name = "theme-anthropic"

// ID 是这套皮肤的机器标记（存进 state.json 的就是它）。
//
// 它是品牌名，所以**不翻译**（与 Theme.Name 的分工：那个是给人看的字，这个是键）。
const ID = "anthropic"

// New 声明这个组件：界面在时把自己这套皮肤登记进去，不在时什么都不做。
// Optional 而不是 Requires 的理由见 modules/theme-dark 的同一段。
func New() modules.Component {
	return modules.Component{
		Name:     Name,
		Desc:     func() string { return i18n.T("the Anthropic brand palette for the web interface", nil) },
		Type:     "cli",
		Requires: []modules.Requirement{modules.Optional(webapi.ThemeCapability)},
		Start:    start,
		Stop:     stop,
	}
}

var release modules.Release

func start(_ context.Context, ctx modules.Context) error {
	port, ok := modules.Get(ctx, webapi.ThemeCapability)
	if !ok {
		return nil
	}
	rel, err := port.RegisterTheme(webapi.Theme{
		ID:   ID,
		Name: func() string { return i18n.T("Anthropic", nil) },
		// 米白底 = 浅色。界面据此把 `color-scheme` 设成 light，于是滚动条、复选框、
		// 自动填充的底色跟着走——那几样不归 CSS 变量管（见 webapi.Theme.Dark）。
		Dark: false,
		CSS: `
  --bg: #faf9f5;
  --panel: #f0eee6;
  --panel-2: #e8e6dc;
  --line: #d8d4c7;
  --ink: #141413;
  --dim: #6f6c64;
  --accent: #d97757;
  --ok: #3d7a4e;
  --warn: #a96a12;
  --danger: #bc3f36;
`,
	})
	if err != nil {
		return err
	}
	release = rel
	return nil
}

func stop(context.Context) error {
	if release != nil {
		_ = release()
		release = nil
	}
	return nil
}
