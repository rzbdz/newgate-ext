// Package themeanthropic 是 anthropic 那套配色：暖米底 + 陶土色强调。
//
// # 它从哪来（2026-09-29 换过一次）
//
// 这一套**原本是出厂那套令牌**（web/src/app.css 的 `:root` 与它的浅色分支），
// 抄自 cc-switch 的参考界面：底色是暖米/暖棕，强调是暖橙，边框、静默底、次要
// 文字全都带同一条橙黄色相（H≈30–38）。
//
// 用户的原话是「跟随系统这个主题才是 anthropic 主题吧，跟随系统默认应该是黑白
// 主题」——那套暖色的语气确实是 Anthropic 式的，而不是一个界面该有的**出厂**样子
// （出厂那套的语义是「不要给我指定一套」）。所以它搬到这里来，出厂换成中性黑白。
//
// 上一版这里是我照着 Anthropic 的品牌色值编的一套十六进制（`#faf9f5` / `#d97757`），
// 用户看过之后说「颜色不对，应该不要了」。留下的教训是：**「某家的品牌色」与
// 「这个界面一直在用的那套暖色」不是一回事**，而后者才是用户认的那一个。
//
// # 为什么只有浅色那一半
//
// 一套皮肤只声明**一个**变体（见 webapi.Theme.CSS），所以那一套**暖深色**
// （原 `:root`，暖棕底）没有搬过来——它没有地方可去。要留它得先把协议改成
// 「一套皮肤给深浅两份」，那是一件单独的事。
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
  --bg: hsl(38 28% 91%);
  --panel: hsl(35 25% 89%);
  --panel-2: hsl(35 20% 85%);
  --line: hsl(35 18% 78%);
  --ink: hsl(30 15% 15%);
  --dim: hsl(30 12% 40%);
  --accent: hsl(15 65% 48%);
  --ok: hsl(142 55% 32%);
  --warn: hsl(32 75% 40%);
  --danger: hsl(0 62% 45%);
`})
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
