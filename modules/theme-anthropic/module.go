// Package themeanthropic is the anthropic palette: warm off-white ground + terracotta accent.
//
// # Where it came from (changed once, 2026-09-29)
//
// This set **used to be the factory token set** (web/src/app.css's `:root` and its light
// branch), copied from cc-switch's reference UI: warm beige/brown ground, warm orange accent,
// and borders, quiet ground and secondary text all on one orange-yellow hue (H≈30-38).
//
// The user said: "the follow-system theme is the anthropic one; the follow-system default
// should be black and white" - that warm tone is Anthropic's, not a UI's **factory** look
// (factory means "do not pick one for me"). So it moved here; the factory went neutral b/w.
//
// The earlier version here was a hex set I made up from Anthropic brand values (`#faf9f5` /
// `#d97757`); after seeing it the user said "wrong colors, drop it". The lesson: **"a
// vendor's brand color" is not "the warm set this UI has always used"** - and the latter counts.
//
// # Why only the light half
//
// A skin declares **one** variant (see webapi.Theme.CSS), so that **warm dark** set (the
// original `:root`, warm brown ground) did not come along - it has nowhere to go. Keeping
// it needs the protocol changed to "one skin, two halves", which is a separate job.
package themeanthropic

import (
	"context"

	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Name is this module's name in the assembly table and in `newgate plugin` (= the dir name).
const Name = "theme-anthropic"

// ID is this skin's machine tag (it is what gets stored in state.json).
//
// It is a brand name, so **not translated** (vs Theme.Name: humans vs a key).
const ID = "anthropic"

// New declares this component: register this skin when the UI is there, do nothing when not.
// Why Optional rather than Requires: see the same passage in modules/theme-dark.
func New() modules.Component {
	return modules.Component{
		Name:     Name,
		Desc:     func() string { return i18n.T("the Anthropic brand palette for the web interface", nil) },
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
		Name: func() string { return i18n.T("Anthropic", nil) },
		// Off-white ground = light. The UI sets `color-scheme` to light from it, so scrollbars,
		// checkboxes and autofill follow - those are not CSS variables (see webapi.Theme.Dark).
		Dark: false,
		CSS: `
  --bg: hsl(38 28% 91%);
  --panel: hsl(35 25% 89%);
  --panel-2: hsl(35 20% 85%);
  --line: hsl(35 18% 78%);
  --ink: hsl(30 15% 15%);
  --dim: hsl(30 12% 40%);
  --accent: hsl(15 65% 48%);
  --ok: hsl(142 55% 32%);
  --warn: hsl(32 75% 40%);
  --danger: hsl(0 62% 45%);
`})
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
