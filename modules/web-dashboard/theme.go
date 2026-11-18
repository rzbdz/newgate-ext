package webdashboard

import (
	"encoding/json"
	"sort"
	"sync"

	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This file is the theme registry (shape in api.go), plus "which one this machine chose".
//
// # Why which-one-is-chosen is this module's state
//
// Same rule as the gateway switches (core/modules/gateway/gatewaystate): **whoever owns
// the state hosts it**, in state.json's `ModuleConfig[Key]`; core does not know this key.
// "Which theme" is this UI's own preference, not part of config semantics - hoisting it
// into domain.State would force the kernel to know a purely front-end concept.

// themeKey is this module's field name inside state.json's ModuleConfig.
const themeKey = "web-dashboard"

// themeState is this module's own section in state.json.
//
// Even one field gets its own struct: ModuleConfig stores **the raw section**, and dumping
// a value into a bare string forces compat reads for the next preference (density, font).
type themeState struct {
	// Theme is the chosen theme ID. Empty = never chosen, the UI uses factory tokens
	// (app.css's `:root`, which follows the system light/dark on its own).
	Theme string `json:"theme,omitempty"`
}

// loadThemeState reads this module's section. **Never fails**: missing section or bad JSON
// means "never chosen" - fall back to factory tokens rather than restyle from junk.
func loadThemeState() themeState {
	var s themeState
	raw := store.LoadState().ModuleConfig[themeKey]
	if len(raw) == 0 {
		return s
	}
	_ = json.Unmarshal(raw, &s)
	return s
}

// selectedTheme is the theme ID chosen on this machine (empty = never chosen).
func selectedTheme() string { return loadThemeState().Theme }

// setSelectedTheme records the chosen theme.
func setSelectedTheme(id string) error {
	raw, err := json.Marshal(themeState{Theme: id})
	if err != nil {
		return err
	}
	st := store.LoadState()
	if st.ModuleConfig == nil {
		st.ModuleConfig = map[string][]byte{}
	}
	st.ModuleConfig[themeKey] = raw
	return store.SaveState(st)
}

// themeRegistry holds the registered themes (interface in api.go's ThemeService).
type themeRegistry struct {
	// A lock, not "read-only after registration": this UI **hangs on the shared port**, whose
	// HTTP server starts with the gateway's Start - so while other modules still register
	// themes in their Start, a browser can already hit /api/themes. The window is short, but
	// it would be a concurrent map read/write (Go kills the process), not ugly data.
	mu   sync.RWMutex
	byID map[string]Theme
}

func newThemeRegistry() *themeRegistry {
	return &themeRegistry{byID: map[string]Theme{}}
}

var _ ThemeService = (*themeRegistry)(nil)

// RegisterTheme, see api.go's ThemeService.
//
// A duplicate ID errors rather than the later one overwriting: when two themes grab one
// identity, "which won" depends on assembly order (directory-name alphabetical order), and
// nobody can see that. Reported, both authors look once and know which one to change.
func (r *themeRegistry) RegisterTheme(t Theme) (modules.Release, error) {
	if t.ID == "" {
		return nil, i18n.E("a theme needs an id", nil)
	}
	if t.Name == nil {
		return nil, i18n.E("theme {id} has no name", i18n.A{"id": t.ID})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byID[t.ID]; dup {
		return nil, i18n.E("theme {id} is already registered", i18n.A{"id": t.ID})
	}
	r.byID[t.ID] = t
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.byID, t.ID)
		return nil
	}, nil
}

// snapshot is the themes registered right now, sorted by ID (same order on two runs).
//
// Sorted, not registration order: registration order is **assembly order** (directory
// names), meaningless, and if the button row changes every time users re-hunt each time
// (same as OverviewAgent sorting by id).
func (r *themeRegistry) snapshot() []Theme {
	r.mu.RLock()
	out := make([]Theme, 0, len(r.byID))
	for _, t := range r.byID {
		out = append(out, t)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// has reports whether an ID is registered (to tell "is the stored theme still here").
func (r *themeRegistry) has(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.byID[id]
	return ok
}

// themeDoc is the /api/themes payload.
type themeDoc struct {
	// Active is the effective theme ID. **Empty = factory tokens** (app.css's :root, which
	// follows the system light/dark) - not a theme module, so it is not in the table below.
	Active string `json:"active"`
	// Themes are the registered themes. Names are translated here (the backend resolves the
	// language; a front-end second guess would be a second source, see web/src/i18n.ts header).
	Themes []themeItem `json:"themes"`
}

type themeItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Dark bool   `json:"dark"`
	CSS  string `json:"css"`
}

// themeDocOf builds the payload. When the stored theme is **gone** (module disabled,
// directory deleted), active is empty: the UI falls back to factory tokens instead of
// clinging to a nonexistent ID.
//
// No quiet rewrite (no write-back to state.json): the user may have only disabled
// it for now; reinstall and it returns. A write-back erases that choice for good, unclicked.
func themeDocOf(r *themeRegistry) themeDoc {
	active := selectedTheme()
	if active != "" && !r.has(active) {
		active = ""
	}
	doc := themeDoc{Active: active, Themes: make([]themeItem, 0, 4)}
	for _, t := range r.snapshot() {
		doc.Themes = append(doc.Themes, themeItem{
			ID: t.ID, Name: t.Name(), Dark: t.Dark, CSS: t.CSS,
		})
	}
	return doc
}

// ---------- the card ----------

// themeConceptID is the card's machine tag (also its concept ID).
const themeConceptID = "web-dashboard.theme"

// themeConcept is the "theme" card.
//
// # Why its own section (rather than inside "config")
//
// Every other sidebar section is about this machine's config; this one is about **what
// this UI looks like**: "which chain my requests take" and "what color this page is" are
// two questions; mixed into one column, readers think the latter is part of routing.
//
// # Why it needs a card at all (the top bar already has a dropdown)
//
// That dropdown is a **shortcut**, not where config lives. Two criteria here:
//
//   - Anything changeable in a UI should be **findable in the config section** - unfindable
//     is nonexistent; "the dropdown at the top right" is not findable (it looks like a view
//     switch, not settings), and being outside the snapshot, search/filter never see it.
//   - "Whoever owns it brings it": this section is registered by this module (same path as
//     other modules' sections), not stuffed into config - that state.json holds several
//     modules' own sections, and config speaking for us would decide for all modules.
//
// Both write the same thing (`selectedTheme`), so dropdown and card never disagree.
func themeConcept(r *themeRegistry) viewapi.Concept {
	return viewapi.Concept{
		ID: themeConceptID, Kind: viewapi.KindToggles,
		Title: i18n.T("Theme", nil),
		Data: viewapi.Toggles{Items: []viewapi.ToggleItem{{
			ID:      "theme",
			Kind:    viewapi.ToggleSelect,
			Value:   selectedTheme(),
			Options: themeOptions(r),
			// The empty option must **name what it is**: a valid value (factory tokens), not a
			// blank nothing-to-pick, and "follow system" must come from the backend (it localizes).
			OptionLabels: map[string]string{
				"": i18n.T("follow the system", nil),
			},
			Label: i18n.T("Theme", nil),
			Why: i18n.T("The colours of this interface. Saved to state.json and applied at "+
				"once — no reload. Nothing but tokens changes: a theme cannot move anything "+
				"around.", nil),
		}},
		},
		// **No File / Base**: unlike switch cards elsewhere, nothing user-editable backs this
		// cell (it lives in state.json's module_config, this module's own section). With File
		// the UI would put it beside state.json's raw half - the whole file - where one keystroke
		// can overwrite another module's section.
		Apply: func(edit json.RawMessage, _ string) (string, error) {
			return applyThemeEdit(r, edit)
		},
	}
}

// themeOptions are the dropdown entries: empty (follow system) first, then sorted by ID.
func themeOptions(r *themeRegistry) []string {
	out := []string{""}
	for _, t := range r.snapshot() {
		out = append(out, t.ID)
	}
	return out
}

// applyThemeEdit persists that cell.
//
// An unknown ID **errors**, not quietly back to factory: likely that module is disabled,
// and "clicked, nothing happened" and "this theme is gone" are two sentences (see /api/themes).
func applyThemeEdit(r *themeRegistry, edit json.RawMessage) (string, error) {
	var patch struct {
		Theme *string `json:"theme"`
	}
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the theme in this request is not readable: {err}", i18n.A{"err": err})
	}
	if patch.Theme == nil {
		return "", i18n.E("the request says nothing about the theme", nil)
	}
	if *patch.Theme != "" && !r.has(*patch.Theme) {
		return "", i18n.E("there is no theme {id} in this build", i18n.A{"id": *patch.Theme})
	}
	if err := setSelectedTheme(*patch.Theme); err != nil {
		return "", err
	}
	return "", nil
}
