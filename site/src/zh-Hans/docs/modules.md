# 写一个模块

一个模块就是**一个目录、一个 `module.go`、一个导出的 `New()`**：

```go
package mymod

import (
	"context"

	modules "github.com/rzbdz/newgate/component"
)

func New() modules.Component {
	return modules.Component{
		Name: "my-module",
		Desc: "一句话说清它是干什么的",
		Requires: []modules.Requirement{
			modules.Need(configapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			svc := modules.MustGet(ctx, configapi.Capability)
			rel, err := svc.RegisterX(...)
			if err != nil {
				return err
			}
			releases = append(releases, rel)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
```

**不需要改任何清单**：装配清单由 `tools/genmodules` 在构建期扫 `modules/` 生成，
目录进来就自动在图上。判据只有一条：根 `module.go` 导出 `func New() modules.Component`。

## 目录名不必是 Go 标识符

`simple-cli` 这种名字是合法的目录名。生成器会把 import 别名拧成 `ext_simple_cli`
（判据在内核的 `tools/genmodules/scan.Ident`，两个仓库共用一份实现）。

## 三条最常踩的

**一、往别人的扩展点插东西，走 `Register`，不要声明成 `Provides`。**
`Provides` 只放**自己的** service。要往别人的扩展点插东西，就消费它的 service 并
调它的 `RegisterX()`——那一对方法就是那条缝。注册 API 返回 `Release`，在 `Stop`
里逆序释放。

**二、界面是弱依赖：`Optional`，不是 `Need`。**
`modules/cli` 是界面，不是依赖。`component.Need(cliapi.Capability)` 等于宣称
「没有界面我就活不了」，会把界面重新拖回依赖图，内核的棘轮当场红。

```go
component.Optional(cliapi.Capability)   // 对
component.Need(cliapi.Capability)       // 错，graph_test 会拦下
```

**三、`Register` 里不许读盘。**
`Start` 在**每一条** `newgate …` 命令里都会跑（敲一条命令、进一个 shell、跑一次
体检）。注册只管**登记一个产出函数**，真正的产出发生在「有人来问界面的那一刻」。
把读盘放进 `Start`，等于每敲一次 `newgate status` 就把所有配置文件重读一遍。

## 往界面上挂东西

契约在叶子包 `lib/view`（界面模块提供账本，业务模块 `Optional` 依赖它）：

```go
Requires: []modules.Requirement{modules.Optional(viewapi.Capability)},

Start: func(_ context.Context, ctx modules.Context) error {
	v, ok := modules.Get(ctx, viewapi.Capability)
	if !ok {
		return nil // 这次装配没有界面 = 我没那一面，功能一个都不少
	}
	rel, err := v.Register("my-module",
		viewapi.Title(func() string { return i18n.T("My module", nil) }),
		myConcepts)
	…
}
```

**栏目名为什么是个闭包**：界面按来源分栏，而「这一栏叫什么」是模块自己的事。但名字
**不能在登记时翻**——登记发生在各模块的 `Start` 里，那时语言层装好了没有是没有保证
的，翻了就会冻在源语言上，而且没有任何东西会因此变红。`view.Title` 收闭包，求值
发生在**快照那一刻**。

**一个概念 = 数据 + 怎么改**：

```go
view.Concept{
	ID:    "my-module.thing",
	Kind:  view.KindRecords,      // 渲染方式
	Title: i18n.T("My thing", nil),
	Data:  view.Records{…},
	Apply: applyThing,            // 这份文件只有你知道怎么写
}
```

- `Kind` 是渲染方式，前端**只认那七种**。加一个模块的面不用改前端——除非它带来
  一种新的**形状**，那时两边都要动（这是诚实的代价）。
- `Apply(edit, base)` 是「拥有那份文件的人才写得出来」的东西：改哪一段、哪些字段
  必须原样保留。界面自己不写文件。
- `Broken` 是「此刻读不出来」：**照常报一张卡片、写上原因**，不要让它从列表里
  消失（用户会以为它不存在），也不要让整次快照失败。
- `Live` 是「这一面会自己变，界面每隔几秒重问一次」。判据是**读一次贵不贵**，
  不是数据变不变。

**写盘一律带基线**（CAS）：`store.WriteIfUnchanged(path, base, data)`。这样「你在
看页面时命令行改过那个文件」会变成一个**摆出来的冲突**，而不是被盖掉。写失败时把
`store.StaleError` 翻成 `view.Conflict`。

## 往那个端口上挂东西（porthub）

网关监听的那个端口**不只属于数据面**。`/` 之外的前缀可以挂别的服务：

```go
Requires: []modules.Requirement{modules.Optional(porthubapi.Capability)},
Start: func(_ context.Context, ctx modules.Context) error {
	hub, ok := modules.Get(ctx, porthubapi.Capability)
	if !ok {
		return nil // 这次装配没有共享端口 = 我没入口，功能照常
	}
	rel, err := hub.Mount("/myservice", "my-module", myHandler)
	…
}
```

三条规矩：

- **保留前缀挂不上去**（`/v1`、`/a`、`/__newgate`）——它们已经归数据面与控制面。
  挂上去失败的样子很吓人：那些路径落进 catch-all 会被**当数据面转发给上游**。
- **挂载是进程内注册**，不起 socket。所以 `Start` 在每条命令里挂一次是无害的。
- **数据面不许知道 porthub 存在**（内核有一条棘轮扫这个）——端口怎么分派是产品
  决定，数据面只该知道自己是一个 handler。

**要在服务期起监听**（真的开 socket）就挂 `lib/serving`：`Start` 在每条命令里都跑，
在那里 `net.Listen` 等于敲一次 `newgate status` 就开一台服务器。注册一个回调，等
拥有端口的模块说「我要开始服务了」才跑：

```go
Requires: []modules.Requirement{modules.Optional(servingapi.Capability)},
Start: func(_ context.Context, ctx modules.Context) error {
	ln, ok := modules.Get(ctx, servingapi.Capability)
	if !ok {
		return nil
	}
	rel, err := ln.OnServe("my-module", func() (func(), error) { return startMyListener() })
	…
}
```

回调是 fail-open 的：一个起不来不会让服务失败。

## 写成发行版的模块

模块之间的 import 是 `github.com/rzbdz/newgate-ext/modules/<名字>`；对内核的 import
是 `github.com/rzbdz/newgate/...`——**用它的公开契约**。

想让它进哪份发行版，写进那份规格书就行，Go 代码一行不用改：

```jsonc
// dist-mine.json
{ "distribution": "mine", "modules": ["tui", "deepseek", "my-module"], "disable": [] }
```

然后 `go run ./tools/distgen`（把名字编进装配清单）再 `build/build.sh`。

## 用户可见的文案

**源码里不许写中文文案**。用户可见的文本一律走：

```go
i18n.T("English {x}", i18n.A{"x": …})
```

中文活在 `modules/i18n/catalogs/zh-Hans.json` 里。键**就是那句英文**（gettext 的
msgid 传统），所以没有「键写错」这回事。机器标记留在消息**外面**：`[tag]`、metrics
计数器名、`ok/warn/bad`、JSON 字段名、命令名、路径；**上游报错的原文也不能翻**
——那是匹配判据，翻了签名永远匹配不上。

```bash
go run github.com/rzbdz/newgate/tools/i18n extract -root . -catalogs modules/i18n/catalogs
go run github.com/rzbdz/newgate/tools/i18n check   -root . -catalogs modules/i18n/catalogs \
                                                   -allowlist tools/i18n-allowlist.json
```

前两条在 CI 里。**账本是构建自己重算的**，不用记得跑它。

## 测试

- **核心逻辑走 TDD**：纯函数、CAS 写入、校验——写在 Go 侧，毫秒级，进 CI。
- **浏览器那一层尽量少动**：只有浏览器能回答的问题（画出来了没有、点了界面变没变）
  才留在那里。
