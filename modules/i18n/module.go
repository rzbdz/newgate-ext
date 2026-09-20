// Package i18n 把**发行版自己的文案**接进内核的消息目录。
//
// # 为什么需要它
//
// 内核只认识自己的模块名。发行版这些模块——客户端接管、上游怪癖补丁、界面——
// 说的话它一句都不认识：那些消息既不在内核的账本里，也不在内核的目录里。
// 内核给的口子是 `lib/i18n.Extend`，注意它是**追加**而不是安装：安装
// （`Install`）是 modules/locale 的事，一次装配只发生一次（它要解析语言、
// 装表）；追加只把**新的** id 并进去。
//
// # 为什么是弱依赖（Optional）
//
// 「这次进程说哪门语言」这一层是可选的：骨架发行版（dist-hello.json）用
// `"disable": ["*"]` 把内核模块全关掉，连 modules/locale 都不在图里。那时这里
// 什么都不该发生——没有语言可装，文案就是源码里那句英文（内核的恒等路径），
// 而「摘掉语言层」也不该让整个发行版装不起来。
//
// 依赖声明成 Optional 还买到了**启动顺序**：modules/locale 在时，它在之前启动
// （Extend 必须在 Install 之后调用，否则追加进的是那张还没装好的表）。
//
// # 目录从哪来
//
// `catalogs/` 下的两份文件：`ledger.json` 是 `tools/i18n extract` 扫源码生成的
// （哪些消息、在哪、带什么实参），`<tag>.json` 是译文。两者都进版本控制，CI 的
// 棘轮（`testing/i18n_test.go`）看着它们——本文件只管嵌进来、在 locale 就绪之后
// 追加。**加一句文案要改的不是这里**：写 `i18n.T("English", …)`，然后跑
// `tools/i18n extract` 与 `tools/i18n sync`。
package i18n

import (
	"context"
	"embed"
	"io/fs"

	modules "github.com/rzbdz/newgate/component"
	corei18n "github.com/rzbdz/newgate/lib/i18n"
	localeapi "github.com/rzbdz/newgate/modules/locale"
)

//go:embed catalogs/*.json
var catalogsFS embed.FS

// CatalogDir 是嵌入目录在 FS 里的路径。
//
// 它是**一处常量**而不是到处写的字面量：工具生成到哪、棘轮测试读哪、进程嵌哪，
// 必须是同一个地方——对不上的症状是「文件在那儿、跑起来却是空的」，而那要等到
// 某一句文案没被翻译时才看得见。
const CatalogDir = "catalogs"

// Name 是这个模块在装配表与 `newgate plugin` 里的名字。
const Name = "i18n"

// New 声明这个组件：启动时把发行版自己的账本与译文并进内核已经装好的语言。
//
// 它没有 Stop：追加是一次性的、没有需要撤销的所有权（`Extend` 加进去的是数据的
// 一份拷贝，不持有任何句柄）。这不是遗漏——组件框架只要求「拿到了什么就在 Stop
// 里还回去」，这里什么都没拿。
func New() modules.Component {
	return modules.Component{
		Name: Name,
		// 词汇表归产品层（见 component.Type 的注释）：文案属于基础设施那一层。
		Type:     "infra",
		Requires: []modules.Requirement{modules.Optional(localeapi.Capability)},
		Start:    start,
	}
}

func start(_ context.Context, ctx modules.Context) error {
	// 没有语言层就没有「这次说哪门语言」这回事：追加没有意义，而且**不该**有意义
	// ——源语言那条路径是恒等的，代码里写的那句话就是最终文本。
	if _, ok := modules.Get(ctx, localeapi.Capability); !ok {
		return nil
	}

	raw, err := fs.ReadFile(catalogsFS, CatalogDir+"/"+corei18n.LedgerName)
	if err != nil {
		// 账本就在这个包里嵌着，读不到只可能是打包错了——报出来，别静默。
		return err
	}
	led, err := corei18n.ParseLedger(raw)
	if err != nil {
		return err
	}
	cats, err := corei18n.CatalogsFromFS(catalogsFS, CatalogDir)
	if err != nil {
		return err
	}
	// 空目录是**合法**的（这个发行版今天还没有自己的文案）：什么都没得追加，
	// 而这与「追加成功」在行为上是同一件事——源语言那条路是恒等的。
	//
	// **这里刻意不写日志**：Start 是**每条 `newgate …` 命令**都会跑的（CLI 自己
	// 也装配一张图），在这里打一行等于给每条命令都加一句噪音。可见性在别处：
	// `newgate lang` 报覆盖率，doctor 报缺口。2026-09-20 实测踩过——一版里每次
	// 敲命令都先来一行 `[i18n] 225 messages appended…`。
	if err := corei18n.Extend(led, cats); err != nil {
		return err
	}
	return nil
}
