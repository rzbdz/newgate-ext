// Package themelight is the "white" skin: pure white ground + neutral grey + deep blue accent.
//
// # Why it is not "the factory set's light version"
//
// The light half of the factory tokens (web/src/app.css's `prefers-color-scheme: light`) is
// **warm beige**, two exposures of the same hue as its dark half. This one goes pure white +
// neutral grey on purpose: it is for "readable on a projector" and "window open, screen
// glare" - places that want contrast, not atmosphere.
//
// It is the other side of the same deck as theme-dark (same neutral scale, same blue):
// switching black to white changes **only brightness** - nothing moves, no meaning changes.
package themelight

import (
	"context"

	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Name is this module's name in the assembly table and in `newgate plugin` (= the dir name).
const Name = "theme-light"

// ID is this skin's machine tag (it is what gets stored in state.json).
const ID = "light"

// New declares this component: register this skin when the UI is there, do nothing when not.
// Why Optional rather than Requires: see the same passage in modules/theme-dark.
func New() modules.Component {
	return modules.Component{
		Name:     Name,
		Desc:     func() string { return i18n.T("a white theme for the web interface", nil) },
		Type:     "cli",
		Requires: []modules.Requirement{modules.Optional(webapi.ThemeCapability)},
		Start:    start,
		Stop:     stop,
	}
}

var release modules.Release

func start(_ context.Context, ctx modules.Context) error {
	port, ok := modules.Get(ctx, webapi.ThemeCapability)
	if !ok {
		return nil
	}
	rel, err := port.RegisterTheme(webapi.Theme{
		ID:   ID,
		Name: func() string { return i18n.T("White", nil) },
		Dark: false,
		// Semantic colors are **deeper** than the dark set's (the same green must be a notch
		// darker on white to read) - not copyable: a copied value makes ok/warn invisible on white.
		CSS: `
  --bg: hsl(0 0% 100%);
  --panel: hsl(0 0% 98%);
  --panel-2: hsl(0 0% 95%);
  --line: hsl(0 0% 86%);
  --ink: hsl(0 0% 8%);
  --dim: hsl(0 0% 42%);
  --accent: hsl(212 88% 42%);
  --ok: hsl(142 62% 28%);
  --warn: hsl(32 88% 34%);
  --danger: hsl(0 68% 44%);
`,
	})
	if err != nil {
		return err
	}
	release = rel
	return nil
}

func stop(context.Context) error {
	if release != nil {
		_ = release()
		release = nil
	}
	return nil
}
