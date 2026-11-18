package gateway

import (
	"fmt"

	"github.com/rzbdz/newgate-ext/lib/style"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
	"github.com/rzbdz/newgate/lib/i18n"
)

// printThinkCache 展示推理内容缓存的命中情况（`newgate st` 末尾那几行）。
//
// **为什么住在这里**（2026-09-18 从 modules/cli 搬来）：推理缓存是数据面的
// 一部分（modules/gateway/thinkcache），「哪些计数值得看、命中和未命中分别意味
// 着什么」全是本模块的知识。它留在界面时，界面得在 Host 上开一个叫
// PrintThinkCache 的口子——那等于界面**按名字认识了一个模块的概念**，正是
// `probe` / `metrics` / `st` 这几个命令搬回来时要拆掉的东西。搬回来之后
// cliapi.Host 不再有一条以某个模块命名的方法。
//
// 数字从**跑着的守护进程**取：缓存在它内存里，命令跑在另一个进程，读本进程的
// thinkcache.Default 只会看到空缓存——那比不显示更误导人。取数走控制面叶子
// （controlplane.State()），与界面无关，任何模块都能读。
//
// 只报计数，永远不报内容。
func printThinkCache() {
	info, ps := controlplane.State()
	if info == nil || ps == nil {
		return // 守护进程没在跑：没有缓存可报
	}
	t := ps.Think
	fmt.Println()
	fmt.Println(style.Field(i18n.T("Reasoning cache", nil), i18n.N(
		"{n} entry · {used} / {max} · hits {hits} · misses {misses} · evictions {evictions}",
		"{n} entries · {used} / {max} · hits {hits} · misses {misses} · evictions {evictions}",
		t.Entries, i18n.A{"used": humanBytes(t.Bytes), "max": humanBytes(t.MaxBytes),
			"hits": t.Hits, "misses": t.Misses, "evictions": t.Evictions})))
	fmt.Println(style.Hint(i18n.T("Clients drop the reasoning content the upstream sends; the proxy stores it and puts it back as-is on the next turn", nil)))
	if t.Misses > 0 {
		fmt.Println(style.Hint(i18n.T("A miss can only be filled with an empty string (for those turns the model cannot see its own previous reasoning); every case is logged", nil)))
	}
	// 解析不出来的那些和「未命中」在结果上一样（都补空串），但处置完全不同：
	// 前者是客户端发来的 JSON 形态我们不认识，后者是缓存里真没有。混在一起报
	// 会把人引到错的地方，所以单独一行。
	if t.Unparsable > 0 {
		fmt.Println(style.Item(style.Warn, i18n.N(
			"Of these, {n} is an assistant message in the request that could not be parsed (not a cache problem, the shape the client sends has changed)",
			"Of these, {n} are assistant messages in the request that could not be parsed (not a cache problem, the shape the client sends has changed)",
			int(t.Unparsable), nil)))
	}
	fmt.Println(style.Hint(i18n.T("Memory + the thinkcache.bin cold layer; restored automatically after a restart, counts only, never content", nil)))
}

// humanBytes 把字节数写成 KB/MB。
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
