// Package buildinfo 是**链接期注入的构建信息**：版本号、构建时间、提交时间。
//
// # 为什么它是一个独立的叶子（2026-09-18）
//
// 这几个值原来只有 modules/cli 看得见（`cli.Version` 那一组包级变量），因为只有
// 界面在打印版本。但**守护进程也要打**——它启动时写的日志行 `newgate <版本> …
// 启动` 是排查「线上跑的是哪一版」的唯一线索（CLAUDE.md §0.1 就靠它判断线上
// checkout）。守护进程的主循环已经搬去 gateway，于是那几个值不能再锁在界面里。
//
// 放在 lib/ 而不是某个模块：它是**进程级事实**，不属于任何一个模块。组合根
// （main）注入一次，谁要谁读。
//
// 与「不静默」那条规矩的关系：版本号是用户判断「我这一发是不是新版本的行为」的
// 唯一依据，所以它必须到处都能拿到，而不是只有某一个命令打得出。
package buildinfo

import (
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// 由组合根通过 Set 注入（ldflags → main → 这里）。零值对开发者友好：make build
// 出来的本机二进制就该显示 dev。
var (
	version    = "dev"
	buildTime  = "unknown"
	commitTime = "unknown"
)

// Set 注入本次构建的信息。组合根在装配完成后调一次。
func Set(v, built, committed string) {
	if v != "" {
		version = v
	}
	if built != "" {
		buildTime = built
	}
	if committed != "" {
		commitTime = committed
	}
}

// Version 版本号（`newgate --version` 与守护进程启动日志都用它）。
func Version() string { return version }

// BuildTimeDisplay 构建时间的展示串。
func BuildTimeDisplay() string { return StampDisplay(buildTime) }

// VersionLine 是 `newgate version` 的三行输出。
//
// `newgate ` 那个前缀是程序名（机器标记），留在消息外面。
func VersionLine() string {
	return "newgate " + version +
		i18n.T("\n  built at {time}", i18n.A{"time": BuildTimeDisplay()}) +
		i18n.T("\n  committed at {time}", i18n.A{"time": StampDisplay(commitTime)})
}

// StampDisplay 把 ldflags 里的时间戳渲染成**一个**本地时间。
//
// 2026-09-18 之前界面里印两个时间（UTC 原文 + 换算出本地），因为 Makefile 用
// `date -u` 打 UTC，而 commitTime 走 git 的本地时间——同一行两个时区，看的人得
// 自己换算。现在 Makefile 统一打本地时区（`%z` 带偏移），这里也就只印一个。
// 解析不了（dev 构建没注入，值是 "unknown"）就原样回，不硬凑。
func StampDisplay(s string) string {
	if t, err := time.Parse("2006-01-02_15:04:05Z0700", s); err == nil {
		return t.Local().Format("2006-01-02 15:04:05 MST")
	}
	return pretty(s)
}

// pretty ldflags 传不了空格，用下划线占位，展示时换回。
func pretty(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '_' {
			r = ' '
		}
		out = append(out, r)
	}
	return string(out)
}
