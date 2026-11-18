// Package ask 是「把 newgate 当快速 LLM 后端直接用」那条路上最小的一件东西：
// 敲一句问题，走**本机正在跑的那个代理**发出去，只把回答的正文打出来。
//
// # 它为什么是个模块
//
// 判据与别处一样（见 core/CLAUDE.md 的「改哪边」）：换个发行版，这东西还该在吗？
// 该——它是消费侧进网关的那扇门，与 `probe` / `metrics` 那几条一样是**机制**，
// 不是某个产品的取舍。所以它住内核。
//
// 它**不自己发请求给上游**：走本机代理，图的就是代理那一整套（档位链、fallback、
// 熔断、上游怪癖修补）在这条路上照样生效。自己拼一遍等于把这些东西抄第二份，
// 而抄来的那份必然漂移——用户拿 `ask` 与拿 claude 得到不同的行为，没人能解释。
//
// # 它不认识「模型」，只认识档位
//
// 请求里的 `model` 填的是**档位名**（`light` / `normal` / …），由代理按 profile
// 解析成哪家哪个模型。这正是这个产品的核心抽象，`ask` 没有理由绕过它。
package ask

import (
	"context"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// rankAsk 决定 `ask` 在「跑一次」那一节里的位置（见 cliapi.HelpLine.Rank）。
// 它排在 `run` 之后：`run` 是启动一个客户端，`ask` 是直接问一句。
const rankAsk = 30

// New 声明 ask 组件。
//
// 依赖只有一条弱边（ui）：装着界面就把命令挂上去，没装就跳过——这条模块本身
// 没有任何后台行为，没有界面时它就是一个「什么都不做」的组件。这是
// core/CLAUDE.md 那条规矩的直接结果：**业务模块不依赖任何 ui**，界面不在时
// 功能一个都不少（这里「不少」的边界是：它本来就只有入口这一个功能）。
func New() modules.Component {
	var release modules.Release
	return modules.Component{
		Name: "ask",
		Desc: func() string {
			return i18n.T("ask one prompt through the running proxy, and print just the answer", nil)
		},
		Type: "cli",
		Requires: []modules.Requirement{
			modules.Optional(cliapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			cli, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			var err error
			release, err = cli.RegisterCommand(askCommand{})
			return err
		},
		Stop: func(context.Context) error {
			if release == nil {
				return nil
			}
			return release()
		},
	}
}
