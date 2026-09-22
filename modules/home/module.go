// Package home 把「一屏看到我这些链」做成一节，并声明自己是界面的落点。
//
// # 它为什么是一个模块，而不是 config 那张卡的一部分
//
// 这一节的素材全部来自 config（见 configapi.Config.AllChains 的注释），但它不是
// config 自己的展示面——两者回答的是**两个不同的问题**：
//
//	config 那一节   「怎么改」：档位绑定编辑器、源文件、全局开关；
//	这一节          「现在是什么」：这一档此刻解析成谁、谁被跳过、为什么。
//
// 同一份数据摆在两个问题底下，用户打开界面时找的地方不一样，所以是两张卡。
//
// 更要紧的是**位置**：首屏落在哪一栏此前是 Source 的字母序（`breaker` 排最前，
// 于是它成了默认），而那是巧合——见 lib/view 的 Section.Default。内核为此只提供
// 一个「谁可以被声明为落点」的口子，**谁是落点由模块自己说**；而内核对「哪一屏
// 最该先看」一无所知（core/CLAUDE.md §0），所以这条消息只能由发行版发出，落在
// 这里。
//
// # 它不认识任何别的模块
//
// 依赖只有两条：config 的读端口（强依赖——这一屏的全部素材来自它，拿不到就什么
// 都画不出来）与 web 界面（弱依赖——界面不在就跳过，本模块没有别的功能会因此
// 少掉，与所有业务模块同一条规矩）。所以它装在哪一份规格书里都不牵连别人，
// 摘掉它只是首屏换个落点。
package home

import (
	"context"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	viewapi "github.com/rzbdz/newgate/lib/view"
	configapi "github.com/rzbdz/newgate/modules/config"
)

// Name 是这个模块在装配表与 `newgate plugin` 里的名字。
//
// 它同时是界面上那一节的 Source（机器标记，见 view.Service.Register）：URL、
// 落点、行动作的回传都按它走，所以它不跟着标题的措辞变。
const Name = "home"

// New 声明这一节。
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: Name,
		Desc: func() string {
			return i18n.T("the landing screen: every chain this machine resolves right now, in one view", nil)
		},
		// 「其它」：它不是内核、网关、界面、客户端、模型，也不是它们之间的桥。
		// 与 arch-diagram 同一类——一个不拥有任何机制的展示面，硬塞进上面任何
		// 一类都会让 `newgate plugin` 的分组说谎。
		//
		// 注意它**不是** "cli"（那是 web-dashboard / tui / simple-cli 的取值）：
		// 那一位是「界面壳」本身，而这里是被界面展示的一节。判据与内核 UI 那条
		// 规矩同源——把它删掉，界面照样成立，只是少了一节。
		Type: "others",
		Requires: []modules.Requirement{
			// 强依赖：链的结论只有 config 算得出来（见 configapi.Config 的注释：
			// 消费者要那份结论就来拿端口，别自己 import store/resolve 去拼）。
			// 这里**没有 fail-open 的余地**：拿不到它，这一屏一张卡都画不出来，
			// 而静默地画一屏空的会让用户以为自己的配置丢了。
			modules.Need(configapi.Capability),
			// 弱依赖：界面在就登记进去，不在就跳过。这条模块被装进任何一份
			// 规格书（包括没装任何界面的）都不会装不起来。
			modules.Optional(viewapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			v, ok := modules.Get(ctx, viewapi.Capability)
			if !ok {
				return nil
			}
			release, err := registerView(v, modules.MustGet(ctx, configapi.Capability))
			if err != nil {
				return err
			}
			releases = append(releases, release)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
