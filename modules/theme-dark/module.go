// Package themedark is the "black" skin: true black ground + neutral grey + blue accent.
//
// # How it relates to the factory set
//
// The factory tokens (web/src/app.css's `:root`) are **warm** - brownish ground, warm orange
// accent. This one goes the other way on purpose: true black (`hsl(0 0% 0%)`, no light on
// OLED), neutral grey mid layers, blue accent. Not "one set at two brightnesses" but two
// registers: factory is paper, this is a terminal. Picked for night use, not system light/dark.
package themedark

import (
	"context"

	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Name is this module's name in the assembly table and in `newgate plugin` (= the dir name).
const Name = "theme-dark"

// ID is this skin's machine tag (it is what gets stored in state.json).
//
// It differs from the directory name by a `theme-` prefix: the directory name should make
// "this is a skin" obvious (see webapi.Theme.ID), while the ID is for people and payloads.
const ID = "dark"

// New declares this component: register this skin when the UI is there, do nothing when not.
//
// # Why Optional (and **not** Requires)
//
// Same as modules/i18n depending on the language layer: with no UI installed (the skeleton
// distribution, or any assembly but `dist-dashboard.json`) this module **installs and loses
// nothing** - all it does is put a CSS block into the UI's table; nobody taking it means
// nothing happened. If it could not install, a pure skin module would drag the graph down.
//
// Declaring the dependency Optional also buys **start order**: with the UI present it always
// comes after the UI (that edge is an ordering edge), so registration follows the registry.
func New() modules.Component {
	return modules.Component{
		Name: Name,
		Desc: func() string { return i18n.T("a true-black theme for the web interface", nil) },
		// Type is "cli": same class as web-dashboard / tui / simple-cli (the existing value
		// for "UI shell"). A skin is part of the UI, not a business module.
		Type:     "cli",
		Requires: []modules.Requirement{modules.Optional(webapi.ThemeCapability)},
		Start:    start,
		Stop:     stop,
	}
}

// release is the undo handle returned at registration. **There is only one module instance**
// (New runs once per assembly), so one package-level variable suffices - no lock needed.
var release modules.Release

func start(_ context.Context, ctx modules.Context) error {
	port, ok := modules.Get(ctx, webapi.ThemeCapability)
	if !ok {
		return nil // no UI in this assembly: nowhere to register, and that is not an error.
	}
	rel, err := port.RegisterTheme(webapi.Theme{
		ID:   ID,
		Name: func() string { return i18n.T("Black", nil) },
		Dark: true,
		// Tokens only, no selectors (see webapi.Theme.CSS). Missing one does not error - it falls
		// back to the factory value, and a half-finished skin is ugly in a way its author can see.
		CSS: `
  --bg: hsl(0 0% 2%);
  --panel: hsl(0 0% 6%);
  --panel-2: hsl(0 0% 11%);
  --line: hsl(0 0% 18%);
  --ink: hsl(0 0% 94%);
  --dim: hsl(0 0% 60%);
  --accent: hsl(212 92% 62%);
  --ok: hsl(142 52% 54%);
  --warn: hsl(38 92% 60%);
  --danger: hsl(0 72% 64%);
`,
	})
	if err != nil {
		return err
	}
	release = rel
	return nil
}

// stop takes the registration back (the framework only asks that you return what you took).
//
// It looks like a formality - the process is exiting, so who cares? The difference is
// **tests and embedding**: testing/testkit assembles and stops graphs over and over in one
// process; skipping it hits "theme dark is already registered", which reads as a broken skin.
func stop(context.Context) error {
	if release != nil {
		_ = release()
		release = nil
	}
	return nil
}
