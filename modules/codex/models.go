package codex

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This file is codex's **model name <-> our tier** table, plus the takeover mode.
//
// # Why it is needed (the conclusion from measuring 2026-09-21)
//
// codex's model catalog is **compiled into its binary**: `codex debug models` prints 9
// (gpt-6-astra, gpt-5.6-sol, gpt-5.6-terra, gpt-5.6-luna, gpt-5.5 …), and it **never asks the
// server**. Measured by pointing its provider at a recording fake upstream: `/v1/models` was
// **never requested once** -- we tried `--enable remote_models`, declaring `models_endpoint`
// in the provider table, and giving `env_key` an API key, all three, and the fake
// upstream always received 0. That path (which `model-provider/src/models_endpoint.rs`
// really is in the binary) is not opened in the "custom provider, no official auth" form.
//
// So the path "push tiers into `/v1/models` and let its own picker show tiers" **does not
// exist**. Do the reverse: **let it keep using its own model names, and recognize the names
// when they come in** -- whichever one the user picks via `/model` in codex lands on whichever
// of our tiers. This is exactly the fix for "it cannot switch models from the inside":
// today config.toml has a tier name, codex knows only one model, and switching does nothing.
//
// # The mechanism is not a new hole
//
// These model names are registered as **dynamic roles** (domain.ExtraRole),
// going the same resolution path as omo's intra-agent slot -- the first line of
// `resolve.ResolveRequest` is `if domain.IsKnownRole(model) -> BuildChain(...)`.
// core does not know which models codex has, nor what "gpt-5.6-luna" is.
//
// # Why a **file** rather than constants in code
//
// codex will ship new models (its own catalog changes). Hardcoding means
// "the upstream adds a model -> we ship a release", while the knowledge of this (which
// name goes to which tier) **users already have**. So a table ships as the factory
// default, lands in `$NEWGATE_HOME/codex-models.json`, and one added line takes effect
// -- the same approach as omo's slot table (omo-slots.json in modules/opencodeomo).
//
// The table is **overlaid**: what the file writes overrides same-named
// factory values, and the extras are new additions. So "add a new model"
// really is just one line, and the five factory ones need not be copied.

// ModelsFile is where this table lives: under the config root, beside other modules' own tables.
func ModelsFile() string { return filepath.Join(paths.Config(), "codex-models.json") }

// The two values of the takeover mode.
const (
	// ModeTakeover writes the **tier name** into config.toml (`model = "normal"`). It was the
	// only behavior before 2026-09-21, and is the default -- changing no one's current state.
	ModeTakeover = "takeover"
	// ModeRename writes **codex's own model name** (`model =
	// "gpt-5.6-luna"`), and the table below maps it back to a tier when the
	// request arrives. The user switching models in codex = switching tiers.
	ModeRename = "rename"
)

// modelTier is one entry in the table.
type modelTier struct {
	// Slug is the model name codex knows (the slug in `codex debug models`). Not translated.
	Slug string `json:"slug"`
	// Tier is which of our tiers it lands on.
	Tier string `json:"tier"`
}

// Models is the shape of this file.
type Models struct {
	// Mode: see the two constants above; empty = takeover (the default).
	Mode string `json:"mode,omitempty"`
	// Models are the entries to **add** or **change** (the five factory ones need not be copied in).
	Models []modelTier `json:"models,omitempty"`
}

// defaultModels is the factory table: the five visible in the codex catalog -> the tiers we have.
//
// Ordered by codex's own priority (the number in `codex debug models`: smaller is more capable):
// the most capable gets heavy, the cheapest gets light. Settled by the user on 2026-09-21.
//
// **Only the five visible ones are registered** (visibility=list):
// the hidden ones (daybreak-*, codex-auto-review) do not appear in
// the picker, and registering them would mean nobody ever picks them.
var defaultModels = []modelTier{
	{Slug: "gpt-6-astra", Tier: "heavy"},
	{Slug: "gpt-5.6-sol", Tier: "normal"},
	{Slug: "gpt-5.6-terra", Tier: "normal"},
	{Slug: "gpt-5.6-luna", Tier: "normal"},
	{Slug: "gpt-5.5", Tier: "light"},
}

// readModels reads this file; when it cannot be read (never
// created) or cannot be parsed, it falls back to the factory table.
//
// Unparseable does **not error and is not silent**: the broken copy is ignored (fail-open,
// takeover writes the default behavior as usual), and diagnostics says so -- a broken
// config is worth knowing about, but must not make `newgate on codex` fail because of it.
func readModels() (Models, string) {
	var m Models
	b, err := ioutil.ReadFile(ModelsFile())
	if err != nil || len(b) == 0 {
		return m, ""
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return Models{}, i18n.T("{file} does not parse ({err}); the built-in table is in use",
			i18n.A{"file": ModelsFile(), "err": err.Error()})
	}
	return m, ""
}

// Mode is the takeover mode right now (default takeover).
func Mode() string {
	m, _ := readModels()
	if m.Mode == ModeRename {
		return ModeRename
	}
	return ModeTakeover
}

// SetMode writes the mode. **Only this one cell is touched**: the rest of the
// table is preserved as is (what the user hand-wrote must not vanish because
// we flipped a switch). When the file does not exist, a minimal one is created.
func SetMode(mode string) error {
	if mode != ModeRename {
		mode = ModeTakeover
	}
	m, _ := readModels()
	m.Mode = mode
	return writeModels(m)
}

func writeModels(m Models) error {
	if err := os.MkdirAll(filepath.Dir(ModelsFile()), 0o770); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return ioutil.WriteFile(ModelsFile(), append(b, '\n'), 0o660)
}

// effectiveModels is the table in effect right now.
//
// # Semantics (changed once on 2026-09-21, forced by the UI)
//
//	file has **no** `models`        -> the five factory ones (fresh install or only `mode`)
//	file **has** `models` (even `[]`) -> **it is everything**; factory values do not apply
//
// It used to be "overlaid": what was in the file overrode same-named ones, extras counted
// as additions, and factory values always backstopped. That semantic was comfortable
// when hand-writing the file (one line adds one line), but **once it became a table in
// the UI it was wrong**: the user deletes a row on the card, saves it back to the file,
// and on read the factory value pushes it back -- the UI shows the delete succeeded
// while nothing happened. A table UI only holds with "what you see is what you get".
//
// The cost is that "add one line" now means writing the whole table (which
// is exactly what the UI does), and hand-writing the file requires copying
// the factory values. That trade-off is explicit: **the UI copy is the primary
// entry** (see the header of modelsview.go), hand-writing is the secondary path.
//
// Stability: two assemblies of the same file yield the same table (the order comes from the file or
// the factory slice, not map iteration), so the UI and takeover asking twice get the same answer.
func effectiveModels() []modelTier {
	m, _ := readModels()
	if m.Models == nil {
		return append([]modelTier(nil), defaultModels...)
	}
	out := make([]modelTier, 0, len(m.Models))
	for _, e := range m.Models {
		if e.Slug == "" || e.Tier == "" {
			continue // half-filled row: saved midway in the UI, must not yield an unmatchable role
		}
		out = append(out, e)
	}
	return out
}

// modelFor asks the reverse: which codex model name
// should be written for this tier (used in rename mode).
//
// When not found it returns the empty string -- the caller falls back to the tier
// name from that (see slotValue in takeover.go), i.e. exactly the same behavior as
// takeover mode. **Fall back rather than error**: a table narrowed down should not
// make takeover fail; it just means "this tier has no corresponding codex model".
func modelFor(tier string) string {
	if tier == "" {
		return ""
	}
	for _, e := range effectiveModels() {
		if e.Tier == tier {
			return e.Slug
		}
	}
	return ""
}

// ---------- Dynamic roles ----------

// rolesProvider hands this table to core's resolution path (roleprov.RoleProvider).
//
// **Contributed only in rename mode** (a trade-off from 2026-09-21, reasoned from measurement):
// once these names are registered as "roles", `IsKnownRole` is true, so **the reverse-resolution
// path is short-circuited** -- and reverse resolution ("resolve a concrete model name
// back to its tier, putting the named model first") is exactly what other clients rely on when
// naming `gpt-5.6-terra` (this machine's smt-codex chain carries that name). In other words:
// unconditional registration would quietly change the behavior of **people who never opted in**.
//
// So it follows the mode -- rename mode is the form the user **explicitly opted into**,
// and there what they want is "codex's model names = tiers"; in takeover mode this table is
// useless for resolution (codex only sends tier names), and registering it is pure side effect.
type rolesProvider struct{}

func (rolesProvider) Source() string { return "codex" }

// WatchFiles: a change to this table must re-feed the roles (mode lives in the same file).
func (rolesProvider) WatchFiles() []string { return []string{ModelsFile()} }

func (rolesProvider) Roles() ([]domain.ExtraRole, error) {
	if Mode() != ModeRename {
		return nil, nil
	}
	models := effectiveModels()
	out := make([]domain.ExtraRole, 0, len(models))
	for _, e := range models {
		out = append(out, domain.ExtraRole{
			Key:     e.Slug,
			Source:  "codex",
			Default: e.Tier,
			// Meta is for humans only (core does not read it): which client asked, which tier it lands on.
			Meta: map[string]string{"client": "codex", "tier": e.Tier},
		})
	}
	return out, nil
}
