package locale

import "github.com/rzbdz/newgate/lib/i18n"

// 语言从哪来——优先级从高到低，第一条「有意见」的说了算：
//
//  1. NEWGATE_LANG                      会话级显式覆盖（测试、脚本、临时切换）
//  2. state.json 的 locale.lang         持久化选择（`newgate lang zh-Hans`）
//  3. LC_ALL → LC_MESSAGES → LANG       跟随系统（POSIX 的惯例）
//  4. 源语言                            谁都没说
//
// **为什么配置排在系统 locale 之上**（这是一个取舍，不是随手排的）：很多开发者
// 用英文系统、但想读中文，反过来也有。POSIX 的 `LANG` 表达的是「这台机器的
// 默认语言」，「配置」表达的是「**我要**读什么」——后者更明确，所以后者赢。
// 代价是 `newgate lang` 在 `LANG=en_US.UTF-8` 的 shell 里可能看不出效果，
// 所以那条命令**必须把来源打出来**（见 command.go），让人知道是谁在说话。
//
// 想反过来（环境优先）就调换这两条的次序——只此一处，有测试钉着。
type request struct {
	Tag    string // 原始要求，可能是空串或 `zh_CN.UTF-8` 这种写法
	Source Source
}

func resolve(getenv func(string) string, configured string) request {
	if v := getenv("NEWGATE_LANG"); v != "" {
		return request{Tag: v, Source: SourceEnvOverride}
	}
	if configured != "" {
		return request{Tag: configured, Source: SourceConfig}
	}
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := getenv(key); v != "" {
			if tag := i18n.Normalize(v); tag != "" { // `C` / `POSIX` = 没意见
				return request{Tag: tag, Source: SourceSystem}
			}
		}
	}
	return request{Tag: i18n.SourceLang, Source: SourceDefault}
}
