package gateway

import (
	"fmt"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate/lib/i18n"
)

// schemaRepairCommand is `newgate schema-repair on|off`.
//
// **Why it lives here and not in modules/cli**: fixing tool schemas is one step of the gateway
// data plane (`schema.Repair`, run on the forwarding hot path), so "is it on" is gateway
// semantics and the command should come from gateway too. Same reason as specialCommand.
//
// Why the command is worth having: this layer **changes the user's request**.
// Some upstreams are very sensitive to a missing `required` in a tool schema,
// and adding an empty array lets the request through; but when checking
// "did newgate break this" you must be able to turn it off at a glance.
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
	// on|off must be given explicitly. **No argument must not mean off**: `truthy("")`
	// is false, so "type it once to see" silently turned repair off - I did exactly that
	// on 2026-09-18 (ran the bare command in a verification loop) and only noticed when
	// `newgate status` printed schema-repair=off. The default action must be "do nothing".
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

// truthy accepts on / 1 / true / yes. The same definition as on the CLI side - the command
// moved, so the criterion must move too, or `newgate schema-repair 1` would silently become "off".
func truthy(s string) bool { return s == "on" || s == "1" || s == "true" || s == "yes" }
