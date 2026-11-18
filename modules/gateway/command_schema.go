package gateway

import (
	"fmt"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate/lib/i18n"
)

// schemaRepairCommand 是 `newgate schema-repair on|off`。
//
// **它住在这里而不是 modules/cli**：工具 schema 的修补是网关数据面的一步
// （`schema.Repair`，跑在转发热路径上），所以「有没有开」是 gateway 的语义，
// 命令也该由 gateway 提供。理由与 specialCommand 同一条。
//
// 为什么值得有这个命令：这一层会**改用户的请求**。某些上游对工具 schema 的
// `required` 缺省极敏感，补一个空数组能让请求过去；但排查「是不是 newgate 改坏的」
// 时必须能一眼关掉它。
type schemaRepairCommand struct{}

var (
	_ cliapi.Command    = (*schemaRepairCommand)(nil)
	_ cliapi.Documented = (*schemaRepairCommand)(nil)
)

func (schemaRepairCommand) Names() []string { return []string{"schema-repair", "schema_repair"} }

func (schemaRepairCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionMaintenance, Rank: rankMaintenance,
		Usage:   "schema-repair on|off",
		Summary: i18n.T("Add an empty array when a tool schema lacks required", nil),
	}
}

func (schemaRepairCommand) Run(host cliapi.Host, args []string) int {
	arg := cliapi.Positional(args, 0)
	// 必须显式给 on|off。**不带参数不能当成 off**：`truthy("")` 是 false，
	// 于是「敲一下看看」会静默关掉修补——2026-09-18 我自己就这么关掉过一次
	// （一个验证循环里裸跑了这条命令），而且直到 `newgate status` 打出
	// schema-repair=off 才发现。默认动作必须是「什么都不做」。
	if arg == "" {
		return host.Die(64, i18n.T("Usage: newgate schema-repair on|off (no argument changes anything)", nil))
	}
	on := truthy(arg)
	if err := gatewaystate.SetSchemaRepair(on); err != nil {
		return host.Die(70, err.Error())
	}
	if on {
		fmt.Println(style.Item(style.OK, "schema repair on") +
			style.Dim(i18n.T("   Adds an empty array when a tool schema lacks required", nil)))
	} else {
		fmt.Println(style.Item(style.Skip, "schema repair off") +
			style.Dim(i18n.T("   Tool schemas are forwarded unchanged", nil)))
	}
	host.NotifyProxy()
	return 0
}

// truthy 认得 on / 1 / true / yes。与 CLI 那侧同一个定义——命令搬过来了，
// 判据也得跟着搬，否则 `newgate schema-repair 1` 会静默变成「关」。
func truthy(s string) bool { return s == "on" || s == "1" || s == "true" || s == "yes" }
