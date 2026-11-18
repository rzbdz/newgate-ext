package pluginmanager

import (
	"fmt"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/lib/durarg"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Status contributes the "module switches" line in `newgate status`: only ones off factory.
//
// Why this module reports it itself instead of letting cli read the ledger: that is the
// step that unties the cycle - if cli Needed this module for this line, this module could
// no longer Need(cli) for its own command (cli -> pluginmanager -> cliapi -> cli is a
// cycle), so `newgate plugin` would live in cli, which would have to know this module.
//
// Shown apart from cli's core switch line (debug / schema-repair / special_treatment):
// those are fields on domain.State, not part of this module's ledger.
func (c *command) Status() []cliapi.StatusLine {
	st := store.LoadState()
	var parts []string
	for _, mod := range c.manager.Modules() {
		for _, sw := range mod.Switches {
			enabled := switchEnabled(st, sw)
			if enabled == sw.Default {
				continue // 还是出厂态，不占版面
			}
			word := "=on"
			if !enabled {
				word = "=off"
			}
			label := sw.Path + word
			if until := Remaining(st, sw.Path); !until.IsZero() {
				label += fmt.Sprintf("(%s)", durarg.Format(int(time.Until(until).Seconds())))
			}
			// Footguns in red: that is the "turning it off breaks correctness" tier, and in a
			// one-line output it must be recognizable at a glance.
			parts = append(parts, dangerColor(sw.Danger)(label))
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return []cliapi.StatusLine{{
		Label: i18n.T("Module switches", nil),
		Value: strings.Join(parts, "   "),
	}}
}

// switchEnabled says whether a switch point is currently on. Polarities come from the
// factory state: factory-on (kill switch) reads Off(), factory-off (mode) reads On().
func switchEnabled(st *domain.State, sw Switch) bool {
	if sw.Default {
		return !Off(st, sw.Path)
	}
	return On(st, sw.Path)
}
