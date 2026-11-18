package webdashboard

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/url"

	modules "github.com/rzbdz/newgate/component"
)

// This file is the **ports the UI provides**: the skin registry (theme) and the extension
// endpoint registry (extension). Same shape: modules optionally depend, register in their
// Start, return on Stop; the UI knows no module.
//
// # Why skins are not hard-coded here
//
// Another implementation of the concept ledger's rule (core/lib/view): **the UI knows no
// skin**, only "there is a table, each entry gives CSS". Adding a skin = adding a directory,
// no change to this package or the front end. With every skin module removed the UI still
// works (the factory tokens in app.css) - the "everything works with no ui" sentence again.
//
// # Direction
//
// The arrow only goes "skin module → UI" (skin modules optionally depend on this port). Same
// as cli/extension: the UI must not depend on any skin, or "would the UI stand without it"
// has no answer.

// Theme is one skin.
type Theme struct {
	// ID is a machine tag, and the value stored in `state.json`. Not translated.
	//
	// Convention: a skin module's directory is `theme-<id>` (`theme-anthropic` → `anthropic`).
	// A convention, not mechanism - mechanism is **the directory name declared by the module**
	// (the scan rule), so a module named otherwise can still register a skin with ID `foo`.
	ID string
	// Name is the human-readable name (translatable).
	//
	// Closure: registration happens in each module's Start, when the language layer may not
	// be installed yet (see lib/view's Title - the same reason).
	Name func() string
	// Dark says whether this skin has a **dark ground**.
	//
	// It must be declared by the skin and sent to the front end, not guessed from color
	// brightness: some browser things are not CSS variables (scrollbars, the tick in an
	// `<input type=checkbox>`, autofill backgrounds) and follow `color-scheme`. Guessing wrong
	// looks like "a very light skin with a black scrollbar" - and nobody reports that as a bug.
	Dark bool
	// CSS is this skin's override of **design tokens**, of the form
	// `--bg: #000; --panel: #0b0b0b;`.
	//
	// Tokens only, **no selectors**: a skin changes a dozen variable values, not the layout.
	// Allowing selectors means letting a skin relayout the whole UI, and "who added this style"
	// then takes reading a string injected at runtime. The token list is app.css's `:root`; a
	// missing one does not error - it falls back to the factory warm value (ugly, but visible
	// ugly, better than silently swapping a palette).
	CSS string
}

// ThemeService is the skin registry.
//
// Same shape as view.Service's Register: hand in, get an undo, return on Stop in reverse.
// Registration happens in each module's Start, and **that order comes from the assembly graph**
// (skin modules optionally depend on this port, so they always come after this module).
type ThemeService interface {
	RegisterTheme(t Theme) (modules.Release, error)
}

// ThemeCapability is its name in the assembly graph.
var ThemeCapability = modules.NewCapability[ThemeService]("web-theme")

// ---------- extension endpoints ----------
//
// The BFF's concept ledger (core/lib/view) only holds things **drawable as a card**. Some
// features are not "a card": opening the model dropdown asks "what models does this
// upstream offer right now". It has no persistable shape, and should not touch the
// network on every snapshot (see the profileData note in config/view.go).
//
// So a second opening: **a name plus a function**. A module registers in its Start,
// the BFF passes the call through on `/api/ext/<name>`. It still **knows no module**:
// only "someone registered this name", like porthub only knowing "someone mounted
// this prefix".
//
// Direction is the same as skins and the concept ledger: only "module → UI". With no
// extensions the endpoint is always 404, and the UI falls back to data it can get.

// ExtensionRequest is all the input of one extension call.
//
// Body is the **raw** JSON: the registering module interprets it, the BFF does not parse it -
// the same rule as apply's `edit`. Context comes from this HTTP request; the extension's
// network calls follow its timeout/cancel (close the page, stop the upstream request).
type ExtensionRequest struct {
	Context context.Context
	Method  string
	Query   url.Values
	Body    json.RawMessage
}

// ExtensionHandler answers one extension call. The returned value is written as JSON (200);
// an error becomes a 400 shown to the UI (**not wrapped**: it is the extension's own words).
type ExtensionHandler func(ExtensionRequest) (any, error)

// ExtensionService is the extension endpoint registry.
//
// Same shape as ThemeService: hand in, get an undo, return on Stop. Duplicate names fail on
// the spot - when two modules claim one name only one gets the request, which shows up at
// runtime as "that other button does nothing", and nobody can see it from the UI.
type ExtensionService interface {
	Register(name string, h ExtensionHandler) (modules.Release, error)
}

// ExtensionCapability is its name in the assembly graph.
var ExtensionCapability = modules.NewCapability[ExtensionService]("web-extension")

// ---------- front-end plugins ----------
//
// The concept ledger (core/lib/view) gives **data plus one known drawing** (Kind); extension
// endpoints give **data**. Both need the front end to know that shape beforehand - so a
// module wanting UI **the front end does not know** used to add a component and teach it
// (that is how the fingerprint screen got in; see README's "who crossed the line"). The
// cost: module knowledge grew in the front end, and removing the module left it behind.
//
// This lets a module **bring its own front-end code**: embed an ES module (and any static
// files it needs) in its own binary section, register a name plus the **slot** it fills. The
// BFF serves those files under `/plugins/<name>/...` and a manifest under `/api/plugins`;
// the UI mounts each plugin into its slot. It knows only slots (its own layout) and the
// "what a plugin looks like" contract, no plugin - the same rule as Kind: **framework in
// front, modules fill it**.
//
// # The plugin JS contract (version 1)
//
//	export function mount(el, host) { ...; return () => { /* unmount */ } }
//
//   - el: an empty container from the UI; the plugin draws freely inside it;
//   - host.ext(name, body): POST `/api/ext/<name>` (paired with the module's endpoint),
//     returns JSON; rejects an Error on non-2xx (message is the BFF/extension's);
//   - host.asset(path): this plugin's own static file address;
//   - host.lang: the UI's current language tag.
//
// Styling uses the UI's design tokens (`var(--panel)`, `var(--ink)`, `var(--ok)`..., see
// app.css's `:root`), so a skin change carries the plugin along. Copy **comes from the
// plugin's own extension endpoint** (backend i18n.T, the same catalog as the CLI): the
// front end's catalog is the UI's, not the module's. A failed mount touches only its own cell.

// PluginSlot is a place on the UI layout where a plugin can mount.
type PluginSlot string

const (
	// SlotTopbar is a small cell at the right of the top bar, left of the skin button: a
	// button/badge; the panel it opens is drawn by the plugin (positioned relative to it).
	SlotTopbar PluginSlot = "topbar"
)

// pluginSlots is every slot the UI knows today. Registering another fails on the spot:
// accepted silently, the plugin appears nowhere and a typo looks like a load failure.
var pluginSlots = map[PluginSlot]bool{SlotTopbar: true}

// Plugin is front-end code a module ships itself.
type Plugin struct {
	// Name is a machine tag and a URL segment (`/plugins/<Name>/...`). Charset as extensions.
	Name string
	// Assets are the plugin's files (usually the module's `//go:embed web`).
	Assets fs.FS
	// Entry is the path of the ES module inside Assets (it exports mount).
	Entry string
	// Slots are the slots it fills.
	Slots []PluginSlot
}

// PluginService is the front-end plugin registry. Same shape as ExtensionService.
type PluginService interface {
	RegisterPlugin(p Plugin) (modules.Release, error)
}

// PluginCapability is its name in the assembly graph.
var PluginCapability = modules.NewCapability[PluginService]("web-plugin")
