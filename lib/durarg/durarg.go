// Package durarg 管「用户敲进来的时长」这一件事：解析与回显。
//
// 为什么单独成包，而不是留在某个命令里：`newgate naked 30s` 与
// `newgate plugin <路径> off 2m` 是两条**不同模块**的命令，但它们接受的时间
// 写法必须是同一套——不然用户学会一个、在另一个上被拒，会以为是自己记错了。
// 放在 lib/ 下是唯一能同时满足「两边都用同一份」和「谁都不用认识谁」的位置：
// 它是纯工具，没有任何 newgate 业务概念，也不依赖任何模块。
//
// 不是模块（不违反 everything is module）：它没有生命周期、没有依赖、没有状态、
// 不是可插拔的东西，就是一个函数库。装配清单扫的是 `modules/`，这里本来也不在
// 那张图里。
package durarg

import (
	"fmt"
	"strings"
	"time"
)

// Parse 把用户输入转成 time.Duration。
//
// 除 time.ParseDuration 认的写法（30s / 2m / 1h）外，额外容两个口语后缀：
// "2min" / "5mins"。理由同上——用户会那么写，而拒绝它没有任何好处。
func Parse(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	norm := s
	switch {
	case strings.HasSuffix(norm, "mins"):
		norm = norm[:len(norm)-3]
	case strings.HasSuffix(norm, "min"):
		norm = norm[:len(norm)-2]
	}
	return time.ParseDuration(norm)
}

// Format 把秒数渲染成人看的短串（`45s` / `2m30s` / `1h20m`）。
//
// 上限只到小时：这些时长全是「等一会儿就恢复」的开关窗口，没有跨天的场景，
// 硬要塞个 `d` 只会让宽度失控。
func Format(sec int) string {
	d := time.Duration(sec) * time.Second
	if d < time.Minute {
		return d.String()
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}
