package archmap

import (
	"sort"
	"time"

	app "github.com/rzbdz/newgate/app"
	modules "github.com/rzbdz/newgate/component"
)

// Build 把整份报告装出来：**每份规格书一张装配图 + 一张 import 图**。
//
// 它原来是测试里的一段（生成产物那一步），提到包里是因为现在有第二个调用方：
// `modules/arch-diagram` 把它挂到共享端口上给浏览器看。两份实现会长歪的地方很
// 具体——规格书怎么枚举、图注怎么写、哪一步失败了怎么降级——所以只留一份。
//
// **规格书表是参数，不是 import**：生成的那份清单 import 了每一个模块，而本包被
// 某个模块 import（arch-diagram）——本包再去 import manifest 就是编译环，实测
// 撞上过。表由组合根递下来（见 core/app 的 SpecAware）。
//
// root 是**源码树的根**（`go list` 要在那里跑）。装配那一半不需要它。
func Build(specs map[string]app.Selection, root string) (Report, error) {
	report := Report{GeneratedAt: time.Now().Format(time.RFC3339)}

	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sel := specs[name]
		// 真跑一遍解析（不 Start 任何模块）：这就是「resolve 出来的」那张图。
		g, err := ResolveGraph(name, name, DistributionNote(sel), sel)
		if err != nil {
			return report, err
		}
		report.Specs = append(report.Specs, g)
	}

	report.Imports = importHalf(root)
	return report, nil
}

// BuildCatalog 是**在线**那一份报告：只有一张装配图——**这个进程此刻装着什么**
// （见 CatalogGraph），加上 import 扫描。
//
// 为什么在线版不画「每份规格书一张」：那张表长在模块们的上面（生成的清单 import
// 了每个模块），所以挂在线上的模块**看不见**它——这就是个编译环，实测撞上过。
// 而「这里现在装着谁」本来就是这个进程最该回答的问题，比「还有哪些装配编得出来」
// 更贴近正在发生的事。
func BuildCatalog(id, title, note string, comps []modules.Component, root string) Report {
	return Report{
		GeneratedAt: time.Now().Format(time.RFC3339),
		Specs:       []Graph{CatalogGraph(id, title, note, comps)},
		Imports:     importHalf(root),
	}
}

// importHalf 是两张报告共用的那一半：扫源码树里的 import。
func importHalf(root string) Graph {
	if root == "" {
		// 没有源码树（部署出去的二进制就是这样）：**不猜**当前目录。猜的话，
		// `go list` 会在一些不相干的目录里跑出一些看起来正常的包名，图上于是
		// 出现一堆没人认识的节点——那比缺半张图糟得多。
		return Unavailable("no source tree given, so imports were not scanned", nil)
	}
	g, err := ImportGraph(root)
	if err != nil {
		// 扫不了源码不是「报告坏了」：装配那一半照样有价值（「这个构建里装了什么」
		// 在任何机器上都问得出来），而 import 那一半需要源码树与 go 工具链。
		// 所以这里不返回错误，把那半张图写成**一句解释**——空图会让人以为没有依赖。
		return Unavailable("the import scan needs the source tree and the go toolchain", err)
	}
	return g
}

// DistributionNote 是悬停/概览里那行说明：这份规格书**装了什么、关了什么**。
//
// 一张装配图最有用的上下文就是这个——同样的模块表，`"disable": ["*"]` 的骨架
// 发行版画出来是十几个点，旗舰版是三十几个，看图的人得先知道自己在看哪一份。
func DistributionNote(sel app.Selection) string {
	parts := []string{plural(len(sel.Extra), "own module", "own modules")}
	switch {
	case sel.AllCore:
		parts = append(parts, "all built-in kernel modules disabled")
	case len(sel.Disable) > 0:
		parts = append(parts, "kernel modules disabled: "+join(sel.Disable, ", "))
	default:
		parts = append(parts, "all built-in kernel modules installed")
	}
	return join(parts, " · ")
}

// Unavailable 是「这一半没画出来，因为……」的那张空图。
func Unavailable(why string, err error) Graph {
	note := why
	if err != nil {
		note += ": " + err.Error()
	}
	return Graph{
		ID:    "imports",
		Title: "imports (not scanned)",
		Note:  note,
	}
}
