package cli

import (
	ext "github.com/rzbdz/newgate-ext/modules/cli/extension"
)

// 本文件是**契约的类型别名转发层**。
//
// 契约本体住在 modules/cli/extension（叶子包），理由写在那个包的注释里：模块若
// 为了拿 `Command` 接口去 import 这个重包（它拖着 runtime / gateway / breaker），
// runtime 与 gateway 的测试就再也引用不了客户端模块，gateway 也注册不了自己的命令。
//
// **账本本身在本包的 service 上**（见 module.go）：界面持有三本账，模块通过
// RegisterXxx 把回调注入进来，界面循环调用拿数据。界面不 import 任何模块。
//
// 本包内的代码继续用这些短名字，不必到处写 ext.。
type (
	Diagnostic         = ext.Diagnostic
	DiagnosticProvider = ext.DiagnosticProvider
	StatusLine         = ext.StatusLine
	StatusProvider     = ext.StatusProvider
	StatusBlock        = ext.StatusBlock
	BlockProvider      = ext.BlockProvider
	Host               = ext.Host
	Command            = ext.Command
	HelpLine           = ext.HelpLine
	Documented         = ext.Documented
	Handoff            = ext.Handoff
	Unstyled           = ext.Unstyled
	Dumper             = ext.Dumper
	DumpSection        = ext.DumpSection
	Glossarist         = ext.Glossarist
	Verbose            = ext.Verbose
	GlossaryLine       = ext.GlossaryLine
	CLI                = ext.CLI
)

// Capability 是界面的端口身份（定义连同 CLI 接口在 extension 包）。
//
// 一个 capability 同时服务两件事：模块用它把命令/诊断/状态行注入进来
// （RegisterXxx），诊断路径用它读界面的账本。**入口不再经它**：界面在 Start 里
// 往 root 的入口账本申报（见 module.go 的 Start），组合根问账本而不是问本包。
var Capability = ext.Capability

// 帮助屏的槽位词表转发（定义在 extension，理由见那边：它是**呈现概念**，
// 而呈现的契约归 extension 这个叶子，不归某个模块）。
type Section = ext.Section

const (
	SectionTakeover    = ext.SectionTakeover
	SectionRunOnce     = ext.SectionRunOnce
	SectionRouting     = ext.SectionRouting
	SectionObserve     = ext.SectionObserve
	SectionMaintenance = ext.SectionMaintenance
	SectionModules     = ext.SectionModules
	SectionUI          = ext.SectionUI
	SectionOthers      = ext.SectionOthers
)

// SectionPlan 是一批节名到实际显示的节的映射 + 显示顺序（规则见 extension）。
type SectionPlan = ext.SectionPlan

// PlanSections 按通用槽位 / 自定义名额 / others 兜底的规则做一次规划。
func PlanSections(declared []Section) SectionPlan { return ext.PlanSections(declared) }
