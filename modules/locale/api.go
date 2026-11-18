// Package locale 决定**这次进程用哪门语言**，并把目录装进 lib/i18n。
//
// 分工要看清：查表、占位符、复数、语言标签匹配都在叶子 lib/i18n 里（无状态、
// 谁都能引）；这里只做一件有状态的事——**解析**：环境变量、state.json 里的持久化
// 选择、以及系统 locale 三者谁说了算。它是可摘的：摘掉之后 lib/i18n 没有目录，
// 每句话都原样输出源语言文本，进程照常。
package locale

import (
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Source 说明这次的语言是**从哪来的**。为什么要把来源报出来：`newgate lang zh-Hans`
// 写进配置之后，如果当前 shell 的 `LANG` 压着它，用户会觉得「我明明设了怎么没用」。
// 来源是那句话的答案——doctor 与 `newgate lang` 都打它。
type Source string

const (
	// SourceEnvOverride 会话级显式覆盖（`NEWGATE_LANG=zh-Hans newgate status`）。
	SourceEnvOverride Source = "NEWGATE_LANG"
	// SourceConfig 配置里持久化的选择（`newgate lang zh-Hans` 写下的）。
	SourceConfig Source = "config"
	// SourceSystem 跟随系统（LC_ALL / LC_MESSAGES / LANG）。
	SourceSystem Source = "system"
	// SourceDefault 谁都没说 → 源语言。
	SourceDefault Source = "default"
)

// Service 是「这次用哪门语言」这件事的所有权端口。
type Service interface {
	// Lang 当前生效的语言 tag（**实际**用的，回退之后的那一个）。
	Lang() string
	// Requested 这次要求的是哪门语言（可能与 Lang 不同：要求了一门没翻的）。
	Requested() string
	// Source 这个选择是从哪来的。
	Source() Source
	// Available 可选语言的体检结果（覆盖率、机翻计数）。
	Available() []i18n.Info
}

// Capability 是这个端口的唯一身份。
var Capability = modules.NewCapability[Service]("locale")
