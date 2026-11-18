package codex

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// modelsConceptID is this card's stable identity.
const modelsConceptID = "codex.models"

// This file is **that fillable table**: which codex model name lands on which of our tiers.
//
// # Why it must be a fillable table, not a JSON file
//
// The first version made this table a file like `$NEWGATE_HOME/codex-models.json`
// -- "dynamic" it was, but the user's words were:
//
//	I used rename mode, why the hell is there no mapping table for the user to fill in
//
// He was right, and what he pointed at is a **UI gap**: after switching to rename mode, the user
// switching a model in codex gets a 404, and all they can do is "go edit a JSON file" -- that is
// not the answer a product should give. This table is **part of the product** (which name counts
// as which tier is the user's own trade-off), so it should live in the UI like other trade-offs.
//
// The file is still its home (that table must be hand-writable, scriptable, and
// committable), but the UI copy is the **primary entry**: on read, if the file
// has no `models`, use the five factory ones, otherwise it is authoritative
// (see effectiveModels in models.go); on write, write the whole table back.
//
// # Write semantics: whole-table overwrite
//
// Saving writes **the whole table in the UI**, not "which rows changed". So deleting a row
// deletes it (factory values will not push it back) -- this is the premise for this card to
// hold: what you see is what you get. Once `models` exists in the file it is authoritative;
// only when absent (or never saved by this card) are the five factory ones used.
//
// The `mode` cell **is preserved as is**: it does not belong to this
// table (see models.go), and saving the table should not wipe it.

// modelsConcept is that mapping table.
func modelsConcept() view.Concept {
	entries := effectiveModels()
	items := make([]view.Record, 0, len(entries))
	for _, e := range entries {
		items = append(items, modelRecord(e))
	}
	return view.Concept{
		ID: modelsConceptID, Kind: view.KindRecords,
		Title: i18n.T("Codex model names", nil),
		// Order 21: right after the "Codex models" card (20). The two are two halves of one thing --
		// that one says "which tier to take", this one says "which codex name counts as which tier".
		Order: 21,
		Data: view.Records{
			Items:    items,
			Base:     store.Revision(ModelsFile()),
			CanAdd:   true,
			AddLabel: i18n.T("+ model", nil),
		},
		Apply: applyModels,
		// The mode sentence **lives here**, on the same card as the table: the table really takes effect
		// only in rename mode, while in takeover mode it affects not one byte (codex only sends tier
		// names). Split apart, the user looks at a table without knowing whether it counts right now.
		Note: modeNote(),
		// The toggle button: only draw "the other one" (see modeActions).
		Actions: modeActions(),
	}
}

// modelRecord lays one mapping out as a small form: the codex name, and which of our tiers.
func modelRecord(e modelTier) view.Record {
	return view.Record{
		// ID uses slug: it is this row's identity right now. Renaming (the left cell) = changing
		// the **value**, not adding a new entry -- the same rule as the providers table.
		ID: e.Slug, Label: e.Slug, Removable: true,
		Fields: []view.Field{
			{
				ID: "slug", Label: i18n.T("codex model", nil), Kind: view.FieldText, Value: e.Slug,
				Placeholder: i18n.T("e.g. gpt-5.6-luna", nil),
				Why:         i18n.T("The model name codex itself sends — copy it out of `codex debug models` (the slug).", nil),
			},
			{
				ID: "tier", Label: i18n.T("tier", nil), Kind: view.FieldSelect, Value: e.Tier,
				Options: append([]string{""}, domain.Roles...),
				Why:     i18n.T("Which tier a request naming that model resolves to. Required — a row without one is rejected on save.", nil),
			},
		},
	}
}

// applyModels writes the table in the UI back to the file.
//
// For semantics see the file header: **whole-table overwrite**. There are only two
// validations, both the kind that "silently errors without them": an empty name
// (that row matches nothing, while the user thinks it was added), two rows with
// the same name (the later one never gets a turn, while both are drawn in the UI).
func applyModels(edit json.RawMessage, base string) (string, error) {
	var patch struct {
		Items []struct {
			ID     string            `json:"id"`
			Values map[string]string `json:"values"`
		} `json:"items"`
	}
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the table in this request is not readable: {err}", i18n.A{"err": err})
	}

	out := make([]modelTier, 0, len(patch.Items))
	seen := map[string]bool{}
	for _, it := range patch.Items {
		slug := strings.TrimSpace(it.Values["slug"])
		tier := strings.TrimSpace(it.Values["tier"])
		if slug == "" {
			return "", i18n.E("a row has no model name — nothing would ever match it", nil)
		}
		if tier == "" {
			// An empty tier **may not be saved**, rather than "stored but does not count": effectiveModels
			// drops rows with no tier (they match nothing), so the row just added in the UI would
			// **disappear by itself** after saving -- what the user sees is "added, gone, no error".
			return "", i18n.E("the row for {slug} has no tier — pick one, or remove the row",
				i18n.A{"slug": slug})
		}
		if seen[slug] {
			return "", i18n.E("two rows are both named \"{slug}\" — only one of them could ever match",
				i18n.A{"slug": slug})
		}
		seen[slug] = true
		out = append(out, modelTier{Slug: slug, Tier: tier})
	}

	// Start from **the raw text on disk** and replace only the models section.
	//
	// Why not "decode into Models and re-serialize": that would **drop unrecognized
	// keys** (today's `mode`, and any key added to this file in the future), and
	// dropping them only shows in the UI as "save succeeded" -- a unit test on 2026-09-21
	// caught the first version exactly that way (`something_else` was gone). So only
	// one section is changed on the raw text, the same approach as the providers path.
	doc := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(ModelsFile()); err == nil && len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return "", i18n.Ef(err, "{file} is not valid JSON", i18n.A{"file": "codex-models.json"})
		}
	}
	body, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	doc["models"] = body
	// When `mode` is absent, fill in **the one in effect right now**: once `models`
	// exists in the file it is authoritative, and if the mode is still empty the
	// reader (Mode()) treats it as takeover -- which does not match what the user just
	// saw in the UI. Writing it all is so "what the file says = what the UI says".
	if _, ok := doc["mode"]; !ok {
		b, err := json.Marshal(Mode())
		if err != nil {
			return "", err
		}
		doc["mode"] = b
	}
	text, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	next, err := store.WriteIfUnchanged(ModelsFile(), base, append(text, '\n'))
	if err != nil {
		return "", modelsErr(err)
	}
	return next, nil
}

// modelsErr is the one sentence given when the table cannot be written.
//
// The conflict branch is separate: **the copy on disk was not broken by us**
// (the user hand-edited it, or another tab saved first), and the table in
// the UI looks perfectly normal -- without saying so, it is all guesswork.
func modelsErr(err error) error {
	if sc, ok := err.(*store.StaleError); ok {
		return i18n.E("{file} changed on disk since this page loaded it — reload and apply again ({err})",
			i18n.A{"file": "codex-models.json", "err": sc.Error()})
	}
	return i18n.Ef(err, "cannot write {file}: {err}", i18n.A{"file": "codex-models.json"})
}
