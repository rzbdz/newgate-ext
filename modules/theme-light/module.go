// Package themelight 是「白」这套皮肤：纯白底 + 中性灰 + 深蓝强调。
//
// # 它为什么不是「出厂那套的浅色版」
//
// 出厂令牌里那套浅色（web/src/app.css 的 `prefers-color-scheme: light`）是**暖米色**
// 的，与它那套深色是同一个色相的两次曝光。这一套刻意走纯白 + 中性灰：它是给
// 「投影仪上要看得清」「窗户开着、屏幕反光」那种场合用的，那里要的是对比度，
// 不是氛围。
//
// 与 theme-dark 是同一副牌的两面（同样是中性灰阶、同一个蓝强调），这样从黑切到白
// 时，**只有亮度变了**——界面上每一样东西的位置、每一处颜色的意思都不动。
package themelight

import (
	"context"

	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Name 是这个模块在装配表与 `newgate plugin` 里的名字（= 目录名）。
const Name = "theme-light"

// ID 是这套皮肤的机器标记（存进 state.json 的就是它）。
const ID = "light"

// New 声明这个组件：界面在时把自己这套皮肤登记进去，不在时什么都不做。
// Optional 而不是 Requires 的理由见 modules/theme-dark 的同一段。
func New() modules.Component {
	return modules.Component{
		Name:     Name,
		Desc:     func() string { return i18n.T("a white theme for the web interface", nil) },
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
		Name: func() string { return i18n.T("White", nil) },
		Dark: false,
		// 语义色比深色那套**更深**（同一个绿压在白底上要暗一档才读得清）——这一点
		// 抄不得，照抄一个值会让白底上的成功/警告色浅到看不出来。
		CSS: `
  --bg: hsl(0 0% 100%);
  --panel: hsl(0 0% 98%);
  --panel-2: hsl(0 0% 95%);
  --line: hsl(0 0% 86%);
  --ink: hsl(0 0% 8%);
  --dim: hsl(0 0% 42%);
  --accent: hsl(212 88% 42%);
  --ok: hsl(142 62% 28%);
  --warn: hsl(32 88% 34%);
  --danger: hsl(0 68% 44%);
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
