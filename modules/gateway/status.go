package gateway

import (
	"strings"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	"github.com/rzbdz/newgate/lib/i18n"
)

// switchStatus reports this module's switches (schema repair, special layer, fallback chain,
// debug) to `newgate status`.
//
// **Why gateway reports it itself instead of letting the cli read state**: these keys now live
// in state.json's ModuleConfig["gateway"] (see the gatewaystate package comment) and are
// gateway's own vocabulary; if the cli parsed them for this row it would just be getting to
// know gateway's internals somewhere else. Whoever owns the state reports it - the same port
// as plugin-manager reporting its switch ledger (RegisterStatus).
//
// Only those **off the factory state** are listed: patches, the special layer and fallback are
// on by default and debug is off by default, so a line saying "same as factory" takes space
// and carries no information, consistent with the status screen's purpose of "seeing at a
// glance whether anything was turned off".
//
// **fallback=off especially must be reported**: what it turns off is "changing parties", and
// the symptom only shows **when an upstream is down** (normally requests are 200 as usual). A
// user who forgets they flipped this switch would misread "no fallback" as "nobody on the
// chain to switch to" - so this row is their only clue.
// The status row's position (smaller Rank first). These rows come before takeover/config on
// the status screen - if the proxy is down, every tool going through newgate is down with it,
// so it comes first.
const rankStatusPatch = 40 // sits after the proxy row (10)

type switchStatus struct{}

var (
	_ cliapi.StatusProvider = switchStatus{}
	_ cliapi.Verbose        = switchStatus{}
)

// Status see the type note. When st is nil nothing is reported (no snapshot installed yet).
//
// Besides this module's own three switches, this also **relays** the status items plugins
// report themselves (special.StatusProvider): that layer's registry belongs to this module and
// plugins are part of it, so this module serves them to the cli. The cli therefore need not
// import gateway/special to display "what a plugin says it is right now" - whoever owns the
// state reports it, and this layer is collected by its owner.
func (switchStatus) Status() []cliapi.StatusLine {
	st := store.LoadState()
	var out []cliapi.StatusLine
	for _, item := range special.Statuses(st) {
		out = append(out, cliapi.StatusLine{Rank: rankStatusPatch, Label: item.Label, Value: item.Value})
	}

	var parts []string
	// Report a parse failure first: it means **every switch below is a fallback**, not the
	// user's current choice. Left unsaid, a patch the user turned off might be silently on
	// while this screen looks perfectly normal (it reads the same Parse) - see the note on
	// gatewaystate.Parse.
	if cfg := gatewaystate.Parse(st); cfg.ParseErr != nil {
		parts = append(parts, style.Red(i18n.T("Failed to parse the gateway section of state.json; every switch fell back to its default (the change may not have taken effect): {err}",
			i18n.A{"err": cfg.ParseErr.Error()})))
	}
	switch {
	case gatewaystate.DebugActive(st):
		s := "debug=on"
		if until := gatewaystate.DebugUntilDisplay(st); until != "" {
			s += " " + i18n.T("(until {until})", i18n.A{"until": until})
		}
		parts = append(parts, s)
	case gatewaystate.Parse(st).Debug != nil && *gatewaystate.Parse(st).Debug:
		parts = append(parts, i18n.T("debug=expired", nil))
	}
	if gatewaystate.FallbackOff(st) {
		parts = append(parts, "fallback=off")
	}
	if !gatewaystate.RepairEnabled(st) {
		parts = append(parts, "schema-repair=off")
	}
	switch {
	case !gatewaystate.SpecialEnabled(st):
		parts = append(parts, "special_treatment=off")
	case len(gatewaystate.Parse(st).SpecialOff) > 0:
		parts = append(parts, i18n.T("special off: {list}", i18n.A{"list": strings.Join(gatewaystate.Parse(st).SpecialOff, ",")}))
	}
	if len(parts) > 0 {
		out = append(out, cliapi.StatusLine{
			Rank:  rankStatusPatch,
			Label: i18n.T("Patch switches", nil),
			Value: strings.Join(parts, "   ") +
				i18n.T("   restore: newgate st on · newgate schema-repair on · newgate fallback on · newgate debug off", nil),
		})
	}
	return out
}

// Verbose reports "the full request log is on".
//
// The UI uses it to decide whether to run a layout self-check on its own output (see
// cliapi.Verbose and layout_audit in modules/cli): with debug on the user is watching details,
// and output alignment is part of those details.
//
// This check used to be the UI reading gateway's state section directly - the UI knowing
// someone else's config structure.
func (switchStatus) Verbose() bool { return gatewaystate.DebugActive(store.LoadState()) }
