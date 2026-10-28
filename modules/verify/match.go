package verify

import "strings"

// 本文件是「指纹说的是谁」与「binding 写的是谁」之间的比较。
//
// # 为什么不能直接比字符串
//
// 参考库的 ID 用点分版本号（`claude-haiku-4.5`、`gpt-5.6-terra`），而上游的
// 模型名用连字符（`claude-haiku-4-5`），还常常带一个日期快照后缀
// （`deepseek-v4.1-flash-1017`）。直接比会把同一件事报成失配——而这条警告一旦
// 经常误报，人就会学会忽略它，真正换模型的时候也没人看了。
//
// 所以先做一次**保守的归一化**：只留字母和数字，去掉分隔符。剩下的判断按
// 「同一性强度」分档（见 api.go 的 Match）：
//
//	完全相同            match
//	只多一个 4 位以上数字  dated（同一个模型的日期/快照版）
//	同族                family
//	其余                mismatch
//
// 日期后缀要求至少 4 位是刻意的：`gpt-5` 与 `gpt-5.5` 去分隔符后是 `gpt5` 与
// `gpt55`，多出来的只有一位数字——那是**另一个模型**，不是快照版。把一位数字
// 当日期会让「5.5 冒充 5」悄悄变成 match。

// Classify 给出 requested 与 predicted 的关系。predicted 为空 = 这一轮没有排名。
func Classify(requested, predicted, family string) Match {
	if strings.TrimSpace(predicted) == "" {
		return MatchUnknown
	}
	r, p := normalize(requested), normalize(predicted)
	if r == "" || p == "" {
		return MatchUnknown
	}
	if r == p {
		return MatchOK
	}
	if datedVariant(r, p) {
		return MatchDated
	}
	f := normalize(family)
	// 只看 **requested** 是否属于这个家族：predicted 当然属于它自己的家族
	// （否则参考库不会给这个 family），拿它去比等于「任何预测都算同族」。
	if f != "" && strings.HasPrefix(r, f) {
		return MatchFamily
	}
	return MatchMismatch
}

// normalize 只留字母与数字，并转小写。
func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// datedVariant：短的那个是长的那个的前缀，且多出来的部分全是数字、至少 4 位。
func datedVariant(a, b string) bool {
	short, long := a, b
	if len(short) > len(long) {
		short, long = long, short
	}
	if !strings.HasPrefix(long, short) {
		return false
	}
	extra := long[len(short):]
	if len(extra) < 4 {
		return false
	}
	for _, r := range extra {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
