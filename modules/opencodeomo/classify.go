package opencodeomo

import "strings"

// ClassifyModel 把一个真实模型名归类到能力档（tier）。
//
// 这是 docs/07-clients-runtime.md 描述的档位启发式。命中都会被记录，
// 便于用户核对——**静默错配比失败更糟**：用 light 档跑本该 heavy 的任务，
// 用户只会觉得模型今天变笨了，完全无从追查。
func ClassifyModel(s string) (tier string, exact bool) {
	m := strings.ToLower(s)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.Contains(m, "fable"):
		return "heavy", true
	case strings.Contains(m, "opus"), strings.Contains(m, "-sol"),
		strings.Contains(m, "flagship"):
		// 四档化（2026-09-16）：opus 从最顶档降为**主力档** normal，
		// 顶上留给 fable。第三方名字里 "-sol" / "flagship" 这类
		// 「厂商最强」的同义词跟着 opus 一起走——它们原本就是按 opus
		// 的体格归的类，别因为档位改名就换体格。
		return "normal", true
	case strings.Contains(m, "gemini") && strings.Contains(m, "pro"),
		strings.Contains(m, "vision"):
		return "vision", true
	case strings.Contains(m, "sonnet"), strings.Contains(m, "terra"),
		strings.Contains(m, "medium"), strings.Contains(m, "-large"):
		return "mid", true
	case strings.Contains(m, "luna"), strings.Contains(m, "haiku"),
		strings.Contains(m, "flash"), strings.Contains(m, "lite"),
		strings.Contains(m, "mini"), strings.Contains(m, "small"):
		return "light", true
	}
	return "mid", false // 猜不出来归中档，但标记为非精确，调用方必须打日志
}
