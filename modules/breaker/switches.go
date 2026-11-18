package breaker

import (
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/pluginmanager"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This module's runtime switch points.
//
// There is one, and it exists for a scene the health table invented and then could not get out
// of (2026-11-18): **a network wobble trips every binding at once**. Availability is
// "two consecutive failures / 60s" (see DefaultPolicy), which is right per binding and wrong for
// a shared cause -- one dropped link produces two failures on every binding in the chain, the
// whole table goes out, and once the link is back the bindings stay out until their cooldown
// expires (up to 10 minutes). The user's own words: "after a network problem is fixed, let me
// disable the breaker with one command".
//
// So this is a kill switch for the **decision**, not for the ledger: turning it off makes every
// binding admissible again immediately and wipes what is currently delisted; the table keeps
// recording, so `newgate breaker` still shows who is failing and why.
//
// Why not "suspend for 5 minutes" (a TTL): the trigger is external and its end is not knowable
// from here. The user decides when the network is back, so the switch stays off until they say
// otherwise -- and `DangerSafe` says exactly that: being off costs the delisting protection, not
// correctness.
const SwitchAdmission = "breaker.admission"

// Switches is this module's switch point list, for module.go to register.
//
// The text is computed here rather than in a package-level var for the same reason as the other
// modules': package init runs before languages are installed, while Switches runs once in Start.
func Switches() []pluginmanager.Switch {
	return []pluginmanager.Switch{
		{
			Path:  SwitchAdmission,
			Title: i18n.T("Health table (delisting)", nil),
			Why: i18n.T("turned off, every binding is admitted again and the delisted ones are "+
				"cleared — the table keeps counting what fails, it just stops steering the "+
				"chain. Use it when one network wobble has emptied the whole table and you know "+
				"the upstreams are fine", nil),
			Danger:  pluginmanager.DangerSafe,
			Default: true,
		},
	}
}

// paused reports whether the switch is currently off. A nil snapshot means "nothing set", i.e.
// not paused -- the same fail-open direction as every other switch lookup (do not stop delisting
// because a file could not be read).
func paused(st *domain.State) bool {
	return pluginmanager.Off(st, SwitchAdmission)
}
