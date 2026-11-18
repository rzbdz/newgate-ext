package breaker

import (
	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/breaker/status"
)

// LatencySample 给出一条 binding 此刻该显示的那个延迟数字，以及它该上的颜色。
//
// ok=false 表示**没有样本**（从没探过、也没有真实流量经过这一条）：那种情况下界面
// 什么都不该画。画一个 `0ms` 是在说「它快得没有延迟」，而真相是「不知道」——这两句
// 话在一个用来**挑快的那条**的页面上差别很大。
//
// # 为什么这个映射要导出来、而不是留在 view.go 里
//
// 同一件事今天有两处界面在说：`breaker.health` 那张表（本包自己的 view.go），
// 以及首屏的路由总览（发行版的 modules/home——它同时看得见链与健康）。两份各写
// 一遍的结果是迟早分家，而分家的症状是**同一条 binding 在两个页面上颜色不一样**，
// 看的人只会去怀疑数据本身。
//
// 阈值不在这个函数里，也不在任何一个界面里：快 / 可用 / 慢是 `status.Grade` 的
// 判据，只有一处实现（3000 / 12000 写在 modules/breaker/status，那里同时也是 CLI
// 用的那一份）。颜色与 CLI 对齐（见 modules/config/commands.go 的
// bindingHealthLabel：绿 / 黄 / 红）——同一个进程的同一份数据，两个界面说同一件事。
//
// 三档**都**上色是刻意的：延迟是这张页面上唯一一个「扫一眼比大小」的数，
// 只给最慢的着色等于让用户去读每一格。
func LatencySample(b Status) (ms int64, tone string, ok bool) {
	if b.Checked.IsZero() {
		return 0, "", false
	}
	switch status.Grade(int(b.Latency), true) {
	case status.LatencyFast:
		return b.Latency, view.ToneOK, true
	case status.LatencyOK:
		return b.Latency, view.ToneWarn, true
	case status.LatencySlow:
		return b.Latency, view.ToneBad, true
	}
	// Grade 在 sampled=true 时到不了 LatencyUnknown。真到了这里说明它改了判据，
	// 那就**别上色**——宁可这一格没有颜色，也不要一个我们自己编出来的结论。
	return b.Latency, "", true
}

// BindingKey 是健康表的行 ID：`provider/model`。
//
// 为什么它要有一个名字：这个键是**跨模块的机器标记**——它既是表里的行 ID
// （view.go 的 healthTable），也是首屏把「链上的这一站」对到「健康表里的那条」
// 用的判据（发行版的 modules/home）。两边各拼一次就必须拼成同一个串，而拼错
// 的症状是「延迟那一格一直是空的」——不报错，只是永远没有数据。
func BindingKey(provider, model string) string { return provider + "/" + model }
