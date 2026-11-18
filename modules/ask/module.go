// Package ask is the smallest thing on the road to "use newgate directly
// as a quick LLM backend": type a question, send it through **the proxy
// running on this machine**, and print only the answer's body text.
//
// # Why it is a module
//
// Same test as elsewhere (see "which side to change" in core/CLAUDE.md): would this still belong in
// another distribution? Yes -- it is the door through which the consumer side enters the gateway, a
// **mechanism** like `probe` / `metrics`, not some product's trade-off. So it lives in the kernel.
//
// It **does not send requests upstream itself**: going through the local proxy
// is precisely so the proxy's whole apparatus (tier chains, fallback, circuit breaking,
// upstream quirk patching) still applies on this road. Assembling it again by hand would
// copy all of that into a second place, and the copy would inevitably drift -- the user
// would get different behavior from `ask` and from claude, and nobody could explain it.
//
// # It knows no "model", only tiers
//
// The `model` in the request carries a **tier name** (`light` / `normal` / …),
// and the proxy resolves it per profile into which provider's which model. That is
// precisely this product's core abstraction, and `ask` has no reason to go around it.
package ask

import (
	"context"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// rankAsk decides where `ask` sits in the "run once" section (see cliapi.HelpLine.Rank).
// It comes after `run`: `run` starts a client, `ask` asks a question directly.
const rankAsk = 30

// New declares the ask component.
//
// There is only one weak edge (ui): mount the command if the UI is installed,
// skip it if not -- this module has no background behavior at all, and without the
// UI it is a component that "does nothing". This is the direct result of the rule in
// core/CLAUDE.md: **business modules depend on no ui**, and when the UI is absent no
// feature is lost (here "no loss" means: its only feature was ever this entry point).
func New() modules.Component {
	var release modules.Release
	return modules.Component{
		Name: "ask",
		Desc: func() string {
			return i18n.T("one prompt in, body text out — for scripts (newgate ask); reasoning goes to stderr", nil)
		},
		Type: "cli",
		Requires: []modules.Requirement{
			modules.Optional(cliapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			cli, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			var err error
			release, err = cli.RegisterCommand(askCommand{})
			return err
		},
		Stop: func(context.Context) error {
			if release == nil {
				return nil
			}
			return release()
		},
	}
}
