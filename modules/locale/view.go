package locale

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/lib/i18n"
)

// This file is the face of "which language this process speaks" handed to the web UI (the
// equivalent of `newgate lang`).
//
// With the CLI side (command.go) they are **two entry points to the same knowledge**: where the
// language lives, how it resolves, and who overrides it after a change are all this module's
// business; the UI only draws this card and hands the user's pick back.
//
// It hangs on the **language module itself** rather than making web-dashboard special-case a
// "language": once the UI knows the thing "language", it also knows "language lives in state.json's
// module_config.locale" and "switching languages makes the process reinstall the tables" -- both of
// which only this module knows (see the "whoever owns the state reports it" rule in docs/09).

// registerView hangs the language card up. Nothing without a web UI (`newgate lang` carries on).
func registerView(v view.Service) (modules.Release, error) {
	return v.Register("locale",
		view.Title(func() string { return i18n.T("Language", nil) }).Of(view.FieldSettings),
		concepts)
}

// concepts is computed **when someone comes to ask** (see the package comment in lib/view):
// registration reads not one file.
func concepts() ([]view.Concept, error) {
	return []view.Concept{languageConcept()}, nil
}

// languageConcept is that card.
//
// It and config's "global settings" card **write the same file** (state.json), but each reports
// its own fields -- that file holds several modules' sections, and serving the whole thing up for
// the user to edit would be letting the UI make decisions for every module (the same rule as on
// config's side). So both cards carry File=state.json: by "which file this concept is about" the UI
// pairs the control half with the raw-text half, side by side (see fileOf in the front end's
// nav.ts).
func languageConcept() view.Concept {
	file := paths.StateFile()
	return view.Concept{
		ID: "locale.language", Kind: view.KindToggles,
		Title: i18n.T("Language", nil),
		Data: view.Toggles{
			File: paths.RelToRoot(file), Base: store.Revision(file),
			Items: []view.ToggleItem{{
				ID:      "lang",
				Kind:    view.ToggleSelect,
				Value:   i18n.Current(),
				Options: tags(),
				Label:   i18n.T("Language", nil),
				Why: i18n.T("Which language this process speaks. Saved to state.json and "+
					"applied at once — no restart.", nil),
			}},
		},
		Apply: applyLanguage,
	}
}

// tags are the selectable languages (the machine values from `newgate lang`'s list).
func tags() []string {
	avail := i18n.Available()
	out := make([]string, 0, len(avail))
	for _, info := range avail {
		out = append(out, info.Language)
	}
	return out
}

// applyLanguage writes into state.json, and **takes effect on the spot**.
//
// Both steps are needed, and the second is exactly why this card exists: on the CLI side writing
// ends it (the next process reads it itself), while the daemon is long-lived -- writing to disk
// without switching tables would leave the UI on the old language after the user picks Chinese,
// until some restart. That looks just like "saving did not take effect".
//
// Switching tables goes through `i18n.Use` (no reinstalling the catalogs), not `i18n.Install`: the
// distribution's translations were Extend-ed in at assembly time, and a reinstall would wipe them
// (see the comment on Use).
func applyLanguage(edit json.RawMessage, base string) (string, error) {
	var patch struct {
		Lang *string `json:"lang"`
	}
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the language in this request is not readable: {err}", i18n.A{"err": err})
	}
	if patch.Lang == nil {
		return "", i18n.E("the request says nothing about the language", nil)
	}
	match := i18n.Match(*patch.Lang, tags())
	if match == "" {
		return "", i18n.E("No such language: {tag}. Available: {list}",
			i18n.A{"tag": *patch.Lang, "list": strings.Join(tags(), " ")})
	}

	file := paths.StateFile()
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	// Touch only the module_config.locale cell: state.json holds several modules' own sections, and
	// rewriting the whole thing would overwrite someone else's with this one's knowledge (the same
	// reason as config's applyStateDefaultProfile).
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", i18n.Ef(err, "{file} is not valid JSON", i18n.A{"file": filepath.Base(file)})
	}
	var mods map[string]json.RawMessage
	if raw, ok := doc["module_config"]; ok {
		if err := json.Unmarshal(raw, &mods); err != nil {
			return "", i18n.Ef(err, "the module_config section is not an object: {err}", nil)
		}
	}
	if mods == nil {
		mods = map[string]json.RawMessage{}
	}
	raw, err := json.Marshal(persisted{Lang: match})
	if err != nil {
		return "", err
	}
	mods[StateKey] = raw
	modsRaw, err := json.Marshal(mods)
	if err != nil {
		return "", err
	}
	doc["module_config"] = modsRaw
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}

	rev, err := store.WriteIfUnchanged(file, base, append(out, '\n'))
	if err != nil {
		return "", conflict("locale.language", file, err, out)
	}
	// Switch tables only after the disk write succeeds: the other way round (switch first, write
	// after) would, when the write fails, leave **this process's** language disagreeing with what is
	// recorded in the file, while the UI the user sees has already changed -- that kind of
	// disagreement is the hardest to attribute.
	i18n.Use(match)
	return rev, nil
}

// conflict turns "the baseline does not match" into the conflict shape the UI knows (carrying both
// sides' original text).
//
// These six lines are the same thing as the two passages in modules/config/view.go and
// modules/pluginmanager/view.go, and the three copies are **deliberate** (the reason is written on
// pluginmanager's copy: the alternative is teaching store about the view contract, which is worse).
// The criterion: when `Conflict` gains a field, all three follow.
func conflict(conceptID, file string, err error, yours []byte) error {
	var stale *store.StaleError
	if errors.As(err, &stale) {
		return &view.Conflict{
			Concept: conceptID, Path: paths.RelToRoot(file),
			Base: stale.Base, Current: stale.Current,
			Yours: string(yours), Theirs: string(stale.Disk),
		}
	}
	return err
}
